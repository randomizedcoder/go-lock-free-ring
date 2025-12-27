# Ring Buffer Retry Strategy Design Document

## Overview

This document proposes an enhancement to the `go-lock-free-ring` library that introduces configurable retry strategies for write operations. Currently, when a write fails because a shard is full, the implementation backs off with a sleep. This document explores alternative strategies, with a focus on a "next shard" approach that could improve throughput by avoiding sleep in scenarios where other shards have available capacity.

---

## Current Implementation Analysis

### WriteWithBackoff Behavior

The current retry mechanism is implemented in `ring.go`:

```go:133:156:ring.go
func (r *ShardedRing) WriteWithBackoff(producerID uint64, value any, config WriteConfig) bool {
	shard := r.selectShard(producerID)
	backoffCount := 0

	for {
		// Try MaxRetries times before sleeping
		for retry := 0; retry < config.MaxRetries; retry++ {
			if shard.write(value) {
				return true
			}
		}

		// All retries failed, backoff
		backoffCount++

		// Check if we've exceeded max backoffs (if limit is set)
		if config.MaxBackoffs > 0 && backoffCount >= config.MaxBackoffs {
			return false
		}

		// Sleep to reduce contention and let consumer catch up
		time.Sleep(config.BackoffDuration)
	}
}
```

### Key Observations

1. **Single Shard Affinity** (`ring.go:134`): The producer selects a shard once via `selectShard(producerID)` and only ever attempts writes to that single shard.

2. **Shard Selection** (`ring.go:107-110`):
   ```go
   func (r *ShardedRing) selectShard(producerID uint64) *Shard {
       shardIdx := producerID & r.mask
       return r.shards[shardIdx]
   }
   ```
   Uses bitwise AND for O(1) selection - fast but inflexible during contention.

3. **Sleep-Based Backoff** (`ring.go:154`): When retries are exhausted, the thread sleeps for `BackoffDuration` (default 100µs).

4. **Configuration** (`ring.go:14-22`):
   ```go
   type WriteConfig struct {
       MaxRetries      int           // write attempts before sleeping (default: 10)
       BackoffDuration time.Duration // sleep duration (default: 100µs)
       MaxBackoffs     int           // max backoff cycles, 0 = unlimited
   }
   ```

### Problem Statement

In a scenario where:
- Producer 0 writes to Shard 0
- Shard 0 becomes full
- Shards 1, 2, 3 have available capacity

The current implementation will **sleep** even though the ring as a whole has capacity. This is suboptimal for throughput-sensitive applications where avoiding sleep is critical.

---

## Proposed Enhancement: Configurable Retry Strategy

### Strategy Enum

Introduce a new configuration option to select the retry strategy:

```go
// RetryStrategy determines how writers handle full shards
type RetryStrategy int

const (
    // SleepBackoff: Current behavior - retry same shard, then sleep
    SleepBackoff RetryStrategy = iota

    // NextShard: Try all shards in round-robin before sleeping
    NextShard

    // RandomShard: Try random shards before sleeping
    RandomShard

    // AdaptiveBackoff: Exponential backoff with jitter on same shard
    AdaptiveBackoff

    // SpinThenYield: Yield processor instead of sleeping (lowest latency, highest CPU)
    SpinThenYield

    // Hybrid: NextShard + AdaptiveBackoff combined
    Hybrid
)
```

### Enhanced WriteConfig

```go
type WriteConfig struct {
    // Strategy determines retry behavior (default: SleepBackoff)
    Strategy RetryStrategy

    // MaxRetries is the number of write attempts per shard (default: 10)
    MaxRetries int

    // BackoffDuration is how long to sleep after all strategies exhausted (default: 100µs)
    BackoffDuration time.Duration

    // MaxBackoffs is the maximum backoff cycles before giving up (0 = unlimited)
    MaxBackoffs int

    // --- Strategy-specific options ---

    // MaxBackoffDuration caps exponential backoff (default: 10ms)
    // Used by: AdaptiveBackoff, Hybrid
    MaxBackoffDuration time.Duration

    // BackoffMultiplier for exponential growth (default: 2.0)
    // Used by: AdaptiveBackoff, Hybrid
    BackoffMultiplier float64
}
```

---

## Architecture: Function Dispatch Pattern

### Design Goal

Instead of using a `switch` statement on every write call to select the strategy, we use **function dispatch** where the strategy function is resolved once at setup time. This moves the decision from the hot path to initialization.

### Writer Type

Introduce a `Writer` type that encapsulates the strategy:

```go
// WriterFunc is the signature for all strategy implementations
type WriterFunc func(r *ShardedRing, producerID uint64, value any, state *writerState) bool

// Writer holds a pre-resolved strategy function for zero-overhead dispatch
type Writer struct {
    ring       *ShardedRing
    config     WriteConfig
    producerID uint64
    writeFunc  WriterFunc       // Strategy resolved at creation time
    state      *writerState     // Mutable state for adaptive strategies
}

// writerState holds per-writer mutable state (for adaptive backoff, etc.)
type writerState struct {
    currentBackoff time.Duration
    backoffCount   int
}

// NewWriter creates a writer with the strategy function resolved once
func NewWriter(ring *ShardedRing, producerID uint64, config WriteConfig) *Writer {
    w := &Writer{
        ring:       ring,
        config:     config,
        producerID: producerID,
        state:      &writerState{currentBackoff: config.BackoffDuration},
    }

    // Resolve strategy function ONCE at setup time
    w.writeFunc = resolveStrategy(config.Strategy)

    return w
}

// resolveStrategy maps strategy enum to function (called once at setup)
func resolveStrategy(strategy RetryStrategy) WriterFunc {
    switch strategy {
    case NextShard:
        return writeWithNextShard
    case RandomShard:
        return writeWithRandomShard
    case AdaptiveBackoff:
        return writeWithAdaptiveBackoff
    case SpinThenYield:
        return writeWithSpinYield
    case Hybrid:
        return writeWithHybrid
    default:
        return writeWithSleepBackoff
    }
}

// Write calls the pre-resolved strategy function (no switch on hot path)
func (w *Writer) Write(value any) bool {
    return w.writeFunc(w.ring, w.producerID, value, w.state)
}
```

### Benefits of Function Dispatch

| Aspect | Switch Dispatch | Function Dispatch |
|--------|-----------------|-------------------|
| **Per-call overhead** | Branch on every write | Single indirect call |
| **CPU branch prediction** | Unpredictable if mixed strategies | N/A (no branch) |
| **Inlining potential** | Limited | Go compiler can devirtualize |
| **Configuration** | Per-call config access | Pre-resolved at setup |

---

## Composable Building Blocks

### Design Philosophy

Rather than implementing each strategy as a monolithic function with duplicated logic, we decompose into small, composable building blocks:

1. **Shard Selectors** - Determine which shard(s) to try
2. **Backoff Functions** - Determine how/whether to pause
3. **Retry Orchestrators** - Combine selectors and backoff into a strategy

### Shard Selector Functions

```go
// ShardSelector returns the next shard index to try, or -1 if exhausted
type ShardSelector func(ring *ShardedRing, producerID uint64, attempt int) int

// affinityShard always returns the producer's affinity shard
func affinityShard(ring *ShardedRing, producerID uint64, attempt int) int {
    if attempt > 0 {
        return -1 // Only one shard to try
    }
    return int(producerID & ring.mask)
}

// roundRobinShards returns shards in order starting from affinity
func roundRobinShards(ring *ShardedRing, producerID uint64, attempt int) int {
    if attempt >= int(ring.numShards) {
        return -1 // All shards exhausted
    }
    startShard := producerID & ring.mask
    return int((startShard + uint64(attempt)) & ring.mask)
}

// randomShards returns random shard indices (affinity first)
func randomShards(ring *ShardedRing, producerID uint64, attempt int) int {
    if attempt >= int(ring.numShards) {
        return -1
    }
    if attempt == 0 {
        return int(producerID & ring.mask) // Affinity first
    }
    return int(uint64(rand.Int63()) & ring.mask)
}
```

### Backoff Functions

