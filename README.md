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
