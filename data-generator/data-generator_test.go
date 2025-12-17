package common

import (
	"context"
	"io"
	"math"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestDataGeneratorRateAccuracy verifies that the generator produces data
// at the target bitrate within acceptable tolerance.
func TestDataGeneratorRateAccuracy(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping rate accuracy test in short mode")
	}

	testCases := []struct {
		name             string
		bitrate          uint64
		duration         time.Duration
		tolerancePercent float64
	}{
		{"2Mbps_5sec", 2_000_000, 5 * time.Second, 3.0},
		{"10Mbps_5sec", 10_000_000, 5 * time.Second, 3.0},
		{"25Mbps_5sec", 25_000_000, 5 * time.Second, 3.0},
		{"50Mbps_5sec", 50_000_000, 5 * time.Second, 5.0},
		// 75 Mb/s requires ~6440 packets/sec (155µs interval)
		// This may not be achievable on all systems due to busy-wait CPU overhead
		{"75Mbps_5sec", 75_000_000, 5 * time.Second, 10.0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.duration)
			defer cancel()

			gen := NewDataGenerator(ctx, tc.bitrate, 0)
			buf := make([]byte, 1456)

			start := time.Now()
			var totalBytes uint64
			for {
				n, err := gen.Read(buf)
				if err != nil {
					break
				}
				totalBytes += uint64(n)
			}
			elapsed := time.Since(start)

			actualBitrate := float64(totalBytes*8) / elapsed.Seconds()
			expectedBitrate := float64(tc.bitrate)
			deviation := math.Abs(actualBitrate-expectedBitrate) / expectedBitrate * 100

			t.Logf("Target: %.2f Mb/s, Actual: %.2f Mb/s, Deviation: %.2f%%",
				expectedBitrate/1e6, actualBitrate/1e6, deviation)
			t.Logf("Total bytes: %d, Duration: %v, Packets: %d",
				totalBytes, elapsed, gen.Stats().PacketsGenerated)

			if deviation > tc.tolerancePercent {
				t.Errorf("Rate deviation %.2f%% exceeds tolerance %.2f%%",
					deviation, tc.tolerancePercent)
			}
		})
	}
}

// TestDataGeneratorSmoothness measures inter-packet timing variance.
// Low variance indicates smooth, consistent traffic.
func TestDataGeneratorSmoothness(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping smoothness test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	gen := NewDataGenerator(ctx, 10_000_000, 0) // 10 Mb/s = ~858 pkt/s
	buf := make([]byte, 1456)

	var intervals []time.Duration
	lastTime := time.Now()

	// Collect 500 interval samples
	for i := 0; i < 500; i++ {
		_, err := gen.Read(buf)
		if err != nil {
			break
		}
		now := time.Now()
		intervals = append(intervals, now.Sub(lastTime))
		lastTime = now
	}

	if len(intervals) < 100 {
		t.Fatalf("Not enough samples: got %d, want >= 100", len(intervals))
	}

	// Calculate statistics
	var sum float64
	for _, d := range intervals {
		sum += float64(d)
	}
	mean := sum / float64(len(intervals))

	var variance float64
	for _, d := range intervals {
		variance += math.Pow(float64(d)-mean, 2)
	}
	stddev := math.Sqrt(variance / float64(len(intervals)))

	// Coefficient of variation (CV)
	cv := stddev / mean * 100

	expectedInterval := time.Duration(1e9 / gen.PacketsPerSecond())
	t.Logf("Expected interval: %v", expectedInterval)
	t.Logf("Mean interval: %.2f µs", mean/1000)
	t.Logf("Stddev: %.2f µs", stddev/1000)
	t.Logf("CV (coefficient of variation): %.2f%%", cv)

	// CV > 100% indicates very bursty traffic
	if cv > 100 {
		t.Errorf("Traffic too bursty: CV=%.2f%% (want <100%%)", cv)
	}
}

// TestDataGeneratorContextCancellation verifies clean shutdown on context cancel.
func TestDataGeneratorContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gen := NewDataGenerator(ctx, 10_000_000, 0)
	buf := make([]byte, 1456)

	// Start reading in goroutine
	done := make(chan error, 1)
	go func() {
		for {
			_, err := gen.Read(buf)
			if err != nil {
				done <- err
				return
			}
		}
	}()

	// Let it run briefly
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Should exit quickly
	select {
	case err := <-done:
		if err != io.EOF {
			t.Errorf("Expected io.EOF, got %v", err)
		}
	case <-time.After(time.Second):
		t.Error("Generator did not exit after context cancellation")
	}
}

