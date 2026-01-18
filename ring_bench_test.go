package ring

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Strategy Benchmarks
// =============================================================================

// BenchmarkWriterStrategy benchmarks each strategy
func BenchmarkWriterStrategy(b *testing.B) {
	strategies := []RetryStrategy{
		SleepBackoff,
		NextShard,
		RandomShard,
		SpinThenYield,
		AutoAdaptive,
	}

	for _, strategy := range strategies {
		b.Run(strategy.String(), func(b *testing.B) {
			ring, _ := NewShardedRing(1000000, 8)
			config := WriteConfig{
				Strategy:        strategy,
				MaxRetries:      10,
				BackoffDuration: 100 * time.Microsecond,
			}
			writer := NewWriter(ring, 0, config)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				writer.Write(i)
				// Drain frequently to prevent filling (every 100 writes)
				if i%100 == 99 {
					for j := 0; j < 100; j++ {
						ring.TryRead()
					}
				}
			}
		})
	}
}

// BenchmarkWriterVsWriteWithBackoff compares Writer to WriteWithBackoff
func BenchmarkWriterVsWriteWithBackoff(b *testing.B) {
	b.Run("WriteWithBackoff", func(b *testing.B) {
		ring, _ := NewShardedRing(1000000, 8)
		config := WriteConfig{
			MaxRetries:      10,
			BackoffDuration: 100 * time.Microsecond,
		}

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			ring.WriteWithBackoff(0, i, config)
			if i%100 == 99 {
				for j := 0; j < 100; j++ {
					ring.TryRead()
				}
			}
		}
	})

	b.Run("Writer_SleepBackoff", func(b *testing.B) {
		ring, _ := NewShardedRing(1000000, 8)
		config := WriteConfig{
			Strategy:        SleepBackoff,
			MaxRetries:      10,
			BackoffDuration: 100 * time.Microsecond,
		}
		writer := NewWriter(ring, 0, config)

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			writer.Write(i)
			if i%100 == 99 {
				for j := 0; j < 100; j++ {
					ring.TryRead()
				}
			}
		}
	})
}

// =============================================================================
// Core Benchmarks
// =============================================================================

// BenchmarkWrite benchmarks single-threaded write performance
func BenchmarkWrite(b *testing.B) {
	ring, _ := NewShardedRing(1000000, 8)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ring.Write(uint64(i), i)
		// Read to prevent ring from filling
		if i%100 == 99 {
			for j := 0; j < 100; j++ {
				ring.TryRead()
			}
		}
	}
}

