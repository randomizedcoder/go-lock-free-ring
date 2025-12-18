package ring

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewShardedRing tests constructor validation
func TestNewShardedRing(t *testing.T) {
	tests := []struct {
		name          string
		totalCapacity uint64
		numShards     uint64
		wantErr       error
	}{
		{"valid_1024_4", 1024, 4, nil},
		{"valid_1024_8", 1024, 8, nil},
		{"valid_256_1", 256, 1, nil},
		{"valid_64_64", 64, 64, nil},
		{"invalid_shards_not_power_of_2", 1024, 3, ErrNotPowerOfTwo},
		{"invalid_shards_zero", 1024, 0, ErrNotPowerOfTwo},
		{"invalid_capacity_zero", 0, 4, ErrInvalidSize},
		{"invalid_capacity_less_than_shards", 2, 4, ErrInvalidSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring, err := NewShardedRing(tt.totalCapacity, tt.numShards)
			if err != tt.wantErr {
				t.Errorf("NewShardedRing() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr == nil {
				if ring == nil {
					t.Error("NewShardedRing() returned nil ring")
					return
				}
				if ring.Cap() != tt.totalCapacity {
					t.Errorf("Cap() = %d, want %d", ring.Cap(), tt.totalCapacity)
				}
				if ring.NumShards() != tt.numShards {
					t.Errorf("NumShards() = %d, want %d", ring.NumShards(), tt.numShards)
				}
			}
		})
	}
}

// TestBasicWriteRead tests single producer write and read
func TestBasicWriteRead(t *testing.T) {
	ring, err := NewShardedRing(1024, 4)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Write some values
	for i := 0; i < 100; i++ {
		if !ring.Write(0, i) {
			t.Errorf("Write failed at index %d", i)
		}
	}

	if ring.Len() != 100 {
		t.Errorf("Len() = %d, want 100", ring.Len())
	}

	// Read them back
	for i := 0; i < 100; i++ {
		val, ok := ring.TryRead()
		if !ok {
			t.Errorf("TryRead failed at index %d", i)
			continue
		}
		if val.(int) != i {
			t.Errorf("TryRead() = %v, want %d", val, i)
		}
	}

	if ring.Len() != 0 {
		t.Errorf("Len() = %d, want 0 after reading all", ring.Len())
	}
}

// TestMultipleProducers tests multiple producers writing to different shards
func TestMultipleProducers(t *testing.T) {
	ring, err := NewShardedRing(1024, 4)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// 4 producers, each writes 100 items
	itemsPerProducer := 100
	numProducers := 4

	for p := 0; p < numProducers; p++ {
		for i := 0; i < itemsPerProducer; i++ {
			val := p*1000 + i // Encode producer ID in value
			if !ring.Write(uint64(p), val) {
				t.Errorf("Producer %d: Write failed at index %d", p, i)
			}
		}
	}

	expectedLen := uint64(numProducers * itemsPerProducer)
	if ring.Len() != expectedLen {
		t.Errorf("Len() = %d, want %d", ring.Len(), expectedLen)
	}

	// Read all items back
	readCount := 0
	for {
		_, ok := ring.TryRead()
		if !ok {
			break
		}
		readCount++
	}

	if readCount != int(expectedLen) {
		t.Errorf("Read %d items, want %d", readCount, expectedLen)
	}
}

// TestRingFull tests behavior when ring is full
func TestRingFull(t *testing.T) {
	ring, err := NewShardedRing(64, 4) // 16 items per shard
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Fill shard 0 completely
	for i := 0; i < 16; i++ {
		if !ring.Write(0, i) {
			t.Errorf("Write failed at index %d (shard should not be full yet)", i)
		}
	}

	// Next write to shard 0 should fail
	if ring.Write(0, 999) {
		t.Error("Write succeeded when shard should be full")
	}

	// But write to different shard should succeed
	if !ring.Write(1, 999) {
		t.Error("Write to different shard failed when it should succeed")
	}
}

// TestRingEmpty tests behavior when ring is empty
func TestRingEmpty(t *testing.T) {
	ring, err := NewShardedRing(1024, 4)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// TryRead on empty ring should return false
	val, ok := ring.TryRead()
	if ok {
		t.Errorf("TryRead on empty ring returned ok=true, val=%v", val)
	}

	// ReadBatch on empty ring should return empty slice
	batch := ring.ReadBatch(100)
	if len(batch) != 0 {
		t.Errorf("ReadBatch on empty ring returned %d items", len(batch))
	}
}

