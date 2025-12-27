package ring

import (
	"sync/atomic"
	"testing"
)

// =============================================================================
// False Sharing Demonstration
// =============================================================================

// shardNoPadding is a shard without cache line padding (demonstrates false sharing)
type shardNoPadding struct {
	writePos uint64
	readPos  uint64
}

// shardWithPadding is a shard with cache line padding (prevents false sharing)
type shardWithPadding struct {
	writePos uint64
	_pad1    [56]byte // Pad to 64 bytes (cache line)
	readPos  uint64
	_pad2    [56]byte // Pad to 64 bytes (cache line)
}

// BenchmarkFalseSharing demonstrates the performance impact of false sharing
// When multiple goroutines write to adjacent memory locations, cache line
// invalidation causes significant slowdown.
func BenchmarkFalseSharing(b *testing.B) {
	b.Run("WithoutPadding", func(b *testing.B) {
		// Create 8 shards without padding - they're adjacent in memory
		shards := make([]shardNoPadding, 8)

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			// Each goroutine writes to a different shard
			id := int(atomic.AddUint64(&shards[0].readPos, 1) - 1)
			shardIdx := id % 8
			for pb.Next() {
				atomic.AddUint64(&shards[shardIdx].writePos, 1)
			}
		})
	})

	b.Run("WithPadding", func(b *testing.B) {
		// Create 8 shards with padding - each on separate cache line
		shards := make([]shardWithPadding, 8)

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			// Each goroutine writes to a different shard
			id := int(atomic.AddUint64(&shards[0].readPos, 1) - 1)
			shardIdx := id % 8
			for pb.Next() {
				atomic.AddUint64(&shards[shardIdx].writePos, 1)
			}
		})
	})
}

// BenchmarkFalseSharingContention shows false sharing with high contention
// This benchmark uses only 2 adjacent counters to maximize the effect
func BenchmarkFalseSharingContention(b *testing.B) {
	b.Run("Adjacent_NoGap", func(b *testing.B) {
		// Two counters right next to each other (same cache line)
		type adjacentCounters struct {
			counter1 uint64
			counter2 uint64
		}
		counters := &adjacentCounters{}

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			// Half the goroutines write to counter1, half to counter2
			id := atomic.AddUint64(&counters.counter1, 0)
			useFirst := id%2 == 0
			for pb.Next() {
				if useFirst {
					atomic.AddUint64(&counters.counter1, 1)
				} else {
					atomic.AddUint64(&counters.counter2, 1)
				}
			}
		})
	})

	b.Run("Separated_64ByteGap", func(b *testing.B) {
		// Two counters on separate cache lines
		type separatedCounters struct {
			counter1 uint64
			_pad     [56]byte // Separate cache lines
			counter2 uint64
		}
		counters := &separatedCounters{}

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			// Half the goroutines write to counter1, half to counter2
			id := atomic.AddUint64(&counters.counter1, 0)
			useFirst := id%2 == 0
			for pb.Next() {
				if useFirst {
					atomic.AddUint64(&counters.counter1, 1)
				} else {
					atomic.AddUint64(&counters.counter2, 1)
				}
			}
		})
	})
}

// =============================================================================
// Padding Size Optimization Benchmarks
// =============================================================================

// These benchmarks help determine the optimal padding size for the Shard struct.
// The goal is to prevent false sharing while minimizing memory overhead.
//
// Shard struct layout (excluding padding):
//   - buffer:   []slot  = 24 bytes (slice header: ptr + len + cap)
//   - size:     uint64  = 8 bytes
//   - writePos: uint64  = 8 bytes (HOT - written by producers)
//   - readPos:  uint64  = 8 bytes (HOT - written by consumer)
//   Total: 48 bytes
//
// Cache line is typically 64 bytes. Padding ensures hot variables (writePos, readPos)
// don't share cache lines with adjacent shards when shards are allocated in a slice.

type shardPad0 struct {
	buffer   [8]uint64 // Simulating slice header + some data
	size     uint64
	writePos uint64
	readPos  uint64
	// No padding - 88 bytes total
}

type shardPad16 struct {
	buffer   [8]uint64
	size     uint64
	writePos uint64
	readPos  uint64
	_        [16]byte // 104 bytes total
}

type shardPad32 struct {
	buffer   [8]uint64
	size     uint64
	writePos uint64
	readPos  uint64
	_        [32]byte // 120 bytes total
}

type shardPad40 struct {
	buffer   [8]uint64
	size     uint64
	writePos uint64
	readPos  uint64
	_        [40]byte // 128 bytes total (2 cache lines)
}

type shardPad48 struct {
	buffer   [8]uint64
	size     uint64
	writePos uint64
	readPos  uint64
	_        [48]byte // 136 bytes total
}

type shardPad56 struct {
	buffer   [8]uint64
	size     uint64
	writePos uint64
	readPos  uint64
	_        [56]byte // 144 bytes total
}

// BenchmarkShardPadding tests different padding sizes to find optimal value
func BenchmarkShardPadding(b *testing.B) {
	b.Run("Pad0_88bytes", func(b *testing.B) {
		shards := make([]shardPad0, 8)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			id := int(atomic.AddUint64(&shards[0].readPos, 1) - 1)
			idx := id % 8
			for pb.Next() {
				atomic.AddUint64(&shards[idx].writePos, 1)
			}
		})
	})

	b.Run("Pad16_104bytes", func(b *testing.B) {
		shards := make([]shardPad16, 8)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			id := int(atomic.AddUint64(&shards[0].readPos, 1) - 1)
			idx := id % 8
			for pb.Next() {
				atomic.AddUint64(&shards[idx].writePos, 1)
			}
		})
	})

	b.Run("Pad32_120bytes", func(b *testing.B) {
		shards := make([]shardPad32, 8)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			id := int(atomic.AddUint64(&shards[0].readPos, 1) - 1)
			idx := id % 8
			for pb.Next() {
				atomic.AddUint64(&shards[idx].writePos, 1)
			}
		})
	})

	b.Run("Pad40_128bytes", func(b *testing.B) {
		shards := make([]shardPad40, 8)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			id := int(atomic.AddUint64(&shards[0].readPos, 1) - 1)
			idx := id % 8
			for pb.Next() {
				atomic.AddUint64(&shards[idx].writePos, 1)
			}
		})
	})

	b.Run("Pad48_136bytes", func(b *testing.B) {
		shards := make([]shardPad48, 8)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			id := int(atomic.AddUint64(&shards[0].readPos, 1) - 1)
			idx := id % 8
			for pb.Next() {
				atomic.AddUint64(&shards[idx].writePos, 1)
			}
		})
	})

	b.Run("Pad56_144bytes", func(b *testing.B) {
		shards := make([]shardPad56, 8)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			id := int(atomic.AddUint64(&shards[0].readPos, 1) - 1)
			idx := id % 8
			for pb.Next() {
				atomic.AddUint64(&shards[idx].writePos, 1)
			}
		})
	})
}