```go
// BackoffFunc performs a backoff action, returns false if should give up
type BackoffFunc func(config *WriteConfig, state *writerState) bool

// sleepBackoff performs fixed-duration sleep
func sleepBackoff(config *WriteConfig, state *writerState) bool {
    state.backoffCount++
    if config.MaxBackoffs > 0 && state.backoffCount >= config.MaxBackoffs {
        return false // Give up
    }
    time.Sleep(config.BackoffDuration)
    return true
}

// yieldBackoff yields processor without sleeping
func yieldBackoff(config *WriteConfig, state *writerState) bool {
    state.backoffCount++
    if config.MaxBackoffs > 0 && state.backoffCount >= config.MaxBackoffs {
        return false
    }
    runtime.Gosched()
    return true
}

// exponentialBackoff performs exponential backoff with jitter
func exponentialBackoff(config *WriteConfig, state *writerState) bool {
    state.backoffCount++
    if config.MaxBackoffs > 0 && state.backoffCount >= config.MaxBackoffs {
        return false
    }

    // Apply jitter: 75-125% of current backoff
    jitter := 0.75 + rand.Float64()*0.5
    sleepDuration := time.Duration(float64(state.currentBackoff) * jitter)
    time.Sleep(sleepDuration)

    // Grow exponentially, capped at max
    multiplier := config.BackoffMultiplier
    if multiplier == 0 {
        multiplier = 2.0
    }
    state.currentBackoff = time.Duration(float64(state.currentBackoff) * multiplier)

    maxBackoff := config.MaxBackoffDuration
    if maxBackoff == 0 {
        maxBackoff = 10 * time.Millisecond
    }
    if state.currentBackoff > maxBackoff {
        state.currentBackoff = maxBackoff
    }

    return true
}

// noBackoff returns immediately (used when shard selector handles all logic)
func noBackoff(config *WriteConfig, state *writerState) bool {
    state.backoffCount++
    return config.MaxBackoffs == 0 || state.backoffCount < config.MaxBackoffs
}
```

### Generic Retry Orchestrator

```go
// retryLoop is the generic retry orchestrator that composes selector + backoff
func retryLoop(
    ring *ShardedRing,
    value any,
    config *WriteConfig,
    state *writerState,
    selectShard ShardSelector,
    producerID uint64,
    backoff BackoffFunc,
) bool {
    for {
        // Try all shards from selector
        for attempt := 0; ; attempt++ {
            shardIdx := selectShard(ring, producerID, attempt)
            if shardIdx < 0 {
                break // Selector exhausted
            }

            shard := ring.shards[shardIdx]

            // Try this shard MaxRetries times
            for retry := 0; retry < config.MaxRetries; retry++ {
                if shard.write(value) {
                    return true
                }
            }
        }

        // All shards/retries exhausted, perform backoff
        if !backoff(config, state) {
            return false // Backoff says give up
        }
    }
}
```

### Strategy Implementations Using Building Blocks

With the composable building blocks, each strategy becomes a thin wrapper:

```go
// writeWithSleepBackoff: affinity shard + fixed sleep
func writeWithSleepBackoff(r *ShardedRing, producerID uint64, value any, state *writerState) bool {
    return retryLoop(r, value, &r.config, state, affinityShard, producerID, sleepBackoff)
}

// writeWithNextShard: round-robin shards + fixed sleep
func writeWithNextShard(r *ShardedRing, producerID uint64, value any, state *writerState) bool {
    return retryLoop(r, value, &r.config, state, roundRobinShards, producerID, sleepBackoff)
}

// writeWithRandomShard: random shards + fixed sleep
func writeWithRandomShard(r *ShardedRing, producerID uint64, value any, state *writerState) bool {
    return retryLoop(r, value, &r.config, state, randomShards, producerID, sleepBackoff)
}

// writeWithAdaptiveBackoff: affinity shard + exponential backoff
func writeWithAdaptiveBackoff(r *ShardedRing, producerID uint64, value any, state *writerState) bool {
    return retryLoop(r, value, &r.config, state, affinityShard, producerID, exponentialBackoff)
}

// writeWithSpinYield: affinity shard + yield (no sleep)
func writeWithSpinYield(r *ShardedRing, producerID uint64, value any, state *writerState) bool {
    return retryLoop(r, value, &r.config, state, affinityShard, producerID, yieldBackoff)
}

// writeWithHybrid: round-robin shards + exponential backoff
func writeWithHybrid(r *ShardedRing, producerID uint64, value any, state *writerState) bool {
    return retryLoop(r, value, &r.config, state, roundRobinShards, producerID, exponentialBackoff)
}
```

### Composition Matrix

| Strategy | Shard Selector | Backoff Function |
|----------|----------------|------------------|
| `SleepBackoff` | `affinityShard` | `sleepBackoff` |
| `NextShard` | `roundRobinShards` | `sleepBackoff` |
| `RandomShard` | `randomShards` | `sleepBackoff` |
| `AdaptiveBackoff` | `affinityShard` | `exponentialBackoff` |
| `SpinThenYield` | `affinityShard` | `yieldBackoff` |
| `Hybrid` | `roundRobinShards` | `exponentialBackoff` |

This design allows easy creation of new strategies by mixing existing components, e.g.:
- `NextShard` + `yieldBackoff` = High-throughput spin with shard fallback
- `randomShards` + `exponentialBackoff` = Load-balanced with graceful degradation

---

## Strategy Descriptions

### Strategy 1: SleepBackoff (Current Default)

**Behavior**: Retry same shard, then sleep for fixed duration.

**Algorithm**:
```
1. Select affinity shard (producerID & mask)
2. Try writing MaxRetries times
3. If failed, sleep for BackoffDuration
4. Repeat until success or MaxBackoffs exceeded
```

**Best for**: Predictable latency, per-producer ordering, light loads.

### Strategy 2: NextShard

**Behavior**: Try all shards in round-robin order before sleeping.

**Algorithm**:
```
1. Start with affinity shard
2. Try writing MaxRetries times
3. If failed, move to next shard: (shardIdx + 1) & mask
4. Repeat for all numShards
5. If all shards failed, sleep once
6. Repeat from step 1
```

**Best for**: Maximum throughput, bursty traffic, when ordering doesn't matter.

### Strategy 3: RandomShard

**Behavior**: Try random shards to spread load evenly.

**Algorithm**:
```
1. Try affinity shard first (MaxRetries times)
2. If failed, try random shard
3. Repeat for numShards-1 more random attempts
4. If all failed, sleep once
5. Repeat from step 1
```

**Best for**: Skewed producer distributions, avoiding thundering herd.

### Strategy 4: AdaptiveBackoff

**Behavior**: Exponential backoff with jitter on same shard.

**Algorithm**:
```
1. Select affinity shard
2. Try writing MaxRetries times
3. If failed, sleep for currentBackoff * jitter
4. Increase currentBackoff: currentBackoff *= multiplier
5. Cap at MaxBackoffDuration
6. Repeat until success or MaxBackoffs exceeded
```

**Best for**: Sustained contention, reducing retry storms, preserving affinity.

### Strategy 5: SpinThenYield

**Behavior**: Yield processor instead of sleeping for lowest latency.

**Algorithm**:
```
1. Select affinity shard
2. Try writing MaxRetries times
3. If failed, call runtime.Gosched() (yield)
4. Repeat until success or MaxBackoffs exceeded
```

**Best for**: Ultra-low latency requirements, CPU budget available, real-time systems.

**Caution**: Can consume 100% CPU if ring is persistently full.

### Strategy 6: Hybrid

**Behavior**: Combines NextShard traversal with exponential backoff.

**Algorithm**:
```
1. Try all shards in round-robin order (MaxRetries each)
2. If all failed, sleep for currentBackoff * jitter
3. Increase currentBackoff exponentially
4. Repeat until success or MaxBackoffs exceeded
```

**Best for**: Complex workloads needing both throughput and graceful degradation.

---

## Strategy Comparison Matrix

| Strategy | Sleep Avoidance | Shard Affinity | CPU Under Load | Ordering | Latency | Complexity |
|----------|-----------------|----------------|----------------|----------|---------|------------|
| **SleepBackoff** | ❌ No | ✅ Preserved | Low | Per-shard | Medium | Simple |
| **NextShard** | ✅ Yes | ❌ Broken | Medium | Mixed | Low | Simple |
| **RandomShard** | ✅ Yes | ❌ Broken | Medium | Mixed | Low | Medium |
| **AdaptiveBackoff** | ❌ No | ✅ Preserved | Low | Per-shard | Variable | Medium |
| **SpinThenYield** | ✅ Yes | ✅ Preserved | High | Per-shard | Lowest | Simple |
| **Hybrid** | ✅ Yes | ❌ Broken | Medium-High | Mixed | Variable | Complex |

---

## Recommended Use Cases

### Use `SleepBackoff` (Current Default) When:
- Per-producer ordering matters
- Predictable latency is more important than throughput
- System is lightly loaded (ring rarely fills)

### Use `NextShard` When:
- Maximum throughput is the priority
- Uneven producer traffic patterns
- Global ordering is not required
- Ring frequently hits capacity bursts

### Use `RandomShard` When:
- Producer IDs are clustered (e.g., sequential assignment)
- Want to avoid hot-spot contention
- Load balancing is important

