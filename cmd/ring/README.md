# cmd/ring/ring.go Design Document

This document describes the design for `ring.go`, an example program demonstrating the lock-free sharded ring buffer library.

## Overview

The program simulates a high-throughput packet processing pipeline:
- **Multiple producers** generate packets at configurable rates (Mb/s)
- **Lock-free ring** buffers packets without mutex contention
- **Single consumer** wakes periodically to batch-read packets
- **B-tree** stores packets sorted by sequence number as a bounded rolling window

This pattern is typical for network packet capture/analysis (RTP, SRT) where packets arrive at high rates but can be processed in batches. The global sequence number simulates real protocol sequence numbers.

## Architecture

```
┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│ Producer 0  │  │ Producer 1  │  │ Producer N  │
│ (rate Mb/s) │  │ (rate Mb/s) │  │ (rate Mb/s) │
└──────┬──────┘  └──────┬──────┘  └──────┬──────┘
       │                │                │
       │ WriteWithBackoff()              │
       ▼                ▼                ▼
┌─────────────────────────────────────────────────┐
│              ShardedRing (lock-free)            │
│  ┌─────────┐ ┌─────────┐ ┌─────────┐           │
│  │ Shard 0 │ │ Shard 1 │ │ Shard N │           │
│  └─────────┘ └─────────┘ └─────────┘           │
└─────────────────────┬───────────────────────────┘
                      │
                      │   ReadBatch() every N ms
                      ▼
              ┌───────────────┐
              │   Consumer    │
              │  (1 worker)   │
              └───────┬───────┘
                      │
                      │   Insert by sequence
                      ▼
              ┌───────────────┐
              │    B-Tree     │
              │(rolling window│
              │ evicts oldest)│
              └───────────────┘
```

## Command Line Interface

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-packetSize` | int | 1450 | Packet payload size in bytes |
| `-rate` | float64 | 10.0 | Target rate per producer in Mb/s |
| `-producers` | int | 4 | Number of producer goroutines |
| `-ringSize` | int | 0 | Ring capacity (0 = auto-calculate based on rate/frequency) |
| `-ringShards` | int | 4 | Number of ring shards (must be power of 2) |
| `-sizeMultiplier` | float64 | 2.0 | Safety multiplier for auto ring size calculation |
| `-btreeSize` | int | 2000 | Maximum packets to keep in B-tree (rolling window) |
| `-btreeDegree` | int | 32 | B-tree degree (branching factor) |
| `-frequency` | int | 10 | Consumer wake interval in milliseconds |
| `-backoff` | duration | 10ms | Backoff sleep duration when ring is full |
| `-maxRetries` | int | 10 | Write retries before backoff sleep |
| `-maxBackoffs` | int | 100 | Maximum backoff attempts before dropping |
| `-statsInterval` | int | 1 | Statistics logging interval in seconds |
| `-duration` | duration | 0 | Run duration (0 = run until SIGINT/SIGTERM) |
| `-profile` | string | "" | Profiling mode (cpu, mem, allocs, heap, mutex, block, trace) |
| `-profilepath` | string | "." | Directory for profile output |
| `-debugLevel` | int | 3 | Log verbosity (0=silent, 7=trace) |

## Automatic Ring Size Calculation

When `-ringSize=0` (default), the program automatically calculates an optimal ring size based on the producer rate and consumer frequency.

### Formula

```
packetsPerSecPerProducer = (rateMbps × 1,000,000) / (8 × packetSize)
totalPacketsPerSec       = packetsPerSecPerProducer × producers
consumerIntervalSec      = frequencyMs / 1000
packetsPerInterval       = totalPacketsPerSec × consumerIntervalSec
ringSize                 = packetsPerInterval × sizeMultiplier
finalSize                = nextPowerOf2(max(ringSize, 64))
```

### Rationale

- **packetsPerInterval**: The expected number of packets that accumulate between consumer wakes
- **sizeMultiplier** (default 2.0): Safety margin for:
  - Timing jitter in producer/consumer scheduling
  - Burst traffic variations
  - Consumer processing time
- **nextPowerOf2**: Efficient modulo operations in ring buffer indexing
- **minimum 64**: Ensures reasonable buffer even at very low rates

### Implementation

```go
// CalculateRingSize computes optimal ring size based on traffic parameters.
// Returns the calculated size rounded up to the next power of 2, minimum 64.
func CalculateRingSize(producers int, rateMbps float64, packetSize int,
                       frequencyMs int, sizeMultiplier float64) int {
    // Packets per second per producer
    packetsPerSecPerProducer := (rateMbps * 1_000_000) / (8.0 * float64(packetSize))

    // Total packets per second from all producers
    totalPacketsPerSec := packetsPerSecPerProducer * float64(producers)

    // Packets that accumulate between consumer wakes
    consumerIntervalSec := float64(frequencyMs) / 1000.0
    packetsPerInterval := totalPacketsPerSec * consumerIntervalSec

    // Apply safety multiplier
    targetSize := packetsPerInterval * sizeMultiplier

    // Minimum size
    if targetSize < 64 {
        targetSize = 64
    }

    // Round up to next power of 2
    return nextPowerOf2(int(math.Ceil(targetSize)))
}