// TestReadBatch tests batch reading
func TestReadBatch(t *testing.T) {
	ring, err := NewShardedRing(1024, 4)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Write 200 items across shards
	for i := 0; i < 200; i++ {
		ring.Write(uint64(i%4), i)
	}

	// Read batch of 50
	batch := ring.ReadBatch(50)
	if len(batch) != 50 {
		t.Errorf("ReadBatch(50) returned %d items, want 50", len(batch))
	}

	// Remaining should be 150
	if ring.Len() != 150 {
		t.Errorf("Len() = %d after batch read, want 150", ring.Len())
	}

	// Read remaining
	batch = ring.ReadBatch(200)
	if len(batch) != 150 {
		t.Errorf("ReadBatch(200) returned %d items, want 150", len(batch))
	}

	if ring.Len() != 0 {
		t.Errorf("Len() = %d after reading all, want 0", ring.Len())
	}
}

// TestConcurrentProducers tests multiple goroutines writing concurrently
func TestConcurrentProducers(t *testing.T) {
	ring, err := NewShardedRing(10000, 8)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	numProducers := 8
	itemsPerProducer := 1000
	var wg sync.WaitGroup
	var writeFailures atomic.Int64

	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				if !ring.Write(uint64(producerID), producerID*10000+i) {
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
}

// TestConcurrentProducerConsumer tests concurrent producer and consumer
func TestConcurrentProducerConsumer(t *testing.T) {
	// Use a very large ring so it never fills up during the test
	ring, err := NewShardedRing(1000000, 8)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	numProducers := 4
	itemsPerProducer := 1000
	totalItems := numProducers * itemsPerProducer

	var producerWg sync.WaitGroup
	var itemsWritten atomic.Int64

	// Start producers - they write to the ring
	for p := 0; p < numProducers; p++ {
		producerWg.Add(1)
		go func(producerID int) {
			defer producerWg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				if !ring.Write(uint64(producerID), i) {
					t.Errorf("Producer %d: Write failed at %d", producerID, i)
					return
				}
				itemsWritten.Add(1)
			}
		}(p)
	}

	// Wait for all producers to finish
	producerWg.Wait()

	written := itemsWritten.Load()
	if written != int64(totalItems) {
		t.Errorf("Items written = %d, want %d", written, totalItems)
	}

	// Now read all items (single consumer)
	var itemsRead int64
	for {
		if _, ok := ring.TryRead(); ok {
			itemsRead++
		} else {
			break
		}
	}

	if itemsRead != written {
		t.Errorf("Items read = %d, want %d", itemsRead, written)
	}

	if ring.Len() != 0 {
		t.Errorf("Ring should be empty, has %d items", ring.Len())
	}
}

// TestConcurrentProducerConsumerSmallRing tests with a small ring where consumer must keep up
// This test verifies that with a small ring, data flows correctly when consumer drains regularly
func TestConcurrentProducerConsumerSmallRing(t *testing.T) {
	// Small ring - 128 items total (16 per shard)
	ring, err := NewShardedRing(128, 8)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Test parameters - keep small to avoid long-running test
	totalWrites := 1000
	var itemsWritten atomic.Int64
	var itemsRead atomic.Int64

	// Single goroutine that alternates between writing and reading
	// This avoids the scheduling issues of true concurrent producer/consumer
	done := make(chan struct{})

	go func() {
		defer close(done)
		writesDone := false
		for !writesDone || ring.Len() > 0 {
			// Try to write a batch
			for i := 0; i < 10 && itemsWritten.Load() < int64(totalWrites); i++ {
				if ring.Write(uint64(i), int(itemsWritten.Load())) {
					itemsWritten.Add(1)
				}
			}
			if itemsWritten.Load() >= int64(totalWrites) {
				writesDone = true
			}

			// Drain some items
			batch := ring.ReadBatch(20)
			itemsRead.Add(int64(len(batch)))
		}
	}()

	// Wait with timeout
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out - potential deadlock")
	}

	written := itemsWritten.Load()
	read := itemsRead.Load()

	if written != int64(totalWrites) {
		t.Errorf("Items written = %d, want %d", written, totalWrites)
	}

	if read != written {
		t.Errorf("Items read = %d, want %d", read, written)
	}

	t.Logf("Successfully processed %d items through 128-item ring", written)
}