### Use `AdaptiveBackoff` When:
- Long-running sustained contention expected
- Want to reduce retry storms
- Single-shard affinity is important

### Use `SpinThenYield` When:
- Ultra-low latency is critical (real-time systems)
- CPU resources are abundant
- Ring full state is brief/transient
- Willing to accept high CPU under contention

### Use `Hybrid` When:
- Need both high throughput and graceful degradation
- Complex workloads with variable patterns
- Willing to accept higher complexity

---

## Testing and Benchmarking

### Unit Test Strategy

Each component should be tested independently, then integrated:

#### 1. Shard Selector Tests

Test each selector function for correctness:

```go
func TestAffinityShard(t *testing.T) {
    tests := []struct {
        name       string
        numShards  uint64
        producerID uint64
        attempt    int
        want       int
    }{
        {"first_attempt_p0", 4, 0, 0, 0},
        {"first_attempt_p1", 4, 1, 0, 1},
        {"first_attempt_p4", 4, 4, 0, 0}, // 4 & 3 = 0
        {"second_attempt", 4, 0, 1, -1},  // exhausted
    }
    // ... test implementation
}

func TestRoundRobinShards(t *testing.T) {
    tests := []struct {
        name       string
        numShards  uint64
        producerID uint64
        attempts   int
        wantSeq    []int // expected sequence of shard indices
    }{
        {"4_shards_from_0", 4, 0, 5, []int{0, 1, 2, 3, -1}},
        {"4_shards_from_2", 4, 2, 5, []int{2, 3, 0, 1, -1}},
        {"8_shards_wraparound", 8, 6, 9, []int{6, 7, 0, 1, 2, 3, 4, 5, -1}},
    }
    // ... test implementation
}

func TestRandomShards(t *testing.T) {
    // Test that:
    // 1. First attempt always returns affinity shard
    // 2. Subsequent attempts return valid shard indices
    // 3. Returns -1 after numShards attempts
    // 4. Distribution is reasonably uniform (statistical test)
}
```

#### 2. Backoff Function Tests

Test each backoff function's behavior:

```go
func TestSleepBackoff(t *testing.T) {
    config := &WriteConfig{
        BackoffDuration: 1 * time.Millisecond,
        MaxBackoffs:     3,
    }
    state := &writerState{}

    // Should return true for first 2 backoffs
    for i := 0; i < 2; i++ {
        start := time.Now()
        result := sleepBackoff(config, state)
        elapsed := time.Since(start)

        if !result {
            t.Errorf("backoff %d: expected true, got false", i)
        }
        if elapsed < config.BackoffDuration {
            t.Errorf("backoff %d: slept %v, expected >= %v", i, elapsed, config.BackoffDuration)
        }
    }

    // Third backoff should return false (MaxBackoffs reached)
    if sleepBackoff(config, state) {
        t.Error("expected false after MaxBackoffs")
    }
}

func TestExponentialBackoff(t *testing.T) {
    config := &WriteConfig{
        BackoffDuration:    1 * time.Millisecond,
        MaxBackoffDuration: 10 * time.Millisecond,
        BackoffMultiplier:  2.0,
        MaxBackoffs:        5,
    }
    state := &writerState{currentBackoff: config.BackoffDuration}

    // Verify exponential growth
    expectedBackoffs := []time.Duration{1, 2, 4, 8, 10} // capped at 10ms
    for i, expected := range expectedBackoffs {
        before := state.currentBackoff
        exponentialBackoff(config, state)
        // Note: actual sleep includes jitter, just verify state update
        if i < len(expectedBackoffs)-1 {
            // Check that backoff grew (approximately)
            _ = before // use in actual test
        }
    }
}

func TestYieldBackoff(t *testing.T) {
    config := &WriteConfig{MaxBackoffs: 3}
    state := &writerState{}

    // Should return quickly (no sleep)
    for i := 0; i < 2; i++ {
        start := time.Now()
        result := yieldBackoff(config, state)
        elapsed := time.Since(start)

        if !result {
            t.Errorf("yield %d: expected true", i)
        }
        if elapsed > 1*time.Millisecond {
            t.Errorf("yield %d: took %v, expected < 1ms", i, elapsed)
        }
    }
}
```

#### 3. Strategy Integration Tests

Test each strategy end-to-end:

```go
func TestWriteWithNextShard(t *testing.T) {
    // Create ring where shard 0 is full, others have space
    ring, _ := NewShardedRing(64, 4) // 16 per shard

    // Fill shard 0
    for i := 0; i < 16; i++ {
        ring.Write(0, i)
    }

    // NextShard strategy should find space in shard 1
    writer := NewWriter(ring, 0, WriteConfig{
        Strategy:   NextShard,
        MaxRetries: 1,
    })

    if !writer.Write("overflow") {
        t.Error("NextShard should have found space in another shard")
    }

    // Verify item is in shard 1, not shard 0
    // (Read from specific shard to verify)
}

func TestWriteWithSpinYield(t *testing.T) {
    ring, _ := NewShardedRing(64, 4)

    // Start consumer that drains slowly
    done := make(chan struct{})
    go func() {
        for {
            select {
            case <-done:
                return
            default:
                ring.TryRead()
                time.Sleep(100 * time.Microsecond)
            }
        }
    }()
    defer close(done)

    writer := NewWriter(ring, 0, WriteConfig{
        Strategy:    SpinThenYield,
        MaxRetries:  10,
        MaxBackoffs: 1000,
    })

    // Fill ring, then write more (should yield until space)
    for i := 0; i < 20; i++ {
        ring.Write(0, i)
    }

    start := time.Now()
    success := writer.Write("yielded")
    elapsed := time.Since(start)

    if !success {
        t.Error("SpinYield should eventually succeed with consumer running")
    }
    t.Logf("SpinYield write took %v", elapsed)
}
```

#### 4. Concurrent Stress Tests

```go
func TestStrategyConcurrentCorrectness(t *testing.T) {
    strategies := []RetryStrategy{
        SleepBackoff, NextShard, RandomShard,
        AdaptiveBackoff, SpinThenYield, Hybrid,
    }

    for _, strategy := range strategies {
        t.Run(strategy.String(), func(t *testing.T) {
            ring, _ := NewShardedRing(1024, 8)
            config := WriteConfig{
                Strategy:        strategy,
                MaxRetries:      10,
                BackoffDuration: 10 * time.Microsecond,
                MaxBackoffs:     0, // unlimited
            }

            numProducers := 8
            itemsPerProducer := 1000
            var written atomic.Int64
            var wg sync.WaitGroup

            // Start consumer
            consumerDone := make(chan struct{})
            var read atomic.Int64
            go func() {
                for {
                    select {
                    case <-consumerDone:
                        return
                    default:
                        if _, ok := ring.TryRead(); ok {
                            read.Add(1)
                        }
                    }
                }
            }()

            // Start producers
            for p := 0; p < numProducers; p++ {
                wg.Add(1)
                go func(id int) {
                    defer wg.Done()
                    writer := NewWriter(ring, uint64(id), config)
                    for i := 0; i < itemsPerProducer; i++ {
                        if writer.Write(i) {
                            written.Add(1)
                        }
                    }
                }(p)
            }

            wg.Wait()
            close(consumerDone)

            // Drain remaining
            for {
                if _, ok := ring.TryRead(); !ok {
                    break
                }
                read.Add(1)
            }

            if read.Load() != written.Load() {
                t.Errorf("read %d != written %d", read.Load(), written.Load())
            }
        })
    }
}
```

### Benchmark Strategy

#### 1. Micro-benchmarks (Per-Strategy)

Measure raw performance of each strategy under controlled conditions:

```go
func BenchmarkStrategyMicro(b *testing.B) {
    strategies := []struct {
        name     string
        strategy RetryStrategy
    }{
        {"SleepBackoff", SleepBackoff},
        {"NextShard", NextShard},
        {"RandomShard", RandomShard},
        {"AdaptiveBackoff", AdaptiveBackoff},
        {"SpinThenYield", SpinThenYield},
        {"Hybrid", Hybrid},
    }

    for _, s := range strategies {
        b.Run(s.name, func(b *testing.B) {
            // Large ring - measure uncontended performance
            ring, _ := NewShardedRing(10_000_000, 8)
            writer := NewWriter(ring, 0, WriteConfig{
                Strategy:        s.strategy,
                MaxRetries:      10,
                BackoffDuration: 100 * time.Microsecond,
            })

            b.ResetTimer()
            b.ReportAllocs()

            for i := 0; i < b.N; i++ {
                writer.Write(i)
                // Periodic drain to prevent fill
                if i%10000 == 9999 {
                    for j := 0; j < 1000; j++ {
                        ring.TryRead()
                    }
                }
            }
        })
    }
}
```