// BenchmarkTryRead benchmarks single-threaded read performance
func BenchmarkTryRead(b *testing.B) {
	ring, _ := NewShardedRing(1000000, 8)

	// Pre-fill ring
	for i := 0; i < 500000; i++ {
		ring.Write(uint64(i), i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, ok := ring.TryRead(); !ok {
			// Refill if empty
			for j := 0; j < 10000; j++ {
				ring.Write(uint64(j), j)
			}
		}
	}
}

// BenchmarkReadBatch benchmarks batch read performance
func BenchmarkReadBatch(b *testing.B) {
	b.Run("batch_10", func(b *testing.B) {
		benchmarkReadBatchSize(b, 10)
	})
	b.Run("batch_100", func(b *testing.B) {
		benchmarkReadBatchSize(b, 100)
	})
	b.Run("batch_1000", func(b *testing.B) {
		benchmarkReadBatchSize(b, 1000)
	})
}

func benchmarkReadBatchSize(b *testing.B, batchSize int) {
	ring, _ := NewShardedRing(1000000, 8)

	// Pre-fill ring
	for i := 0; i < 500000; i++ {
		ring.Write(uint64(i), i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		batch := ring.ReadBatch(batchSize)
		if len(batch) == 0 {
			// Refill if empty
			for j := 0; j < batchSize*10; j++ {
				ring.Write(uint64(j), j)
			}
		}
	}
}

// BenchmarkReadBatchIntoPool benchmarks zero-allocation batch read using sync.Pool
func BenchmarkReadBatchIntoPool(b *testing.B) {
	b.Run("batch_10", func(b *testing.B) {
		benchmarkReadBatchIntoPoolSize(b, 10)
	})
	b.Run("batch_100", func(b *testing.B) {
		benchmarkReadBatchIntoPoolSize(b, 100)
	})
	b.Run("batch_1000", func(b *testing.B) {
		benchmarkReadBatchIntoPoolSize(b, 1000)
	})
}

func benchmarkReadBatchIntoPoolSize(b *testing.B, batchSize int) {
	ring, _ := NewShardedRing(1000000, 8)

	// Use pointer type to avoid int->any boxing allocations
	type item struct{ val int }

	// Pre-allocate items to reuse (simulating real usage with pooled objects)
	items := make([]*item, 500000)
	for i := range items {
		items[i] = &item{val: i}
	}

	// Pre-fill ring with pointers (no boxing allocation)
	for i := 0; i < 500000; i++ {
		ring.Write(uint64(i), items[i])
	}

	// Create pool for batch slices
	pool := sync.Pool{
		New: func() any {
			return make([]any, 0, batchSize)
		},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Get buffer from pool
		buf := pool.Get().([]any)

		// Read into pooled buffer
		buf = ring.ReadBatchInto(buf, batchSize)

		if len(buf) == 0 {
			// Refill if empty (reuse same items)
			for j := 0; j < batchSize*10 && j < len(items); j++ {
				ring.Write(uint64(j), items[j])
			}
		}

		// Return buffer to pool
		pool.Put(buf[:0])
	}
}

// BenchmarkConcurrentWrite benchmarks concurrent write performance
func BenchmarkConcurrentWrite(b *testing.B) {
	b.Run("1_producer", func(b *testing.B) {
		benchmarkConcurrentWriteN(b, 1)
	})
	b.Run("2_producers", func(b *testing.B) {
		benchmarkConcurrentWriteN(b, 2)
	})
	b.Run("4_producers", func(b *testing.B) {
		benchmarkConcurrentWriteN(b, 4)
	})
	b.Run("8_producers", func(b *testing.B) {
		benchmarkConcurrentWriteN(b, 8)
	})
}

func benchmarkConcurrentWriteN(b *testing.B, numProducers int) {
	ring, _ := NewShardedRing(10000000, 8)

	b.ResetTimer()
	b.ReportAllocs()
	b.SetParallelism(numProducers)

	b.RunParallel(func(pb *testing.PB) {
		producerID := uint64(0)
		i := 0
		for pb.Next() {
			ring.Write(producerID, i)
			i++
			// Prevent filling by periodically reading
			if i%1000 == 0 {
				for j := 0; j < 100; j++ {
					ring.TryRead()
				}
			}
		}
	})
}

// BenchmarkProducerConsumer benchmarks write-then-read cycle
func BenchmarkProducerConsumer(b *testing.B) {
	ring, _ := NewShardedRing(10000, 8)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Write
		ring.Write(uint64(i%8), i)

		// Read periodically to prevent filling
		if i%100 == 99 {
			for j := 0; j < 100; j++ {
				ring.TryRead()
			}
		}
	}
}

// BenchmarkWriteContention benchmarks write contention with many producers on same shard
func BenchmarkWriteContention(b *testing.B) {
	ring, _ := NewShardedRing(10000000, 1) // Single shard = maximum contention

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			ring.Write(0, i) // All write to same shard
			i++
			if i%1000 == 0 {
				for j := 0; j < 100; j++ {
					ring.TryRead()
				}
			}
		}
	})
}

// BenchmarkWriteNoContention benchmarks write with no contention (each goroutine has own shard)
func BenchmarkWriteNoContention(b *testing.B) {
	ring, _ := NewShardedRing(10000000, 64) // Many shards

	b.ResetTimer()
	b.ReportAllocs()

	var producerCounter atomic.Uint64

	b.RunParallel(func(pb *testing.PB) {
		producerID := producerCounter.Add(1) - 1
		i := 0
		for pb.Next() {
			ring.Write(producerID, i) // Each producer to own shard
			i++
			if i%1000 == 0 {
				for j := 0; j < 100; j++ {
					ring.TryRead()
				}
			}
		}
	})
}

// BenchmarkShardCount benchmarks impact of shard count on performance
func BenchmarkShardCount(b *testing.B) {
	b.Run("01_shards", func(b *testing.B) {
		benchmarkShardCountN(b, 1)
	})
	b.Run("02_shards", func(b *testing.B) {
		benchmarkShardCountN(b, 2)
	})
	b.Run("04_shards", func(b *testing.B) {
		benchmarkShardCountN(b, 4)
	})
	b.Run("08_shards", func(b *testing.B) {
		benchmarkShardCountN(b, 8)
	})
	b.Run("16_shards", func(b *testing.B) {
		benchmarkShardCountN(b, 16)
	})
	b.Run("32_shards", func(b *testing.B) {
		benchmarkShardCountN(b, 32)
	})
}

func benchmarkShardCountN(b *testing.B, numShards uint64) {
	ring, _ := NewShardedRing(1000000, numShards)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ring.Write(uint64(i), i)
		if i%100 == 99 {
			for j := 0; j < 100; j++ {
				ring.TryRead()
			}
		}
	}
}