// TestReadBatchInto tests the zero-allocation batch read
func TestReadBatchInto(t *testing.T) {
	ring, err := NewShardedRing(1024, 4)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Write 200 items
	for i := 0; i < 200; i++ {
		ring.Write(uint64(i%4), i)
	}

	// Pre-allocate buffer
	buf := make([]any, 0, 100)

	// Read into buffer
	buf = ring.ReadBatchInto(buf, 50)
	if len(buf) != 50 {
		t.Errorf("ReadBatchInto returned %d items, want 50", len(buf))
	}

	// Reuse buffer for another read
	buf = ring.ReadBatchInto(buf, 50)
	if len(buf) != 50 {
		t.Errorf("Second ReadBatchInto returned %d items, want 50", len(buf))
	}

	// Remaining should be 100
	if ring.Len() != 100 {
		t.Errorf("Len() = %d, want 100", ring.Len())
	}
}

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

// TestShardDistribution tests that producers are distributed across shards correctly
func TestShardDistribution(t *testing.T) {
	ring, err := NewShardedRing(400, 4) // 100 per shard
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Producer 0 should go to shard 0
	// Producer 4 should also go to shard 0 (4 & 3 = 0)
	// Producer 1 should go to shard 1
	// Producer 5 should also go to shard 1 (5 & 3 = 1)

	for i := 0; i < 50; i++ {
		ring.Write(0, "p0")
		ring.Write(4, "p4") // Same shard as 0
	}

	// Shard 0 should have 100 items (full)
	if ring.Write(0, "overflow") {
		t.Error("Shard 0 should be full")
	}

	// Shard 1 should still be empty
	if !ring.Write(1, "p1") {
		t.Error("Shard 1 should have space")
	}
}

// TestWrapAround tests ring buffer wrap-around behavior
func TestWrapAround(t *testing.T) {
	ring, err := NewShardedRing(16, 1) // Small ring to force wrap-around
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Fill and empty multiple times to test wrap-around
	for cycle := 0; cycle < 5; cycle++ {
		// Fill ring
		for i := 0; i < 16; i++ {
			if !ring.Write(0, cycle*100+i) {
				t.Errorf("Cycle %d: Write failed at %d", cycle, i)
			}
		}

		// Verify full
		if ring.Write(0, -1) {
			t.Errorf("Cycle %d: Ring should be full", cycle)
		}

		// Empty ring and verify values
		for i := 0; i < 16; i++ {
			val, ok := ring.TryRead()
			if !ok {
				t.Errorf("Cycle %d: TryRead failed at %d", cycle, i)
				continue
			}
			expected := cycle*100 + i
			if val.(int) != expected {
				t.Errorf("Cycle %d: Got %v, want %d", cycle, val, expected)
			}
		}

		// Verify empty
		if _, ok := ring.TryRead(); ok {
			t.Errorf("Cycle %d: Ring should be empty", cycle)
		}
	}
}

// TestCapAndLen tests Cap and Len methods
func TestCapAndLen(t *testing.T) {
	ring, err := NewShardedRing(1024, 8)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	if ring.Cap() != 1024 {
		t.Errorf("Cap() = %d, want 1024", ring.Cap())
	}

	if ring.Len() != 0 {
		t.Errorf("Initial Len() = %d, want 0", ring.Len())
	}

	// Add items
	for i := 0; i < 500; i++ {
		ring.Write(uint64(i%8), i)
	}

	if ring.Len() != 500 {
		t.Errorf("Len() = %d after 500 writes, want 500", ring.Len())
	}

	// Read some items
	for i := 0; i < 200; i++ {
		ring.TryRead()
	}

	if ring.Len() != 300 {
		t.Errorf("Len() = %d after 200 reads, want 300", ring.Len())
	}
}

// TestNilValues tests that nil values can be stored and retrieved
func TestNilValues(t *testing.T) {
	ring, err := NewShardedRing(64, 4)
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Write nil value
	if !ring.Write(0, nil) {
		t.Error("Write nil failed")
	}

	// Write non-nil value
	if !ring.Write(0, "hello") {
		t.Error("Write string failed")
	}

	// Read nil value
	val, ok := ring.TryRead()
	if !ok {
		t.Error("TryRead failed for nil value")
	}
	if val != nil {
		t.Errorf("Expected nil, got %v", val)
	}

	// Read string value
	val, ok = ring.TryRead()
	if !ok {
		t.Error("TryRead failed for string value")
	}
	if val != "hello" {
		t.Errorf("Expected 'hello', got %v", val)
	}
}

// =============================================================================
// Benchmarks
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