// TestDataGeneratorPayloadContent verifies the payload contains expected pattern.
func TestDataGeneratorPayloadContent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gen := NewDataGenerator(ctx, 1_000_000, 100) // Small payload for easy inspection
	buf := make([]byte, 100)

	n, err := gen.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != 100 {
		t.Errorf("Read returned %d bytes, expected 100", n)
	}

	// Verify pattern
	pattern := []byte("abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ!@#$%^&*()")
	for i := 0; i < n; i++ {
		expected := pattern[i%len(pattern)]
		if buf[i] != expected {
			t.Errorf("Byte %d: expected %c, got %c", i, expected, buf[i])
		}
	}
}

// TestDataGeneratorStats verifies statistics collection.
func TestDataGeneratorStats(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	gen := NewDataGenerator(ctx, 10_000_000, 1000)
	buf := make([]byte, 1000)

	// Read some packets
	for i := 0; i < 100; i++ {
		_, err := gen.Read(buf)
		if err != nil {
			break
		}
	}

	stats := gen.Stats()

	if stats.PacketsGenerated < 50 {
		t.Errorf("Expected at least 50 packets, got %d", stats.PacketsGenerated)
	}

	expectedBytes := stats.PacketsGenerated * 1000
	if stats.BytesGenerated != expectedBytes {
		t.Errorf("Expected %d bytes, got %d", expectedBytes, stats.BytesGenerated)
	}

	if stats.TargetBitrate != 10_000_000 {
		t.Errorf("Expected target bitrate 10000000, got %d", stats.TargetBitrate)
	}

	if stats.Duration <= 0 {
		t.Error("Duration should be positive")
	}

	t.Logf("Stats: %+v", stats)
}

// TestDataGeneratorPayloadSize verifies custom payload size works.
func TestDataGeneratorPayloadSize(t *testing.T) {
	testCases := []struct {
		name        string
		payloadSize uint32
		expected    int
	}{
		{"default", 0, 1456},
		{"custom_1316", 1316, 1316},
		{"custom_1000", 1000, 1000},
		{"small_100", 100, 100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gen := NewDataGenerator(ctx, 1_000_000, tc.payloadSize)

			if gen.PayloadSize() != tc.expected {
				t.Errorf("PayloadSize(): got %d, want %d", gen.PayloadSize(), tc.expected)
			}

			buf := make([]byte, 2000) // Larger than any payload
			n, err := gen.Read(buf)
			if err != nil {
				t.Fatalf("Read failed: %v", err)
			}
			if n != tc.expected {
				t.Errorf("Read returned %d bytes, want %d", n, tc.expected)
			}
		})
	}
}

// TestDataGeneratorSmallBuffer verifies behavior with buffer smaller than payload.
func TestDataGeneratorSmallBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gen := NewDataGenerator(ctx, 1_000_000, 1456)
	buf := make([]byte, 100) // Smaller than payload size

	n, err := gen.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	// Should only read up to buffer size
	if n != 100 {
		t.Errorf("Read returned %d bytes, expected 100", n)
	}
}

// TestDataGeneratorUnlimited verifies raw throughput without rate limiting.
// This demonstrates the maximum possible throughput of the generator itself,
// which is expected to be in the Gb/s range since it's just a memcpy.
func TestDataGeneratorUnlimited(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping unlimited rate test in short mode")
	}

	// Create a generator with no rate limiting (very high target that won't be limiting)
	// We use a bitrate so high that the rate limiter will never block
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 100 Gb/s target = effectively unlimited
	gen := NewDataGeneratorUnlimited(ctx, 1456)
	buf := make([]byte, 1456)

	start := time.Now()
	var totalBytes uint64
	var packets int
	for {
		n, err := gen.Read(buf)
		if err != nil {
			break
		}
		totalBytes += uint64(n)
		packets++
	}
	elapsed := time.Since(start)

	actualBitrate := float64(totalBytes*8) / elapsed.Seconds()
	packetsPerSec := float64(packets) / elapsed.Seconds()

	t.Logf("Unlimited mode throughput:")
	t.Logf("  Duration: %v", elapsed)
	t.Logf("  Packets: %d (%.0f pkt/s)", packets, packetsPerSec)
	t.Logf("  Bytes: %d", totalBytes)
	t.Logf("  Throughput: %.2f Gb/s (%.2f Mb/s)", actualBitrate/1e9, actualBitrate/1e6)

	// Expect at least 1 Gb/s in unlimited mode (just memcpy)
	if actualBitrate < 1e9 {
		t.Errorf("Unlimited throughput too low: %.2f Gb/s (expected >= 1 Gb/s)", actualBitrate/1e9)
	}
}

