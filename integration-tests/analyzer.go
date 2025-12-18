package integration_tests

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ProfileAnalyzer analyzes profile files using go tool pprof
type ProfileAnalyzer struct {
	TopCount int // Number of top entries to extract (default: 5)
}

// NewProfileAnalyzer creates a new profile analyzer
func NewProfileAnalyzer() *ProfileAnalyzer {
	return &ProfileAnalyzer{
		TopCount: 5,
	}
}

// ProfileEntry represents a single entry in the top N output
type ProfileEntry struct {
	Rank     int     // 1-based rank
	Flat     string  // Flat value (e.g., "2.5s", "10MB")
	FlatPct  float64 // Flat percentage
	Sum      float64 // Cumulative sum percentage
	Cum      string  // Cumulative value
	CumPct   float64 // Cumulative percentage
	Function string  // Function name
}

// ProfileAnalysisResult contains the analyzed profile data
type ProfileAnalysisResult struct {
	TestID       string
	ProfileType  ProfileType
	FilePath     string
	TopEntries   []ProfileEntry
	TotalSamples string // e.g., "10.5s" or "50MB"
	Error        error
}

// Analyze runs go tool pprof on a profile file and extracts the top entries
func (pa *ProfileAnalyzer) Analyze(profPath string, profType ProfileType, testID string) (*ProfileAnalysisResult, error) {
	result := &ProfileAnalysisResult{
		TestID:      testID,
		ProfileType: profType,
		FilePath:    profPath,
	}

	// Build pprof command
	args := []string{"tool", "pprof", "-top", fmt.Sprintf("-nodecount=%d", pa.TopCount)}

	// Add sample_index for memory profiles
	switch profType {
	case ProfileMem, ProfileHeap:
		args = append(args, "-sample_index=inuse_space")
	case ProfileAllocs:
		args = append(args, "-sample_index=alloc_objects")
	}

	args = append(args, profPath)

	cmd := exec.Command("go", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		result.Error = fmt.Errorf("pprof failed: %w\nstderr: %s", err, stderr.String())
		return result, result.Error
	}

	// Parse the output
	entries, total, err := parseTopOutput(stdout.String())
	if err != nil {
		result.Error = fmt.Errorf("failed to parse pprof output: %w", err)
		return result, result.Error
	}

	result.TopEntries = entries
	result.TotalSamples = total

	return result, nil
}

// parseTopOutput parses the output of `go tool pprof -top`
//
// Example output format:
// Showing nodes accounting for 4.52s, 90.04% of 5.02s total
// Showing top 5 nodes out of 45
//       flat  flat%   sum%        cum   cum%
//      2.50s 49.80% 49.80%      2.50s 49.80%  runtime.usleep
//      1.20s 23.90% 73.71%      1.20s 23.90%  sync/atomic.AddUint64
//      0.50s  9.96% 83.67%      0.60s 11.95%  main.producer
//      0.20s  3.98% 87.65%      0.20s  3.98%  runtime.madvise
//      0.12s  2.39% 90.04%      4.90s 97.61%  main.consumer
func parseTopOutput(output string) ([]ProfileEntry, string, error) {
	var entries []ProfileEntry
	var totalSamples string

	// Regex for total line
	totalRe := regexp.MustCompile(`of ([\d.]+\w+) total`)
	// Regex for data lines
	// Match: flat flat% sum% cum cum% function
	// Example: "2.50s 49.80% 49.80%      2.50s 49.80%  runtime.usleep"
	dataRe := regexp.MustCompile(`^\s*([\d.]+\w*)\s+([\d.]+)%\s+([\d.]+)%\s+([\d.]+\w*)\s+([\d.]+)%\s+(.+)$`)

	scanner := bufio.NewScanner(strings.NewReader(output))
	rank := 0

	for scanner.Scan() {
		line := scanner.Text()

		// Extract total
		if matches := totalRe.FindStringSubmatch(line); matches != nil {
			totalSamples = matches[1]
		}

		// Skip header lines
		if strings.Contains(line, "flat%") || strings.Contains(line, "Showing") {
			continue
		}

		// Parse data lines
		if matches := dataRe.FindStringSubmatch(line); matches != nil {
			rank++
			flatPct, _ := strconv.ParseFloat(matches[2], 64)
			sumPct, _ := strconv.ParseFloat(matches[3], 64)
			cumPct, _ := strconv.ParseFloat(matches[5], 64)

			entry := ProfileEntry{
				Rank:     rank,
				Flat:     matches[1],
				FlatPct:  flatPct,
				Sum:      sumPct,
				Cum:      matches[4],
				CumPct:   cumPct,
				Function: strings.TrimSpace(matches[6]),
			}
			entries = append(entries, entry)
		}
	}

	return entries, totalSamples, nil
}