// BenchmarkThroughput measures sustained throughput with concurrent producers
func BenchmarkThroughput(b *testing.B) {
	ring, _ := NewShardedRing(10000000, 8)

	b.ResetTimer()
	b.ReportAllocs()

	var counter atomic.Uint64

	b.RunParallel(func(pb *testing.PB) {
		producerID := counter.Add(1) - 1
		i := 0
		for pb.Next() {
			ring.Write(producerID, i)
			i++
			// Periodic drain to prevent filling
			if i%1000 == 0 {
				for j := 0; j < 100; j++ {
					ring.TryRead()
				}
			}
		}
	})
}

// BenchmarkTryReadRotation benchmarks TryRead with rotating shard start.
//
// Background: TryRead uses a rotating start shard (readStartShard) to ensure
// fair distribution across shards. Without rotation, shard 0 would always be
// checked first, leading to uneven draining.
//
// Key finding: We tested atomic vs non-atomic for readStartShard counter.
// Since this is MPSC (single consumer), atomic is unnecessary. Results:
//
//	| Shards | Atomic (ns/op) | Non-Atomic (ns/op) | Improvement |
//	|--------|----------------|-------------------|-------------|
//	| 4      | ~27.6          | ~23.7             | ~14% faster |
//	| 8      | ~33.5          | ~28.9             | ~14% faster |
//	| 16     | ~35.2          | ~31.5             | ~11% faster |
//
// Note: The B/op shown here comes from boxing int→any during Write refills,
// NOT from TryRead. See BenchmarkTryReadRotationZeroAlloc for proof.
func BenchmarkTryReadRotation(b *testing.B) {
	b.Run("4_shards", func(b *testing.B) {
		benchmarkTryReadRotationN(b, 4)
	})
	b.Run("8_shards", func(b *testing.B) {
		benchmarkTryReadRotationN(b, 8)
	})
	b.Run("16_shards", func(b *testing.B) {
		benchmarkTryReadRotationN(b, 16)
	})
}

func benchmarkTryReadRotationN(b *testing.B, numShards uint64) {
	ring, _ := NewShardedRing(numShards*10000, numShards)

	// Pre-fill ring evenly across shards
	for i := uint64(0); i < numShards*5000; i++ {
		ring.Write(i%numShards, int(i))
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, ok := ring.TryRead(); !ok {
			// Refill if empty
			for j := uint64(0); j < numShards*100; j++ {
				ring.Write(j%numShards, int(j))
			}
		}
	}
}

// BenchmarkTryReadRotationZeroAlloc proves TryRead is zero-allocation when using
// pointer types (the recommended production pattern).
//
// Background: BenchmarkTryReadRotation shows 2-6 B/op, which might suggest
// TryRead allocates. This benchmark proves those allocations come from boxing
// value types (int→any) during Write, NOT from TryRead itself.
//
// Key finding: Using pointer types (*item) instead of value types (int):
//
//	| Shards | int boxing (B/op) | *item pointers (B/op) |
//	|--------|-------------------|----------------------|
//	| 4      | 2                 | 0                    |
//	| 8      | 5                 | 0                    |
//
// Bonus: Pointer types are also faster (~38% for 4 shards) because no boxing
// overhead is incurred during Write operations.
//
// Production recommendation: Always store pointer types (e.g., *Packet) in the
// ring, typically obtained from sync.Pool. This achieves true zero-allocation
// steady-state operation as documented in the README.
func BenchmarkTryReadRotationZeroAlloc(b *testing.B) {
	b.Run("4_shards", func(b *testing.B) {
		benchmarkTryReadRotationZeroAllocN(b, 4)
	})
	b.Run("8_shards", func(b *testing.B) {
		benchmarkTryReadRotationZeroAllocN(b, 8)
	})
}

func benchmarkTryReadRotationZeroAllocN(b *testing.B, numShards uint64) {
	ring, _ := NewShardedRing(numShards*10000, numShards)

	// Use pointer type to avoid boxing allocations
	type item struct{ val int }

	// Pre-allocate items (simulating real usage with pooled objects)
	items := make([]*item, numShards*5000)
	for i := range items {
		items[i] = &item{val: i}
	}

	// Pre-fill ring with pointers (no boxing allocation)
	for i := uint64(0); i < numShards*5000; i++ {
		ring.Write(i%numShards, items[i])
	}

	b.ResetTimer()
	b.ReportAllocs()

	refillIdx := 0
	for i := 0; i < b.N; i++ {
		if _, ok := ring.TryRead(); !ok {
			// Refill with pre-allocated pointers (no allocation)
			for j := 0; j < int(numShards*100) && refillIdx < len(items); j++ {
				ring.Write(uint64(j)%numShards, items[refillIdx%len(items)])
				refillIdx++
			}
		}
	}
}
