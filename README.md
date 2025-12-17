# go-lock-free-ring

Simple example of a lock free ring library written in golang.

ring.go is an example program using the ring library with integration tests to demonstrate the performance.

<img src="./go-lock-free-ring.png" alt="go-lock-free-ring" width="80%" height="80%"/>

## Background - Lock free rings
There are lots of lock free rings, where Herb does a good job explaining them in this video

e.g.
CppCon 2014: Herb Sutter "Lock-Free Programming (or, Juggling Razor Blades), Part I"
https://youtu.be/c1gO9aB9nbs?si=K2y67zBI8HGfmFHF

## Inspiration

To implment this found this blog, so I gave it a quick shot

https://congdong007.github.io/2025/08/16/Lock-Free-golang/

From the blog:
```bash
Fundamental Concepts

    Ring Buffer
        A ring buffer (or circular buffer) is a fixed-size queue in which the write and read pointers wrap around once they reach the end of the underlying array. It is widely used in high-performance systems such as logging pipelines, network buffers, and message queues.

    MPSC (Multi-Producer, Single-Consumer)

        An MPSC queue allows multiple producers (writers) to insert elements concurrently, while only a single consumer (reader) retrieves elements.

        Write operations must address concurrent access among producers.

        Read operations are simpler, as they are performed by a single consumer thread.

    Lock-Free
        Instead of using traditional mutexes, lock-free structures rely on atomic operations (e.g., Compare-And-Swap, CAS) to ensure correctness under concurrency. This avoids lock contention, reducing latency and improving throughput.

    Sharded
        A sharded design partitions a large ring buffer into several smaller independent sub-buffers (shards). Producers are distributed across shards (e.g., by hashing or thread affinity), which minimizes contention. The consumer then sequentially or cyclically retrieves items from all shards.

Operating Principles

A sharded lock-free MPSC ring buffer typically operates as follows:

    Initialization

        The total buffer capacity is divided into N shards (e.g., 8 sub-buffers).

        Each shard itself is implemented as a lock-free MPSC ring buffer.

    Producer Writes (Concurrent)

        Each producer selects a shard based on a hash function, producer ID, or randomized strategy.

        The producer atomically advances the write pointer in that shard using CAS and writes its data.

        Because writes are distributed across shards, contention is significantly reduced.

    Consumer Reads (Single Thread)

        The consumer iterates over all shards, checking each for available entries.

        Data is retrieved by advancing the shard’s read pointer.

        As only one consumer exists, no synchronization overhead is required for reading.

Motivation for Sharding

    Challenge with Conventional MPSC Buffers
        In a non-sharded MPSC buffer, all producers compete on a single shared write pointer, resulting in substantial contention under high concurrency.

    Benefits of Sharding

        Each shard has fewer competing producers, reducing contention on its write pointer.

        The single consumer can still process data deterministically by scanning all shards.

        Performance gains are particularly evident when the number of producers is large relative to the consumer.

Application Scenarios

    High-performance logging systems (multiple threads writing logs, one thread persisting to storage).

    Network servers (multiple connections producing packets, one thread aggregating and processing them).

    Data acquisition systems (multiple sensors producing input, one thread consuming for analysis).

Implementation Notes in Golang

    In Go, the sync/atomic package is typically used for lock-free synchronization.
    Key implementation aspects include:

    Write Operation

idx := atomic.AddUint64(&shard.writePos, 1) - 1
buffer[idx % shard.size] = value

    Read Operation (single-threaded, no CAS required)

if shard.readPos < shard.writePos {
    val := buffer[shard.readPos % shard.size]
    shard.readPos++
    return val
}

    Shard Selection

shardID := hash(producerID) % numShards
shard := shards[shardID]

    Consumer Loop

for {
    for _, shard := range shards {
        if val, ok := shard.TryRead(); ok {
            process(val)
        }
    }
}

Advantages and Limitations

    Advantages

        Lock-free design avoids mutex contention.

        Sharding reduces producer contention and increases throughput.

        Single-consumer semantics simplify design and maintain order within each shard.

    Limitations

        The consumer must poll multiple shards, which may increase latency with many shards.

        Buffer utilization may be uneven if some shards are heavily loaded while others remain idle.

        The design does not extend naturally to multiple consumers.


```

## Ring Library Design

This section describes the detailed implementation of the `ring.go` lock-free ring buffer library.

### Architecture Overview

The library implements a **Sharded Lock-Free MPSC (Multi-Producer, Single-Consumer) Ring Buffer**. The design partitions a single large ring buffer into multiple independent shards, where each shard is itself a lock-free circular buffer. This approach dramatically reduces write contention among producers while maintaining simplicity for the single consumer.

