package integration_tests

import (
	"fmt"
	"math"
	"time"
)

// ValidationConfig defines the thresholds for test validation
type ValidationConfig struct {
	RateTolerance     float64       // Allowed deviation from expected rate (percentage)
	MaxDropRate       float64       // Maximum acceptable drop rate (percentage)
	MaxShutdownTime   time.Duration // Maximum acceptable shutdown time
}

// DefaultValidationConfig returns the default validation configuration
func DefaultValidationConfig() ValidationConfig {
	return ValidationConfig{
		RateTolerance:   5.0,              // ±5%
		MaxDropRate:     1.0,              // <1%
		MaxShutdownTime: 2 * time.Second,  // <2s
	}
}

// StrictValidationConfig returns stricter validation thresholds
func StrictValidationConfig() ValidationConfig {
	return ValidationConfig{
		RateTolerance:   2.0,              // ±2%
		MaxDropRate:     0.1,              // <0.1%
		MaxShutdownTime: 1 * time.Second,  // <1s
	}
}

// RelaxedValidationConfig returns more lenient validation thresholds
func RelaxedValidationConfig() ValidationConfig {
	return ValidationConfig{
		RateTolerance:   10.0,             // ±10%
		MaxDropRate:     5.0,              // <5%
		MaxShutdownTime: 5 * time.Second,  // <5s
	}
}

// TestResult contains the full result of running a test case
type TestResult struct {
	TestCase     TestCase
	Execution    *ExecutionResult
	Metrics      *ParsedMetrics
	Validation   *ValidationResult
	Passed       bool
	Error        error
}

// ValidationResult contains the results of validating test metrics
type ValidationResult struct {
	// Expected vs actual
	ExpectedRate float64
	AchievedRate float64
	RateDeviation float64 // Percentage

	// Individual validations
	RateValid     bool
	RateMessage   string

	DropRateValid bool
	DropRateMessage string

	ShutdownValid bool
	ShutdownMessage string

	// Overall
	AllValid bool
	Messages []string
}

// Validate validates the test results against the configuration
func Validate(tc TestCase, metrics *ParsedMetrics, cfg ValidationConfig) *ValidationResult {
	result := &ValidationResult{
		ExpectedRate: tc.ExpectedTotalRate(),
		AchievedRate: metrics.AverageRate,
	}

	// Calculate rate deviation
	if result.ExpectedRate > 0 {
		result.RateDeviation = ((result.AchievedRate - result.ExpectedRate) / result.ExpectedRate) * 100
	}

	// Validate rate
	if math.Abs(result.RateDeviation) <= cfg.RateTolerance {
		result.RateValid = true
		result.RateMessage = fmt.Sprintf("Rate %.2f Mb/s within %.1f%% of expected %.2f Mb/s (deviation: %+.2f%%)",
			result.AchievedRate, cfg.RateTolerance, result.ExpectedRate, result.RateDeviation)
	} else {
		result.RateValid = false
		result.RateMessage = fmt.Sprintf("Rate %.2f Mb/s outside %.1f%% tolerance of expected %.2f Mb/s (deviation: %+.2f%%)",
			result.AchievedRate, cfg.RateTolerance, result.ExpectedRate, result.RateDeviation)
		result.Messages = append(result.Messages, result.RateMessage)
	}

	// Validate drop rate
	if metrics.DropRate <= cfg.MaxDropRate {
		result.DropRateValid = true
		result.DropRateMessage = fmt.Sprintf("Drop rate %.2f%% within %.1f%% threshold",
			metrics.DropRate, cfg.MaxDropRate)
	} else {
		result.DropRateValid = false
		result.DropRateMessage = fmt.Sprintf("Drop rate %.2f%% exceeds %.1f%% threshold",
			metrics.DropRate, cfg.MaxDropRate)
		result.Messages = append(result.Messages, result.DropRateMessage)
	}

	// Validate shutdown time
	if metrics.ShutdownDuration <= cfg.MaxShutdownTime {
		result.ShutdownValid = true
		result.ShutdownMessage = fmt.Sprintf("Shutdown time %v within %v threshold",
			metrics.ShutdownDuration, cfg.MaxShutdownTime)
	} else {
		result.ShutdownValid = false
		result.ShutdownMessage = fmt.Sprintf("Shutdown time %v exceeds %v threshold",
			metrics.ShutdownDuration, cfg.MaxShutdownTime)
		result.Messages = append(result.Messages, result.ShutdownMessage)
	}

	// Overall validation
	result.AllValid = result.RateValid && result.DropRateValid && result.ShutdownValid

	return result
}

