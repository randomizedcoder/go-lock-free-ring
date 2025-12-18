package ring

import (
	"errors"
	"sync/atomic"
	"time"
)

var (
	ErrNotPowerOfTwo = errors.New("numShards must be a power of 2")
	ErrInvalidSize   = errors.New("totalCapacity must be greater than 0 and divisible by numShards")
)

// WriteConfig configures the backoff behavior for WriteWithBackoff
type WriteConfig struct {
	// MaxRetries is the number of write attempts before sleeping (default: 10)
	MaxRetries int
	// BackoffDuration is how long to sleep after MaxRetries failures (default: 100µs)
	BackoffDuration time.Duration
	// MaxBackoffs is the maximum number of backoff cycles before giving up (0 = unlimited)
	MaxBackoffs int
}

// DefaultWriteConfig returns sensible defaults for write backoff
func DefaultWriteConfig() WriteConfig {
	return WriteConfig{
		MaxRetries:      10,
		BackoffDuration: 100 * time.Microsecond,
		MaxBackoffs:     0, // unlimited
	}
}

// slot holds a value with its sequence number for safe concurrent access
type slot struct {
	seq   uint64 // Sequence number - matches position when data is ready
	value any
}

// Shard is an independent lock-free ring buffer segment
type Shard struct {
	buffer   []slot
	size     uint64
	writePos uint64 // Next position to claim for writing
	readPos  uint64 // Next position to read from

	// Cache line padding (64 bytes typical cache line)
	// Note: With []*Shard (pointer array), each shard is heap-allocated separately,
	// so padding has minimal impact (run: make bench-padding to verify).
	// Kept as defensive measure for:
	// - Future changes to allocation patterns
	// - Platforms with different allocator behaviors
	// - Embedded/contiguous allocation scenarios
	// Can be removed to save ~40 bytes per shard if memory is constrained.
	//_ [40]byte
}

// ShardedRing is a sharded lock-free MPSC ring buffer
type ShardedRing struct {
	shards    []*Shard
	numShards uint64
	mask      uint64
}

// NewShardedRing creates a new sharded ring buffer
// totalCapacity: total number of items the ring can hold across all shards
// numShards: number of shards (must be a power of 2)
func NewShardedRing(totalCapacity uint64, numShards uint64) (*ShardedRing, error) {
	if !isPowerOfTwo(numShards) {
		return nil, ErrNotPowerOfTwo
	}

	if totalCapacity == 0 || totalCapacity < numShards {
		return nil, ErrInvalidSize
	}

	shardCapacity := totalCapacity / numShards
	if shardCapacity == 0 {
		return nil, ErrInvalidSize
	}

	shards := make([]*Shard, numShards)
	for i := uint64(0); i < numShards; i++ {
		buffer := make([]slot, shardCapacity)
		// Initialize each slot's sequence to its index (marks as "empty/ready for write")
		for j := uint64(0); j < shardCapacity; j++ {
			buffer[j].seq = j
		}
		shards[i] = &Shard{
			buffer: buffer,
			size:   shardCapacity,
		}
	}

	return &ShardedRing{
		shards:    shards,
		numShards: numShards,
		mask:      numShards - 1,
	}, nil
}

// isPowerOfTwo checks if n is a power of 2
func isPowerOfTwo(n uint64) bool {
	return n > 0 && (n&(n-1)) == 0
}

// selectShard returns the shard for a given producer ID
func (r *ShardedRing) selectShard(producerID uint64) *Shard {
	shardIdx := producerID & r.mask
	return r.shards[shardIdx]
}

// Write writes a value to the ring using the producer ID for shard selection
// Returns true on success, false if the selected shard is full (non-blocking)
func (r *ShardedRing) Write(producerID uint64, value any) bool {
	shard := r.selectShard(producerID)
	return shard.write(value)
}

// WriteWithBackoff writes a value with configurable retry and backoff behavior
// It tries MaxRetries times, then sleeps for BackoffDuration, and repeats
// Returns true on success, false if MaxBackoffs is reached (when MaxBackoffs > 0)
//
// Example usage:
//
//	config := ring.WriteConfig{
//	    MaxRetries:      10,              // Try 10 times before sleeping
//	    BackoffDuration: 100 * time.Microsecond, // Sleep 100µs between retry batches
//	    MaxBackoffs:     1000,            // Give up after 1000 backoff cycles
//	}
//	if !ring.WriteWithBackoff(producerID, value, config) {
//	    // Handle: ring is persistently full, consider dropping or signaling backpressure
//	}
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