func nextPowerOf2(n int) int {
    if n <= 0 {
        return 1
    }
    n--
    n |= n >> 1
    n |= n >> 2
    n |= n >> 4
    n |= n >> 8
    n |= n >> 16
    n |= n >> 32
    return n + 1
}
```

### Example Calculations

| producers | rate (Mb/s) | packetSize | frequency (ms) | pkt/interval | ×2.0 | final size |
|-----------|-------------|------------|----------------|--------------|------|------------|
| 4 | 1 | 1450 | 10 | 3.4 | 6.9 | **64** (min) |
| 4 | 10 | 1450 | 10 | 34.5 | 69 | **128** |
| 4 | 50 | 1450 | 10 | 172 | 345 | **512** |
| 8 | 50 | 1450 | 10 | 345 | 690 | **1024** |
| 16 | 100 | 1450 | 10 | 1379 | 2758 | **4096** |
| 4 | 10 | 1450 | 50 | 172 | 345 | **512** |
| 4 | 10 | 1450 | 100 | 345 | 690 | **1024** |
| 32 | 100 | 1450 | 5 | 1379 | 2758 | **4096** |

### Usage Examples

```bash
# Auto-calculate ring size (default -ringSize=0)
./ring -producers=4 -rate=10 -frequency=10
# Calculated: 128 slots

# Higher rate needs bigger ring
./ring -producers=8 -rate=50 -frequency=10
# Calculated: 1024 slots

# Longer consumer interval needs bigger ring
./ring -producers=4 -rate=10 -frequency=100
# Calculated: 1024 slots

# Override with explicit size
./ring -producers=4 -rate=10 -ringSize=2048
# Uses: 2048 slots (user-specified)

# Increase safety margin
./ring -producers=4 -rate=10 -frequency=10 -sizeMultiplier=4.0
# Calculated: 256 slots (4x instead of 2x)
```

### Logging

When auto-calculating, the program logs the decision:
```
Ring size auto-calculated: 128 (4 producers × 10.0 Mb/s, 10ms interval, 2.0x multiplier)
```

When user-specified:
```
Ring size: 2048 (user-specified)
```

## Data Structures

### Packet

```go
type Packet struct {
    Sequence uint64   // Global monotonic sequence number (like RTP/SRT)
    Data     *[]byte  // Pointer to payload (from bufPool)
}

// Implement btree.Item interface for sorting by sequence
func (p *Packet) Less(than btree.Item) bool {
    return p.Sequence < than.(*Packet).Sequence
}
```

### Object Pools

Two sync.Pools minimize allocations in the hot path:

```go
// Buffer pool for packet payloads
var bufPool = sync.Pool{
    New: func() any {
        b := make([]byte, packetSize)
        // Fill with deterministic non-zero pattern
        for i := range b {
            b[i] = byte((i % 255) + 1)
        }
        return &b
    },
}

