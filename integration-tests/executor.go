package integration_tests

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Executor runs cmd/ring with test configurations
type Executor struct {
	BinaryPath string // Path to the cmd/ring binary
	OutputDir  string // Directory for logs and profiles
}

// NewExecutor creates a new test executor
func NewExecutor(binaryPath, outputDir string) (*Executor, error) {
	// Ensure binary exists
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("binary not found: %s", binaryPath)
	}

	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output dir: %w", err)
	}

	// Create subdirectories
	for _, subdir := range []string{"logs", "profiles"} {
		if err := os.MkdirAll(filepath.Join(outputDir, subdir), 0755); err != nil {
			return nil, fmt.Errorf("failed to create %s dir: %w", subdir, err)
		}
	}

	return &Executor{
		BinaryPath: binaryPath,
		OutputDir:  outputDir,
	}, nil
}

// ExecutionResult contains the output from running a test
type ExecutionResult struct {
	TestCase    TestCase
	Stdout      string
	Stderr      string
	ExitCode    int
	StartTime   time.Time
	EndTime     time.Time
	Duration    time.Duration
	Error       error
	ProfilePath string // Path to profile file if profiling was enabled
}

// Run executes a test case and returns the result
func (e *Executor) Run(ctx context.Context, tc TestCase, profileMode string) (*ExecutionResult, error) {
	// Use background context if none provided
	if ctx == nil {
		ctx = context.Background()
	}

	result := &ExecutionResult{
		TestCase:  tc,
		StartTime: time.Now(),
	}

	// Build command arguments
	args := []string{
		fmt.Sprintf("-producers=%d", tc.Producers),
		fmt.Sprintf("-rate=%.2f", tc.Rate),
		fmt.Sprintf("-packetSize=%d", tc.PacketSize),
		fmt.Sprintf("-frequency=%d", tc.Frequency),
		fmt.Sprintf("-duration=%s", tc.Duration),
		fmt.Sprintf("-btreeSize=%d", tc.BTreeSize),
		"-debugLevel=3", // Ensure stats are logged
		"-statsInterval=1",
	}

	// Add ring size if specified (otherwise auto-calculate)
	if tc.RingSize > 0 {
		args = append(args, fmt.Sprintf("-ringSize=%d", tc.RingSize))
	}

	// Add ring shards if specified
	if tc.RingShards > 0 {
		args = append(args, fmt.Sprintf("-ringShards=%d", tc.RingShards))
	}

	// Add profiling if requested
	if profileMode != "" {
		profilePath := filepath.Join(e.OutputDir, "profiles", fmt.Sprintf("%s_%s.prof", tc.ID, profileMode))
		args = append(args,
			fmt.Sprintf("-profile=%s", profileMode),
			fmt.Sprintf("-profilepath=%s", filepath.Dir(profilePath)),
		)
		result.ProfilePath = profilePath
	}

	// Create command with context for timeout
	cmdCtx, cancel := context.WithTimeout(ctx, tc.Duration+30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, e.BinaryPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the command
	err := cmd.Run()
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}
		result.Error = err
	}

	// Save log file
	logPath := filepath.Join(e.OutputDir, "logs", fmt.Sprintf("%s.log", tc.ID))
	logContent := fmt.Sprintf("=== Test: %s ===\n", tc.ID)
	logContent += fmt.Sprintf("Config: %s\n", tc.ConfigString())
	logContent += fmt.Sprintf("Command: %s %v\n", e.BinaryPath, args)
	logContent += fmt.Sprintf("Start: %s\n", result.StartTime.Format(time.RFC3339))
	logContent += fmt.Sprintf("End: %s\n", result.EndTime.Format(time.RFC3339))
	logContent += fmt.Sprintf("Duration: %s\n", result.Duration)
	logContent += fmt.Sprintf("Exit Code: %d\n", result.ExitCode)
	logContent += "\n=== STDOUT ===\n"
	logContent += result.Stdout
	logContent += "\n=== STDERR ===\n"
	logContent += result.Stderr

	if err := os.WriteFile(logPath, []byte(logContent), 0644); err != nil {
		// Log write failure but don't fail the test
		fmt.Fprintf(os.Stderr, "Warning: failed to write log file %s: %v\n", logPath, err)
	}

	return result, nil
}

// BuildBinary builds the cmd/ring binary if needed
func BuildBinary(projectRoot string) (string, error) {
	binaryPath := filepath.Join(projectRoot, "bin", "ring")

	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/ring/")
	cmd.Dir = projectRoot

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to build binary: %w\nstderr: %s", err, stderr.String())
	}

	return binaryPath, nil
}

// FindProjectRoot finds the project root directory
func FindProjectRoot() (string, error) {
	// Start from current directory and walk up
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		// Check if go.mod exists
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		// Move to parent directory
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (no go.mod found)")
		}
		dir = parent
	}
}

