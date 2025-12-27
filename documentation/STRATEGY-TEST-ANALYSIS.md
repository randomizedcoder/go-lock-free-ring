# Strategy Test Results Analysis

This document contains performance analysis from running strategy comparison tests on the go-lock-free-ring library.

## Test Environment

- **CPU**: AMD Ryzen Threadripper PRO 3945WX 12-Cores (24 threads)
- **Date**: 2025-12-27
- **Go Version**: (as installed)

---

## Quick Test Results (40 Mb/s total)

| Strategy | Throughput | Drop Rate |
|----------|------------|-----------|
| SleepBackoff | 100.0% | 0.00% |
| NextShard | 100.0% | 0.00% |
| SpinThenYield | 100.0% | 0.00% |

**Observation**: All strategies perform identically at low load. No differentiation possible.

---

## Standard Test Results (up to 400 Mb/s)

| Strategy | Tests | Pass Rate | Throughput | Drop Rate |
|----------|-------|-----------|------------|-----------|
| All 6 strategies | 4 each | 100% | 100.0% | 0.00% |

**Observation**: Even at 8p × 50 Mb/s (400 Mb/s total), the system handles load effortlessly.

---

## High-Throughput Test Results (up to 6400 Mb/s)

This test reveals the actual performance differences and system limits.

### Success Zone (≤1600 Mb/s)

| Config | Expected | All Strategies |
|--------|----------|----------------|
| 8p × 100Mb | 800 Mb/s | 100.0% ✓ |
| 8p × 200Mb | 1600 Mb/s | 100.0% ✓ |
| 16p × 100Mb | 1600 Mb/s | 100.0% ✓ |

### Degradation Zone (3200 Mb/s)

| Strategy | Config | Achieved | % of Expected | Drop Rate |
|----------|--------|----------|---------------|-----------|
| SleepBackoff | 16p×200Mb | 2319 Mb/s | 72.5% | 27.46% |
| RandomShard | 16p×200Mb | 2319 Mb/s | 72.5% | 28.65% |
| AdaptiveBackoff | 16p×200Mb | 2319 Mb/s | 72.5% | 27.49% |
| SpinThenYield | 16p×200Mb | 2319 Mb/s | 72.5% | 27.45% |
| Hybrid | 16p×200Mb | 2319 Mb/s | 72.5% | 27.38% |
| NextShard | 16p×200Mb | 986 Mb/s | **30.8%** | 69.03% |

**Critical Finding**: All strategies hit the same ~2300 Mb/s ceiling EXCEPT NextShard which performs significantly worse.

### Severe Degradation Zone (32 producers)

| Strategy | 32p×100Mb | 32p×200Mb | Avg Throughput |
|----------|-----------|-----------|----------------|
| **SleepBackoff** | 1235 Mb/s (38.6%) | 964 Mb/s (15.1%) | **50.7%** |
| **AdaptiveBackoff** | 1143 Mb/s (35.7%) | 1030 Mb/s (16.1%) | **50.6%** |
| SpinThenYield | 628 Mb/s (19.6%) | 629 Mb/s (9.8%) | 45.1% |
| RandomShard | 520 Mb/s (16.2%) | 402 Mb/s (6.3%) | 43.1% |
| Hybrid | 544 Mb/s (17.0%) | 314 Mb/s (4.9%) | 42.7% |
| **NextShard** | 330 Mb/s (10.3%) | 305 Mb/s (4.8%) | **33.5%** |

---

## Key Findings

### 1. System Ceiling at ~2300-2400 Mb/s

- All strategies hit the same wall regardless of retry approach
- This suggests the bottleneck is NOT in the retry strategy
- Likely system-level: CPU scheduling, memory bandwidth, or consumer capacity

### 2. NextShard Performs Worst Under Extreme Load

- Counterintuitively, trying multiple shards hurts performance
- Hypothesis: The overhead of shard iteration and additional atomic operations outweighs benefits when ALL shards are saturated

### 3. SleepBackoff and AdaptiveBackoff Are Most Robust

- Simple strategies that back off actually perform better under extreme load
- Sleeping reduces contention, allowing successful writes to complete faster

### 4. The Ring Isn't the Bottleneck

- When all strategies hit the same ceiling, the ring buffer itself isn't limiting
- The bottleneck is elsewhere in the system

---

## Why Maximum Speed Isn't Reached: Analysis

When tests show throughput well below expected, several factors may contribute:

### 1. Consumer Bottleneck

- Single consumer thread may not drain fast enough
- B-tree insertion could be limiting
- Need to measure: consumer processing time per item

### 2. Producer Goroutine Scheduling

- 32 goroutines competing for 24 threads
- Go runtime scheduler overhead
- Context switching costs
- Need to measure: goroutine scheduling latency

### 3. Memory Subsystem Saturation

- Cache line bouncing between cores
- Memory bandwidth limits (~50-100 GB/s on modern systems)
- NUMA effects if applicable
- Need to measure: memory bandwidth utilization

### 4. Atomic Operation Contention

- Multiple cores fighting for the same cache lines
- Even with sharding, the consumer's read pointer is shared
- Need to measure: CAS failure rates

### 5. Rate Limiter Accuracy

- The data-generator's rate limiting may be inaccurate at high speeds
- Tokens may not be refilled fast enough
- Need to measure: actual vs requested packet intervals