// Packet pool for Packet structs
var pktPool = sync.Pool{
    New: func() any {
        return &Packet{}
    },
}
```

### Write Configuration

```go
var writeConfig = ring.WriteConfig{
    MaxRetries:      *maxRetries,     // default: 10
    BackoffDuration: *backoff,         // default: 10ms
    MaxBackoffs:     *maxBackoffs,     // default: 100
}
```

## Component Design

### Producer Goroutines

Each producer:
1. Calculates packets/second from `rate` Mb/s and `packetSize`
2. Uses time-based pacing for high rates (>1000 pkt/s)
3. Gets buffer from `bufPool` and packet from `pktPool`
4. Assigns sequence number from global atomic counter
5. Writes packet using `ring.WriteWithBackoff(producerID, packet, config)`
6. On final failure: returns buffer and packet to pools, increments drop counter

```go
func producer(ctx context.Context, wg *sync.WaitGroup, id uint64,
              ring *ring.ShardedRing, seq *atomic.Uint64) {
    defer wg.Done()

    // Calculate pacing
    packetsPerSec := (*rate * 1_000_000) / (8.0 * float64(*packetSize))
    intervalNanos := int64(float64(time.Second) / packetsPerSec)
    startTime := time.Now().UnixNano()
    var packetCount uint64

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        // Get from pools
        pkt := pktPool.Get().(*Packet)
        pkt.Sequence = seq.Add(1)
        buf := bufPool.Get().(*[]byte)
        pkt.Data = buf

        // Write with backoff
        if !ring.WriteWithBackoff(id, pkt, writeConfig) {
            // Failed after all retries - drop packet
            bufPool.Put(buf)
            pkt.Data = nil
            pktPool.Put(pkt)
            droppedCount.Add(1)
        } else {
            producedCount.Add(1)
        }

        // Pace to target rate
        packetCount++
        expectedTime := startTime + int64(packetCount)*intervalNanos
        for time.Now().UnixNano() < expectedTime {
            // busy-wait for high-rate pacing
        }
    }
}
```

### Consumer Worker

Single goroutine that:
1. Wakes every `frequency` milliseconds via `time.Ticker`
2. Calls `ring.ReadBatch(maxBatch)` to get pending packets
3. Inserts each packet into B-tree (sorted by sequence)
4. Trims B-tree if size exceeds `btreeSize` (evicts oldest)
5. Returns evicted buffers and packets to pools
6. Logs stats every `statsInterval` seconds

```go
func consumer(ctx context.Context, wg *sync.WaitGroup,
              ring *ring.ShardedRing, tree *btree.BTree) {
    defer wg.Done()

    ticker := time.NewTicker(time.Duration(*frequency) * time.Millisecond)
    defer ticker.Stop()

    statsTicker := time.NewTicker(time.Duration(*statsInterval) * time.Second)
    defer statsTicker.Stop()

    var insertedThisPeriod, trimmedThisPeriod uint64

    for {
        select {
        case <-ctx.Done():
            // Graceful shutdown: drain remaining items from ring
            drainRing(ring, tree)
            return

        case <-ticker.C:
            items := ring.ReadBatch(1000)
            for _, item := range items {
                pkt := item.(*Packet)
                tree.ReplaceOrInsert(pkt)
                insertedThisPeriod++
                consumedCount.Add(1)
            }

            // Trim oldest if over capacity (rolling window)
            for tree.Len() > *btreeSize {
                oldest := tree.DeleteMin()
                if oldest != nil {
                    pkt := oldest.(*Packet)
                    bufPool.Put(pkt.Data)
                    pkt.Data = nil
                    pktPool.Put(pkt)
                    trimmedThisPeriod++
                    trimmedCount.Add(1)
                }
            }

        case <-statsTicker.C:
            if *debugLevel >= 3 {
                log.Printf("[stats] inserted=%d trimmed=%d btree=%d ring=%d/%d",
                    insertedThisPeriod, trimmedThisPeriod,
                    tree.Len(), ring.Len(), ring.Cap())
            }
            insertedThisPeriod = 0
            trimmedThisPeriod = 0
        }
    }
}

