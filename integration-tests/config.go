package integration_tests

import (
	"fmt"
	"time"
)

// TestCase represents a single integration test configuration
type TestCase struct {
	ID          string        // Test identifier (e.g., "T001")
	Name        string        // Human-readable name
	Producers   int           // Number of producers
	Rate        float64       // Per-producer rate in Mb/s
	PacketSize  int           // Packet size in bytes
	Frequency   int           // Consumer wake interval in ms
	Duration    time.Duration // Test duration
	RingSize    int           // 0 = auto-calculate
	RingShards  int           // Number of shards (0 = match producers)
	BTreeSize   int           // B-tree capacity
}

// ExpectedTotalRate returns the expected total throughput in Mb/s
func (tc TestCase) ExpectedTotalRate() float64 {
	return float64(tc.Producers) * tc.Rate
}

// ConfigString returns a short configuration description
func (tc TestCase) ConfigString() string {
	return fmt.Sprintf("%dp×%.0fMb/%db/%dms",
		tc.Producers, tc.Rate, tc.PacketSize, tc.Frequency)
}

// TestMatrixConfig defines the parameter ranges for test generation
type TestMatrixConfig struct {
	Producers   []int           // Producer counts to test
	Rates       []float64       // Per-producer rates (Mb/s)
	PacketSizes []int           // Packet sizes (bytes)
	Frequencies []int           // Consumer wake intervals (ms)
	Duration    time.Duration   // Test duration for each test
	BTreeSize   int             // B-tree capacity (constant)
}

// DefaultTestMatrixConfig returns the default test matrix configuration
func DefaultTestMatrixConfig() TestMatrixConfig {
	return TestMatrixConfig{
		Producers:   []int{1, 2, 4, 8, 16},
		Rates:       []float64{1, 10, 50, 100},
		PacketSizes: []int{500, 1450, 9000},
		Frequencies: []int{5, 10, 50, 100},
		Duration:    10 * time.Second,
		BTreeSize:   2000,
	}
}

// SmallTestMatrixConfig returns a smaller matrix for quick testing
func SmallTestMatrixConfig() TestMatrixConfig {
	return TestMatrixConfig{
		Producers:   []int{1, 4},
		Rates:       []float64{10, 50},
		PacketSizes: []int{1450},
		Frequencies: []int{10},
		Duration:    5 * time.Second,
		BTreeSize:   2000,
	}
}

// SmokeTestMatrixConfig returns minimal tests for CI/smoke testing
func SmokeTestMatrixConfig() TestMatrixConfig {
	return TestMatrixConfig{
		Producers:   []int{4},
		Rates:       []float64{10},
		PacketSizes: []int{1450},
		Frequencies: []int{10},
		Duration:    3 * time.Second,
		BTreeSize:   2000,
	}
}

// GenerateTestCases generates all test case combinations from the matrix config
func GenerateTestCases(cfg TestMatrixConfig) []TestCase {
	var tests []TestCase
	id := 1

	for _, producers := range cfg.Producers {
		for _, rate := range cfg.Rates {
			for _, packetSize := range cfg.PacketSizes {
				for _, frequency := range cfg.Frequencies {
					tc := TestCase{
						ID:         fmt.Sprintf("T%03d", id),
						Name:       generateTestName(producers, rate, packetSize, frequency),
						Producers:  producers,
						Rate:       rate,
						PacketSize: packetSize,
						Frequency:  frequency,
						Duration:   cfg.Duration,
						RingSize:   0, // auto-calculate
						RingShards: nextPowerOf2(producers),
						BTreeSize:  cfg.BTreeSize,
					}
					tests = append(tests, tc)
					id++
				}
			}
		}
	}

	return tests
}

// GenerateTestCasesFiltered generates test cases with optional filtering
func GenerateTestCasesFiltered(cfg TestMatrixConfig, filter TestFilter) []TestCase {
	all := GenerateTestCases(cfg)
	if filter == nil {
		return all
	}

	var filtered []TestCase
	for _, tc := range all {
		if filter(tc) {
			filtered = append(filtered, tc)
		}
	}
	return filtered
}

// TestFilter is a function that returns true if a test case should be included
type TestFilter func(TestCase) bool

// FilterByMaxRate returns a filter that excludes tests above a rate threshold
func FilterByMaxRate(maxTotalRate float64) TestFilter {
	return func(tc TestCase) bool {
		return tc.ExpectedTotalRate() <= maxTotalRate
	}
}

// FilterByProducers returns a filter for specific producer counts
func FilterByProducers(producers ...int) TestFilter {
	set := make(map[int]bool)
	for _, p := range producers {
		set[p] = true
	}
	return func(tc TestCase) bool {
		return set[tc.Producers]
	}
}

// FilterByPacketSize returns a filter for specific packet sizes
func FilterByPacketSize(sizes ...int) TestFilter {
	set := make(map[int]bool)
	for _, s := range sizes {
		set[s] = true
	}
	return func(tc TestCase) bool {
		return set[tc.PacketSize]
	}
}

// CombineFilters combines multiple filters with AND logic
func CombineFilters(filters ...TestFilter) TestFilter {
	return func(tc TestCase) bool {
		for _, f := range filters {
			if !f(tc) {
				return false
			}
		}
		return true
	}
}