#### 2. Contention Benchmarks

Measure behavior when ring is frequently full:

```go
func BenchmarkStrategyUnderContention(b *testing.B) {
    strategies := []RetryStrategy{
        SleepBackoff, NextShard, SpinThenYield,
    }

    for _, strategy := range strategies {
        b.Run(strategy.String(), func(b *testing.B) {
            // Small ring - high contention
            ring, _ := NewShardedRing(128, 8)

            // Start slow consumer
            done := make(chan struct{})
            go func() {
                for {
                    select {
                    case <-done:
                        return
                    default:
                        ring.ReadBatch(10)
                        time.Sleep(50 * time.Microsecond)
                    }
                }
            }()

            config := WriteConfig{
                Strategy:        strategy,
                MaxRetries:      10,
                BackoffDuration: 100 * time.Microsecond,
                MaxBackoffs:     100,
            }

            b.ResetTimer()

            b.RunParallel(func(pb *testing.PB) {
                writer := NewWriter(ring, uint64(rand.Int63()), config)
                i := 0
                for pb.Next() {
                    writer.Write(i)
                    i++
                }
            })

            close(done)
        })
    }
}
```

#### 3. Latency Distribution Benchmarks

Measure tail latencies (P50, P95, P99):

```go
func BenchmarkStrategyLatencyDistribution(b *testing.B) {
    strategies := []RetryStrategy{SleepBackoff, NextShard, SpinThenYield}

    for _, strategy := range strategies {
        b.Run(strategy.String(), func(b *testing.B) {
            ring, _ := NewShardedRing(1024, 8)

            // Consumer running
            done := make(chan struct{})
            go func() {
                for {
                    select {
                    case <-done:
                        return
                    default:
                        ring.ReadBatch(50)
                    }
                }
            }()

            writer := NewWriter(ring, 0, WriteConfig{
                Strategy:        strategy,
                MaxRetries:      10,
                BackoffDuration: 100 * time.Microsecond,
            })

            latencies := make([]time.Duration, 0, b.N)

            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                start := time.Now()
                writer.Write(i)
                latencies = append(latencies, time.Since(start))
            }
            b.StopTimer()

            close(done)

            // Calculate percentiles
            sort.Slice(latencies, func(i, j int) bool {
                return latencies[i] < latencies[j]
            })

            p50 := latencies[len(latencies)*50/100]
            p95 := latencies[len(latencies)*95/100]
            p99 := latencies[len(latencies)*99/100]

            b.ReportMetric(float64(p50.Nanoseconds()), "p50-ns")
            b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns")
            b.ReportMetric(float64(p99.Nanoseconds()), "p99-ns")
        })
    }
}
```

#### 4. CPU Usage Benchmarks

Compare CPU consumption under similar throughput:

```go
func BenchmarkStrategyCPUUsage(b *testing.B) {
    // This benchmark should be run with:
    // go test -bench=CPUUsage -cpuprofile=cpu.prof

    strategies := []RetryStrategy{SleepBackoff, SpinThenYield}

    for _, strategy := range strategies {
        b.Run(strategy.String(), func(b *testing.B) {
            ring, _ := NewShardedRing(256, 8) // Small ring, more contention

            done := make(chan struct{})
            go func() {
                for {
                    select {
                    case <-done:
                        return
                    default:
                        ring.ReadBatch(20)
                        time.Sleep(10 * time.Microsecond)
                    }
                }
            }()

            writer := NewWriter(ring, 0, WriteConfig{
                Strategy:        strategy,
                MaxRetries:      10,
                BackoffDuration: 100 * time.Microsecond,
            })

            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                writer.Write(i)
            }
            b.StopTimer()

            close(done)
        })
    }
}
```

### Makefile Targets

Add these targets for easy benchmarking:

```makefile
# Benchmark all strategies
bench-strategies:
	go test -bench=BenchmarkStrategy -benchmem -count=3 ./...

# Benchmark under contention
bench-strategies-contention:
	go test -bench=BenchmarkStrategyUnderContention -benchmem -count=3 ./...

# Benchmark latency distribution
bench-strategies-latency:
	go test -bench=BenchmarkStrategyLatencyDistribution -benchmem ./...

# Compare SpinYield vs SleepBackoff CPU usage
bench-strategies-cpu:
	go test -bench=BenchmarkStrategyCPUUsage -benchmem -cpuprofile=cpu-strategies.prof ./...
	go tool pprof -top cpu-strategies.prof

# Run all strategy benchmarks with profiling
bench-strategies-profile:
	go test -bench=BenchmarkStrategy -benchmem \
		-cpuprofile=cpu.prof \
		-memprofile=mem.prof \
		./...
```

### Integration Test with cmd/ring

This section details the specific code changes needed to support strategy testing in the example application and integration test framework.

---

#### Changes to `cmd/ring/ring.go`

##### New Command Line Flags (after line 51)

Add new flags to the existing flag block (`cmd/ring/ring.go:33-51`):

```go
// Command line flags (cmd/ring/ring.go:33-51, add after line 51)
var (
    // ... existing flags (lines 33-51) ...

    // NEW: Strategy selection flag
    strategyFlag = flag.String("strategy", "SleepBackoff",
        "Retry strategy: SleepBackoff, NextShard, RandomShard, AdaptiveBackoff, SpinThenYield, Hybrid")

    // NEW: GOMAXPROCS control for contention testing
    gomaxprocsFlag = flag.Int("gomaxprocs", 0,
        "Set GOMAXPROCS (0=use runtime default, typically NumCPU)")

    // NEW: Adaptive backoff specific flags
    maxBackoffDur = flag.Duration("maxBackoff", 10*time.Millisecond,
        "Maximum backoff duration for adaptive strategies")
    backoffMultiplier = flag.Float64("backoffMult", 2.0,
        "Backoff multiplier for exponential strategies")
)
```

##### New Strategy Parsing Function (after `validateFlags()`, ~line 392)

```go
// parseStrategy converts string flag to RetryStrategy (cmd/ring/ring.go, after line 392)
func parseStrategy(s string) (ring.RetryStrategy, error) {
    switch strings.ToLower(s) {
    case "sleepbackoff", "sleep":
        return ring.SleepBackoff, nil
    case "nextshard", "next":
        return ring.NextShard, nil
    case "randomshard", "random":
        return ring.RandomShard, nil
    case "adaptivebackoff", "adaptive":
        return ring.AdaptiveBackoff, nil
    case "spinthenyield", "spin", "yield":
        return ring.SpinThenYield, nil
    case "hybrid":
        return ring.Hybrid, nil
    default:
        return ring.SleepBackoff, fmt.Errorf("unknown strategy: %s", s)
    }
}
```

##### Modify `main()` Function (cmd/ring/ring.go:74-186)

Add GOMAXPROCS and strategy setup after `flag.Parse()` (line 75):

```go
func main() {
    flag.Parse()

    // NEW: Set GOMAXPROCS if specified (add after line 75)
    if *gomaxprocsFlag > 0 {
        prev := runtime.GOMAXPROCS(*gomaxprocsFlag)
        log.Printf("GOMAXPROCS set to %d (was %d)", *gomaxprocsFlag, prev)
    } else {
        log.Printf("GOMAXPROCS: %d (default)", runtime.GOMAXPROCS(0))
    }

    // NEW: Parse strategy (add after GOMAXPROCS)
    strategy, err := parseStrategy(*strategyFlag)
    if err != nil {
        log.Fatalf("Invalid strategy: %v", err)
    }
    log.Printf("Using retry strategy: %s", *strategyFlag)

    if err := validateFlags(); err != nil {
        log.Fatalf("Invalid flags: %v", err)
    }
    // ... rest of main() ...
```

##### Modify WriteConfig Creation (cmd/ring/ring.go:128-132)

Update the `writeConfig` creation to include strategy:

```go
    // Create write config (cmd/ring/ring.go:128-132, replace existing)
    writeConfig = ring.WriteConfig{
        Strategy:           strategy,                    // NEW
        MaxRetries:         *maxRetries,
        BackoffDuration:    *backoffDur,
        MaxBackoffs:        *maxBackoffs,
        MaxBackoffDuration: *maxBackoffDur,              // NEW
        BackoffMultiplier:  *backoffMultiplier,          // NEW
    }
```

##### Modify Producer to Use Writer (cmd/ring/ring.go:188-237)

Replace direct `WriteWithBackoff` call with `Writer`:

