package ring

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// WriteWithBackoff Tests
// =============================================================================

// TestWriteWithBackoff tests the backoff write mechanism
func TestWriteWithBackoff(t *testing.T) {
	ring, err := NewShardedRing(64, 4) // Small ring: 16 per shard
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	config := WriteConfig{
		MaxRetries:      5,
		BackoffDuration: 10 * time.Microsecond,
		MaxBackoffs:     100, // Give up after 100 backoff cycles
	}

	// Fill the ring completely (single shard)
	for i := 0; i < 16; i++ {
		if !ring.Write(0, i) {
			t.Fatalf("Initial fill failed at %d", i)
		}
	}

	// Now the shard is full - WriteWithBackoff should fail after max backoffs
	// since there's no consumer draining
	success := ring.WriteWithBackoff(0, 999, config)
	if success {
		t.Error("WriteWithBackoff should have failed on full ring with no consumer")
	}

	// Drain some items
	for i := 0; i < 5; i++ {
		ring.TryRead()
	}

	// Now write should succeed
	success = ring.WriteWithBackoff(0, 999, config)
	if !success {
		t.Error("WriteWithBackoff should have succeeded after draining")
	}
}

// TestWriteWithBackoffConcurrent tests backoff with concurrent producer and consumer
func TestWriteWithBackoffConcurrent(t *testing.T) {
	// Small ring - 128 items
	ring, err := NewShardedRing(128, 8)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	config := WriteConfig{
		MaxRetries:      10,
		BackoffDuration: 100 * time.Microsecond,
		MaxBackoffs:     0, // Unlimited - will eventually succeed
	}

	numProducers := 4
	itemsPerProducer := 1000
	totalItems := int64(numProducers * itemsPerProducer)

	var producerWg sync.WaitGroup
	var consumerWg sync.WaitGroup
	var itemsWritten atomic.Int64
	var itemsRead atomic.Int64
	var producersDone atomic.Bool

	// Start consumer
	consumerWg.Add(1)
	go func() {
		defer consumerWg.Done()
		for !producersDone.Load() || ring.Len() > 0 {
			batch := ring.ReadBatch(50)
			itemsRead.Add(int64(len(batch)))
			if len(batch) == 0 {
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	// Start producers with backoff
	for p := 0; p < numProducers; p++ {
		producerWg.Add(1)
		go func(producerID int) {
			defer producerWg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				// This will backoff instead of spinning aggressively
				ring.WriteWithBackoff(uint64(producerID), i, config)
				itemsWritten.Add(1)
			}
		}(p)
	}

	// Wait for producers
	done := make(chan struct{})
	go func() {
		producerWg.Wait()
		producersDone.Store(true)
		consumerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("Test timed out")
	}

	written := itemsWritten.Load()
	read := itemsRead.Load()

	if written != totalItems {
		t.Errorf("Items written = %d, want %d", written, totalItems)
	}

	if read != written {
		t.Errorf("Items read = %d, want %d", read, written)
	}

	t.Logf("Successfully processed %d items through 128-item ring with backoff", written)
}

// TestDefaultWriteConfig tests the default configuration
func TestDefaultWriteConfig(t *testing.T) {
	config := DefaultWriteConfig()

	if config.MaxRetries != 10 {
		t.Errorf("Default MaxRetries = %d, want 10", config.MaxRetries)
	}
	if config.BackoffDuration != 100*time.Microsecond {
		t.Errorf("Default BackoffDuration = %v, want 100µs", config.BackoffDuration)
	}
	if config.MaxBackoffs != 0 {
		t.Errorf("Default MaxBackoffs = %d, want 0 (unlimited)", config.MaxBackoffs)
	}
}

// =============================================================================
// Strategy Tests
// =============================================================================

// TestRetryStrategyString tests the String method of RetryStrategy
func TestRetryStrategyString(t *testing.T) {
	tests := []struct {
		strategy RetryStrategy
		want     string
	}{
		{SleepBackoff, "SleepBackoff"},
		{NextShard, "NextShard"},
		{RandomShard, "RandomShard"},
		{AdaptiveBackoff, "AdaptiveBackoff"},
		{SpinThenYield, "SpinThenYield"},
		{Hybrid, "Hybrid"},
		{AutoAdaptive, "AutoAdaptive"},
		{RetryStrategy(99), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.strategy.String(); got != tt.want {
				t.Errorf("RetryStrategy.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNewWriter tests Writer creation and basic functionality
func TestNewWriter(t *testing.T) {
	ring, err := NewShardedRing(1024, 4)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	config := WriteConfig{
		Strategy:        NextShard,
		MaxRetries:      10,
		BackoffDuration: 100 * time.Microsecond,
		MaxBackoffs:     100,
	}

	writer := NewWriter(ring, 0, config)
	if writer == nil {
		t.Fatal("NewWriter returned nil")
	}

	// Test basic write
	if !writer.Write("test") {
		t.Error("Writer.Write failed on empty ring")
	}

	// Read back
	val, ok := ring.TryRead()
	if !ok {
		t.Error("TryRead failed")
	}
	if val != "test" {
		t.Errorf("Got %v, want 'test'", val)
	}
}

// TestWriterStrategies tests that each strategy can write successfully
func TestWriterStrategies(t *testing.T) {
	strategies := []RetryStrategy{
		SleepBackoff,
		NextShard,
		RandomShard,
		AdaptiveBackoff,
		SpinThenYield,
		Hybrid,
		AutoAdaptive,
	}

	for _, strategy := range strategies {
		t.Run(strategy.String(), func(t *testing.T) {
			// Use large ring so single-shard strategies don't fill up
			// 400 capacity with 4 shards = 100 per shard
			ring, err := NewShardedRing(400, 4)
			if err != nil {
				t.Fatalf("NewShardedRing failed: %v", err)
			}

			config := WriteConfig{
				Strategy:           strategy,
				MaxRetries:         10,
				BackoffDuration:    10 * time.Microsecond,
				MaxBackoffs:        100,
				MaxBackoffDuration: 1 * time.Millisecond,
				BackoffMultiplier:  2.0,
			}

			writer := NewWriter(ring, 0, config)

			// Write 50 items (fits in one shard with room to spare)
			numItems := 50
			for i := 0; i < numItems; i++ {
				if !writer.Write(i) {
					t.Errorf("Write failed at index %d", i)
				}
			}

			if ring.Len() != uint64(numItems) {
				t.Errorf("Ring length = %d, want %d", ring.Len(), numItems)
			}

			// Read them all back (don't check order for multi-shard strategies)
			readCount := 0
			for {
				_, ok := ring.TryRead()
				if !ok {
					break
				}
				readCount++
			}

			if readCount != numItems {
				t.Errorf("Read %d items, want %d", readCount, numItems)
			}
		})
	}
}

// TestNextShardFallback tests that NextShard strategy falls back to other shards
func TestNextShardFallback(t *testing.T) {
	ring, err := NewShardedRing(64, 4) // 16 per shard
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Fill shard 0 completely using direct writes
	for i := 0; i < 16; i++ {
		if !ring.Write(0, i) {
			t.Fatalf("Failed to fill shard 0 at index %d", i)
		}
	}

	// Verify shard 0 is full
	if ring.Write(0, "overflow") {
		t.Error("Shard 0 should be full")
	}

	// Now use NextShard strategy - it should write to another shard
	config := WriteConfig{
		Strategy:        NextShard,
		MaxRetries:      1, // Low retries to quickly move to next shard
		BackoffDuration: 10 * time.Microsecond,
		MaxBackoffs:     10,
	}

	writer := NewWriter(ring, 0, config) // Producer 0 would normally use shard 0

	// This should succeed by falling back to other shards
	if !writer.Write("fallback") {
		t.Error("NextShard strategy should have found space in another shard")
	}

	// Verify total count
	if ring.Len() != 17 {
		t.Errorf("Ring length = %d, want 17", ring.Len())
	}
}

// TestWriterConcurrent tests concurrent writes with different strategies
func TestWriterConcurrent(t *testing.T) {
	strategies := []RetryStrategy{
		SleepBackoff,
		NextShard,
		SpinThenYield,
		AutoAdaptive,
	}

	for _, strategy := range strategies {
		t.Run(strategy.String(), func(t *testing.T) {
			ring, err := NewShardedRing(10000, 8)
			if err != nil {
				t.Fatalf("NewShardedRing failed: %v", err)
			}

			config := WriteConfig{
				Strategy:        strategy,
				MaxRetries:      10,
				BackoffDuration: 10 * time.Microsecond,
				MaxBackoffs:     0, // Unlimited
			}

			numProducers := 4
			itemsPerProducer := 500
			var wg sync.WaitGroup
			var writeFailures atomic.Int64

			for p := 0; p < numProducers; p++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					writer := NewWriter(ring, uint64(producerID), config)
					for i := 0; i < itemsPerProducer; i++ {
						if !writer.Write(producerID*10000 + i) {
							writeFailures.Add(1)
						}
					}
				}(p)
			}

			wg.Wait()

			expectedItems := uint64(numProducers*itemsPerProducer) - uint64(writeFailures.Load())
			actualLen := ring.Len()

			if actualLen != expectedItems {
				t.Errorf("Len() = %d, want %d (failures: %d)", actualLen, expectedItems, writeFailures.Load())
			}

			// Read all items
			readCount := 0
			for {
				_, ok := ring.TryRead()
				if !ok {
					break
				}
				readCount++
			}

			if readCount != int(expectedItems) {
				t.Errorf("Read %d items, want %d", readCount, expectedItems)
			}
		})
	}
}

// TestWriterReset tests the Reset method
func TestWriterReset(t *testing.T) {
	ring, err := NewShardedRing(256, 4)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	config := WriteConfig{
		Strategy:           AdaptiveBackoff,
		MaxRetries:         5,
		BackoffDuration:    100 * time.Microsecond,
		MaxBackoffs:        10,
		MaxBackoffDuration: 1 * time.Millisecond,
		BackoffMultiplier:  2.0,
	}

	writer := NewWriter(ring, 0, config)

	// Write some items
	for i := 0; i < 10; i++ {
		writer.Write(i)
	}

	// Reset the writer
	writer.Reset()

	// State should be reset
	if writer.state.backoffCount != 0 {
		t.Errorf("backoffCount = %d after Reset, want 0", writer.state.backoffCount)
	}
	if writer.state.currentBackoff != config.BackoffDuration {
		t.Errorf("currentBackoff = %v after Reset, want %v", writer.state.currentBackoff, config.BackoffDuration)
	}
}

// TestDefaultWriteConfigWithStrategy tests default config includes strategy fields
func TestDefaultWriteConfigWithStrategy(t *testing.T) {
	config := DefaultWriteConfig()

	// Default is now AutoAdaptive for high-performance by default
	if config.Strategy != AutoAdaptive {
		t.Errorf("Default Strategy = %v, want AutoAdaptive", config.Strategy)
	}
	if config.MaxRetries != 10 {
		t.Errorf("Default MaxRetries = %d, want 10", config.MaxRetries)
	}
	if config.BackoffDuration != 100*time.Microsecond {
		t.Errorf("Default BackoffDuration = %v, want 100µs", config.BackoffDuration)
	}
	if config.MaxBackoffs != 0 {
		t.Errorf("Default MaxBackoffs = %d, want 0 (unlimited)", config.MaxBackoffs)
	}
	if config.MaxBackoffDuration != 10*time.Millisecond {
		t.Errorf("Default MaxBackoffDuration = %v, want 10ms", config.MaxBackoffDuration)
	}
	if config.BackoffMultiplier != 2.0 {
		t.Errorf("Default BackoffMultiplier = %v, want 2.0", config.BackoffMultiplier)
	}
	// AutoAdaptive-specific defaults
	if config.AdaptiveIdleIterations != 100000 {
		t.Errorf("Default AdaptiveIdleIterations = %d, want 100000", config.AdaptiveIdleIterations)
	}
	if config.AdaptiveWarmupIterations != 1000 {
		t.Errorf("Default AdaptiveWarmupIterations = %d, want 1000", config.AdaptiveWarmupIterations)
	}
	if config.AdaptiveSleepDuration != 100*time.Microsecond {
		t.Errorf("Default AdaptiveSleepDuration = %v, want 100µs", config.AdaptiveSleepDuration)
	}
}

// TestConfigPresets tests the various configuration presets
func TestConfigPresets(t *testing.T) {
	// Test HighThroughputConfig
	ht := HighThroughputConfig()
	if ht.Strategy != AutoAdaptive {
		t.Errorf("HighThroughputConfig Strategy = %v, want AutoAdaptive", ht.Strategy)
	}
	if ht.AdaptiveIdleIterations != 500000 {
		t.Errorf("HighThroughputConfig AdaptiveIdleIterations = %d, want 500000", ht.AdaptiveIdleIterations)
	}

	// Test LowLatencyConfig
	ll := LowLatencyConfig()
	if ll.Strategy != SpinThenYield {
		t.Errorf("LowLatencyConfig Strategy = %v, want SpinThenYield", ll.Strategy)
	}

	// Test CPUFriendlyConfig
	cf := CPUFriendlyConfig()
	if cf.Strategy != AutoAdaptive {
		t.Errorf("CPUFriendlyConfig Strategy = %v, want AutoAdaptive", cf.Strategy)
	}
	if cf.AdaptiveIdleIterations != 10000 {
		t.Errorf("CPUFriendlyConfig AdaptiveIdleIterations = %d, want 10000", cf.AdaptiveIdleIterations)
	}

	// Test BurstyTrafficConfig
	bt := BurstyTrafficConfig()
	if bt.Strategy != AutoAdaptive {
		t.Errorf("BurstyTrafficConfig Strategy = %v, want AutoAdaptive", bt.Strategy)
	}
	if bt.AdaptiveWarmupIterations != 10000 {
		t.Errorf("BurstyTrafficConfig AdaptiveWarmupIterations = %d, want 10000", bt.AdaptiveWarmupIterations)
	}

	// Test LegacySleepConfig
	ls := LegacySleepConfig()
	if ls.Strategy != SleepBackoff {
		t.Errorf("LegacySleepConfig Strategy = %v, want SleepBackoff", ls.Strategy)
	}
}