func drainRing(ring *ring.ShardedRing, tree *btree.BTree) {
    for {
        items := ring.ReadBatch(1000)
        if len(items) == 0 {
            break
        }
        for _, item := range items {
            pkt := item.(*Packet)
            tree.ReplaceOrInsert(pkt)
            consumedCount.Add(1)
        }
    }
}
```

### Main Function

```go
var (
    // Flags
    packetSize     = flag.Int("packetSize", 1450, "Packet payload size in bytes")
    rate           = flag.Float64("rate", 10.0, "Target rate per producer in Mb/s")
    producers      = flag.Int("producers", 4, "Number of producer goroutines")
    ringSize       = flag.Int("ringSize", 0, "Ring capacity (0=auto-calculate)")
    ringShards     = flag.Int("ringShards", 4, "Number of ring shards (power of 2)")
    sizeMultiplier = flag.Float64("sizeMultiplier", 2.0, "Safety multiplier for auto ring size")
    btreeSize      = flag.Int("btreeSize", 2000, "Maximum packets in B-tree")
    btreeDegree    = flag.Int("btreeDegree", 32, "B-tree branching factor")
    frequency      = flag.Int("frequency", 10, "Consumer wake interval (ms)")
    backoff        = flag.Duration("backoff", 10*time.Millisecond, "Backoff duration")
    maxRetries     = flag.Int("maxRetries", 10, "Retries before backoff")
    maxBackoffs   = flag.Int("maxBackoffs", 100, "Max backoffs before drop")
    statsInterval = flag.Int("statsInterval", 1, "Stats logging interval (seconds)")
    duration      = flag.Duration("duration", 0, "Run duration (0=until signal)")
    profileFlag   = flag.String("profile", "", "Profiling mode")
    profilePath   = flag.String("profilepath", ".", "Profile output directory")
    debugLevel    = flag.Int("debugLevel", 3, "Log verbosity (0-7)")

    // Global counters (atomic)
    producedCount atomic.Uint64
    droppedCount  atomic.Uint64
    consumedCount atomic.Uint64
    trimmedCount  atomic.Uint64
)

func main() {
    flag.Parse()

    if err := validateFlags(); err != nil {
        log.Fatalf("Invalid flags: %v", err)
    }

    // Setup profiling (if requested)
    var prof interface{ Stop() }
    if *profileFlag != "" {
        prof = setupProfiler(*profileFlag, *profilePath)
        defer prof.Stop()
    }

    // Setup signal handling for graceful shutdown
    ctx, stop := signal.NotifyContext(context.Background(),
        os.Interrupt, syscall.SIGTERM)
    defer stop()

    // Determine ring size (auto-calculate or user-specified)
    actualRingSize := *ringSize
    if actualRingSize == 0 {
        actualRingSize = CalculateRingSize(*producers, *rate, *packetSize,
                                           *frequency, *sizeMultiplier)
        log.Printf("Ring size auto-calculated: %d (%d producers × %.1f Mb/s, %dms interval, %.1fx multiplier)",
            actualRingSize, *producers, *rate, *frequency, *sizeMultiplier)
    } else {
        log.Printf("Ring size: %d (user-specified)", actualRingSize)
    }

    // Create ring buffer
    r, err := ring.NewShardedRing(*ringShards, actualRingSize)
    if err != nil {
        log.Fatalf("Failed to create ring: %v", err)
    }

    // Create B-tree
    tree := btree.New(*btreeDegree)

    // Create write config
    writeConfig = ring.WriteConfig{
        MaxRetries:      *maxRetries,
        BackoffDuration: *backoff,
        MaxBackoffs:     *maxBackoffs,
    }

    var wg sync.WaitGroup

    // Start consumer FIRST (so it's ready when producers start)
    wg.Add(1)
    go consumer(ctx, &wg, r, tree)

    // Start producers
    var seq atomic.Uint64
    for i := 0; i < *producers; i++ {
        wg.Add(1)
        go producer(ctx, &wg, uint64(i), r, &seq)
    }

    // Handle duration or wait for signal
    if *duration > 0 {
        select {
        case <-time.After(*duration):
            log.Printf("Duration %v reached, shutting down...", *duration)
            stop()
        case <-ctx.Done():
            // Signal received before duration
        }
    } else {
        <-ctx.Done()
    }

    log.Println("Shutdown signal received, waiting for goroutines...")
    wg.Wait()

    // Print final stats
    printFinalStats(tree)
}