```go
// producer function (cmd/ring/ring.go:188-237)
func producer(ctx context.Context, wg *sync.WaitGroup, id uint64,
    r *ring.ShardedRing, seq *atomic.Uint64) {
    defer wg.Done()

    // NEW: Create writer with strategy resolved once
    writer := ring.NewWriter(r, id, writeConfig)

    // ... existing pacing calculation (lines 193-201) ...

    for {
        select {
        case <-ctx.Done():
            if *debugLevel >= 4 {
                log.Printf("Producer %d: shutting down", id)
            }
            return
        default:
        }

        // Get from pools
        buf := bufPool.Get().(*[]byte)
        pkt := pktPool.Get().(*Packet)
        pkt.Sequence = seq.Add(1)
        pkt.Data = buf

        // NEW: Use Writer instead of WriteWithBackoff (line 220)
        if !writer.Write(pkt) {
            // Failed after all retries - drop packet
            bufPool.Put(buf)
            pkt.Data = nil
            pktPool.Put(pkt)
            droppedCount.Add(1)
        } else {
            producedCount.Add(1)
        }

        // ... existing pacing logic (lines 230-236) ...
    }
}
```

##### Update Logging (cmd/ring/ring.go:134-135)

Update startup log to include strategy:

```go
    // cmd/ring/ring.go:134-135
    log.Printf("Starting: %d producers @ %.1f Mb/s, ring=%d/%d shards, strategy=%s, btree max=%d, consumer every %dms",
        *numProducers, *rateMbps, actualRingSize, *ringShards, *strategyFlag, *btreeSizeFlag, *frequencyMs)
```

---

#### Changes to `integration-tests/config.go`

##### Extend TestCase Struct (integration-tests/config.go:8-20)

Add strategy and GOMAXPROCS fields:

```go
// TestCase represents a single integration test configuration
// (integration-tests/config.go:8-20, extend struct)
type TestCase struct {
    ID          string        // Test identifier (e.g., "T001")
    Name        string        // Human-readable name
    Producers   int           // Number of producers
    Rate        float64       // Per-producer rate in Mb/s
    PacketSize  int           // Packet size in bytes
    Frequency   int           // Consumer wake interval in ms
    Duration    time.Duration // Test duration
    RingSize    int           // 0 = auto-calculate
    RingShards  int           // Number of shards (0 = match producers)
    BTreeSize   int           // B-tree capacity

    // NEW: Strategy testing fields
    Strategy    string        // Retry strategy name (empty = default SleepBackoff)
    GOMAXPROCS  int           // GOMAXPROCS setting (0 = runtime default)
}
```

##### Update ConfigString Method (integration-tests/config.go:28-31)

```go
// ConfigString returns a short configuration description
// (integration-tests/config.go:28-31, update)
func (tc TestCase) ConfigString() string {
    cfg := fmt.Sprintf("%dp×%.0fMb/%db/%dms",
        tc.Producers, tc.Rate, tc.PacketSize, tc.Frequency)
    if tc.Strategy != "" {
        cfg += "/" + tc.Strategy
    }
    if tc.GOMAXPROCS > 0 {
        cfg += fmt.Sprintf("/P%d", tc.GOMAXPROCS)
    }
    return cfg
}
```

##### Add Strategy Test Matrix (after line 193, new section)

```go
// StrategyTestMatrixConfig defines parameters for strategy comparison tests
// (integration-tests/config.go, add after line 193)
type StrategyTestMatrixConfig struct {
    Strategies  []string        // Strategies to test
    GOMAXPROCS  []int           // GOMAXPROCS values to test (0 = default)
    Producers   int             // Number of producers (constant for comparison)
    Rate        float64         // Per-producer rate in Mb/s
    PacketSize  int             // Packet size in bytes
    Frequency   int             // Consumer wake interval in ms
    Duration    time.Duration   // Test duration for each test
    RingSize    int             // Ring size (0 = auto)
    BTreeSize   int             // B-tree capacity
}

// DefaultStrategyTestMatrixConfig returns the default strategy test matrix
func DefaultStrategyTestMatrixConfig() StrategyTestMatrixConfig {
    return StrategyTestMatrixConfig{
        Strategies: []string{
            "SleepBackoff",
            "NextShard",
            "SpinThenYield",
            "AdaptiveBackoff",
            "Hybrid",
        },
        GOMAXPROCS: []int{0},     // Default only
        Producers:  4,
        Rate:       50,
        PacketSize: 1450,
        Frequency:  10,
        Duration:   10 * time.Second,
        BTreeSize:  2000,
    }
}

// ContentionStrategyTestMatrixConfig returns a matrix focused on contention effects
func ContentionStrategyTestMatrixConfig() StrategyTestMatrixConfig {
    return StrategyTestMatrixConfig{
        Strategies: []string{
            "SleepBackoff",
            "NextShard",
            "SpinThenYield",
        },
        GOMAXPROCS: []int{1, 2, 4, 0}, // 1=max contention, 0=default (all cores)
        Producers:  8,
        Rate:       25,                 // Lower rate to allow contention to manifest
        PacketSize: 1450,
        Frequency:  10,
        Duration:   10 * time.Second,
        BTreeSize:  2000,
    }
}

// HighThroughputStrategyTestConfig returns config for high-throughput strategy tests
func HighThroughputStrategyTestConfig() StrategyTestMatrixConfig {
    return StrategyTestMatrixConfig{
        Strategies: []string{
            "SleepBackoff",
            "NextShard",
            "SpinThenYield",
            "Hybrid",
        },
        GOMAXPROCS: []int{0},          // Default (all cores)
        Producers:  8,
        Rate:       100,               // High rate to stress strategies
        PacketSize: 1450,
        Frequency:  5,                 // Fast consumer
        Duration:   15 * time.Second,
        BTreeSize:  4000,
    }
}

// GenerateStrategyTestCases generates test cases for strategy comparison
func GenerateStrategyTestCases(cfg StrategyTestMatrixConfig) []TestCase {
    var tests []TestCase
    id := 1

    for _, gomaxprocs := range cfg.GOMAXPROCS {
        for _, strategy := range cfg.Strategies {
            tc := TestCase{
                ID:         fmt.Sprintf("S%03d", id),
                Name:       generateStrategyTestName(strategy, gomaxprocs, cfg),
                Producers:  cfg.Producers,
                Rate:       cfg.Rate,
                PacketSize: cfg.PacketSize,
                Frequency:  cfg.Frequency,
                Duration:   cfg.Duration,
                RingSize:   cfg.RingSize,
                RingShards: nextPowerOf2(cfg.Producers),
                BTreeSize:  cfg.BTreeSize,
                Strategy:   strategy,
                GOMAXPROCS: gomaxprocs,
            }
            tests = append(tests, tc)
            id++
        }
    }

    return tests
}

// generateStrategyTestName creates a human-readable name for strategy tests
func generateStrategyTestName(strategy string, gomaxprocs int, cfg StrategyTestMatrixConfig) string {
    procDesc := "default"
    if gomaxprocs > 0 {
        procDesc = fmt.Sprintf("P%d", gomaxprocs)
    }
    return fmt.Sprintf("%dp_%.0fMb_%s_%s",
        cfg.Producers, cfg.Rate, strategy, procDesc)
}

// PredefinedStrategyTestSets contains curated strategy test configurations
var PredefinedStrategyTestSets = map[string][]TestCase{
    "strategy-quick": {
        // Quick comparison of main strategies
        {ID: "S001", Name: "4p_50Mb_SleepBackoff", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 5 * time.Second, BTreeSize: 2000, Strategy: "SleepBackoff"},
        {ID: "S002", Name: "4p_50Mb_NextShard", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 5 * time.Second, BTreeSize: 2000, Strategy: "NextShard"},
        {ID: "S003", Name: "4p_50Mb_SpinThenYield", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 5 * time.Second, BTreeSize: 2000, Strategy: "SpinThenYield"},
    },
    "strategy-contention": {
        // GOMAXPROCS=1 creates maximum contention
        {ID: "S001", Name: "8p_25Mb_SleepBackoff_P1", Producers: 8, Rate: 25, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SleepBackoff", GOMAXPROCS: 1},
        {ID: "S002", Name: "8p_25Mb_NextShard_P1", Producers: 8, Rate: 25, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "NextShard", GOMAXPROCS: 1},
        {ID: "S003", Name: "8p_25Mb_SpinThenYield_P1", Producers: 8, Rate: 25, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SpinThenYield", GOMAXPROCS: 1},
        // GOMAXPROCS=2 - limited parallelism
        {ID: "S004", Name: "8p_25Mb_SleepBackoff_P2", Producers: 8, Rate: 25, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SleepBackoff", GOMAXPROCS: 2},
        {ID: "S005", Name: "8p_25Mb_NextShard_P2", Producers: 8, Rate: 25, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "NextShard", GOMAXPROCS: 2},
        {ID: "S006", Name: "8p_25Mb_SpinThenYield_P2", Producers: 8, Rate: 25, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SpinThenYield", GOMAXPROCS: 2},
        // Default GOMAXPROCS - full parallelism
        {ID: "S007", Name: "8p_25Mb_SleepBackoff_Pdef", Producers: 8, Rate: 25, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SleepBackoff", GOMAXPROCS: 0},
        {ID: "S008", Name: "8p_25Mb_NextShard_Pdef", Producers: 8, Rate: 25, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "NextShard", GOMAXPROCS: 0},
        {ID: "S009", Name: "8p_25Mb_SpinThenYield_Pdef", Producers: 8, Rate: 25, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SpinThenYield", GOMAXPROCS: 0},
    },
    "strategy-full": {
        // All strategies at multiple GOMAXPROCS levels
        // GOMAXPROCS=1 (sequential execution, maximum contention)
        {ID: "S001", Name: "4p_50Mb_SleepBackoff_P1", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SleepBackoff", GOMAXPROCS: 1},
        {ID: "S002", Name: "4p_50Mb_NextShard_P1", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "NextShard", GOMAXPROCS: 1},
        {ID: "S003", Name: "4p_50Mb_SpinThenYield_P1", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SpinThenYield", GOMAXPROCS: 1},
        {ID: "S004", Name: "4p_50Mb_AdaptiveBackoff_P1", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "AdaptiveBackoff", GOMAXPROCS: 1},
        {ID: "S005", Name: "4p_50Mb_Hybrid_P1", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "Hybrid", GOMAXPROCS: 1},
        // GOMAXPROCS=4 (limited parallelism)
        {ID: "S006", Name: "4p_50Mb_SleepBackoff_P4", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SleepBackoff", GOMAXPROCS: 4},
        {ID: "S007", Name: "4p_50Mb_NextShard_P4", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "NextShard", GOMAXPROCS: 4},
        {ID: "S008", Name: "4p_50Mb_SpinThenYield_P4", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SpinThenYield", GOMAXPROCS: 4},
        {ID: "S009", Name: "4p_50Mb_AdaptiveBackoff_P4", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "AdaptiveBackoff", GOMAXPROCS: 4},
        {ID: "S010", Name: "4p_50Mb_Hybrid_P4", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "Hybrid", GOMAXPROCS: 4},
        // GOMAXPROCS=0 (default, all cores)
        {ID: "S011", Name: "4p_50Mb_SleepBackoff_Pdef", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SleepBackoff", GOMAXPROCS: 0},
        {ID: "S012", Name: "4p_50Mb_NextShard_Pdef", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "NextShard", GOMAXPROCS: 0},
        {ID: "S013", Name: "4p_50Mb_SpinThenYield_Pdef", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "SpinThenYield", GOMAXPROCS: 0},
        {ID: "S014", Name: "4p_50Mb_AdaptiveBackoff_Pdef", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "AdaptiveBackoff", GOMAXPROCS: 0},
        {ID: "S015", Name: "4p_50Mb_Hybrid_Pdef", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000, Strategy: "Hybrid", GOMAXPROCS: 0},
    },
}

// FilterByStrategy returns a filter for specific strategies
func FilterByStrategy(strategies ...string) TestFilter {
    set := make(map[string]bool)
    for _, s := range strategies {
        set[s] = true
    }
    return func(tc TestCase) bool {
        return set[tc.Strategy]
    }
}

// FilterByGOMAXPROCS returns a filter for specific GOMAXPROCS values
func FilterByGOMAXPROCS(procs ...int) TestFilter {
    set := make(map[int]bool)
    for _, p := range procs {
        set[p] = true
    }
    return func(tc TestCase) bool {
        return set[tc.GOMAXPROCS]
    }
}
```