```
┌─────────────────────────────────────────────────────────────────┐
│                      ShardedRing                                │
│  ┌─────────┐  ┌─────────┐  ┌─────────┐       ┌─────────┐       │
│  │ Shard 0 │  │ Shard 1 │  │ Shard 2 │  ...  │ Shard N │       │
│  │ ┌─────┐ │  │ ┌─────┐ │  │ ┌─────┐ │       │ ┌─────┐ │       │
│  │ │ buf │ │  │ │ buf │ │  │ │ buf │ │       │ │ buf │ │       │
│  │ │ [0] │ │  │ │ [0] │ │  │ │ [0] │ │       │ │ [0] │ │       │
│  │ │ [1] │ │  │ │ [1] │ │  │ │ [1] │ │       │ │ [1] │ │       │
│  │ │ ... │ │  │ │ ... │ │  │ │ ... │ │       │ │ ... │ │       │
│  │ │ [n] │ │  │ │ [n] │ │  │ │ [n] │ │       │ │ [n] │ │       │
│  │ └─────┘ │  │ └─────┘ │  │ └─────┘ │       │ └─────┘ │       │
│  │writePos │  │writePos │  │writePos │       │writePos │       │
│  │readPos  │  │readPos  │  │readPos  │       │readPos  │       │
│  └─────────┘  └─────────┘  └─────────┘       └─────────┘       │
└─────────────────────────────────────────────────────────────────┘
        ▲               ▲               ▲               ▲
        │               │               │               │
   Producer 0      Producer 1      Producer 2      Producer N
   (hash → 0)      (hash → 1)      (hash → 2)      (hash → N)

                              │
                              ▼
                    ┌─────────────────┐
                    │  Single Reader  │
                    │  (polls all     │
                    │   shards)       │
                    └─────────────────┘
```

### Data Structures

#### Shard Structure

Each shard is an independent lock-free ring buffer:

```go
type Shard struct {
    buffer   []any      // circular buffer storage
    size     uint64     // capacity of this shard
    writePos uint64     // atomic: next write position (monotonically increasing)
    readPos  uint64     // read position (only accessed by consumer)
    _        [56]byte   // cache line padding to prevent false sharing
}
```

**Cache Line Padding**: The padding field ensures that each shard's hot variables (`writePos`, `readPos`) reside on separate cache lines (typically 64 bytes). This prevents false sharing, where multiple CPU cores invalidate each other's caches when accessing adjacent memory locations.

#### ShardedRing Structure

The main ring structure manages multiple shards:

```go
type ShardedRing struct {
    shards    []*Shard  // array of shard pointers
    numShards uint64    // number of shards (power of 2 recommended)
    mask      uint64    // numShards - 1, for fast modulo via bitwise AND
}
```

### API Design

#### Constructor

```go
func NewShardedRing(totalCapacity uint64, numShards uint64) *ShardedRing
```

- `totalCapacity`: Total number of items the ring can hold across all shards
- `numShards`: Number of shards to partition the ring into (should be power of 2)
- Each shard capacity = `totalCapacity / numShards`

#### Producer Interface

```go
func (r *ShardedRing) Write(producerID uint64, value any) bool
```

- `producerID`: Unique identifier for the producer (used for shard selection)
- `value`: The data to write into the ring
- Returns `true` on success, `false` if the selected shard is full

#### Consumer Interface

```go
func (r *ShardedRing) TryRead() (any, bool)
```

- Attempts to read one item from any shard
- Returns the value and `true` if an item was read
- Returns `nil` and `false` if all shards are empty

```go
func (r *ShardedRing) ReadBatch(maxItems int) []any
```

- Reads up to `maxItems` from all shards in a round-robin fashion
- Returns a slice of items read (may be empty if ring is empty)
- More efficient for batch processing scenarios

### Implementation Details

#### Write Operation (Lock-Free)

The write operation uses atomic Compare-And-Swap (CAS) semantics via `atomic.AddUint64`:

```go
func (s *Shard) Write(value any) bool {
    // Atomically claim the next write slot
    pos := atomic.AddUint64(&s.writePos, 1) - 1

    // Check for buffer overflow (write catching up to read)
    // Note: readPos is atomically loaded since producers read it while consumer writes it
    if pos - atomic.LoadUint64(&s.readPos) >= s.size {
        // Ring is full - could spin-wait, return false, or handle overflow
        atomic.AddUint64(&s.writePos, ^uint64(0)) // decrement writePos
        return false
    }

    // Write to the slot (index wraps around using modulo)
    idx := pos % s.size
    s.buffer[idx] = value

    return true
}
```