func validateFlags() error {
    if *packetSize <= 0 {
        return fmt.Errorf("packetSize must be positive")
    }
    if *rate <= 0 {
        return fmt.Errorf("rate must be positive")
    }
    if *producers <= 0 {
        return fmt.Errorf("producers must be positive")
    }
    // ringShards power-of-2 check done by ring.NewShardedRing
    return nil
}

func printFinalStats(tree *btree.BTree) {
    produced := producedCount.Load()
    dropped := droppedCount.Load()
    consumed := consumedCount.Load()
    trimmed := trimmedCount.Load()

    log.Printf("=== Final Statistics ===")
    log.Printf("Produced: %d", produced)
    log.Printf("Dropped:  %d (%.2f%%)", dropped,
        float64(dropped)/float64(produced+dropped)*100)
    log.Printf("Consumed: %d", consumed)
    log.Printf("Trimmed:  %d", trimmed)
    log.Printf("BTree final size: %d", tree.Len())
}
```

## Rate Limiting Strategy

Following the data-generator pattern:

| Rate | Packets/sec | Strategy |
|------|-------------|----------|
| < 1 Mb/s | < 100 | `time.Sleep` acceptable |
| 1-10 Mb/s | 100-1000 | Busy-wait pacing |
| > 10 Mb/s | > 1000 | Busy-wait pacing (tight loop) |

Calculation:
```
packetsPerSec = (rate_mbps * 1,000,000) / (8 * packetSize)

Example: 10 Mb/s with 1450-byte packets
packetsPerSec = (10 * 1,000,000) / (8 * 1450) = 862 packets/sec
interval = 1.16 ms per packet
```

## Memory Management

### Allocation Strategy

| Component | Allocation | Reuse Strategy |
|-----------|------------|----------------|
| Packet payloads (`*[]byte`) | `bufPool.Get()` | `bufPool.Put()` on B-tree trim |
| Packet structs (`*Packet`) | `pktPool.Get()` | `pktPool.Put()` on B-tree trim |
| ReadBatch slice | Per-call | Could optimize with `ReadBatchInto` |

### Pool Lifecycle

```
Producer:
  bufPool.Get() → pktPool.Get() → ring.Write() → [packet in ring]

Consumer:
  ring.ReadBatch() → [packet in btree] → (on trim) → bufPool.Put() + pktPool.Put()
