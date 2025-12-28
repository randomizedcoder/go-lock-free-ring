package ring

import (
	"math/rand"
	"runtime"
	"time"
)

// =============================================================================
// Strategy Implementations
// =============================================================================

// writeWithSleepBackoff implements the SleepBackoff strategy (default behavior)
// Retry same shard, then sleep for fixed duration
func writeWithSleepBackoff(r *ShardedRing, producerID uint64, value any, config *WriteConfig, state *writerState) bool {
	shard := r.selectShard(producerID)

	for {
		// Try MaxRetries times before sleeping
		for retry := 0; retry < config.MaxRetries; retry++ {
			if shard.write(value) {
				return true
			}
		}

		// All retries failed, backoff
		state.backoffCount++

		// Check if we've exceeded max backoffs (if limit is set)
		if config.MaxBackoffs > 0 && state.backoffCount >= config.MaxBackoffs {
			return false
		}

		// Sleep to reduce contention and let consumer catch up
		time.Sleep(config.BackoffDuration)
	}
}

// writeWithNextShard implements the NextShard strategy
// Try all shards in round-robin order before sleeping
func writeWithNextShard(r *ShardedRing, producerID uint64, value any, config *WriteConfig, state *writerState) bool {
	startShard := producerID & r.mask

	for {
		// Try all shards in round-robin order
		for shardOffset := uint64(0); shardOffset < r.numShards; shardOffset++ {
			shardIdx := (startShard + shardOffset) & r.mask
			shard := r.shards[shardIdx]

			// Try this shard MaxRetries times
			for retry := 0; retry < config.MaxRetries; retry++ {
				if shard.write(value) {
					return true
				}
			}
		}

		// All shards tried and failed - backoff
		state.backoffCount++

		if config.MaxBackoffs > 0 && state.backoffCount >= config.MaxBackoffs {
			return false
		}

		time.Sleep(config.BackoffDuration)
	}
}

// writeWithRandomShard implements the RandomShard strategy
// Try random shards to spread load evenly
func writeWithRandomShard(r *ShardedRing, producerID uint64, value any, config *WriteConfig, state *writerState) bool {
	affinityShard := producerID & r.mask

	for {
		// First try the affinity shard
		shard := r.shards[affinityShard]
		for retry := 0; retry < config.MaxRetries; retry++ {
			if shard.write(value) {
				return true
			}
		}

		// Try random shards
		for attempt := uint64(0); attempt < r.numShards-1; attempt++ {
			randomIdx := uint64(rand.Int63()) & r.mask
			if randomIdx == affinityShard {
				randomIdx = (randomIdx + 1) & r.mask
			}

			shard := r.shards[randomIdx]
			for retry := 0; retry < config.MaxRetries; retry++ {
				if shard.write(value) {
					return true
				}
			}
		}

		// All attempts failed
		state.backoffCount++
		if config.MaxBackoffs > 0 && state.backoffCount >= config.MaxBackoffs {
			return false
		}
		time.Sleep(config.BackoffDuration)
	}
}

// writeWithAdaptiveBackoff implements the AdaptiveBackoff strategy
// Exponential backoff with jitter on same shard
func writeWithAdaptiveBackoff(r *ShardedRing, producerID uint64, value any, config *WriteConfig, state *writerState) bool {
	shard := r.selectShard(producerID)

	// Get multiplier with default
	multiplier := config.BackoffMultiplier
	if multiplier == 0 {
		multiplier = 2.0
	}

	// Get max backoff with default
	maxBackoff := config.MaxBackoffDuration
	if maxBackoff == 0 {
		maxBackoff = 10 * time.Millisecond
	}

	for {
		for retry := 0; retry < config.MaxRetries; retry++ {
			if shard.write(value) {
				return true
			}
		}

		state.backoffCount++
		if config.MaxBackoffs > 0 && state.backoffCount >= config.MaxBackoffs {
			return false
		}

		// Add jitter: 75-125% of current backoff
		jitter := 0.75 + rand.Float64()*0.5
		sleepDuration := time.Duration(float64(state.currentBackoff) * jitter)
		time.Sleep(sleepDuration)

		// Exponential increase, capped at max
		state.currentBackoff = time.Duration(float64(state.currentBackoff) * multiplier)
		if state.currentBackoff > maxBackoff {
			state.currentBackoff = maxBackoff
		}
	}
}

// writeWithSpinYield implements the SpinThenYield strategy
// Yield processor instead of sleeping for lowest latency
func writeWithSpinYield(r *ShardedRing, producerID uint64, value any, config *WriteConfig, state *writerState) bool {
	shard := r.selectShard(producerID)

	for {
		for retry := 0; retry < config.MaxRetries; retry++ {
			if shard.write(value) {
				return true
			}
		}

		state.backoffCount++
		if config.MaxBackoffs > 0 && state.backoffCount >= config.MaxBackoffs {
			return false
		}

		// Yield instead of sleep - allows other goroutines to run
		runtime.Gosched()
	}
}

// writeWithHybrid implements the Hybrid strategy
// Combines NextShard traversal with exponential backoff
func writeWithHybrid(r *ShardedRing, producerID uint64, value any, config *WriteConfig, state *writerState) bool {
	startShard := producerID & r.mask

	// Get multiplier with default
	multiplier := config.BackoffMultiplier
	if multiplier == 0 {
		multiplier = 2.0
	}

	// Get max backoff with default
	maxBackoff := config.MaxBackoffDuration
	if maxBackoff == 0 {
		maxBackoff = 10 * time.Millisecond
	}

	for {
		// Phase 1: Try all shards (NextShard strategy)
		for shardOffset := uint64(0); shardOffset < r.numShards; shardOffset++ {
			shardIdx := (startShard + shardOffset) & r.mask
			shard := r.shards[shardIdx]

			for retry := 0; retry < config.MaxRetries; retry++ {
				if shard.write(value) {
					return true
				}
			}
		}

		// Phase 2: Adaptive backoff
		state.backoffCount++
		if config.MaxBackoffs > 0 && state.backoffCount >= config.MaxBackoffs {
			return false
		}

		// Add jitter: 75-125% of current backoff
		jitter := 0.75 + rand.Float64()*0.5
		sleepDuration := time.Duration(float64(state.currentBackoff) * jitter)
		time.Sleep(sleepDuration)

		// Exponential increase, capped at max
		state.currentBackoff = time.Duration(float64(state.currentBackoff) * multiplier)
		if state.currentBackoff > maxBackoff {
			state.currentBackoff = maxBackoff
		}
	}
}