---

#### Changes to `integration-tests/executor.go`

##### Extend Run() Method (integration-tests/executor.go:57-147)

Add strategy and GOMAXPROCS to command arguments:

```go
// Run executes a test case and returns the result
// (integration-tests/executor.go:57-147, update args building)
func (e *Executor) Run(ctx context.Context, tc TestCase, profileMode string) (*ExecutionResult, error) {
    // ... existing setup (lines 58-68) ...

    // Build command arguments (lines 69-89)
    args := []string{
        fmt.Sprintf("-producers=%d", tc.Producers),
        fmt.Sprintf("-rate=%.2f", tc.Rate),
        fmt.Sprintf("-packetSize=%d", tc.PacketSize),
        fmt.Sprintf("-frequency=%d", tc.Frequency),
        fmt.Sprintf("-duration=%s", tc.Duration),
        fmt.Sprintf("-btreeSize=%d", tc.BTreeSize),
        "-debugLevel=3",
        "-statsInterval=1",
    }

    // NEW: Add strategy if specified (add after line 79)
    if tc.Strategy != "" {
        args = append(args, fmt.Sprintf("-strategy=%s", tc.Strategy))
    }

    // NEW: Add GOMAXPROCS if specified
    if tc.GOMAXPROCS > 0 {
        args = append(args, fmt.Sprintf("-gomaxprocs=%d", tc.GOMAXPROCS))
    }

    // Add ring size if specified (existing, line 82-84)
    // ... rest of method unchanged ...
```

---

#### New File: `integration-tests/strategy_runner.go`

Create a dedicated runner for strategy comparison tests:

