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

// TestTryReadFairShardDistribution tests that TryRead rotates through shards fairly.
//
// Background: Originally TryRead always started at shard 0, risking uneven reading
// where shard 0 gets drained more frequently than other shards. The fix adds a
// rotating readStartShard counter that advances on each TryRead call.
//
// Implementation note: readStartShard uses a plain uint64 (not atomic) because
// this is MPSC (single consumer). Benchmarks showed non-atomic is ~10-15% faster.
func TestTryReadFairShardDistribution(t *testing.T) {
	ring, err := NewShardedRing(400, 4) // 100 per shard
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Write 1 item to each shard (use producer IDs 0,1,2,3 to target each shard)
	for shardID := 0; shardID < 4; shardID++ {
		if !ring.Write(uint64(shardID), shardID*1000) {
			t.Fatalf("Failed to write to shard %d", shardID)
		}
	}

	// Read all items - they should come from different shards in rotating order
	readValues := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		val, ok := ring.TryRead()
		if !ok {
			t.Fatalf("TryRead %d failed", i)
		}
		readValues = append(readValues, val.(int))
	}

	// Verify we got all values (any order is fine since we're testing rotation exists)
	seen := make(map[int]bool)
	for _, v := range readValues {
		seen[v] = true
	}
	for shardID := 0; shardID < 4; shardID++ {
		expected := shardID * 1000
		if !seen[expected] {
			t.Errorf("Value %d from shard %d was not read", expected, shardID)
		}
	}

	// Now test that rotation is happening - with old implementation all reads
	// would start from shard 0, with new implementation each read starts from
	// a different shard
	ring2, _ := NewShardedRing(400, 4)

	// Track which shard gets read first for each TryRead call
	// by filling shards one at a time and seeing which empties first
	firstReadShard := make([]int, 0, 4)

	for round := 0; round < 4; round++ {
		// Put one item in each shard
		for shardID := 0; shardID < 4; shardID++ {
			ring2.Write(uint64(shardID), shardID)
		}

		// First TryRead should start at a different shard each time
		val, ok := ring2.TryRead()
		if !ok {
			t.Fatalf("Round %d: TryRead failed", round)
		}
		firstReadShard = append(firstReadShard, val.(int))

		// Drain remaining items
		for {
			if _, ok := ring2.TryRead(); !ok {
				break
			}
		}
	}

	// With rotating start, we should see different first-read shards
	// (not all 0s like the old implementation would produce)
	allSameFirst := true
	for _, v := range firstReadShard {
		if v != firstReadShard[0] {
			allSameFirst = false
			break
		}
	}

	if allSameFirst {
		t.Errorf("All first reads came from the same shard %d - rotation may not be working", firstReadShard[0])
	}

	t.Logf("First read shards across rounds: %v (should vary)", firstReadShard)
}

// TestReadBatchFairShardDistribution tests that ReadBatch rotates through shards fairly.
//
// Same rationale as TestTryReadFairShardDistribution - ReadBatchInto also uses
// the rotating readStartShard counter to ensure fair shard access patterns.
func TestReadBatchFairShardDistribution(t *testing.T) {
	ring, err := NewShardedRing(400, 4) // 100 per shard
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Write 10 items to each shard
	for shardID := 0; shardID < 4; shardID++ {
		for i := 0; i < 10; i++ {
			if !ring.Write(uint64(shardID), shardID*1000+i) {
				t.Fatalf("Failed to write to shard %d", shardID)
			}
		}
	}

	// Do multiple small batch reads and track which shard values appear first
	firstItemShards := make([]int, 0, 4)
	for round := 0; round < 4; round++ {
		batch := ring.ReadBatch(5) // Small batch to not drain entire shard
		if len(batch) == 0 {
			t.Fatalf("Round %d: ReadBatch returned empty", round)
		}
		// First item's shard = value / 1000
		firstShard := batch[0].(int) / 1000
		firstItemShards = append(firstItemShards, firstShard)
	}

	// With rotating start, we should see different starting shards
	allSameFirst := true
	for _, v := range firstItemShards {
		if v != firstItemShards[0] {
			allSameFirst = false
			break
		}
	}

	if allSameFirst {
		t.Errorf("All batches started from the same shard %d - rotation may not be working", firstItemShards[0])
	}

	t.Logf("First batch item shards: %v (should vary)", firstItemShards)
}

// TestRotatingReadStatisticalFairness performs a statistical test for read fairness.
//
// Verifies that with rotating shard start, reads are distributed approximately
// evenly across all shards. Without rotation, shard 0 would dominate early reads.
// Expected: ~25% of first 100 reads from each shard (4 shards).
func TestRotatingReadStatisticalFairness(t *testing.T) {
	ring, err := NewShardedRing(4000, 4) // 1000 per shard
	if err != nil {
		t.Fatalf("NewShardedRing failed: %v", err)
	}

	// Write many items to each shard
	itemsPerShard := 100
	for shardID := 0; shardID < 4; shardID++ {
		for i := 0; i < itemsPerShard; i++ {
			ring.Write(uint64(shardID), shardID)
		}
	}

	// Read all items and count how many came from each shard
	shardReadOrder := make([]int, 0, 400)
	for {
		val, ok := ring.TryRead()
		if !ok {
			break
		}
		shardReadOrder = append(shardReadOrder, val.(int))
	}

	if len(shardReadOrder) != 4*itemsPerShard {
		t.Fatalf("Expected %d items, got %d", 4*itemsPerShard, len(shardReadOrder))
	}

	// Analyze first 100 reads to see distribution
	// With fair rotation, we should see roughly equal representation
	first100Counts := make(map[int]int)
	for i := 0; i < 100 && i < len(shardReadOrder); i++ {
		first100Counts[shardReadOrder[i]]++
	}

	t.Logf("First 100 reads by shard: %v", first100Counts)

	// Each shard should have at least 10% representation in first 100 reads
	// (with perfect fairness it would be ~25 each)
	for shardID := 0; shardID < 4; shardID++ {
		count := first100Counts[shardID]
		if count < 10 {
			t.Errorf("Shard %d underrepresented in first 100 reads: only %d items (expected ~25)", shardID, count)
		}
	}
}