// TestSuiteResult contains results for a complete test suite run
type TestSuiteResult struct {
	Name        string
	StartTime   time.Time
	EndTime     time.Time
	Duration    time.Duration
	Results     []TestResult
	TotalTests  int
	PassedTests int
	FailedTests int
	SkippedTests int
}

// NewTestSuiteResult creates a new test suite result
func NewTestSuiteResult(name string) *TestSuiteResult {
	return &TestSuiteResult{
		Name:      name,
		StartTime: time.Now(),
	}
}

// AddResult adds a test result to the suite
func (s *TestSuiteResult) AddResult(result TestResult) {
	s.Results = append(s.Results, result)
	s.TotalTests++
	if result.Passed {
		s.PassedTests++
	} else {
		s.FailedTests++
	}
}

// Finish marks the suite as complete
func (s *TestSuiteResult) Finish() {
	s.EndTime = time.Now()
	s.Duration = s.EndTime.Sub(s.StartTime)
}

// PassRate returns the percentage of tests that passed
func (s *TestSuiteResult) PassRate() float64 {
	if s.TotalTests == 0 {
		return 0
	}
	return float64(s.PassedTests) / float64(s.TotalTests) * 100
}

// Summary returns a summary string
func (s *TestSuiteResult) Summary() string {
	return fmt.Sprintf("Suite: %s | Total: %d | Passed: %d | Failed: %d | Duration: %v | Pass Rate: %.1f%%",
		s.Name, s.TotalTests, s.PassedTests, s.FailedTests, s.Duration.Round(time.Second), s.PassRate())
}

// FailedResults returns only the failed test results
func (s *TestSuiteResult) FailedResults() []TestResult {
	var failed []TestResult
	for _, r := range s.Results {
		if !r.Passed {
			failed = append(failed, r)
		}
	}
	return failed
}

// RunTest executes a single test case and returns the result
func RunTest(executor *Executor, tc TestCase, valCfg ValidationConfig, profileMode string) TestResult {
	result := TestResult{
		TestCase: tc,
	}

	// Execute the test
	execResult, err := executor.Run(nil, tc, profileMode)
	result.Execution = execResult

	if err != nil {
		result.Error = fmt.Errorf("execution failed: %w", err)
		result.Passed = false
		return result
	}

	// Check for execution errors
	if execResult.Error != nil && execResult.ExitCode != 0 {
		result.Error = fmt.Errorf("command failed with exit code %d: %w", execResult.ExitCode, execResult.Error)
		result.Passed = false
		return result
	}

	// Parse output (Go's log package writes to stderr)
	// Combine stdout and stderr for parsing
	combinedOutput := execResult.Stdout + "\n" + execResult.Stderr
	metrics, err := ParseOutput(combinedOutput)
	if err != nil {
		result.Error = fmt.Errorf("failed to parse output: %w", err)
		result.Passed = false
		return result
	}
	result.Metrics = metrics

	// Validate metrics
	if !metrics.HasValidStats() {
		result.Error = fmt.Errorf("no valid statistics found in output")
		result.Passed = false
		return result
	}

	validation := Validate(tc, metrics, valCfg)
	result.Validation = validation
	result.Passed = validation.AllValid

	if !result.Passed {
		result.Error = fmt.Errorf("validation failed: %v", validation.Messages)
	}

	return result
}