```go
// integration-tests/strategy_runner.go (new file)
package integration_tests

import (
    "context"
    "fmt"
    "log"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "time"
)

// StrategyComparisonResult holds results for comparing strategies
type StrategyComparisonResult struct {
    Tests           []TestResult
    ByStrategy      map[string][]TestResult
    ByGOMAXPROCS    map[int][]TestResult
    BestThroughput  *TestResult
    LowestDropRate  *TestResult
    LowestCPU       *TestResult // If CPU profiling enabled
    Summary         StrategyComparisonSummary
}

// StrategyComparisonSummary provides high-level comparison metrics
type StrategyComparisonSummary struct {
    TotalTests       int
    StrategiesTested []string
    GOMAXPROCSValues []int
    TestDuration     time.Duration

    // Per-strategy aggregated metrics
    StrategyMetrics  map[string]StrategyMetrics
}

// StrategyMetrics holds aggregated metrics for a single strategy
type StrategyMetrics struct {
    Strategy        string
    TestCount       int
    AvgThroughput   float64  // Mb/s
    AvgDropRate     float64  // %
    AvgDeviation    float64  // % from expected
    PassCount       int
    FailCount       int

    // Per-GOMAXPROCS breakdown
    ByGOMAXPROCS    map[int]StrategyMetrics
}

// RunStrategyComparison runs a full strategy comparison test suite
func RunStrategyComparison(
    executor *Executor,
    tests []TestCase,
    profileMode string,
    valCfg ValidationConfig,
) (*StrategyComparisonResult, error) {

    result := &StrategyComparisonResult{
        ByStrategy:   make(map[string][]TestResult),
        ByGOMAXPROCS: make(map[int][]TestResult),
    }

    log.Printf("Starting strategy comparison: %d tests", len(tests))

    for i, tc := range tests {
        log.Printf("[%d/%d] Running %s (strategy=%s, GOMAXPROCS=%d)...",
            i+1, len(tests), tc.ID, tc.Strategy, tc.GOMAXPROCS)

        testResult := RunTest(executor, tc, valCfg, profileMode)
        result.Tests = append(result.Tests, testResult)

        // Index by strategy
        strategy := tc.Strategy
        if strategy == "" {
            strategy = "SleepBackoff"
        }
        result.ByStrategy[strategy] = append(result.ByStrategy[strategy], testResult)

        // Index by GOMAXPROCS
        result.ByGOMAXPROCS[tc.GOMAXPROCS] = append(result.ByGOMAXPROCS[tc.GOMAXPROCS], testResult)

        // Track best results
        if testResult.Metrics != nil {
            if result.BestThroughput == nil || testResult.Metrics.AverageRate > result.BestThroughput.Metrics.AverageRate {
                result.BestThroughput = &testResult
            }
            if result.LowestDropRate == nil || testResult.Metrics.DropRate < result.LowestDropRate.Metrics.DropRate {
                result.LowestDropRate = &testResult
            }
        }

        // Brief pause between tests
        time.Sleep(500 * time.Millisecond)
    }

    // Calculate summary
    result.Summary = calculateStrategySummary(result)

    return result, nil
}

// calculateStrategySummary computes aggregate statistics
func calculateStrategySummary(result *StrategyComparisonResult) StrategyComparisonSummary {
    summary := StrategyComparisonSummary{
        TotalTests:      len(result.Tests),
        StrategyMetrics: make(map[string]StrategyMetrics),
    }

    // Collect unique strategies and GOMAXPROCS values
    strategySet := make(map[string]bool)
    procsSet := make(map[int]bool)

    for _, tr := range result.Tests {
        strategy := tr.TestCase.Strategy
        if strategy == "" {
            strategy = "SleepBackoff"
        }
        strategySet[strategy] = true
        procsSet[tr.TestCase.GOMAXPROCS] = true

        // Aggregate metrics
        sm, ok := summary.StrategyMetrics[strategy]
        if !ok {
            sm = StrategyMetrics{
                Strategy:     strategy,
                ByGOMAXPROCS: make(map[int]StrategyMetrics),
            }
        }

        sm.TestCount++
        if tr.Passed {
            sm.PassCount++
        } else {
            sm.FailCount++
        }

        if tr.Metrics != nil {
            sm.AvgThroughput += tr.Metrics.AverageRate
            sm.AvgDropRate += tr.Metrics.DropRate
        }
        if tr.Validation != nil {
            sm.AvgDeviation += tr.Validation.RateDeviation
        }

        summary.StrategyMetrics[strategy] = sm
    }

    // Calculate averages
    for strategy, sm := range summary.StrategyMetrics {
        if sm.TestCount > 0 {
            sm.AvgThroughput /= float64(sm.TestCount)
            sm.AvgDropRate /= float64(sm.TestCount)
            sm.AvgDeviation /= float64(sm.TestCount)
        }
        summary.StrategyMetrics[strategy] = sm
    }

    // Convert sets to sorted slices
    for s := range strategySet {
        summary.StrategiesTested = append(summary.StrategiesTested, s)
    }
    sort.Strings(summary.StrategiesTested)

    for p := range procsSet {
        summary.GOMAXPROCSValues = append(summary.GOMAXPROCSValues, p)
    }
    sort.Ints(summary.GOMAXPROCSValues)

    return summary
}

// GenerateStrategyComparisonReport generates a detailed comparison report
func GenerateStrategyComparisonReport(result *StrategyComparisonResult) string {
    var sb strings.Builder

    sb.WriteString("=" + strings.Repeat("=", 79) + "\n")
    sb.WriteString("Strategy Comparison Report\n")
    sb.WriteString("Generated: " + time.Now().Format("2006-01-02 15:04:05") + "\n")
    sb.WriteString("=" + strings.Repeat("=", 79) + "\n\n")

    // Summary table
    sb.WriteString("== Summary ==\n")
    sb.WriteString(fmt.Sprintf("Total Tests: %d\n", result.Summary.TotalTests))
    sb.WriteString(fmt.Sprintf("Strategies: %v\n", result.Summary.StrategiesTested))
    sb.WriteString(fmt.Sprintf("GOMAXPROCS: %v\n\n", result.Summary.GOMAXPROCSValues))

    // Per-strategy results
    sb.WriteString("== Strategy Performance ==\n")
    sb.WriteString(fmt.Sprintf("%-15s %8s %10s %10s %10s %8s\n",
        "Strategy", "Tests", "Avg Mb/s", "Avg Drop%", "Avg Dev%", "Pass%"))
    sb.WriteString(strings.Repeat("-", 70) + "\n")

    for _, strategy := range result.Summary.StrategiesTested {
        sm := result.Summary.StrategyMetrics[strategy]
        passRate := float64(sm.PassCount) / float64(sm.TestCount) * 100
        sb.WriteString(fmt.Sprintf("%-15s %8d %10.2f %10.2f %10.2f %7.1f%%\n",
            strategy, sm.TestCount, sm.AvgThroughput, sm.AvgDropRate, sm.AvgDeviation, passRate))
    }
    sb.WriteString("\n")

    // Best performers
    if result.BestThroughput != nil {
        sb.WriteString("== Best Performers ==\n")
        sb.WriteString(fmt.Sprintf("Highest Throughput: %s (%s) - %.2f Mb/s\n",
            result.BestThroughput.TestCase.ID,
            result.BestThroughput.TestCase.Strategy,
            result.BestThroughput.Metrics.AverageRate))
    }
    if result.LowestDropRate != nil {
        sb.WriteString(fmt.Sprintf("Lowest Drop Rate: %s (%s) - %.4f%%\n",
            result.LowestDropRate.TestCase.ID,
            result.LowestDropRate.TestCase.Strategy,
            result.LowestDropRate.Metrics.DropRate))
    }
    sb.WriteString("\n")

    // GOMAXPROCS impact analysis
    sb.WriteString("== GOMAXPROCS Impact ==\n")
    sb.WriteString(fmt.Sprintf("%-10s %-15s %10s %10s\n",
        "GOMAXPROCS", "Strategy", "Avg Mb/s", "Drop%"))
    sb.WriteString(strings.Repeat("-", 50) + "\n")

    for _, procs := range result.Summary.GOMAXPROCSValues {
        tests := result.ByGOMAXPROCS[procs]
        procsLabel := "default"
        if procs > 0 {
            procsLabel = fmt.Sprintf("%d", procs)
        }

        // Group by strategy within this GOMAXPROCS value
        byStrategy := make(map[string][]TestResult)
        for _, tr := range tests {
            strategy := tr.TestCase.Strategy
            if strategy == "" {
                strategy = "SleepBackoff"
            }
            byStrategy[strategy] = append(byStrategy[strategy], tr)
        }

        for _, strategy := range result.Summary.StrategiesTested {
            strategyTests := byStrategy[strategy]
            if len(strategyTests) == 0 {
                continue
            }

            var avgRate, avgDrop float64
            for _, tr := range strategyTests {
                if tr.Metrics != nil {
                    avgRate += tr.Metrics.AverageRate
                    avgDrop += tr.Metrics.DropRate
                }
            }
            avgRate /= float64(len(strategyTests))
            avgDrop /= float64(len(strategyTests))

            sb.WriteString(fmt.Sprintf("%-10s %-15s %10.2f %10.4f\n",
                procsLabel, strategy, avgRate, avgDrop))
        }
    }
    sb.WriteString("\n")

    // Individual test results
    sb.WriteString("== Individual Test Results ==\n")
    sb.WriteString(fmt.Sprintf("%-6s %-20s %8s %10s %10s %8s %6s\n",
        "ID", "Name", "GOMAXP", "Rate Mb/s", "Drop%", "Dev%", "Pass"))
    sb.WriteString(strings.Repeat("-", 75) + "\n")

    for _, tr := range result.Tests {
        procs := "def"
        if tr.TestCase.GOMAXPROCS > 0 {
            procs = fmt.Sprintf("%d", tr.TestCase.GOMAXPROCS)
        }

        rate := 0.0
        drop := 0.0
        dev := 0.0
        if tr.Metrics != nil {
            rate = tr.Metrics.AverageRate
            drop = tr.Metrics.DropRate
        }
        if tr.Validation != nil {
            dev = tr.Validation.RateDeviation
        }

        passStr := "✓"
        if !tr.Passed {
            passStr = "✗"
        }

        sb.WriteString(fmt.Sprintf("%-6s %-20s %8s %10.2f %10.4f %+7.2f%% %6s\n",
            tr.TestCase.ID,
            truncateString(tr.TestCase.Name, 20),
            procs,
            rate, drop, dev, passStr))
    }

    return sb.String()
}

func truncateString(s string, maxLen int) string {
    if len(s) <= maxLen {
        return s
    }
    return s[:maxLen-3] + "..."
}
```

---

#### Makefile Targets for Strategy Testing

Add these targets to `Makefile`:

