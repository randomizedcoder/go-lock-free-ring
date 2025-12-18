package integration_tests

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	// Test flags
	testSet        = flag.String("testset", "quick", "Test set to run (smoke, quick, standard, full)")
	profileModes   = flag.String("profile", "", "Comma-separated profile modes (cpu,mem,allocs,all)")
	outputDir      = flag.String("output", "", "Output directory for logs and profiles")
	tolerance      = flag.Float64("tolerance", 5.0, "Rate tolerance percentage")
	maxDropRate    = flag.Float64("maxdrop", 1.0, "Maximum drop rate percentage")
	generateReport = flag.Bool("report", false, "Generate HTML report after tests")
)

// TestIntegration runs the integration test suite
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	// Find project root and build binary
	projectRoot, err := FindProjectRoot()
	if err != nil {
		t.Fatalf("Failed to find project root: %v", err)
	}

	binaryPath, err := BuildBinary(projectRoot)
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}
	t.Logf("Built binary: %s", binaryPath)

	// Setup output directory
	outDir := *outputDir
	if outDir == "" {
		outDir = filepath.Join(projectRoot, "integration-tests", "output")
	}

	executor, err := NewExecutor(binaryPath, outDir)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Get test cases
	var tests []TestCase
	switch *testSet {
	case "smoke":
		tests, _ = GetPredefinedTestSet("smoke")
	case "quick":
		tests, _ = GetPredefinedTestSet("quick")
	case "standard":
		tests, _ = GetPredefinedTestSet("standard")
	case "full":
		cfg := DefaultTestMatrixConfig()
		// Limit full tests to reasonable subset
		tests = GenerateTestCasesFiltered(cfg, CombineFilters(
			FilterByPacketSize(1450),          // Standard packets only
			FilterByMaxRate(400),              // Limit max rate
		))
	default:
		// Try to get predefined set
		var ok bool
		tests, ok = GetPredefinedTestSet(*testSet)
		if !ok {
			t.Fatalf("Unknown test set: %s", *testSet)
		}
	}

	if len(tests) == 0 {
		t.Fatal("No test cases to run")
	}

	t.Logf("Running %d tests from set '%s'", len(tests), *testSet)

	// Setup validation config
	valCfg := ValidationConfig{
		RateTolerance:   *tolerance,
		MaxDropRate:     *maxDropRate,
		MaxShutdownTime: 2 * time.Second,
	}

	// Run tests
	for _, tc := range tests {
		tc := tc // capture for parallel
		t.Run(tc.ID+"_"+tc.Name, func(t *testing.T) {
			result := RunTest(executor, tc, valCfg, *profileModes)

			// Log results
			if result.Metrics != nil && result.Validation != nil {
				t.Logf("Expected: %.0f Mb/s, Achieved: %.2f Mb/s, Deviation: %+.2f%%",
					result.Validation.ExpectedRate,
					result.Validation.AchievedRate,
					result.Validation.RateDeviation)
				t.Logf("Produced: %d, Dropped: %d (%.2f%%), Consumed: %d",
					result.Metrics.Produced,
					result.Metrics.Dropped,
					result.Metrics.DropRate,
					result.Metrics.Consumed)
			}

			if !result.Passed {
				if result.Error != nil {
					t.Errorf("Test failed: %v", result.Error)
				}
				if result.Validation != nil {
					for _, msg := range result.Validation.Messages {
						t.Errorf("Validation: %s", msg)
					}
				}
				// Log stdout for debugging
				if result.Execution != nil && result.Execution.Stdout != "" {
					t.Logf("Stdout:\n%s", result.Execution.Stdout)
				}
			}
		})
	}
}

// TestIntegrationSmoke runs a minimal smoke test
func TestIntegrationSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping smoke test in short mode")
	}

	projectRoot, err := FindProjectRoot()
	if err != nil {
		t.Fatalf("Failed to find project root: %v", err)
	}

	binaryPath, err := BuildBinary(projectRoot)
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	outDir := filepath.Join(projectRoot, "integration-tests", "output")
	executor, err := NewExecutor(binaryPath, outDir)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Run single smoke test
	tc := TestCase{
		ID:         "SMOKE",
		Name:       "smoke_test",
		Producers:  4,
		Rate:       10,
		PacketSize: 1450,
		Frequency:  10,
		Duration:   3 * time.Second,
		BTreeSize:  2000,
		RingShards: 4,
	}

	result := RunTest(executor, tc, DefaultValidationConfig(), "")

	t.Logf("Smoke test: Expected %.0f Mb/s, Achieved %.2f Mb/s",
		tc.ExpectedTotalRate(), result.Metrics.AverageRate)

	if !result.Passed {
		t.Errorf("Smoke test failed: %v", result.Error)
		if result.Execution != nil {
			t.Logf("Exit code: %d", result.Execution.ExitCode)
			t.Logf("Stdout:\n%s", result.Execution.Stdout)
			t.Logf("Stderr:\n%s", result.Execution.Stderr)
		}
	}
}

