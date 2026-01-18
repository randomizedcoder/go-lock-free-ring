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

	// AutoAdaptive: Starts with Yield (fast), relaxes to Sleep when idle
	// Best for high-throughput scenarios that also need CPU-friendliness when idle
	AutoAdaptive
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
	case AutoAdaptive:
		return "AutoAdaptive"
	default:
		return "Unknown"
	}
}

// WriteConfig configures the backoff behavior for WriteWithBackoff and Writer
type WriteConfig struct {
	// Strategy determines retry behavior (default: AutoAdaptive)
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

	// --- AutoAdaptive Strategy Options ---
	// These control when the strategy switches between Yield (fast) and Sleep (CPU-friendly) modes.
	// The goal is to maximize throughput when active while minimizing CPU when truly idle.

	// AdaptiveIdleIterations is how many consecutive failed write cycles before
	// switching from Yield to Sleep mode. Higher = stays in fast mode longer when idle.
	// Used by: AutoAdaptive
	// Default: 100000 (conservative - stays fast for ~1-10ms of idle time)
	// Tune lower (e.g., 10000) if CPU usage when idle is a concern
	// Tune higher (e.g., 1000000) for extremely latency-sensitive applications
	AdaptiveIdleIterations int

	// AdaptiveWarmupIterations is how many iterations to skip idle counting after
	// a successful write. This prevents flapping between modes during bursty traffic.
	// Used by: AutoAdaptive
	// Default: 1000 (brief warmup period after each success)
	// Tune higher for very bursty workloads with gaps between bursts
	AdaptiveWarmupIterations int

	// AdaptiveSleepDuration is how long to sleep when in Sleep mode.
	// Lower = more responsive wake-up, higher CPU. Higher = slower wake-up, less CPU.
	// Used by: AutoAdaptive
	// Default: 100µs (good balance of responsiveness and CPU efficiency)
	AdaptiveSleepDuration time.Duration
}

// DefaultWriteConfig returns the recommended default configuration.
// Uses AutoAdaptive strategy: high-performance Yield mode when active,
// automatically relaxes to Sleep mode only after sustained idle period.
//
// This is the recommended starting point for most applications.
// The defaults are tuned to stay in high-performance mode aggressively
// and only sleep when truly idle.
func DefaultWriteConfig() WriteConfig {
	return WriteConfig{
		Strategy:                 AutoAdaptive,
		MaxRetries:               10,
		BackoffDuration:          100 * time.Microsecond,
		MaxBackoffs:              0, // unlimited
		MaxBackoffDuration:       10 * time.Millisecond,
		BackoffMultiplier:        2.0,
		AdaptiveIdleIterations:   100000, // ~100k iterations before sleep (~1-10ms idle)
		AdaptiveWarmupIterations: 1000,   // Brief warmup after each success
		AdaptiveSleepDuration:    100 * time.Microsecond,
	}
}

// HighThroughputConfig returns config for maximum throughput scenarios.
// More aggressive than default: stays in Yield mode even longer before sleeping.
// Use when latency is critical and CPU usage is not a concern.
func HighThroughputConfig() WriteConfig {
	return WriteConfig{
		Strategy:                 AutoAdaptive,
		MaxRetries:               10,
		BackoffDuration:          100 * time.Microsecond,
		MaxBackoffs:              0, // unlimited
		MaxBackoffDuration:       10 * time.Millisecond,
		BackoffMultiplier:        2.0,
		AdaptiveIdleIterations:   500000, // Very slow to enter sleep mode
		AdaptiveWarmupIterations: 5000,   // Longer warmup for bursty traffic
		AdaptiveSleepDuration:    50 * time.Microsecond,
	}
}

// LowLatencyConfig returns config optimized for minimal latency.
// Uses pure SpinThenYield - never sleeps, burns CPU when idle.
// Only use when you have dedicated CPU cores and need absolute minimum latency.
func LowLatencyConfig() WriteConfig {
	return WriteConfig{
		Strategy:        SpinThenYield,
		MaxRetries:      10,
		BackoffDuration: 100 * time.Microsecond, // Not used by SpinThenYield
		MaxBackoffs:     0,
	}
}

// CPUFriendlyConfig returns config that minimizes CPU usage.
// Quickly switches to Sleep mode when idle. Good for battery-powered
// devices or shared environments where CPU efficiency matters.
func CPUFriendlyConfig() WriteConfig {
	return WriteConfig{
		Strategy:                 AutoAdaptive,
		MaxRetries:               10,
		BackoffDuration:          100 * time.Microsecond,
		MaxBackoffs:              0,
		MaxBackoffDuration:       10 * time.Millisecond,
		BackoffMultiplier:        2.0,
		AdaptiveIdleIterations:   10000, // Quick to enter sleep mode
		AdaptiveWarmupIterations: 100,   // Short warmup
		AdaptiveSleepDuration:    500 * time.Microsecond,
	}
}

// BurstyTrafficConfig returns config optimized for bursty workloads.
// Stays in high-performance mode longer between bursts to handle
// the next burst immediately without wake-up latency.
func BurstyTrafficConfig() WriteConfig {
	return WriteConfig{
		Strategy:                 AutoAdaptive,
		MaxRetries:               10,
		BackoffDuration:          100 * time.Microsecond,
		MaxBackoffs:              0,
		MaxBackoffDuration:       10 * time.Millisecond,
		BackoffMultiplier:        2.0,
		AdaptiveIdleIterations:   200000, // Moderate idle threshold
		AdaptiveWarmupIterations: 10000,  // Long warmup to handle gaps between bursts
		AdaptiveSleepDuration:    100 * time.Microsecond,
	}
}

// LegacySleepConfig returns config that always uses Sleep-based backoff.
// This was the original default behavior before AutoAdaptive was introduced.
// Use only if you specifically need the old behavior.
func LegacySleepConfig() WriteConfig {
	return WriteConfig{
		Strategy:        SleepBackoff,
		MaxRetries:      10,
		BackoffDuration: 100 * time.Microsecond,
		MaxBackoffs:     0,
	}
}

// AdaptiveMode represents the current mode for AutoAdaptive strategy
type AdaptiveMode int

const (
	// AdaptiveModeYield uses runtime.Gosched() - fast but uses CPU
	AdaptiveModeYield AdaptiveMode = iota
	// AdaptiveModeSleep uses time.Sleep() - slower but CPU-friendly
	AdaptiveModeSleep
)

// writerState holds per-writer mutable state for adaptive strategies
type writerState struct {
	currentBackoff time.Duration
	backoffCount   int

	// AutoAdaptive state
	adaptiveMode    AdaptiveMode
	idleIterations  int // Counts consecutive failed write cycles
	warmupRemaining int // Iterations to skip idle counting after success (anti-flap)
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
	case AutoAdaptive:
		return writeWithAutoAdaptive
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
	w.state.adaptiveMode = AdaptiveModeYield
	w.state.idleIterations = 0
	w.state.warmupRemaining = 0
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