```makefile
# === Strategy Comparison Testing ===

# Quick strategy comparison (3 main strategies, default GOMAXPROCS)
test-integration-strategy-quick:
	cd integration-tests && go test -v -run TestStrategyComparison -testset=strategy-quick -timeout 5m

# Strategy comparison with contention testing (GOMAXPROCS=1,2,4,default)
test-integration-strategy-contention:
	cd integration-tests && go test -v -run TestStrategyComparison -testset=strategy-contention -timeout 30m

# Full strategy comparison (all strategies, all GOMAXPROCS levels)
test-integration-strategy-full:
	cd integration-tests && go test -v -run TestStrategyComparison -testset=strategy-full -timeout 60m

# Strategy comparison with CPU profiling
test-integration-strategy-profile:
	cd integration-tests && go test -v -run TestStrategyComparison -testset=strategy-quick -profile=cpu -timeout 10m

# Individual strategy tests with specific GOMAXPROCS
test-strategy-sleep-p1:
	./bin/ring -strategy=SleepBackoff -producers=4 -rate=50 -duration=10s -gomaxprocs=1

test-strategy-next-p1:
	./bin/ring -strategy=NextShard -producers=4 -rate=50 -duration=10s -gomaxprocs=1

test-strategy-spin-p1:
	./bin/ring -strategy=SpinThenYield -producers=4 -rate=50 -duration=10s -gomaxprocs=1

# Compare all strategies at GOMAXPROCS=1 (maximum contention)
test-strategy-all-p1:
	@echo "=== GOMAXPROCS=1 Strategy Comparison ==="
	@for strategy in SleepBackoff NextShard SpinThenYield AdaptiveBackoff Hybrid; do \
		echo "--- $$strategy ---"; \
		./bin/ring -strategy=$$strategy -producers=4 -rate=50 -duration=10s -gomaxprocs=1 2>&1 | grep -E "(Starting|Final|stats)"; \
		echo ""; \
	done

# Compare all strategies at default GOMAXPROCS
test-strategy-all-default:
	@echo "=== Default GOMAXPROCS Strategy Comparison ==="
	@for strategy in SleepBackoff NextShard SpinThenYield AdaptiveBackoff Hybrid; do \
		echo "--- $$strategy ---"; \
		./bin/ring -strategy=$$strategy -producers=4 -rate=50 -duration=10s 2>&1 | grep -E "(Starting|Final|stats)"; \
		echo ""; \
	done

# Run strategy comparison and generate summary report
test-integration-strategy-report:
	cd integration-tests && go test -v -run TestStrategyComparison -testset=strategy-full -report=summary -timeout 60m
	@echo "Report saved to integration-tests/output/strategy-report-latest.txt"
```

---

#### Running Strategy Tests Sequentially with Summary

The test runner can be invoked from Go tests or as a standalone tool:

```go
// integration-tests/integration_test.go (add new test function)

func TestStrategyComparison(t *testing.T) {
    testSet := os.Getenv("TESTSET")
    if testSet == "" {
        testSet = flag.Lookup("testset").Value.String()
    }
    if testSet == "" {
        testSet = "strategy-quick"
    }

    tests, ok := PredefinedStrategyTestSets[testSet]
    if !ok {
        // Try generating from matrix
        switch testSet {
        case "strategy-contention-gen":
            tests = GenerateStrategyTestCases(ContentionStrategyTestMatrixConfig())
        case "strategy-throughput-gen":
            tests = GenerateStrategyTestCases(HighThroughputStrategyTestConfig())
        default:
            t.Fatalf("Unknown test set: %s", testSet)
        }
    }

    projectRoot, err := FindProjectRoot()
    require.NoError(t, err)

    binaryPath, err := BuildBinary(projectRoot)
    require.NoError(t, err)

    outputDir := filepath.Join(projectRoot, "integration-tests", "output")
    executor, err := NewExecutor(binaryPath, outputDir)
    require.NoError(t, err)

    profileMode := os.Getenv("PROFILE")
    if profileMode == "" {
        profileMode = flag.Lookup("profile").Value.String()
    }

    valCfg := DefaultValidationConfig()

    // Run the comparison
    result, err := RunStrategyComparison(executor, tests, profileMode, valCfg)
    require.NoError(t, err)

    // Generate and save report
    report := GenerateStrategyComparisonReport(result)
    reportPath := filepath.Join(outputDir, fmt.Sprintf("strategy-report-%s.txt",
        time.Now().Format("20060102-150405")))
    err = os.WriteFile(reportPath, []byte(report), 0644)
    require.NoError(t, err)

    // Also create latest symlink
    latestPath := filepath.Join(outputDir, "strategy-report-latest.txt")
    os.Remove(latestPath)
    os.Symlink(filepath.Base(reportPath), latestPath)

    // Print summary to test output
    t.Log("\n" + report)

    // Assert all tests passed (or just report)
    for _, tr := range result.Tests {
        if !tr.Passed {
            t.Logf("FAIL: %s - %v", tr.TestCase.ID, tr.Error)
        }
    }
}
```

---

#### Expected GOMAXPROCS Impact

| GOMAXPROCS | Effect | Expected Strategy Performance |
|------------|--------|-------------------------------|
| **1** | Maximum contention - all goroutines share one OS thread | `SpinThenYield` may starve other goroutines; `SleepBackoff` allows fair scheduling; `NextShard` has minimal benefit (all shards on same core) |
| **2** | Limited parallelism - producer contention high | `NextShard` starts showing benefit; `SpinThenYield` can still cause starvation |
| **4** | Moderate parallelism | Strategies start differentiating; `NextShard` shows clear benefit if producers > 4 |
| **0 (default)** | Full parallelism (NumCPU) | Best case for all strategies; `SpinThenYield` excels with dedicated cores |

##### Hypothesis: Results by GOMAXPROCS

| Strategy | GOMAXPROCS=1 | GOMAXPROCS=4 | GOMAXPROCS=default |
|----------|--------------|--------------|-------------------|
| SleepBackoff | Stable, predictable | Good | Good |
| NextShard | Minimal benefit | Moderate benefit | Best for bursts |
| SpinThenYield | May cause starvation | Better | Excellent |
| AdaptiveBackoff | Good for long waits | Good | Good |
| Hybrid | Minimal benefit | Good | Excellent |

### Expected Benchmark Results (Hypothesis)

| Strategy | Uncontended (ns/op) | Contended (ns/op) | P99 Latency | CPU % (contended) |
|----------|---------------------|-------------------|-------------|-------------------|
| SleepBackoff | ~25 | ~100,000+ | High | Low |
| NextShard | ~30 | ~50 | Low | Medium |
| SpinThenYield | ~25 | ~30 | Lowest | Highest |
| AdaptiveBackoff | ~25 | ~50,000+ | Variable | Low |
| Hybrid | ~35 | ~100 | Medium | Medium-High |

**Note**: Actual results will vary based on:
- CPU architecture and cache sizes
- Number of cores and GOMAXPROCS
- Consumer drain rate
- Ring size relative to working set

---

## Implementation Plan

### Phase 1: Core Infrastructure
1. Add `RetryStrategy` type and constants to `ring.go`
2. Add `Writer` type with function dispatch
3. Implement `writerState` for mutable strategy state
4. Add `resolveStrategy()` function

### Phase 2: Building Blocks
1. Implement shard selector functions
2. Implement backoff functions
3. Implement generic `retryLoop` orchestrator

### Phase 3: Strategy Implementations
1. Wire up `SleepBackoff` (existing behavior)
2. Implement `NextShard`
3. Implement `SpinThenYield`
4. Implement remaining strategies

### Phase 4: Testing
1. Unit tests for selectors and backoff functions
2. Integration tests for each strategy
3. Concurrent correctness tests
4. Add benchmark suite

### Phase 5: Documentation & Integration
1. Update `README.md` with strategy selection guide
2. Add `-strategy` flag to `cmd/ring`
3. Add Makefile targets for strategy benchmarks

---

## Potential Additional Strategies

### OverflowShard
Reserve one or more shards as "overflow" that any producer can use when their affinity shard is full. Maintains better locality than full NextShard.

### WorkStealing (Consumer-Side)
Instead of changing producer behavior, have the consumer move items between shards to balance load. Complex but preserves producer affinity.

### PriorityBased
Allow producers to specify priority. High-priority producers get preference or different retry behavior.

### BoundedSpin
Spin for a fixed number of iterations, then yield, then sleep. Combines benefits of spinning and yielding:
```go
func boundedSpinBackoff(config *WriteConfig, state *writerState) bool {
    // Spin for N iterations
    for i := 0; i < 100; i++ {
        // Busy-wait (compiler won't optimize due to atomic)
        _ = atomic.LoadUint64(&spinCounter)
    }
    // Then yield
    runtime.Gosched()
    // Only sleep every Nth backoff
    if state.backoffCount%10 == 0 {
        time.Sleep(config.BackoffDuration)
    }
    return true
}
```

---

## Conclusion

The composable building block design with function dispatch provides:

1. **Zero-overhead strategy selection** - Decision made once at Writer creation
2. **Easy extensibility** - New strategies by combining existing selectors/backoffs
3. **Testability** - Each component tested in isolation
4. **Performance** - No runtime switch, potential for inlining

The `SpinThenYield` strategy is particularly interesting for latency-sensitive applications willing to trade CPU cycles for reduced tail latencies. Combined with `NextShard`, it offers maximum throughput with minimal blocking.

---

## References

- Current implementation: `ring.go:133-156` (WriteWithBackoff)
- Shard selection: `ring.go:107-110` (selectShard)
- Configuration: `ring.go:14-31` (WriteConfig, DefaultWriteConfig)
- Consumer polling: `ring.go:185-192` (TryRead)
- Batch reading: `ring.go:238-254` (ReadBatchInto)
- Existing backoff test: `ring_test.go:403-440` (TestWriteWithBackoff)
- Concurrent test: `ring_test.go:442-520` (TestWriteWithBackoffConcurrent)