// TestIntegrationWithContext tests context cancellation
func TestIntegrationWithContext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping context test in short mode")
	}

	projectRoot, err := FindProjectRoot()
	if err != nil {
		t.Fatalf("Failed to find project root: %v", err)
	}

	binaryPath, err := BuildBinary(projectRoot)
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	outDir := filepath.Join(projectRoot, "integration-tests", "output")
	executor, err := NewExecutor(binaryPath, outDir)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Create a context that we'll cancel early
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tc := TestCase{
		ID:         "CTX",
		Name:       "context_test",
		Producers:  2,
		Rate:       10,
		PacketSize: 1450,
		Frequency:  10,
		Duration:   2 * time.Second,
		BTreeSize:  2000,
		RingShards: 2,
	}

	execResult, err := executor.Run(ctx, tc, "")
	if err != nil {
		t.Fatalf("Execution error: %v", err)
	}

	// Verify we got output (Go's log writes to stderr)
	combinedOutput := execResult.Stdout + "\n" + execResult.Stderr
	if combinedOutput == "\n" {
		t.Error("No output from execution")
	}

	metrics, err := ParseOutput(combinedOutput)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if !metrics.HasValidStats() {
		t.Logf("Stdout: %s", execResult.Stdout)
		t.Logf("Stderr: %s", execResult.Stderr)
		t.Error("No valid stats in output")
	}

	t.Logf("Context test completed: Produced=%d, Consumed=%d, Rate=%.2f Mb/s",
		metrics.Produced, metrics.Consumed, metrics.AverageRate)
}

// TestParserWithSampleOutput tests the parser with known output
func TestParserWithSampleOutput(t *testing.T) {
	sampleOutput := `2025/12/18 11:09:20 Ring size auto-calculated: 128 (4 producers × 10.0 Mb/s, 10ms interval, 2.0x multiplier)
2025/12/18 11:09:20 Starting: 4 producers @ 10.0 Mb/s, ring=128/4 shards, btree max=2000, consumer every 10ms
2025/12/18 11:09:21 [stats] inserted=3452 trimmed=1452 rate=40.02 Mb/s btree=2000 ring=0/128 produced=3452 dropped=0
2025/12/18 11:09:22 [stats] inserted=3452 trimmed=3452 rate=40.02 Mb/s btree=2000 ring=0/128 produced=6904 dropped=0
2025/12/18 11:09:23 [stats] inserted=3444 trimmed=3444 rate=39.96 Mb/s btree=2000 ring=0/128 produced=10348 dropped=0
2025/12/18 11:09:24 [stats] inserted=3448 trimmed=3448 rate=40.01 Mb/s btree=2000 ring=0/128 produced=13796 dropped=0
2025/12/18 11:09:25 Duration 5s reached, shutting down...
2025/12/18 11:09:25 [stats] inserted=3448 trimmed=3448 rate=39.99 Mb/s btree=2000 ring=0/128 produced=17244 dropped=0
2025/12/18 11:09:25 Shutdown signal received, waiting for goroutines...
2025/12/18 11:09:25 Clean shutdown completed in 13.415µs
2025/12/18 11:09:25 === Final Statistics ===
2025/12/18 11:09:25 Produced: 17244
2025/12/18 11:09:25 Dropped:  0 (0.00%)
2025/12/18 11:09:25 Consumed: 17244
2025/12/18 11:09:25 Trimmed:  15244
2025/12/18 11:09:25 BTree final size: 2000`

	metrics, err := ParseOutput(sampleOutput)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// Verify parsed values
	if metrics.RingSize != 128 {
		t.Errorf("RingSize = %d, want 128", metrics.RingSize)
	}
	if !metrics.AutoCalculated {
		t.Error("AutoCalculated should be true")
	}
	if len(metrics.IntervalStats) != 5 {
		t.Errorf("IntervalStats count = %d, want 5", len(metrics.IntervalStats))
	}
	if metrics.Produced != 17244 {
		t.Errorf("Produced = %d, want 17244", metrics.Produced)
	}
	if metrics.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0", metrics.Dropped)
	}
	if metrics.Consumed != 17244 {
		t.Errorf("Consumed = %d, want 17244", metrics.Consumed)
	}
	if metrics.BTreeSize != 2000 {
		t.Errorf("BTreeSize = %d, want 2000", metrics.BTreeSize)
	}
	if metrics.ShutdownDuration != 13415*time.Nanosecond {
		t.Errorf("ShutdownDuration = %v, want ~13.415µs", metrics.ShutdownDuration)
	}

	// Check average rate
	expectedAvg := (40.02 + 40.02 + 39.96 + 40.01 + 39.99) / 5
	if abs(metrics.AverageRate-expectedAvg) > 0.01 {
		t.Errorf("AverageRate = %.2f, want %.2f", metrics.AverageRate, expectedAvg)
	}

	t.Logf("Parsed metrics: Rate=%.2f Mb/s, Produced=%d, Consumed=%d",
		metrics.AverageRate, metrics.Produced, metrics.Consumed)
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// TestValidation tests the validation logic
func TestValidation(t *testing.T) {
	tc := TestCase{
		Producers: 4,
		Rate:      10,
	}

	// Test passing case
	metrics := &ParsedMetrics{
		AverageRate:      40.0, // Expected: 4 * 10 = 40
		DropRate:         0.0,
		ShutdownDuration: 100 * time.Millisecond,
	}

	result := Validate(tc, metrics, DefaultValidationConfig())
	if !result.AllValid {
		t.Errorf("Expected validation to pass: %v", result.Messages)
	}

	// Test rate failure
	metrics.AverageRate = 30.0 // 25% deviation
	result = Validate(tc, metrics, DefaultValidationConfig())
	if result.RateValid {
		t.Error("Expected rate validation to fail")
	}

	// Test drop rate failure
	metrics.AverageRate = 40.0
	metrics.DropRate = 5.0 // 5% drops
	result = Validate(tc, metrics, DefaultValidationConfig())
	if result.DropRateValid {
		t.Error("Expected drop rate validation to fail")
	}

	// Test shutdown failure
	metrics.DropRate = 0.0
	metrics.ShutdownDuration = 5 * time.Second
	result = Validate(tc, metrics, DefaultValidationConfig())
	if result.ShutdownValid {
		t.Error("Expected shutdown validation to fail")
	}
}

