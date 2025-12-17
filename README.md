# go-lock-free-ring

There are lots of lock free rings

e.g.

CppCon 2014: Herb Sutter "Lock-Free Programming (or, Juggling Razor Blades), Part I"
https://youtu.be/c1gO9aB9nbs?si=K2y67zBI8HGfmFHF

Wanted to make a little version, and found this blog, so I gave it a quick shot based on a blog

https://congdong007.github.io/2025/08/16/Lock-Free-golang/

```
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
