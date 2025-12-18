package integration_tests

import (
	"testing"
	"time"
)

func TestGenerateTestCases(t *testing.T) {
	cfg := DefaultTestMatrixConfig()
	tests := GenerateTestCases(cfg)

	// Calculate expected number of combinations
	expected := len(cfg.Producers) * len(cfg.Rates) * len(cfg.PacketSizes) * len(cfg.Frequencies)

	if len(tests) != expected {
		t.Errorf("GenerateTestCases() generated %d tests, want %d", len(tests), expected)
	}

	// Verify all tests have unique IDs
	ids := make(map[string]bool)
	for _, tc := range tests {
		if ids[tc.ID] {
			t.Errorf("Duplicate test ID: %s", tc.ID)
		}
		ids[tc.ID] = true
	}

	// Verify all tests have non-empty names
	for _, tc := range tests {
		if tc.Name == "" {
			t.Errorf("Test %s has empty name", tc.ID)
		}
	}
}

func TestGenerateTestCasesSmall(t *testing.T) {
	cfg := SmallTestMatrixConfig()
	tests := GenerateTestCases(cfg)

	// 2 producers * 2 rates * 1 packet size * 1 frequency = 4 tests
	expected := 2 * 2 * 1 * 1
	if len(tests) != expected {
		t.Errorf("SmallTestMatrixConfig generated %d tests, want %d", len(tests), expected)
	}
}

func TestGenerateTestCasesSmoke(t *testing.T) {
	cfg := SmokeTestMatrixConfig()
	tests := GenerateTestCases(cfg)

	// 1 producer * 1 rate * 1 packet size * 1 frequency = 1 test
	expected := 1
	if len(tests) != expected {
		t.Errorf("SmokeTestMatrixConfig generated %d tests, want %d", len(tests), expected)
	}
}

func TestExpectedTotalRate(t *testing.T) {
	tests := []struct {
		producers int
		rate      float64
		want      float64
	}{
		{1, 10, 10},
		{4, 10, 40},
		{4, 50, 200},
		{8, 100, 800},
		{16, 100, 1600},
	}

	for _, tt := range tests {
		tc := TestCase{Producers: tt.producers, Rate: tt.rate}
		got := tc.ExpectedTotalRate()
		if got != tt.want {
			t.Errorf("ExpectedTotalRate() for %d×%.0f = %.0f, want %.0f",
				tt.producers, tt.rate, got, tt.want)
		}
	}
}

func TestFilterByMaxRate(t *testing.T) {
	cfg := TestMatrixConfig{
		Producers:   []int{1, 4, 8},
		Rates:       []float64{10, 100},
		PacketSizes: []int{1450},
		Frequencies: []int{10},
		Duration:    5 * time.Second,
		BTreeSize:   2000,
	}

	_ = GenerateTestCases(cfg) // verify it generates without error
	filtered := GenerateTestCasesFiltered(cfg, FilterByMaxRate(100))

	// Only tests with total rate <= 100 Mb/s
	// 1×10=10, 1×100=100, 4×10=40, 4×100=400 (excluded), 8×10=80, 8×100=800 (excluded)
	expectedCount := 4
	if len(filtered) != expectedCount {
		t.Errorf("FilterByMaxRate(100) returned %d tests, want %d", len(filtered), expectedCount)
	}

	for _, tc := range filtered {
		if tc.ExpectedTotalRate() > 100 {
			t.Errorf("Filtered test %s has rate %.0f > 100", tc.ID, tc.ExpectedTotalRate())
		}
	}
}

func TestFilterByProducers(t *testing.T) {
	cfg := DefaultTestMatrixConfig()

	filtered := GenerateTestCasesFiltered(cfg, FilterByProducers(1, 4))

	for _, tc := range filtered {
		if tc.Producers != 1 && tc.Producers != 4 {
			t.Errorf("Filtered test %s has %d producers, want 1 or 4", tc.ID, tc.Producers)
		}
	}
}

func TestFilterByPacketSize(t *testing.T) {
	cfg := DefaultTestMatrixConfig()

	filtered := GenerateTestCasesFiltered(cfg, FilterByPacketSize(1450))

	for _, tc := range filtered {
		if tc.PacketSize != 1450 {
			t.Errorf("Filtered test %s has packet size %d, want 1450", tc.ID, tc.PacketSize)
		}
	}
}