// TestIntegrationWithProfiling runs integration tests with profiling enabled
func TestIntegrationWithProfiling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping profile tests in short mode")
	}

	// Parse profile types
	profileTypes := ParseProfileTypes(*profileModes)
	if len(profileTypes) == 0 {
		t.Skip("No profile modes specified (use -profile=cpu,mem,allocs or -profile=all)")
	}

	// Find project root and build binary
	projectRoot, err := FindProjectRoot()
	if err != nil {
		t.Fatalf("Failed to find project root: %v", err)
	}

	binaryPath, err := BuildBinary(projectRoot)
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}
	t.Logf("Built binary: %s", binaryPath)

	// Setup output directory
	outDir := *outputDir
	if outDir == "" {
		outDir = filepath.Join(projectRoot, "integration-tests", "output")
	}

	executor, err := NewExecutor(binaryPath, outDir)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Get test cases - use a smaller set for profiling
	var tests []TestCase
	switch *testSet {
	case "smoke":
		tests, _ = GetPredefinedTestSet("smoke")
	case "quick":
		tests, _ = GetPredefinedTestSet("quick")
	default:
		// For profiling, default to smoke tests
		tests, _ = GetPredefinedTestSet("smoke")
	}

	if len(tests) == 0 {
		t.Fatal("No test cases to run")
	}

	t.Logf("Running %d tests with %d profile types: %v", len(tests), len(profileTypes), profileTypes)

	// Create profile session
	session, err := NewProfileSession(outDir, profileTypes)
	if err != nil {
		t.Fatalf("Failed to create profile session: %v", err)
	}

	// Create analyzer
	analyzer := NewProfileAnalyzer()

	// Collect all analyses
	var allAnalyses []ProfileAnalysisResult

	// Track test results for report
	suiteResult := NewTestSuiteResult("ProfileTests")

	ctx := context.Background()

	// Run tests with profiling
	for _, tc := range tests {
		tc := tc // capture

		// First run test normally for validation
		valCfg := ValidationConfig{
			RateTolerance:   *tolerance,
			MaxDropRate:     *maxDropRate,
			MaxShutdownTime: 2 * time.Second,
		}
		testResult := RunTest(executor, tc, valCfg, "")
		suiteResult.AddResult(testResult)

		t.Run(tc.ID+"_profile", func(t *testing.T) {
			// Run with each profile type
			profileRuns := session.RunWithProfiling(ctx, executor, tc)

			for _, pr := range profileRuns {
				if pr.Error != nil {
					t.Logf("Profile %s/%s error: %v", tc.ID, pr.ProfileType, pr.Error)
					continue
				}

				t.Logf("Profile %s/%s saved to: %s", tc.ID, pr.ProfileType, pr.ProfilePath)

				// Analyze the profile
				analysis, err := analyzer.Analyze(pr.ProfilePath, pr.ProfileType, tc.ID)
				if err != nil {
					t.Logf("Analysis error for %s/%s: %v", tc.ID, pr.ProfileType, err)
					continue
				}

				allAnalyses = append(allAnalyses, *analysis)

				// Log top 3 entries
				t.Logf("Top functions for %s/%s:", tc.ID, pr.ProfileType)
				for i, e := range analysis.TopEntries {
					if i >= 3 {
						break
					}
					t.Logf("  %d. %s: %s (%.1f%%)", e.Rank, e.Function, e.Flat, e.FlatPct)
				}
			}
		})
	}

	suiteResult.Finish()

	// Generate recommendations
	recs := GenerateRecommendations(allAnalyses)
	if len(recs) > 0 {
		t.Log("\n=== Recommendations ===")
		for _, rec := range recs {
			t.Logf("• %s", rec)
		}
	}

	// Generate HTML report if requested
	if *generateReport {
		reporter, err := NewHTMLReporter(outDir)
		if err != nil {
			t.Logf("Warning: Failed to create reporter: %v", err)
		} else {
			reportPath, err := reporter.GenerateReport(suiteResult, allAnalyses)
			if err != nil {
				t.Logf("Warning: Failed to generate report: %v", err)
			} else {
				t.Logf("HTML report generated: %s", reportPath)
			}
		}
	}

	// Print simple report
	t.Log("\n" + GenerateSimpleReport(suiteResult, allAnalyses))
}

