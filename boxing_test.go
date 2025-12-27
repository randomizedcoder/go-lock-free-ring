package ring

import (
	"testing"
)

// Pointer type to avoid int->any boxing
type testItem struct{ val int }

func BenchmarkWriterNoBoxing(b *testing.B) {
	r, _ := NewShardedRing(1000000, 8)
	config := WriteConfig{
		Strategy:   NextShard,
		MaxRetries: 10,
	}
	writer := NewWriter(r, 0, config)

	// Pre-allocate items to reuse
	items := make([]*testItem, 1000)
	for i := range items {
		items[i] = &testItem{val: i}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		writer.Write(items[i%1000]) // Pointer - no boxing
		if i%100 == 99 {
			for j := 0; j < 100; j++ {
				r.TryRead()
			}
		}
	}
}

func BenchmarkWriterWithBoxing(b *testing.B) {
	r, _ := NewShardedRing(1000000, 8)
	config := WriteConfig{
		Strategy:   NextShard,
		MaxRetries: 10,
	}
	writer := NewWriter(r, 0, config)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		writer.Write(i) // int -> any boxing
		if i%100 == 99 {
			for j := 0; j < 100; j++ {
				r.TryRead()
			}
		}
	}
}