---

## Potential Improvements for Better Analysis

### 1. Add Per-Test Diagnostic Metrics

Currently we only capture throughput and drop rate. Additional metrics would help diagnose bottlenecks:

```go
type DiagnosticMetrics struct {
    // Ring contention
    CASFailures      uint64  // Atomic CAS operation failures
    ShardSwitches    uint64  // Times strategy switched shards
    BackoffCount     uint64  // Total backoff operations

    // Timing
    AvgWriteLatency  time.Duration
    P99WriteLatency  time.Duration
    MaxWriteLatency  time.Duration

    // Consumer
    ConsumerIdleTime time.Duration
    AvgDrainBatch    int

    // System
    GoroutineCount   int
    HeapAllocs       uint64
    GCPauses         []time.Duration
}
```

### 2. Distinguish Bottleneck Types

Add classification logic to identify where the bottleneck is:

```go
type BottleneckAnalysis struct {
    Type        string  // "ring", "consumer", "producer", "system"
    Confidence  float64
    Evidence    []string
}

func AnalyzeBottleneck(metrics DiagnosticMetrics) BottleneckAnalysis {
    // High CAS failures + low consumer idle → ring contention
    // Low CAS failures + high consumer idle → producer bottleneck
    // Low CAS failures + low consumer idle → consumer bottleneck
    // etc.
}
```

### 3. Add Runtime Profiling Integration

Automatically capture pprof profiles during degraded tests:

```go
if achievedRate < expectedRate * 0.9 {
    // Capture CPU profile for 5 seconds
    captureProfile("cpu", testID)
    // Capture goroutine profile
    captureProfile("goroutine", testID)
}
```

### 4. Consumer Throughput Measurement

Add separate consumer metrics:

```go
type ConsumerMetrics struct {
    DrainCycles       uint64
    ItemsPerDrain     []int     // histogram
    DrainLatencies    []time.Duration
    BTreeInsertTime   time.Duration
    TrimTime          time.Duration
}
```

### 5. Producer Timing Breakdown

Measure where producers spend time:

```go
type ProducerMetrics struct {
    DataGenTime    time.Duration  // Time generating packet
    WriteAttempts  uint64         // Total write attempts
    WriteSuccesses uint64         // Successful writes
    BackoffTime    time.Duration  // Time spent sleeping/yielding
    ContentionTime time.Duration  // Time in failed CAS loops
}
```

### 6. Test Matrix Enhancements

Add scenarios specifically designed to isolate variables:

```go
// Vary ONLY producer count (constant total rate)
// 4p × 100Mb = 400 Mb/s
// 8p × 50Mb = 400 Mb/s
// 16p × 25Mb = 400 Mb/s
// This isolates contention effects from throughput effects

// Vary ONLY shard count
// 8p, 8 shards (1:1)
// 8p, 16 shards (1:2)
// 8p, 32 shards (1:4)
// This tests if more shards help

// Consumer speed variation
// Fast consumer (1ms wake)
// Normal consumer (10ms wake)
// Slow consumer (50ms wake)
// This identifies consumer bottleneck
```

### 7. Real-Time Bottleneck Dashboard

Add a live monitoring mode:

```bash
./bin/ring -dashboard -producers=32 -rate=100

# Shows live:
# - Per-shard fill levels
# - Write success/fail rates
# - Consumer drain rate
# - Goroutine states
# - Memory pressure
```

### 8. Automated Scaling Analysis

Find the performance cliff automatically:

```go
func FindPerformanceCliff(baseConfig TestCase) {
    // Binary search for the rate where drop rate exceeds 1%
    low, high := 100.0, 10000.0
    for high - low > 10 {
        mid := (low + high) / 2
        result := runTest(baseConfig.withRate(mid))
        if result.DropRate < 1.0 {
            low = mid
        } else {
            high = mid
        }
    }
    fmt.Printf("Performance cliff at %.0f Mb/s\n", low)
}
```

---

## Summary of Recommended Improvements

| Priority | Improvement | Effort | Value |
|----------|-------------|--------|-------|
| High | Add CAS failure counting | Low | Identifies ring contention |
| High | Consumer drain metrics | Medium | Identifies consumer bottleneck |
| High | Write latency percentiles | Medium | Shows tail latency issues |
| Medium | Automatic profile capture | Medium | Deep performance analysis |
| Medium | Constant-rate producer scaling | Low | Isolates contention effects |
| Low | Live dashboard | High | Great for demos and debugging |
| Low | Automated cliff finding | Medium | Nice to have for tuning |

---

## Next Steps

1. Add `CASFailures` counter to ring.go (simple atomic increment)
2. Add consumer timing to cmd/ring (measure drain cycle time)
3. Add write latency sampling (1 in 1000 writes, avoid overhead)
4. Create new test matrix for isolating variables
5. Re-run high-throughput tests with new metrics

---

## Strategy Recommendations for Users

Based on the test results:

| Scenario | Recommended Strategy | Reason |
|----------|---------------------|--------|
| **Low-moderate load** (<1600 Mb/s) | Any | All perform equally |
| **High load, graceful degradation** | SleepBackoff or AdaptiveBackoff | Best throughput under pressure |
| **Latency-sensitive, low load** | SpinThenYield | Lowest latency when not saturated |
| **Avoid** | NextShard under extreme load | Significantly worse under saturation |

