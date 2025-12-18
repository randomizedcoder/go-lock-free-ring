package integration_tests

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParsedMetrics contains metrics extracted from cmd/ring output
type ParsedMetrics struct {
	// Ring size (auto-calculated or specified)
	RingSize     int
	RingShards   int
	AutoCalculated bool

	// Per-interval stats (from [stats] lines)
	IntervalStats []IntervalStat

	// Final statistics
	Produced  uint64
	Dropped   uint64
	Consumed  uint64
	Trimmed   uint64
	DropRate  float64
	BTreeSize int

	// Shutdown timing
	ShutdownDuration time.Duration

	// Calculated rates (from interval stats)
	AverageRate float64
	MinRate     float64
	MaxRate     float64
}

// IntervalStat represents one [stats] log line
type IntervalStat struct {
	Inserted  uint64
	Trimmed   uint64
	Rate      float64 // Mb/s
	BTreeLen  int
	RingLen   int
	RingCap   int
	Produced  uint64
	Dropped   uint64
}

// Regular expressions for parsing
var (
	// Ring size auto-calculated: 128 (4 producers × 10.0 Mb/s, 10ms interval, 2.0x multiplier)
	ringSizeAutoRe = regexp.MustCompile(`Ring size auto-calculated: (\d+)`)
	// Ring size: 2048 (user-specified)
	ringSizeUserRe = regexp.MustCompile(`Ring size: (\d+) \(user-specified\)`)

	// [stats] inserted=3452 trimmed=1452 rate=40.02 Mb/s btree=2000 ring=0/128 produced=3452 dropped=0
	statsRe = regexp.MustCompile(`\[stats\] inserted=(\d+) trimmed=(\d+) rate=([\d.]+) Mb/s btree=(\d+) ring=(\d+)/(\d+) produced=(\d+) dropped=(\d+)`)

	// Final stats
	producedRe  = regexp.MustCompile(`Produced: (\d+)`)
	droppedRe   = regexp.MustCompile(`Dropped:\s+(\d+) \(([\d.]+)%\)`)
	consumedRe  = regexp.MustCompile(`Consumed: (\d+)`)
	trimmedRe   = regexp.MustCompile(`Trimmed:\s+(\d+)`)
	btreeSizeRe = regexp.MustCompile(`BTree final size: (\d+)`)

	// Clean shutdown completed in 10.701µs
	shutdownRe = regexp.MustCompile(`Clean shutdown completed in ([\d.]+)(µs|ms|s)`)
)

// ParseOutput parses the stdout from cmd/ring and extracts metrics
func ParseOutput(stdout string) (*ParsedMetrics, error) {
	metrics := &ParsedMetrics{
		MinRate: -1, // sentinel for "not set"
	}

	lines := strings.Split(stdout, "\n")

	for _, line := range lines {
		// Parse ring size
		if matches := ringSizeAutoRe.FindStringSubmatch(line); matches != nil {
			metrics.RingSize, _ = strconv.Atoi(matches[1])
			metrics.AutoCalculated = true
		} else if matches := ringSizeUserRe.FindStringSubmatch(line); matches != nil {
			metrics.RingSize, _ = strconv.Atoi(matches[1])
			metrics.AutoCalculated = false
		}

		// Parse interval stats
		if matches := statsRe.FindStringSubmatch(line); matches != nil {
			stat := IntervalStat{}
			stat.Inserted, _ = strconv.ParseUint(matches[1], 10, 64)
			stat.Trimmed, _ = strconv.ParseUint(matches[2], 10, 64)
			stat.Rate, _ = strconv.ParseFloat(matches[3], 64)
			stat.BTreeLen, _ = strconv.Atoi(matches[4])
			stat.RingLen, _ = strconv.Atoi(matches[5])
			stat.RingCap, _ = strconv.Atoi(matches[6])
			stat.Produced, _ = strconv.ParseUint(matches[7], 10, 64)
			stat.Dropped, _ = strconv.ParseUint(matches[8], 10, 64)
			metrics.IntervalStats = append(metrics.IntervalStats, stat)
		}

		// Parse final stats
		if matches := producedRe.FindStringSubmatch(line); matches != nil {
			metrics.Produced, _ = strconv.ParseUint(matches[1], 10, 64)
		}
		if matches := droppedRe.FindStringSubmatch(line); matches != nil {
			metrics.Dropped, _ = strconv.ParseUint(matches[1], 10, 64)
			metrics.DropRate, _ = strconv.ParseFloat(matches[2], 64)
		}
		if matches := consumedRe.FindStringSubmatch(line); matches != nil {
			metrics.Consumed, _ = strconv.ParseUint(matches[1], 10, 64)
		}
		if matches := trimmedRe.FindStringSubmatch(line); matches != nil {
			metrics.Trimmed, _ = strconv.ParseUint(matches[1], 10, 64)
		}
		if matches := btreeSizeRe.FindStringSubmatch(line); matches != nil {
			metrics.BTreeSize, _ = strconv.Atoi(matches[1])
		}

		// Parse shutdown duration
		if matches := shutdownRe.FindStringSubmatch(line); matches != nil {
			value, _ := strconv.ParseFloat(matches[1], 64)
			unit := matches[2]
			switch unit {
			case "µs":
				metrics.ShutdownDuration = time.Duration(value * float64(time.Microsecond))
			case "ms":
				metrics.ShutdownDuration = time.Duration(value * float64(time.Millisecond))
			case "s":
				metrics.ShutdownDuration = time.Duration(value * float64(time.Second))
			}
		}
	}

	// Calculate rate statistics from interval stats
	if len(metrics.IntervalStats) > 0 {
		var sum float64
		for _, stat := range metrics.IntervalStats {
			sum += stat.Rate
			if metrics.MinRate < 0 || stat.Rate < metrics.MinRate {
				metrics.MinRate = stat.Rate
			}
			if stat.Rate > metrics.MaxRate {
				metrics.MaxRate = stat.Rate
			}
		}
		metrics.AverageRate = sum / float64(len(metrics.IntervalStats))
	}

	if metrics.MinRate < 0 {
		metrics.MinRate = 0
	}

	return metrics, nil
}

// HasValidStats returns true if the metrics contain valid statistics
func (m *ParsedMetrics) HasValidStats() bool {
	return m.Produced > 0 || len(m.IntervalStats) > 0
}

