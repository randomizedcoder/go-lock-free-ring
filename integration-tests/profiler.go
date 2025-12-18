package integration_tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProfileType represents a supported profiling mode
type ProfileType string

const (
	ProfileCPU    ProfileType = "cpu"
	ProfileMem    ProfileType = "mem"
	ProfileAllocs ProfileType = "allocs"
	ProfileHeap   ProfileType = "heap"
	ProfileMutex  ProfileType = "mutex"
	ProfileBlock  ProfileType = "block"
	ProfileTrace  ProfileType = "trace"
)

// AllProfileTypes returns all supported profile types
func AllProfileTypes() []ProfileType {
	return []ProfileType{
		ProfileCPU,
		ProfileMem,
		ProfileAllocs,
		ProfileHeap,
		ProfileMutex,
		ProfileBlock,
	}
}

// ParseProfileTypes parses a comma-separated string of profile types
func ParseProfileTypes(s string) []ProfileType {
	if s == "" {
		return nil
	}
	if s == "all" {
		return AllProfileTypes()
	}

	parts := strings.Split(s, ",")
	var types []ProfileType
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "cpu":
			types = append(types, ProfileCPU)
		case "mem":
			types = append(types, ProfileMem)
		case "allocs":
			types = append(types, ProfileAllocs)
		case "heap":
			types = append(types, ProfileHeap)
		case "mutex":
			types = append(types, ProfileMutex)
		case "block":
			types = append(types, ProfileBlock)
		case "trace":
			types = append(types, ProfileTrace)
		}
	}
	return types
}

// ProfileSession manages profile collection for a test suite
type ProfileSession struct {
	OutputDir    string          // Base directory for profiles
	ProfilesDir  string          // Subdirectory for profile files
	ProfileTypes []ProfileType   // Profile types to collect
	Results      []ProfileRunResult // Results from profile runs
}

// ProfileRunResult contains the result of running a test with profiling
type ProfileRunResult struct {
	TestCase    TestCase
	ProfileType ProfileType
	ProfilePath string // Path to the .prof file
	Execution   *ExecutionResult
	Error       error
}

// NewProfileSession creates a new profile collection session
func NewProfileSession(outputDir string, profileTypes []ProfileType) (*ProfileSession, error) {
	// Create unique profiles directory with timestamp
	timestamp := time.Now().Format("20060102-150405")
	profilesDir := filepath.Join(outputDir, "profiles", timestamp)

	if err := os.MkdirAll(profilesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create profiles directory: %w", err)
	}

	return &ProfileSession{
		OutputDir:    outputDir,
		ProfilesDir:  profilesDir,
		ProfileTypes: profileTypes,
	}, nil
}

// RunWithProfiling runs a single test with each configured profile type
func (ps *ProfileSession) RunWithProfiling(ctx context.Context, executor *Executor, tc TestCase) []ProfileRunResult {
	var results []ProfileRunResult

	for _, profType := range ps.ProfileTypes {
		result := ProfileRunResult{
			TestCase:    tc,
			ProfileType: profType,
		}

		// Build expected profile filename (cmd/ring uses pkg/profile naming)
		result.ProfilePath = filepath.Join(ps.ProfilesDir, fmt.Sprintf("%s_%s.pprof", tc.ID, profType))

		// Create a modified test case with longer duration for profiling
		profilingTC := tc
		if profilingTC.Duration < 10*time.Second {
			profilingTC.Duration = 10 * time.Second // Minimum duration for meaningful profiles
		}

		// Run the test with this profile type
		// Note: cmd/ring writes profiles to the profilepath directory
		execResult, err := executor.Run(ctx, profilingTC, string(profType))
		result.Execution = execResult
		if err != nil {
			result.Error = fmt.Errorf("execution failed: %w", err)
		}

		// Check if profile file was created
		// pkg/profile creates files like cpu.pprof, mem.pprof in the profilepath directory
		expectedProfName := getProfileFileName(profType)
		actualProfPath := filepath.Join(executor.OutputDir, "profiles", expectedProfName)

		// Move the profile to our session directory with test-specific name
		if _, err := os.Stat(actualProfPath); err == nil {
			targetPath := filepath.Join(ps.ProfilesDir, fmt.Sprintf("%s_%s.pprof", tc.ID, profType))
			if err := os.Rename(actualProfPath, targetPath); err != nil {
				// If rename fails (cross-device), try copy
				data, readErr := os.ReadFile(actualProfPath)
				if readErr == nil {
					os.WriteFile(targetPath, data, 0644)
					os.Remove(actualProfPath)
				}
			}
			result.ProfilePath = targetPath
		} else {
			// Profile file not found - check alternate location
			altPath := filepath.Join(executor.OutputDir, "profiles", tc.ID+"_"+string(profType)+".prof")
			if _, err := os.Stat(altPath); err == nil {
				result.ProfilePath = altPath
			}
		}

		results = append(results, result)
		ps.Results = append(ps.Results, result)
	}

	return results
}