**Key Points**:
- `atomic.AddUint64` is atomic and returns the new value; subtracting 1 gives us our claimed position
- Multiple producers may call this simultaneously; each gets a unique position
- The modulo operation wraps the linear position into the circular buffer index
- Overflow detection compares write position against read position

#### Read Operation (Single Consumer)

Since only one consumer exists, read operations require no atomic synchronization:

```go
func (s *Shard) TryRead() (any, bool) {
    // Check if there's data to read
    writePos := atomic.LoadUint64(&s.writePos)
    if s.readPos >= writePos {
        return nil, false // empty
    }

    // Read the value
    idx := s.readPos % s.size
    value := s.buffer[idx]

    // Clear the slot (optional, helps GC for pointer types)
    s.buffer[idx] = nil

    // Advance read position
    s.readPos++

    return value, true
}
```

**Key Points**:
- `readPos` is not atomic because only the single consumer modifies it
- `writePos` is loaded atomically to get a consistent view of producer progress
- Clearing the slot helps the garbage collector reclaim referenced objects

#### Shard Selection

Producers are distributed across shards using a hash function:

```go
func (r *ShardedRing) selectShard(producerID uint64) *Shard {
    // Fast modulo for power-of-2 shard counts
    shardIdx := producerID & r.mask
    return r.shards[shardIdx]
}
```

Alternative selection strategies:
- **Round-robin per producer**: Each producer maintains its own counter and cycles through shards
- **Random selection**: `rand.Uint64() & r.mask` for load balancing
- **Thread affinity**: Use goroutine ID (if available) for cache locality

#### Consumer Polling Loop

The consumer iterates through all shards to collect available data:

```go
func (r *ShardedRing) ReadBatch(maxItems int) []any {
    result := make([]any, 0, maxItems)

    // Round-robin through all shards
    for i := uint64(0); i < r.numShards && len(result) < maxItems; i++ {
        shard := r.shards[i]
        for len(result) < maxItems {
            if val, ok := shard.TryRead(); ok {
                result = append(result, val)
            } else {
                break // this shard is empty
            }
        }
    }

    return result
}
```

### Memory Management Integration

The ring library is designed to work with `sync.Pool` for efficient memory reuse:

#### Producer Side

```go
// Get buffer from pool
buf := bufPool.Get().([]byte)

// Create packet with pooled buffer
pkt := &packet{
    sequence: nextSeq,
    data:     &buf,
}

// Write to ring
ring.Write(producerID, pkt)
```

#### Consumer Side

```go
// Read from ring
items := ring.ReadBatch(1000)

for _, item := range items {
    pkt := item.(*packet)

    // Process packet...

    // Return buffer to pool when done
    bufPool.Put(*pkt.data)
}
```

### Synchronization Strategy Summary

| Operation | Synchronization | Reason |
|-----------|-----------------|--------|
| Write (claim slot) | `atomic.AddUint64` | Multiple producers compete for slots |
| Write (store value) | None | Each producer writes to its own claimed slot |
| Read (check availability) | `atomic.LoadUint64` on writePos | See latest producer progress |
| Read (load value) | None | Single consumer, no competition |
| Read (advance readPos) | None | Single consumer owns readPos |

### Design Trade-offs

#### Advantages

1. **Lock-Free**: No mutex contention, predictable latency
2. **Sharded**: Reduces CAS contention among producers proportionally to shard count
3. **Cache-Friendly**: Padding prevents false sharing between shards
4. **Simple Consumer**: Single-threaded consumer requires no synchronization on reads
5. **Memory-Efficient**: Works with `sync.Pool` for zero-allocation steady-state operation

#### Limitations

1. **Consumer Polling Overhead**: Consumer must check all shards, latency increases with shard count
2. **Uneven Load**: Some shards may fill faster than others depending on producer distribution
3. **Single Consumer Only**: Design does not extend to multiple consumers without additional synchronization
4. **Ordering**: Global ordering is not preserved; only per-shard ordering is maintained

### Configuration Recommendations

| Parameter | Recommendation | Rationale |
|-----------|----------------|-----------|
| `numShards` | Number of producers or nearest power of 2 | Minimizes per-shard contention |
| `totalCapacity` | Expected burst size × 2 | Headroom for bursty traffic |
| `shardCapacity` | At least 64 items | Amortize cache line overhead |

### Error Handling

The ring library handles edge cases:

1. **Ring Full**: `Write()` returns `false`, caller decides retry/drop strategy
2. **Ring Empty**: `TryRead()` returns `false`, consumer continues polling other shards
3. **Overflow Detection**: Write position cannot overtake read position by more than buffer size