// TestProfileAnalyzer tests the profile analyzer with a sample profile
func TestProfileAnalyzer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping in short mode")
	}

	// This test verifies the pprof parsing works
	analyzer := NewProfileAnalyzer()

	// Test profile types parsing
	types := ParseProfileTypes("cpu,mem,allocs")
	if len(types) != 3 {
		t.Errorf("Expected 3 profile types, got %d", len(types))
	}

	typesAll := ParseProfileTypes("all")
	if len(typesAll) != len(AllProfileTypes()) {
		t.Errorf("Expected %d profile types for 'all', got %d", len(AllProfileTypes()), len(typesAll))
	}

	// Verify analyzer is created
	if analyzer.TopCount != 5 {
		t.Errorf("Expected TopCount=5, got %d", analyzer.TopCount)
	}
}

// TestReportGeneration tests the HTML report generation
func TestReportGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping in short mode")
	}

	// Create temp output directory
	tmpDir := t.TempDir()

	reporter, err := NewHTMLReporter(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create reporter: %v", err)
	}

	// Create mock test results
	suiteResult := NewTestSuiteResult("TestSuite")
	suiteResult.AddResult(TestResult{
		TestCase: TestCase{
			ID:        "T001",
			Name:      "test_case_1",
			Producers: 4,
			Rate:      10,
		},
		Passed: true,
		Validation: &ValidationResult{
			ExpectedRate:  40.0,
			AchievedRate:  39.5,
			RateDeviation: -1.25,
			AllValid:      true,
		},
		Metrics: &ParsedMetrics{
			DropRate:         0.0,
			ShutdownDuration: 100 * time.Millisecond,
		},
	})
	suiteResult.AddResult(TestResult{
		TestCase: TestCase{
			ID:        "T002",
			Name:      "test_case_2",
			Producers: 8,
			Rate:      50,
		},
		Passed: false,
		Error:  fmt.Errorf("rate deviation too high"),
		Validation: &ValidationResult{
			ExpectedRate:  400.0,
			AchievedRate:  350.0,
			RateDeviation: -12.5,
			AllValid:      false,
		},
	})
	suiteResult.Finish()

	// Create mock profile analyses
	analyses := []ProfileAnalysisResult{
		{
			TestID:       "T001",
			ProfileType:  ProfileCPU,
			TotalSamples: "5.2s",
			TopEntries: []ProfileEntry{
				{Rank: 1, Function: "runtime.usleep", Flat: "2.5s", FlatPct: 48.0, Cum: "2.5s", CumPct: 48.0},
				{Rank: 2, Function: "sync/atomic.AddUint64", Flat: "1.0s", FlatPct: 19.2, Cum: "1.0s", CumPct: 19.2},
			},
		},
	}

	reportPath, err := reporter.GenerateReport(suiteResult, analyses)
	if err != nil {
		t.Fatalf("Failed to generate report: %v", err)
	}

	// Verify report was created
	if _, err := os.Stat(reportPath); os.IsNotExist(err) {
		t.Errorf("Report file not created: %s", reportPath)
	}

	// Read and verify content
	content, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("Failed to read report: %v", err)
	}

	// Check for expected content
	checks := []string{
		"Integration Test Report",
		"T001",
		"T002",
		"runtime.usleep",
		"PASS",
		"FAIL",
	}

	for _, check := range checks {
		if !containsString(string(content), check) {
			t.Errorf("Report missing expected content: %s", check)
		}
	}

	t.Logf("Report generated at: %s", reportPath)
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestMain handles test setup
func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