// getProfileFileName returns the filename that pkg/profile creates for each mode
func getProfileFileName(pt ProfileType) string {
	switch pt {
	case ProfileCPU:
		return "cpu.pprof"
	case ProfileMem, ProfileHeap:
		return "mem.pprof"
	case ProfileAllocs:
		return "mem.pprof" // allocs also writes to mem.pprof
	case ProfileMutex:
		return "mutex.pprof"
	case ProfileBlock:
		return "block.pprof"
	case ProfileTrace:
		return "trace.out"
	default:
		return string(pt) + ".pprof"
	}
}

// GetProfilePaths returns all profile file paths that exist
func (ps *ProfileSession) GetProfilePaths() []string {
	var paths []string
	for _, result := range ps.Results {
		if result.ProfilePath != "" {
			if _, err := os.Stat(result.ProfilePath); err == nil {
				paths = append(paths, result.ProfilePath)
			}
		}
	}
	return paths
}

// ProfileTestSuite runs a complete test suite with profiling
type ProfileTestSuite struct {
	Name         string
	Tests        []TestCase
	ProfileTypes []ProfileType
	Session      *ProfileSession
	Executor     *Executor
	Analyzer     *ProfileAnalyzer
}

// NewProfileTestSuite creates a new profile test suite
func NewProfileTestSuite(name string, tests []TestCase, profileTypes []ProfileType, executor *Executor, outputDir string) (*ProfileTestSuite, error) {
	session, err := NewProfileSession(outputDir, profileTypes)
	if err != nil {
		return nil, err
	}

	return &ProfileTestSuite{
		Name:         name,
		Tests:        tests,
		ProfileTypes: profileTypes,
		Session:      session,
		Executor:     executor,
		Analyzer:     NewProfileAnalyzer(),
	}, nil
}

// ProfileSuiteResult contains all results from a profile test suite run
type ProfileSuiteResult struct {
	Name            string
	StartTime       time.Time
	EndTime         time.Time
	Duration        time.Duration
	ProfileRuns     []ProfileRunResult
	ProfileAnalysis []ProfileAnalysisResult
	TestResults     []TestResult
	TotalTests      int
	TotalProfiles   int
}

// Run executes the profile test suite
func (pts *ProfileTestSuite) Run(ctx context.Context, valCfg ValidationConfig) (*ProfileSuiteResult, error) {
	result := &ProfileSuiteResult{
		Name:      pts.Name,
		StartTime: time.Now(),
	}

	for _, tc := range pts.Tests {
		// Run test without profiling first for validation
		testResult := RunTest(pts.Executor, tc, valCfg, "")
		result.TestResults = append(result.TestResults, testResult)
		result.TotalTests++

		// Run with each profile type
		profileRuns := pts.Session.RunWithProfiling(ctx, pts.Executor, tc)
		result.ProfileRuns = append(result.ProfileRuns, profileRuns...)
		result.TotalProfiles += len(profileRuns)

		// Analyze each profile
		for _, pr := range profileRuns {
			if pr.ProfilePath != "" && pr.Error == nil {
				analysis, err := pts.Analyzer.Analyze(pr.ProfilePath, pr.ProfileType, tc.ID)
				if err == nil && analysis != nil {
					result.ProfileAnalysis = append(result.ProfileAnalysis, *analysis)
				}
			}
		}
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	return result, nil
}