## Repository layout

This repo contains:
- Ring library
The design is this repo is primarily a library that implements the lock free ring.  The idea is this library should make it easy to create a multi producer single consumer ring.

The intended reason for implementation is for reading packets at high data rates from the network, placing the packets into the lock-free ring at high speeds, and then to have a single consumer waking up every 10ms to read as many packets as it can from the ring.

This is ring.go and ring_test.go

- data-generator
In the ./data-generator/ folder is an example of a data-generator with tests, that shows how the producers will use the rate limit library to produce to the ring at the constant packets/second rate.  This was created primarily to test the packets per second code works correctly.

- integration-tests
The ./integration-tests/ folder contains the code for the integration tests

- Ring example
The ./cmd/ring/ring.go contains a main function that demonstrates how to use the ring.

The ring function takes multiple cli flag arguments
- packetSize
The packet size in bytes.  A single []byte will be created of this size and will be populated with random data once.  The packet will be reused by all the generators as packets the generators send.
- rate
The rate in Mb/s that each data generating producer will try to push the packets into the ring.  This Mb/s rate will be converted in a packet per second rate by simple arithmatic.
- producers
The number of data generator producers that will be generating the data being pushed into the ring.
- ringSize
The size of the lock free ring.
- ringShards
The numbers of shards for the ring.  This should be a power of x2 and the ring.go will error if it's not.
- btreeSize
The maxmum number of items in the btree.  The packets get put into the btree, and it will keep this many items deleting the oldest ones to maintain this size.
- frequency
This is the frequency in milliseconds that the reader wakes up to read the packets from the ring, and insert them into the b-tree structure.
- profileFlag
Which profiling to enable
- profilePath
Where to put the pprof file
- debugLevel
Integer like a syslog level 0-7 for the logging level


## Profiling
```
import 	"github.com/pkg/profile"

	profileFlag = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
	profilePath = flag.String("profilepath", ".", "directory to write profile files to")

	// Setup profiling if requested
	var p func(*profile.Profile)
	switch *profileFlag {
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
	}

	// Store profile so we can stop it explicitly on signal
	var prof interface{ Stop() }
	if p != nil {
		prof = profile.Start(profile.ProfilePath(*profilePath), profile.NoShutdownHook, p)
		defer prof.Stop()
	}
```

## ring.go Overview
This ring.go essentially does:
1. Creates a bufPool to allow []byte memory reuse
2. Sets up the lock free ring of size ringSize
3. Create the b-tree structure to store btreeSize number of packets
4. Starts the single reader worker
4.1 The reader just runs in a ticker timed loop at frequency milliseconds
4.1 Reader will read from the ring and insert into the btree (btree will sort based on the packet sequence number)
4.2 Check the size of the btree, and if it's > btreeSize elements, it will start from the .Min() and iterate up until enough elements have been removed
4.2.1 To safely remove the elements it needs .Put() the data to the sync.Pool, then delete the item from the btree
5. Creates multiple producers that will
5.1 Create new packet stucture, with
5.2 .Get() from the sync.Pool to define the data element
5.3 Based on the packets/second rate, calculated based on the packetSize and rate Mb/s.  The rate limit implementation follows the data-generator.go method.


The producers will `buf := bufPool.Get().([]byte)` from a sync.Pool the provides already initalized data.  This will be slightly slow initially, as each []byte gets initialized, but once .Puts() start to occur then there will be reuse.

```
    bufPool := sync.Pool{
        New: func() any {
            // Allocate a []byte of length 1400
            b := make([]byte, payloadSize)

            // Initialize with non-zero data
            // Example: fill with a deterministic pattern
            for i := range b {
                b[i] = byte((i % 255) + 1) // never zero
            }

            return b
        },
```


```
type packet struct {
	sequence uint32
    data *[]byte
}
```

## Tests
- lock-free-ring tests
- data-generator for rate-limit testing
- integration tests that run the ring.go with various configurations to test it works
e.g.
test0 := packetSize=1450,rate=1,producers=4,ringSize=1000,btreeSize=2000,frequency=50
test1 := packetSize=1450,rate=10,producers=4,ringSize=1000,btreeSize=2000,frequency=10
test2 := packetSize=1450,rate=50,producers=4,ringSize=1000,btreeSize=2000,frequency=10
test3 := packetSize=1450,rate=50,producers=10,ringSize=1000,btreeSize=2000,frequency=10

- integration tests run with the profiling enabled

## Non-functional objectives
- Go idiomatic
- Low comments with clear variable names for easy reading
