package ring

import (
	"time"
)

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

// String returns the string representation of a RetryStrategy
func (s RetryStrategy) String() string {
	switch s {
	case SleepBackoff:
		return "SleepBackoff"
	case NextShard:
		return "NextShard"
	case RandomShard:
		return "RandomShard"
	case AdaptiveBackoff:
		return "AdaptiveBackoff"
	case SpinThenYield:
		return "SpinThenYield"
	case Hybrid:
		return "Hybrid"
	default:
		return "Unknown"
	}
}

// WriteConfig configures the backoff behavior for WriteWithBackoff and Writer
type WriteConfig struct {
	// Strategy determines retry behavior (default: SleepBackoff)
	Strategy RetryStrategy

	// MaxRetries is the number of write attempts per shard (default: 10)
	MaxRetries int

	// BackoffDuration is how long to sleep after MaxRetries failures (default: 100µs)
	BackoffDuration time.Duration

	// MaxBackoffs is the maximum number of backoff cycles before giving up (0 = unlimited)
	MaxBackoffs int

	// MaxBackoffDuration caps exponential backoff (default: 10ms)
	// Used by: AdaptiveBackoff, Hybrid
	MaxBackoffDuration time.Duration

	// BackoffMultiplier for exponential growth (default: 2.0)
	// Used by: AdaptiveBackoff, Hybrid
	BackoffMultiplier float64
}

// DefaultWriteConfig returns sensible defaults for write backoff
func DefaultWriteConfig() WriteConfig {
	return WriteConfig{
		Strategy:           SleepBackoff,
		MaxRetries:         10,
		BackoffDuration:    100 * time.Microsecond,
		MaxBackoffs:        0, // unlimited
		MaxBackoffDuration: 10 * time.Millisecond,
		BackoffMultiplier:  2.0,
	}
}

// writerState holds per-writer mutable state for adaptive strategies
type writerState struct {
	currentBackoff time.Duration
	backoffCount   int
}

// WriterFunc is the signature for all strategy implementations
type WriterFunc func(r *ShardedRing, producerID uint64, value any, config *WriteConfig, state *writerState) bool

// Writer holds a pre-resolved strategy function for zero-overhead dispatch
type Writer struct {
	ring       *ShardedRing
	config     WriteConfig
	producerID uint64
	writeFunc  WriterFunc   // Strategy resolved at creation time
	state      *writerState // Mutable state for adaptive strategies
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
	return w.writeFunc(w.ring, w.producerID, value, &w.config, w.state)
}

// Reset resets the writer state (useful for reusing writers)
func (w *Writer) Reset() {
	w.state.currentBackoff = w.config.BackoffDuration
	w.state.backoffCount = 0
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