// PredefinedTestSets contains curated sets of test configurations
var PredefinedTestSets = map[string][]TestCase{
	"smoke": {
		{ID: "T001", Name: "smoke_basic", Producers: 4, Rate: 10, PacketSize: 1450, Frequency: 10, Duration: 3 * time.Second, BTreeSize: 2000},
	},
	"quick": {
		{ID: "T001", Name: "single_producer_10Mbps", Producers: 1, Rate: 10, PacketSize: 1450, Frequency: 10, Duration: 5 * time.Second, BTreeSize: 2000},
		{ID: "T002", Name: "four_producers_10Mbps", Producers: 4, Rate: 10, PacketSize: 1450, Frequency: 10, Duration: 5 * time.Second, BTreeSize: 2000},
		{ID: "T003", Name: "four_producers_50Mbps", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 5 * time.Second, BTreeSize: 2000},
		{ID: "T004", Name: "eight_producers_50Mbps", Producers: 8, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 5 * time.Second, BTreeSize: 2000},
	},
	"standard": {
		{ID: "T001", Name: "1p_10Mb_standard", Producers: 1, Rate: 10, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000},
		{ID: "T002", Name: "4p_10Mb_standard", Producers: 4, Rate: 10, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000},
		{ID: "T003", Name: "4p_50Mb_standard", Producers: 4, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000},
		{ID: "T004", Name: "8p_50Mb_standard", Producers: 8, Rate: 50, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000},
		{ID: "T005", Name: "8p_100Mb_standard", Producers: 8, Rate: 100, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000},
		{ID: "T006", Name: "16p_100Mb_standard", Producers: 16, Rate: 100, PacketSize: 1450, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000},
		{ID: "T007", Name: "4p_50Mb_small_pkt", Producers: 4, Rate: 50, PacketSize: 500, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000},
		{ID: "T008", Name: "4p_50Mb_jumbo", Producers: 4, Rate: 50, PacketSize: 9000, Frequency: 10, Duration: 10 * time.Second, BTreeSize: 2000},
		{ID: "T009", Name: "4p_10Mb_slow_consumer", Producers: 4, Rate: 10, PacketSize: 1450, Frequency: 50, Duration: 10 * time.Second, BTreeSize: 2000},
		{ID: "T010", Name: "4p_10Mb_fast_consumer", Producers: 4, Rate: 10, PacketSize: 1450, Frequency: 5, Duration: 10 * time.Second, BTreeSize: 2000},
	},
}

// GetPredefinedTestSet returns a predefined test set by name
func GetPredefinedTestSet(name string) ([]TestCase, bool) {
	tests, ok := PredefinedTestSets[name]
	return tests, ok
}

// generateTestName creates a human-readable test name
func generateTestName(producers int, rate float64, packetSize int, frequency int) string {
	var pktDesc string
	switch packetSize {
	case 500:
		pktDesc = "small"
	case 1450:
		pktDesc = "std"
	case 9000:
		pktDesc = "jumbo"
	default:
		pktDesc = fmt.Sprintf("%db", packetSize)
	}

	var freqDesc string
	switch frequency {
	case 5:
		freqDesc = "fast"
	case 10:
		freqDesc = "normal"
	case 50:
		freqDesc = "slow"
	case 100:
		freqDesc = "veryslow"
	default:
		freqDesc = fmt.Sprintf("%dms", frequency)
	}

	return fmt.Sprintf("%dp_%.0fMb_%s_%s", producers, rate, pktDesc, freqDesc)
}

// nextPowerOf2 returns the smallest power of 2 >= n
func nextPowerOf2(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	return n + 1
}

// PrintTestMatrix prints a summary of the test matrix
func PrintTestMatrix(tests []TestCase) {
	fmt.Printf("Test Matrix: %d test cases\n", len(tests))
	fmt.Println("=" + string(make([]byte, 79)))
	fmt.Printf("%-6s %-30s %8s %8s %8s %8s %10s\n",
		"ID", "Name", "Prod", "Rate", "PktSize", "Freq", "TotalRate")
	fmt.Println("-" + string(make([]byte, 79)))

	for _, tc := range tests {
		fmt.Printf("%-6s %-30s %8d %7.0f %8d %7dms %9.0f Mb/s\n",
			tc.ID, tc.Name, tc.Producers, tc.Rate, tc.PacketSize, tc.Frequency, tc.ExpectedTotalRate())
	}
	fmt.Println("=" + string(make([]byte, 79)))
}

// MatrixStats returns statistics about a test matrix
type MatrixStats struct {
	TotalTests      int
	UniqueProducers []int
	UniqueRates     []float64
	UniquePackets   []int
	UniqueFreqs     []int
	MinTotalRate    float64
	MaxTotalRate    float64
	EstimatedTime   time.Duration
}

// GetMatrixStats calculates statistics for a test matrix
func GetMatrixStats(tests []TestCase) MatrixStats {
	if len(tests) == 0 {
		return MatrixStats{}
	}

	producerSet := make(map[int]bool)
	rateSet := make(map[float64]bool)
	packetSet := make(map[int]bool)
	freqSet := make(map[int]bool)

	minRate := tests[0].ExpectedTotalRate()
	maxRate := tests[0].ExpectedTotalRate()
	var totalDuration time.Duration

	for _, tc := range tests {
		producerSet[tc.Producers] = true
		rateSet[tc.Rate] = true
		packetSet[tc.PacketSize] = true
		freqSet[tc.Frequency] = true

		rate := tc.ExpectedTotalRate()
		if rate < minRate {
			minRate = rate
		}
		if rate > maxRate {
			maxRate = rate
		}
		totalDuration += tc.Duration
	}

	stats := MatrixStats{
		TotalTests:    len(tests),
		MinTotalRate:  minRate,
		MaxTotalRate:  maxRate,
		EstimatedTime: totalDuration,
	}

	for p := range producerSet {
		stats.UniqueProducers = append(stats.UniqueProducers, p)
	}
	for r := range rateSet {
		stats.UniqueRates = append(stats.UniqueRates, r)
	}
	for p := range packetSet {
		stats.UniquePackets = append(stats.UniquePackets, p)
	}
	for f := range freqSet {
		stats.UniqueFreqs = append(stats.UniqueFreqs, f)
	}

	return stats
}