// TestDataGeneratorMinimalAllocations verifies minimal allocations in hot path.
// The rate limiter's Wait() creates 1 timer allocation per call, which is expected.
// This is ~4300 allocs/sec at 50 Mb/s, which is negligible compared to the
// 6.25 million channel ops/sec in the old design.
func TestDataGeneratorMinimalAllocations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gen := NewDataGenerator(ctx, 100_000_000, 0) // High rate to minimize wait time
	buf := make([]byte, 1456)

	// Warm up
	for i := 0; i < 100; i++ {
		gen.Read(buf)
	}

	// Measure allocations
	allocs := testing.AllocsPerRun(1000, func() {
		gen.Read(buf)
	})

	t.Logf("Allocations per Read: %.2f", allocs)

	// Rate limiter Wait() creates 1 timer allocation per call
	// This is acceptable - 4300 allocs/sec vs 6.25M channel ops/sec previously
	if allocs > 2 {
		t.Errorf("Too many allocations per Read: got %.2f, want <= 2", allocs)
	}
}

// BenchmarkDataGenerator benchmarks the generator at various bitrates.
func BenchmarkDataGenerator(b *testing.B) {
	benchmarks := []struct {
		name    string
		bitrate uint64
	}{
		{"2Mbps", 2_000_000},
		{"10Mbps", 10_000_000},
		{"50Mbps", 50_000_000},
		{"100Mbps", 100_000_000},
		{"500Mbps", 500_000_000},
		{"1Gbps", 1_000_000_000},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			ctx := context.Background()
			gen := NewDataGenerator(ctx, bm.bitrate, 0)
			buf := make([]byte, 1456)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				gen.Read(buf)
			}
		})
	}
}

// BenchmarkRateLimiterOverhead measures pure rate limiter overhead.
func BenchmarkRateLimiterOverhead(b *testing.B) {
	rates := []struct {
		name        string
		ratePerSec  float64
		description string
	}{
		{"1k_ops", 1000, "1K ops/sec"},
		{"10k_ops", 10000, "10K ops/sec"},
		{"100k_ops", 100000, "100K ops/sec (50 Mb/s equivalent)"},
		{"1M_ops", 1000000, "1M ops/sec (1 Gb/s equivalent)"},
	}

	for _, r := range rates {
		b.Run(r.name, func(b *testing.B) {
			limiter := rate.NewLimiter(rate.Limit(r.ratePerSec), 10)
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				limiter.Wait(ctx)
			}
		})
	}
}

// BenchmarkDataGeneratorThroughput measures actual throughput achieved.
func BenchmarkDataGeneratorThroughput(b *testing.B) {
	// Run for fixed duration and measure throughput
	duration := 2 * time.Second

	benchmarks := []struct {
		name    string
		bitrate uint64
	}{
		{"Target_10Mbps", 10_000_000},
		{"Target_50Mbps", 50_000_000},
		{"Target_100Mbps", 100_000_000},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()

			gen := NewDataGenerator(ctx, bm.bitrate, 0)
			buf := make([]byte, 1456)

			b.ResetTimer()

			var totalBytes uint64
			start := time.Now()
			for {
				n, err := gen.Read(buf)
				if err != nil {
					break
				}
				totalBytes += uint64(n)
			}
			elapsed := time.Since(start)

			actualBitrate := float64(totalBytes*8) / elapsed.Seconds()
			b.ReportMetric(actualBitrate/1e6, "Mb/s")
			b.ReportMetric(float64(gen.Stats().PacketsGenerated)/elapsed.Seconds(), "pkt/s")
		})
	}
}