```

### Memory Bounds

| Component | Size Formula | Example (defaults) |
|-----------|--------------|-------------------|
| Ring | `ringSize × sizeof(slot)` ≈ `ringSize × 24` | 1024 × 24 = 24 KB |
| B-tree | `btreeSize × (16 + packetSize)` | 2000 × 1466 = 2.9 MB |
| Pools | Grows/shrinks with demand | Variable |

## Metrics and Logging

### Atomic Counters

| Counter | Description |
|---------|-------------|
| `producedCount` | Packets successfully written to ring |
| `droppedCount` | Packets dropped (ring full after all retries) |
| `consumedCount` | Packets read from ring |
| `trimmedCount` | Packets evicted from B-tree |

### Periodic Stats (every `statsInterval` seconds)

```
[stats] inserted=862 trimmed=0 btree=1724 ring=0/1024
```

### Final Summary

```
=== Final Statistics ===
Produced: 86200
Dropped:  0 (0.00%)
Consumed: 86200
Trimmed:  84200
BTree final size: 2000
```

## Graceful Shutdown

Shutdown sequence:
1. SIGINT/SIGTERM or duration expires → `stop()` cancels context
2. Producers check `ctx.Done()` and exit their loops, call `wg.Done()`
3. Consumer checks `ctx.Done()`, drains remaining ring items, calls `wg.Done()`
4. `wg.Wait()` blocks until all goroutines complete
5. Final stats printed

## Profiling Support

```go
func setupProfiler(mode, path string) interface{ Stop() } {
    var p func(*profile.Profile)
    switch mode {
    case "cpu":
        p = profile.CPUProfile
    case "mem":
        p = profile.MemProfile
    case "allocs":
        p = profile.MemProfileAllocs
    case "heap":
        p = profile.MemProfileHeap
    case "rate":
        p = profile.MemProfileRate(2048)
    case "mutex":
        p = profile.MutexProfile
    case "block":
        p = profile.BlockProfile
    case "thread":
        p = profile.ThreadcreationProfile
    case "trace":
        p = profile.TraceProfile
    default:
        log.Printf("Unknown profile mode: %s", mode)
        return nil
    }
    return profile.Start(profile.ProfilePath(path), profile.NoShutdownHook, p)
}
```

## Dependencies

```go
import (
    "github.com/google/btree"       // B-tree for sorted storage
    "github.com/pkg/profile"        // pprof wrapper
    ring "github.com/randomizedcoder/go-lock-free-ring"
)
```

Add to `go.mod`:
```
require (
    github.com/google/btree v1.1.2
    github.com/pkg/profile v1.7.0
)
```

## Tests for Ring Size Calculation

The `CalculateRingSize` function requires comprehensive tests:

### Test Cases

```go
func TestCalculateRingSize(t *testing.T) {
    tests := []struct {
        name           string
        producers      int
        rateMbps       float64
        packetSize     int
        frequencyMs    int
        sizeMultiplier float64
        want           int
    }{
        // Minimum size enforcement
        {
            name: "very low rate returns minimum 64",
            producers: 1, rateMbps: 0.1, packetSize: 1450,
            frequencyMs: 10, sizeMultiplier: 2.0,
            want: 64,
        },
        // Standard calculations
        {
            name: "4 producers 10Mbps 10ms",
            producers: 4, rateMbps: 10.0, packetSize: 1450,
            frequencyMs: 10, sizeMultiplier: 2.0,
            want: 128,
        },
        {
            name: "4 producers 50Mbps 10ms",
            producers: 4, rateMbps: 50.0, packetSize: 1450,
            frequencyMs: 10, sizeMultiplier: 2.0,
            want: 512,
        },
        {
            name: "8 producers 50Mbps 10ms",
            producers: 8, rateMbps: 50.0, packetSize: 1450,
            frequencyMs: 10, sizeMultiplier: 2.0,
            want: 1024,
        },
        {
            name: "16 producers 100Mbps 10ms",
            producers: 16, rateMbps: 100.0, packetSize: 1450,
            frequencyMs: 10, sizeMultiplier: 2.0,
            want: 4096,
        },
        // Longer consumer interval
        {
            name: "4 producers 10Mbps 100ms (10x interval)",
            producers: 4, rateMbps: 10.0, packetSize: 1450,
            frequencyMs: 100, sizeMultiplier: 2.0,
            want: 1024,
        },
        // Different multiplier
        {
            name: "4 producers 10Mbps 10ms 4x multiplier",
            producers: 4, rateMbps: 10.0, packetSize: 1450,
            frequencyMs: 10, sizeMultiplier: 4.0,
            want: 256,
        },
        // Small packet size (more packets per Mb)
        {
            name: "small packets 500 bytes",
            producers: 4, rateMbps: 10.0, packetSize: 500,
            frequencyMs: 10, sizeMultiplier: 2.0,
            want: 256,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := CalculateRingSize(tt.producers, tt.rateMbps, tt.packetSize,
                                     tt.frequencyMs, tt.sizeMultiplier)
            if got != tt.want {
                t.Errorf("CalculateRingSize() = %d, want %d", got, tt.want)
            }
        })
    }
}