func TestCombineFilters(t *testing.T) {
	cfg := TestMatrixConfig{
		Producers:   []int{1, 4, 8, 16},
		Rates:       []float64{10, 50, 100},
		PacketSizes: []int{500, 1450, 9000},
		Frequencies: []int{10},
		Duration:    5 * time.Second,
		BTreeSize:   2000,
	}

	combined := CombineFilters(
		FilterByProducers(4, 8),
		FilterByPacketSize(1450),
		FilterByMaxRate(500),
	)

	filtered := GenerateTestCasesFiltered(cfg, combined)

	for _, tc := range filtered {
		if tc.Producers != 4 && tc.Producers != 8 {
			t.Errorf("Test %s: producers=%d, want 4 or 8", tc.ID, tc.Producers)
		}
		if tc.PacketSize != 1450 {
			t.Errorf("Test %s: packetSize=%d, want 1450", tc.ID, tc.PacketSize)
		}
		if tc.ExpectedTotalRate() > 500 {
			t.Errorf("Test %s: rate=%.0f > 500", tc.ID, tc.ExpectedTotalRate())
		}
	}
}

func TestPredefinedTestSets(t *testing.T) {
	// Verify all predefined sets exist and are non-empty
	for name, tests := range PredefinedTestSets {
		if len(tests) == 0 {
			t.Errorf("Predefined set %q is empty", name)
		}

		// Verify all tests have valid fields
		for _, tc := range tests {
			if tc.ID == "" {
				t.Errorf("Test in set %q has empty ID", name)
			}
			if tc.Producers <= 0 {
				t.Errorf("Test %s in set %q has invalid producers: %d", tc.ID, name, tc.Producers)
			}
			if tc.Rate <= 0 {
				t.Errorf("Test %s in set %q has invalid rate: %.0f", tc.ID, name, tc.Rate)
			}
		}
	}
}

func TestGetPredefinedTestSet(t *testing.T) {
	tests, ok := GetPredefinedTestSet("smoke")
	if !ok {
		t.Error("GetPredefinedTestSet(smoke) returned false")
	}
	if len(tests) == 0 {
		t.Error("smoke test set is empty")
	}

	_, ok = GetPredefinedTestSet("nonexistent")
	if ok {
		t.Error("GetPredefinedTestSet(nonexistent) should return false")
	}
}

func TestGetMatrixStats(t *testing.T) {
	cfg := SmallTestMatrixConfig()
	tests := GenerateTestCases(cfg)
	stats := GetMatrixStats(tests)

	if stats.TotalTests != len(tests) {
		t.Errorf("TotalTests=%d, want %d", stats.TotalTests, len(tests))
	}

	if stats.MinTotalRate <= 0 {
		t.Errorf("MinTotalRate=%.0f, want > 0", stats.MinTotalRate)
	}

	if stats.MaxTotalRate < stats.MinTotalRate {
		t.Errorf("MaxTotalRate=%.0f < MinTotalRate=%.0f", stats.MaxTotalRate, stats.MinTotalRate)
	}

	if stats.EstimatedTime <= 0 {
		t.Errorf("EstimatedTime=%v, want > 0", stats.EstimatedTime)
	}
}

func TestConfigString(t *testing.T) {
	tc := TestCase{
		Producers:  4,
		Rate:       50,
		PacketSize: 1450,
		Frequency:  10,
	}

	got := tc.ConfigString()
	want := "4p×50Mb/1450b/10ms"
	if got != want {
		t.Errorf("ConfigString() = %q, want %q", got, want)
	}
}

func TestNextPowerOf2(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{3, 4},
		{4, 4},
		{5, 8},
		{7, 8},
		{8, 8},
		{9, 16},
		{15, 16},
		{16, 16},
		{17, 32},
	}

	for _, tt := range tests {
		got := nextPowerOf2(tt.input)
		if got != tt.want {
			t.Errorf("nextPowerOf2(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// Example of printing the full matrix (can be run with -v flag)
func TestPrintFullMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping matrix print in short mode")
	}

	t.Log("=== Default Matrix ===")
	cfg := DefaultTestMatrixConfig()
	tests := GenerateTestCases(cfg)
	stats := GetMatrixStats(tests)

	t.Logf("Total tests: %d", stats.TotalTests)
	t.Logf("Producers: %v", stats.UniqueProducers)
	t.Logf("Rates: %v", stats.UniqueRates)
	t.Logf("Packet sizes: %v", stats.UniquePackets)
	t.Logf("Frequencies: %v", stats.UniqueFreqs)
	t.Logf("Rate range: %.0f - %.0f Mb/s", stats.MinTotalRate, stats.MaxTotalRate)
	t.Logf("Estimated time: %v", stats.EstimatedTime)
}

// Benchmark test case generation
func BenchmarkGenerateTestCases(b *testing.B) {
	cfg := DefaultTestMatrixConfig()
	for i := 0; i < b.N; i++ {
		GenerateTestCases(cfg)
	}
}