// AnalyzeMultiple analyzes multiple profile files
func (pa *ProfileAnalyzer) AnalyzeMultiple(profiles []ProfileRunResult) []ProfileAnalysisResult {
	var results []ProfileAnalysisResult

	for _, pr := range profiles {
		if pr.ProfilePath == "" || pr.Error != nil {
			continue
		}

		result, err := pa.Analyze(pr.ProfilePath, pr.ProfileType, pr.TestCase.ID)
		if err != nil {
			// Store error in result but continue
			result = &ProfileAnalysisResult{
				TestID:      pr.TestCase.ID,
				ProfileType: pr.ProfileType,
				FilePath:    pr.ProfilePath,
				Error:       err,
			}
		}
		results = append(results, *result)
	}

	return results
}

// GetProfileSummary returns a human-readable summary of a profile analysis
func (par *ProfileAnalysisResult) GetProfileSummary() string {
	if par.Error != nil {
		return fmt.Sprintf("Error: %v", par.Error)
	}

	if len(par.TopEntries) == 0 {
		return "No entries found"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Top %d functions (total: %s):\n", len(par.TopEntries), par.TotalSamples))

	for _, e := range par.TopEntries {
		sb.WriteString(fmt.Sprintf("  %d. %s: %s (%.1f%%) cum: %s (%.1f%%)\n",
			e.Rank, e.Function, e.Flat, e.FlatPct, e.Cum, e.CumPct))
	}

	return sb.String()
}

// GenerateRecommendations generates recommendations based on profile analysis
func GenerateRecommendations(analyses []ProfileAnalysisResult) []string {
	var recommendations []string
	seenRecs := make(map[string]bool)

	for _, analysis := range analyses {
		if analysis.Error != nil {
			continue
		}

		for _, entry := range analysis.TopEntries {
			rec := generateRecommendation(entry, analysis.ProfileType)
			if rec != "" && !seenRecs[rec] {
				recommendations = append(recommendations, rec)
				seenRecs[rec] = true
			}
		}
	}

	return recommendations
}

// generateRecommendation generates a recommendation for a single profile entry
func generateRecommendation(entry ProfileEntry, profType ProfileType) string {
	fn := entry.Function

	switch profType {
	case ProfileCPU:
		if strings.Contains(fn, "runtime.usleep") && entry.FlatPct > 30 {
			return "High CPU in runtime.usleep indicates effective rate limiting is working correctly"
		}
		if strings.Contains(fn, "sync/atomic") && entry.FlatPct > 20 {
			return "Significant atomic operations usage - expected for lock-free data structure"
		}
		if strings.Contains(fn, "runtime.mcall") && entry.FlatPct > 15 {
			return "Scheduler overhead detected - consider reducing goroutine count or adding runtime.Gosched()"
		}
		if strings.Contains(fn, "runtime.lock") && entry.FlatPct > 10 {
			return "Lock contention detected - review mutex usage in hot paths"
		}

	case ProfileMem, ProfileHeap:
		if strings.Contains(fn, "sync.(*Pool).Get") && entry.FlatPct > 20 {
			return "Pool allocation overhead detected - pool may be undersized or items recycled too slowly"
		}
		if strings.Contains(fn, "make") && entry.FlatPct > 30 {
			return "Excessive slice/map allocations - consider pre-allocation or pooling"
		}

	case ProfileAllocs:
		if strings.Contains(fn, "newobject") && entry.FlatPct > 50 {
			return "High allocation rate - review hot paths for allocation reduction opportunities"
		}

	case ProfileMutex:
		if entry.FlatPct > 20 {
			return fmt.Sprintf("Mutex contention in %s (%.1f%%) - consider lock-free alternatives or finer-grained locking", fn, entry.FlatPct)
		}

	case ProfileBlock:
		if strings.Contains(fn, "chan") && entry.FlatPct > 20 {
			return "Channel blocking detected - consider buffered channels or alternative synchronization"
		}
	}

	return ""
}

// CompareProfiles compares two profile analyses to detect regressions
func CompareProfiles(baseline, current *ProfileAnalysisResult) *ProfileComparison {
	if baseline == nil || current == nil {
		return nil
	}
	if baseline.ProfileType != current.ProfileType {
		return nil
	}

	comparison := &ProfileComparison{
		ProfileType: baseline.ProfileType,
		Baseline:    baseline,
		Current:     current,
	}

	// Build map of baseline functions
	baselineMap := make(map[string]ProfileEntry)
	for _, e := range baseline.TopEntries {
		baselineMap[e.Function] = e
	}

	// Compare current to baseline
	for _, cur := range current.TopEntries {
		if base, ok := baselineMap[cur.Function]; ok {
			diff := cur.FlatPct - base.FlatPct
			if diff > 5 { // 5% threshold for regression
				comparison.Regressions = append(comparison.Regressions, FunctionDiff{
					Function:    cur.Function,
					BaselinePct: base.FlatPct,
					CurrentPct:  cur.FlatPct,
					DiffPct:     diff,
				})
			} else if diff < -5 { // 5% improvement
				comparison.Improvements = append(comparison.Improvements, FunctionDiff{
					Function:    cur.Function,
					BaselinePct: base.FlatPct,
					CurrentPct:  cur.FlatPct,
					DiffPct:     diff,
				})
			}
		} else {
			// New function in current
			comparison.NewEntries = append(comparison.NewEntries, cur)
		}
	}

	return comparison
}

// ProfileComparison contains the comparison between two profile analyses
type ProfileComparison struct {
	ProfileType  ProfileType
	Baseline     *ProfileAnalysisResult
	Current      *ProfileAnalysisResult
	Regressions  []FunctionDiff
	Improvements []FunctionDiff
	NewEntries   []ProfileEntry
}

// FunctionDiff represents the difference in a function's profile between runs
type FunctionDiff struct {
	Function    string
	BaselinePct float64
	CurrentPct  float64
	DiffPct     float64 // Positive = regression, Negative = improvement
}

// HasRegressions returns true if there are any performance regressions
func (pc *ProfileComparison) HasRegressions() bool {
	return len(pc.Regressions) > 0
}

// Summary returns a summary of the comparison
func (pc *ProfileComparison) Summary() string {
	if pc == nil {
		return "No comparison available"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Profile comparison for %s:\n", pc.ProfileType))

	if len(pc.Regressions) > 0 {
		sb.WriteString("  Regressions:\n")
		for _, r := range pc.Regressions {
			sb.WriteString(fmt.Sprintf("    - %s: %.1f%% -> %.1f%% (+%.1f%%)\n",
				r.Function, r.BaselinePct, r.CurrentPct, r.DiffPct))
		}
	}

	if len(pc.Improvements) > 0 {
		sb.WriteString("  Improvements:\n")
		for _, i := range pc.Improvements {
			sb.WriteString(fmt.Sprintf("    - %s: %.1f%% -> %.1f%% (%.1f%%)\n",
				i.Function, i.BaselinePct, i.CurrentPct, i.DiffPct))
		}
	}

	if len(pc.Regressions) == 0 && len(pc.Improvements) == 0 {
		sb.WriteString("  No significant changes detected\n")
	}

	return sb.String()
}