// write writes a value to the shard (lock-free)
func (s *Shard) write(value any) bool {
	// Atomically claim the next write slot
	pos := atomic.AddUint64(&s.writePos, 1) - 1
	idx := pos % s.size
	sl := &s.buffer[idx]

	// Check if slot is available for writing
	// Slot is available when seq == pos (initial or after consumer released it)
	seq := atomic.LoadUint64(&sl.seq)
	if seq != pos {
		// Slot not available - ring is full, unclaim and return false
		atomic.AddUint64(&s.writePos, ^uint64(0))
		return false
	}

	// Write the value
	sl.value = value

	// Signal that this slot is ready by setting seq to pos+1
	atomic.StoreUint64(&sl.seq, pos+1)

	return true
}

// TryRead attempts to read one item from any shard
// Returns the value and true if an item was read, nil and false if all shards are empty
func (r *ShardedRing) TryRead() (any, bool) {
	for i := uint64(0); i < r.numShards; i++ {
		if val, ok := r.shards[i].tryRead(); ok {
			return val, true
		}
	}
	return nil, false
}

// tryRead attempts to read one item from the shard
func (s *Shard) tryRead() (any, bool) {
	readPos := atomic.LoadUint64(&s.readPos)
	idx := readPos % s.size
	sl := &s.buffer[idx]

	// Check if the slot has data ready (seq should be readPos+1 when written)
	seq := atomic.LoadUint64(&sl.seq)
	if seq != readPos+1 {
		return nil, false // Slot not ready yet
	}

	// Read the value
	value := sl.value

	// Clear the value (helps GC for pointer types)
	sl.value = nil

	// Mark slot as available for the next write at position readPos + size
	// The next write to this slot will be at position readPos + size
	atomic.StoreUint64(&sl.seq, readPos+s.size)

	// Advance read position
	atomic.StoreUint64(&s.readPos, readPos+1)

	return value, true
}

// ReadBatch reads up to maxItems from all shards in a round-robin fashion
// Returns a slice of items read (may be empty if ring is empty)
func (r *ShardedRing) ReadBatch(maxItems int) []any {
	result := make([]any, 0, maxItems)
	return r.ReadBatchInto(result, maxItems)
}

// ReadBatchInto reads up to maxItems into the provided slice (for zero-alloc operation)
// The slice is reset to length 0, then items are appended up to maxItems
// Returns the slice with items read (may be empty if ring is empty)
// Usage with sync.Pool:
//
//	buf := pool.Get().([]any)[:0]
//	buf = ring.ReadBatchInto(buf, 100)
//	// process buf...
//	pool.Put(buf)
func (r *ShardedRing) ReadBatchInto(buf []any, maxItems int) []any {
	result := buf[:0]

	// Round-robin through all shards
	for i := uint64(0); i < r.numShards && len(result) < maxItems; i++ {
		shard := r.shards[i]
		for len(result) < maxItems {
			if val, ok := shard.tryRead(); ok {
				result = append(result, val)
			} else {
				break
			}
		}
	}

	return result
}

// Len returns the approximate total number of items in the ring
// Note: this is a snapshot and may not be perfectly accurate under concurrent access
// It counts claimed positions minus read positions (some may still be in-flight writes)
func (r *ShardedRing) Len() uint64 {
	var total uint64
	for _, shard := range r.shards {
		writePos := atomic.LoadUint64(&shard.writePos)
		readPos := atomic.LoadUint64(&shard.readPos)
		if writePos > readPos {
			total += writePos - readPos
		}
	}
	return total
}

// Cap returns the total capacity of the ring
func (r *ShardedRing) Cap() uint64 {
	if len(r.shards) == 0 {
		return 0
	}
	return r.shards[0].size * r.numShards
}

// NumShards returns the number of shards in the ring
func (r *ShardedRing) NumShards() uint64 {
	return r.numShards
}
