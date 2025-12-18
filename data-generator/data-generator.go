// Package common provides shared utilities for SRT client applications.
package common

import (
	"context"
	"io"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// DataGeneratorStats holds statistics about data generation.
type DataGeneratorStats struct {
	PacketsGenerated uint64
	BytesGenerated   uint64
	StartTime        time.Time
	Duration         time.Duration
	TargetBitrate    uint64
	ActualBitrate    float64
}

// DataGenerator generates test data at a specified bitrate.
// It implements io.Reader and can be used with any io.Writer.
//
// The generator uses a token bucket rate limiter to control the rate
// of packet generation. Each Read() call returns one packet's worth
// of data (up to payloadSize bytes) and waits for the rate limiter.
//
// The payload is pre-filled with a test pattern at initialization
// and reused for every Read() call, ensuring zero allocations in the
// hot path.
//
// For high-rate traffic (>10k packets/sec), the generator uses a
// time-based pacing approach that calculates when each packet should
// be sent based on its sequence number, avoiding timer allocation
// overhead.
type DataGenerator struct {
	payloadSize   int
	packetsPerSec float64
	limiter       *rate.Limiter
	payload       []byte
	ctx           context.Context

	// High-rate optimization: time-based pacing
	highRateMode  bool
	intervalNanos int64  // Expected interval between packets in nanoseconds
	startTimeNano int64  // Start time in nanoseconds (for pacing calculation)
	packetCount   uint64 // Non-atomic, only accessed from Read()

	// Statistics (atomic for thread safety)
	packetsGenerated atomic.Uint64
	bytesGenerated   atomic.Uint64
	startTime        time.Time
	targetBitrate    uint64
}

// NewDataGenerator creates a rate-limited data generator.
//
// Parameters:
//   - ctx: Context for cancellation
//   - bitrate: Target bits per second
//   - payloadSize: Bytes per packet (default: 1456 if 0)
//
// The generator calculates packets per second from the bitrate and payload size,
// then uses a token bucket limiter to maintain the target rate.
func NewDataGenerator(ctx context.Context, bitrate uint64, payloadSize uint32) *DataGenerator {
	if payloadSize == 0 {
		payloadSize = 1456 // MAX_PAYLOAD_SIZE from SRT
	}

	// Convert bitrate to packets per second
	bytesPerSec := float64(bitrate) / 8.0
	packetsPerSec := bytesPerSec / float64(payloadSize)

	// Pre-fill payload with test pattern ONCE (never changes)
	payload := make([]byte, payloadSize)
	pattern := []byte("abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ!@#$%^&*()")
	for i := range payload {
		payload[i] = pattern[i%len(pattern)]
	}

	// Calculate interval between packets in nanoseconds
	intervalNanos := int64(float64(time.Second) / packetsPerSec)

	// For traffic where interval < 1ms (>1000 pkt/s), use time-based pacing
	// This avoids the timer allocation overhead of rate.Limiter.Wait()
	// At 50 Mb/s with 1456-byte packets: 4293 pkt/s = 233µs interval
	highRateMode := packetsPerSec > 1000

	// Create rate limiter with burst allowance (used for low-rate mode)
	burstSize := 1
	if !highRateMode {
		burstSize = 2
	}

	now := time.Now()
	return &DataGenerator{
		payloadSize:   int(payloadSize),
		packetsPerSec: packetsPerSec,
		limiter:       rate.NewLimiter(rate.Limit(packetsPerSec), burstSize),
		payload:       payload,
		ctx:           ctx,
		highRateMode:  highRateMode,
		intervalNanos: intervalNanos,
		startTimeNano: now.UnixNano(),
		packetCount:   0,
		startTime:     now,
		targetBitrate: bitrate,
	}
}

// Read generates data at the configured rate.
// Returns up to payloadSize bytes per call.
//
// Each call to Read:
// 1. Checks for context cancellation (non-blocking)
// 2. Waits for the rate limiter (blocking, but context-aware) - unless unlimited mode
// 3. Copies pre-filled payload to the buffer (single memcpy)
//
// Returns io.EOF when the context is cancelled.
func (g *DataGenerator) Read(p []byte) (int, error) {
	// Check for cancellation (non-blocking)
	select {
	case <-g.ctx.Done():
		return 0, io.EOF
	default:
	}

	// Determine bytes to return (up to payload size)
	n := len(p)
	if n > g.payloadSize {
		n = g.payloadSize
	}

	// Rate limiting (skip if unlimited mode)
	if g.limiter != nil {
		if g.highRateMode {
			// High-rate mode: use time-based pacing with busy-wait for final microseconds
			// This avoids timer allocation overhead of rate.Limiter.Wait()
			if err := g.waitHighRate(); err != nil {
				return 0, io.EOF
			}
		} else {
			// Normal mode: use rate.Limiter.Wait()
			if err := g.limiter.Wait(g.ctx); err != nil {
				return 0, io.EOF
			}
		}
	}
	// If limiter is nil, no rate limiting (unlimited mode)

	// Copy pre-filled payload (single memcpy!)
	copy(p[:n], g.payload[:n])

	// Update statistics (atomic, lock-free)
	g.packetsGenerated.Add(1)
	g.bytesGenerated.Add(uint64(n))

	return n, nil
}

// waitHighRate implements efficient high-rate pacing using time-based scheduling.
// It calculates when packet N should be sent based on start time and interval,
// then waits until that time.
//
// For intervals > 1ms, it uses sleep + busy-wait.
// For intervals < 1ms, it uses pure busy-wait (required for sub-millisecond precision).
func (g *DataGenerator) waitHighRate() error {
	// Calculate when this packet should be sent
	// Packet N should be sent at: startTime + N * interval
	targetTime := g.startTimeNano + int64(g.packetCount)*g.intervalNanos
	g.packetCount++

	// If interval is > 1ms, we can use sleep for most of it
	if g.intervalNanos > 1000000 {
		for {
			now := time.Now().UnixNano()
			remaining := targetTime - now

			if remaining <= 0 {
				return nil
			}

			select {
			case <-g.ctx.Done():
				return g.ctx.Err()
			default:
			}

			if remaining > 500000 { // > 500µs
				time.Sleep(time.Duration(remaining - 200000)) // Wake up 200µs early
			}
			// Busy-wait for the rest
		}
	}

	// For sub-millisecond intervals, use pure busy-wait with periodic context check
	// This is necessary because time.Sleep() has ~50-100µs minimum granularity on Linux
	checkCounter := 0
	for {
		now := time.Now().UnixNano()
		if now >= targetTime {
			return nil
		}

		// Check for cancellation every 1000 iterations (roughly every 10-50µs at high rate)
		checkCounter++
		if checkCounter >= 1000 {
			checkCounter = 0
			select {
			case <-g.ctx.Done():
				return g.ctx.Err()
			default:
			}
		}
	}
}

// Stats returns generation statistics.
func (g *DataGenerator) Stats() DataGeneratorStats {
	packets := g.packetsGenerated.Load()
	bytes := g.bytesGenerated.Load()
	duration := time.Since(g.startTime)

	var actualBitrate float64
	if duration.Seconds() > 0 {
		actualBitrate = float64(bytes*8) / duration.Seconds()
	}

	return DataGeneratorStats{
		PacketsGenerated: packets,
		BytesGenerated:   bytes,
		StartTime:        g.startTime,
		Duration:         duration,
		TargetBitrate:    g.targetBitrate,
		ActualBitrate:    actualBitrate,
	}
}

// ActualBitrate returns the measured bitrate since start in bits per second.
func (g *DataGenerator) ActualBitrate() float64 {
	bytes := g.bytesGenerated.Load()
	duration := time.Since(g.startTime)
	if duration.Seconds() > 0 {
		return float64(bytes*8) / duration.Seconds()
	}
	return 0
}

// PacketsPerSecond returns the target packets per second rate.
func (g *DataGenerator) PacketsPerSecond() float64 {
	return g.packetsPerSec
}

// PayloadSize returns the configured payload size in bytes.
func (g *DataGenerator) PayloadSize() int {
	return g.payloadSize
}

// Close is a no-op that satisfies io.Closer.
// The DataGenerator has no resources to release since it uses no
// channels or goroutines.
func (g *DataGenerator) Close() error {
	return nil
}

// NewDataGeneratorUnlimited creates a data generator with no rate limiting.
// This is useful for testing maximum throughput of downstream components.
// The generator will produce data as fast as possible (limited only by memcpy speed).
func NewDataGeneratorUnlimited(ctx context.Context, payloadSize uint32) *DataGenerator {
	if payloadSize == 0 {
		payloadSize = 1456
	}

	// Pre-fill payload with test pattern
	payload := make([]byte, payloadSize)
	pattern := []byte("abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ!@#$%^&*()")
	for i := range payload {
		payload[i] = pattern[i%len(pattern)]
	}

	now := time.Now()
	return &DataGenerator{
		payloadSize:   int(payloadSize),
		packetsPerSec: 0, // Indicates unlimited mode
		limiter:       nil,
		payload:       payload,
		ctx:           ctx,
		highRateMode:  false,
		intervalNanos: 0,
		startTimeNano: now.UnixNano(),
		packetCount:   0,
		startTime:     now,
		targetBitrate: 0,
	}
}