func TestNextPowerOf2(t *testing.T) {
    tests := []struct {
        input int
        want  int
    }{
        {0, 1},
        {1, 1},
        {2, 2},
        {3, 4},
        {5, 8},
        {63, 64},
        {64, 64},
        {65, 128},
        {1000, 1024},
        {1025, 2048},
    }

    for _, tt := range tests {
        t.Run(fmt.Sprintf("input_%d", tt.input), func(t *testing.T) {
            got := nextPowerOf2(tt.input)
            if got != tt.want {
                t.Errorf("nextPowerOf2(%d) = %d, want %d", tt.input, got, tt.want)
            }
        })
    }
}
```

### Edge Case Tests

```go
func TestCalculateRingSizeEdgeCases(t *testing.T) {
    // Zero producers should still return minimum
    size := CalculateRingSize(0, 10.0, 1450, 10, 2.0)
    if size != 64 {
        t.Errorf("zero producers: got %d, want 64", size)
    }

    // Very high rate
    size = CalculateRingSize(32, 1000.0, 1450, 10, 2.0)
    if size < 32768 {
        t.Errorf("very high rate: got %d, want >= 32768", size)
    }

    // Result is always power of 2
    for _, n := range []int{64, 128, 256, 512, 1024, 2048, 4096} {
        if n&(n-1) != 0 {
            t.Errorf("%d is not a power of 2", n)
        }
    }
}
```

## Usage Examples

```bash
# Auto-calculate ring size (default -ringSize=0)
./ring -producers=4 -rate=10 -duration=10s
# Output: Ring size auto-calculated: 128

# High throughput with auto-sizing
./ring -producers=8 -rate=50 -duration=30s
# Output: Ring size auto-calculated: 1024

# Override with explicit size
./ring -producers=8 -rate=50 -ringSize=16384 -ringShards=8 -btreeSize=10000 -duration=30s
# Output: Ring size: 16384 (user-specified)

# Higher safety margin for bursty traffic
./ring -producers=4 -rate=10 -sizeMultiplier=4.0 -duration=10s
# Output: Ring size auto-calculated: 256 (4.0x multiplier)

# Stress test with aggressive backoff settings
./ring -producers=16 -rate=100 -backoff=1ms -maxRetries=5 -maxBackoffs=50 -duration=60s

# With CPU profiling
./ring -producers=4 -rate=10 -duration=30s -profile=cpu -profilepath=./profiles

# Debug mode with verbose logging
./ring -producers=2 -rate=1 -debugLevel=7 -duration=10s -statsInterval=1

# Run until interrupted (Ctrl+C)
./ring -producers=4 -rate=10
```

## Test Configurations

Reference configurations for integration tests (using auto ring sizing):

| Test | packetSize | rate | producers | ringSize | expected auto | ringShards | btreeSize | frequency |
|------|------------|------|-----------|----------|---------------|------------|-----------|-----------|
| test0 | 1450 | 1 | 4 | 0 (auto) | 64 | 4 | 2000 | 50 |
| test1 | 1450 | 10 | 4 | 0 (auto) | 128 | 4 | 2000 | 10 |
| test2 | 1450 | 50 | 4 | 0 (auto) | 512 | 4 | 2000 | 10 |
| test3 | 1450 | 50 | 10 | 0 (auto) | 1024 | 8 | 2000 | 10 |
| test4 | 1450 | 100 | 16 | 0 (auto) | 4096 | 16 | 5000 | 10 |
