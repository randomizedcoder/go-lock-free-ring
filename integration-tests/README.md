# Integration Tests Design Document

This document describes the design for the integration test suite that validates the lock-free ring buffer under various configurations and produces performance analysis reports.

## Overview

The integration tests verify end-to-end functionality of the ring buffer system by:
1. Running `cmd/ring` with various configurations (rates, producers, ring sizes)
2. Validating that the consumer achieves the target throughput
3. Optionally running with profiling enabled
4. Producing an automated HTML report with profile analysis

## Test Matrix

### Configuration Parameters

| Parameter | Test Values | Description |
|-----------|-------------|-------------|
| `producers` | 1, 2, 4, 8, 16 | Number of producer goroutines |
| `rate` | 1, 10, 50, 100 | Per-producer rate in Mb/s |
| `packetSize` | 500, 1450, 9000 | Packet sizes (small, standard, jumbo) |
| `frequency` | 5, 10, 50, 100 | Consumer wake interval in ms |
| `duration` | 10s, 30s, 60s | Test duration |

### Test Scenarios

| Test ID | producers | rate | packetSize | frequency | Expected Total Rate |
|---------|-----------|------|------------|-----------|---------------------|
| T01 | 1 | 10 | 1450 | 10 | 10 Mb/s |
| T02 | 4 | 10 | 1450 | 10 | 40 Mb/s |
| T03 | 4 | 50 | 1450 | 10 | 200 Mb/s |
| T04 | 8 | 50 | 1450 | 10 | 400 Mb/s |
| T05 | 8 | 100 | 1450 | 10 | 800 Mb/s |
| T06 | 16 | 100 | 1450 | 10 | 1600 Mb/s |
| T07 | 4 | 50 | 500 | 10 | 200 Mb/s (more packets) |
| T08 | 4 | 50 | 9000 | 10 | 200 Mb/s (jumbo frames) |
| T09 | 4 | 10 | 1450 | 50 | 40 Mb/s (slower consumer) |
| T10 | 4 | 10 | 1450 | 5 | 40 Mb/s (faster consumer) |

### Validation Criteria

| Metric | Tolerance | Description |
|--------|-----------|-------------|
| Achieved Rate | ±5% of target | Consumer throughput must match expected |
| Drop Rate | < 1% | Acceptable packet drop percentage |
| Shutdown Time | < 2s | Clean shutdown must complete quickly |

## Test Results and Throughput Constraints

### Full Matrix Results (68 tests)

Running `make test-integration-full` executes 68 test combinations across producers (1-16), rates (1-100 Mb/s), and consumer frequencies (5-100ms).

| Result | Count | Percentage |
|--------|-------|------------|
| **PASS** | 61 | 90% |
| **FAIL** | 7 | 10% |

### Analysis of Failures

All 7 failures share a common pattern: **high aggregate rate + slow consumer frequency**. This reveals the fundamental throughput constraint of the system.

| Test ID | Config | Expected | Achieved | Consumer Freq | Issue |
|---------|--------|----------|----------|---------------|-------|
| T092 | 2p×100Mb | 200 Mb/s | 116 Mb/s | 100ms | Ring fills faster than consumer drains |
| T128 | 4p×50Mb | 200 Mb/s | 116 Mb/s | 100ms | Ring fills faster than consumer drains |
| T139 | 4p×100Mb | 400 Mb/s | 232 Mb/s | 50ms | Ring fills faster than consumer drains |
| T140 | 4p×100Mb | 400 Mb/s | 116 Mb/s | 100ms | Ring fills faster than consumer drains |
| T175 | 8p×50Mb | 400 Mb/s | 232 Mb/s | 50ms | Ring fills faster than consumer drains |
| T176 | 8p×50Mb | 400 Mb/s | 116 Mb/s | 100ms | Ring fills faster than consumer drains |
| T212 | 16p×10Mb | 160 Mb/s | 116 Mb/s | 100ms | Ring fills faster than consumer drains |

### Consumer Frequency vs Maximum Sustainable Throughput

The integration tests reveal clear throughput limits based on consumer wake frequency:

| Consumer Frequency | Max Sustainable Rate | Notes |
|--------------------|---------------------|-------|
| 5ms (fast) | **800+ Mb/s** | All tests pass |
| 10ms (normal) | **800+ Mb/s** | All tests pass |
| 50ms (slow) | **~232 Mb/s** | Limited by ring drain rate |
| 100ms (veryslow) | **~116 Mb/s** | Limited by ring drain rate |

### Why This Happens

The ring buffer has a finite capacity. Between consumer wake intervals, producers generate packets:

```
packets_per_interval = (rate_mbps × 1,000,000) / (8 × packet_size) × producers × (frequency_ms / 1000)
```

For 4 producers × 100 Mb/s with 100ms consumer interval:
```
packets = (100 × 1,000,000) / (8 × 1450) × 4 × 0.1 = 3,448 packets per interval
```

With auto-calculated ring size of 4096 slots, this nearly fills the ring every interval. Add timing jitter and the ring overflows, triggering producer backoff.

### Configuration Guidelines

Based on these results, recommended configurations:

| Target Throughput | Recommended Consumer Frequency | Ring Size Multiplier |
|-------------------|-------------------------------|---------------------|
| < 100 Mb/s | 50-100ms acceptable | 2x (default) |
| 100-400 Mb/s | 10-50ms recommended | 2-3x |
| > 400 Mb/s | 5-10ms required | 3-4x |

### Key Takeaways

1. **Consumer frequency is the primary throughput limiter** - not the ring size or producer count
2. **The auto-calculated ring size works correctly** - it sizes based on expected packets per interval
3. **Producer backoff prevents data corruption** - when the ring fills, producers wait rather than corrupt
4. **These failures are expected behavior** - they validate the system correctly handles overload

### Running the Tests

```bash
# Quick test set (4 tests, ~20s) - recommended for CI
make test-integration

# Standard test set (10 tests, ~100s)
make test-integration-standard

# Full matrix (68 tests, ~12min) - reveals throughput constraints
make test-integration-full

# Smoke test only (~3s)
make test-integration-smoke
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Integration Test Runner                       │
│                      (integration_test.go)                       │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      Test Configuration                          │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐           │
│  │ TestCase │ │ TestCase │ │ TestCase │ │ TestCase │  ...      │
│  │   T01    │ │   T02    │ │   T03    │ │   T04    │           │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘           │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      Test Executor                               │
│  1. Build cmd/ring binary                                       │
│  2. For each test case:                                         │
│     a. Run with configuration                                   │
│     b. Parse stdout for metrics                                 │
│     c. Validate against criteria                                │
│     d. Optionally run with profiling                           │
│  3. Generate HTML report                                        │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      Profile Analyzer                            │
│  1. Parse .prof files with go tool pprof                       │
│  2. Extract top 5 functions per category                        │
│  3. Generate summary for HTML report                            │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      HTML Report Generator                       │
│  - Test summary (pass/fail)                                     │
│  - Configuration details                                        │
│  - Performance metrics                                          │
│  - Profile analysis (top 5 per category)                        │
└─────────────────────────────────────────────────────────────────┘
```

## Data Structures

### TestCase

```go
type TestCase struct {
    ID          string        // Test identifier (e.g., "T01")
    Name        string        // Human-readable name
    Producers   int           // Number of producers
    Rate        float64       // Per-producer rate in Mb/s
    PacketSize  int           // Packet size in bytes
    Frequency   int           // Consumer wake interval in ms
    Duration    time.Duration // Test duration
    RingSize    int           // 0 = auto-calculate
    RingShards  int           // Number of shards
    BTreeSize   int           // B-tree capacity
}
```

### TestResult

```go
type TestResult struct {
    TestCase       TestCase
    Passed         bool
    StartTime      time.Time
    EndTime        time.Time

    // Metrics from cmd/ring output
    Produced       uint64
    Dropped        uint64
    Consumed       uint64
    Trimmed        uint64

    // Calculated metrics
    ExpectedRate   float64       // Expected total Mb/s
    AchievedRate   float64       // Actual achieved Mb/s
    RateDeviation  float64       // Percentage deviation
    DropRate       float64       // Drop percentage
    ShutdownTime   time.Duration

    // Validation results
    RateValid      bool
    DropRateValid  bool
    ShutdownValid  bool

    // Error if test failed
    Error          error
}
```

### ProfileResult

```go
type ProfileResult struct {
    TestID      string
    ProfileType string           // cpu, mem, allocs, heap, mutex, block
    FilePath    string           // Path to .prof file
    TopEntries  []ProfileEntry   // Top 5 entries
}

type ProfileEntry struct {
    Rank       int
    Function   string
    Flat       string    // Flat time/memory
    FlatPct    float64   // Flat percentage
    Cum        string    // Cumulative time/memory
    CumPct     float64   // Cumulative percentage
}
```

### TestSuite

```go
type TestSuite struct {
    Name           string
    Tests          []TestCase
    ProfileModes   []string        // Profile modes to run (cpu, mem, etc.)
    OutputDir      string          // Directory for profiles and reports
    ReportPath     string          // HTML report output path
}
```

## Command Line Interface

```bash
# Run all integration tests
go test -v ./integration-tests/

# Run with profiling enabled
go test -v ./integration-tests/ -profile=cpu,mem,allocs

# Run specific test by ID
go test -v ./integration-tests/ -run=T01

# Run with custom duration
go test -v ./integration-tests/ -duration=60s

# Generate HTML report only (from existing profiles)
go test -v ./integration-tests/ -report-only

# Run quick smoke tests (subset of tests)
go test -v ./integration-tests/ -short
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-profile` | string | "" | Comma-separated profile modes (cpu,mem,allocs,heap,mutex,block) |
| `-duration` | duration | 10s | Override test duration |
| `-tolerance` | float | 5.0 | Rate tolerance percentage |
| `-output` | string | "./integration-tests/output" | Output directory |
| `-report-only` | bool | false | Generate report from existing profiles |
| `-parallel` | int | 1 | Number of tests to run in parallel |

## Profile Analysis

### Supported Profile Types

| Profile | Flag | Measures |
|---------|------|----------|
| CPU | `cpu` | CPU time per function |
| Memory | `mem` | Memory allocations (inuse) |
| Allocations | `allocs` | Total allocations (count) |
| Heap | `heap` | Heap memory profile |
| Mutex | `mutex` | Mutex contention |
| Block | `block` | Blocking operations |

### Profile Extraction

The profile analyzer runs `go tool pprof` to extract top functions:

```bash
# CPU profile
go tool pprof -top -nodecount=5 cpu.prof

# Memory profile
go tool pprof -top -nodecount=5 -sample_index=alloc_space mem.prof

# Allocations profile
go tool pprof -top -nodecount=5 -sample_index=alloc_objects allocs.prof
```

### Automated Analysis

```go
func analyzeProfile(profPath string, profType string) (*ProfileResult, error) {
    cmd := exec.Command("go", "tool", "pprof",
        "-top", "-nodecount=5", profPath)

    output, err := cmd.Output()
    if err != nil {
        return nil, err
    }

    entries := parseTopOutput(output)
    return &ProfileResult{
        ProfileType: profType,
        FilePath:    profPath,
        TopEntries:  entries,
    }, nil
}
```

## HTML Report

### Report Structure

```
integration-tests/
└── output/
    ├── report.html           # Main HTML report
    ├── profiles/
    │   ├── T01_cpu.prof
    │   ├── T01_mem.prof
    │   ├── T02_cpu.prof
    │   └── ...
    └── logs/
        ├── T01.log           # Raw output from cmd/ring
        ├── T02.log
        └── ...
```

### HTML Report Sections

1. **Summary**
   - Total tests: X
   - Passed: Y
   - Failed: Z
   - Duration: HH:MM:SS

2. **Test Results Table**
   | Test | Config | Expected Rate | Achieved Rate | Deviation | Drop % | Status |
   |------|--------|---------------|---------------|-----------|--------|--------|
   | T01 | 1p×10Mb | 10 Mb/s | 10.02 Mb/s | +0.2% | 0% | ✅ |
   | T02 | 4p×10Mb | 40 Mb/s | 39.85 Mb/s | -0.4% | 0% | ✅ |

3. **Profile Analysis** (per test with profiling)
   - **T01 CPU Profile**
     | Rank | Function | Flat | Flat% | Cum | Cum% |
     |------|----------|------|-------|-----|------|
     | 1 | runtime.usleep | 2.5s | 45% | 2.5s | 45% |
     | 2 | sync/atomic.AddUint64 | 0.8s | 15% | 0.8s | 15% |
     | ... | | | | | |

4. **Failed Tests Details**
   - Error messages
   - Stdout/stderr logs

5. **Recommendations**
   - Auto-generated based on profile analysis
   - e.g., "High CPU in runtime.usleep suggests rate limiting is working correctly"

### HTML Template

```go
const reportTemplate = `
<!DOCTYPE html>
<html>
<head>
    <title>Integration Test Report - {{.Timestamp}}</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 40px; }
        .summary { display: flex; gap: 20px; margin-bottom: 30px; }
        .summary-card { padding: 20px; border-radius: 8px; background: #f5f5f5; }
        .pass { color: #22c55e; }
        .fail { color: #ef4444; }
        table { border-collapse: collapse; width: 100%; margin: 20px 0; }
        th, td { border: 1px solid #ddd; padding: 12px; text-align: left; }
        th { background: #4f46e5; color: white; }
        tr:nth-child(even) { background: #f9fafb; }
        .profile-section { margin: 30px 0; padding: 20px; background: #fafafa; border-radius: 8px; }
        h1 { color: #1f2937; }
        h2 { color: #374151; border-bottom: 2px solid #e5e7eb; padding-bottom: 10px; }
        h3 { color: #4b5563; }
    </style>
</head>
<body>
    <h1>🔬 Integration Test Report</h1>
    <p>Generated: {{.Timestamp}}</p>

    <div class="summary">
        <div class="summary-card">
            <h3>Total Tests</h3>
            <p style="font-size: 2em;">{{.TotalTests}}</p>
        </div>
        <div class="summary-card">
            <h3>Passed</h3>
            <p style="font-size: 2em;" class="pass">{{.PassedTests}}</p>
        </div>
        <div class="summary-card">
            <h3>Failed</h3>
            <p style="font-size: 2em;" class="fail">{{.FailedTests}}</p>
        </div>
        <div class="summary-card">
            <h3>Duration</h3>
            <p style="font-size: 2em;">{{.Duration}}</p>
        </div>
    </div>

    <h2>📊 Test Results</h2>
    <table>
        <tr>
            <th>Test</th>
            <th>Configuration</th>
            <th>Expected Rate</th>
            <th>Achieved Rate</th>
            <th>Deviation</th>
            <th>Drop %</th>
            <th>Status</th>
        </tr>
        {{range .Results}}
        <tr>
            <td>{{.TestCase.ID}}</td>
            <td>{{.TestCase.Producers}}p × {{.TestCase.Rate}} Mb/s</td>
            <td>{{printf "%.0f" .ExpectedRate}} Mb/s</td>
            <td>{{printf "%.2f" .AchievedRate}} Mb/s</td>
            <td>{{printf "%+.2f" .RateDeviation}}%</td>
            <td>{{printf "%.2f" .DropRate}}%</td>
            <td>{{if .Passed}}✅{{else}}❌{{end}}</td>
        </tr>
        {{end}}
    </table>

    {{if .ProfileResults}}
    <h2>🔍 Profile Analysis</h2>
    {{range .ProfileResults}}
    <div class="profile-section">
        <h3>{{.TestID}} - {{.ProfileType}} Profile</h3>
        <table>
            <tr>
                <th>Rank</th>
                <th>Function</th>
                <th>Flat</th>
                <th>Flat %</th>
                <th>Cum</th>
                <th>Cum %</th>
            </tr>
            {{range .TopEntries}}
            <tr>
                <td>{{.Rank}}</td>
                <td><code>{{.Function}}</code></td>
                <td>{{.Flat}}</td>
                <td>{{printf "%.1f" .FlatPct}}%</td>
                <td>{{.Cum}}</td>
                <td>{{printf "%.1f" .CumPct}}%</td>
            </tr>
            {{end}}
        </table>
    </div>
    {{end}}
    {{end}}

    {{if .FailedTests}}
    <h2>❌ Failed Test Details</h2>
    {{range .FailedResults}}
    <div class="profile-section">
        <h3>{{.TestCase.ID}} - {{.TestCase.Name}}</h3>
        <p><strong>Error:</strong> {{.Error}}</p>
        <pre>{{.Log}}</pre>
    </div>
    {{end}}
    {{end}}

</body>
</html>
`
```

## Implementation Plan

### Phase 1: Basic Test Runner
1. Define test cases and configurations
2. Execute cmd/ring with parameters
3. Parse stdout for metrics
4. Validate against criteria
5. Output pass/fail results

### Phase 2: Profile Support
1. Add profile flag handling
2. Run tests with profiling enabled
3. Collect .prof files
4. Store in organized directory structure

### Phase 3: Profile Analysis
1. Implement pprof output parser
2. Extract top 5 functions per profile
3. Store ProfileResult structs

### Phase 4: HTML Report
1. Implement HTML template
2. Generate summary statistics
3. Format test results table
4. Include profile analysis sections
5. Add failed test details

### Phase 5: Makefile Integration
1. Add `make integration-test` target
2. Add `make integration-test-profile` target
3. Add `make integration-report` target

## File Structure

```
integration-tests/
├── README.md              # This design document
├── integration_test.go    # Main test runner
├── config.go              # Test case definitions
├── executor.go            # Test execution logic
├── parser.go              # Output parsing
├── profiler.go            # Profile management
├── analyzer.go            # Profile analysis
├── reporter.go            # HTML report generation
├── templates/
│   └── report.html        # HTML template
└── output/                # Generated output (gitignored)
    ├── report.html
    ├── profiles/
    └── logs/
```

## Makefile Targets

```makefile
# Run integration tests
integration-test:
	go test -v -timeout=30m ./integration-tests/

# Run integration tests with profiling
integration-test-profile:
	go test -v -timeout=60m ./integration-tests/ -args -profile=cpu,mem,allocs

# Run quick smoke tests
integration-test-short:
	go test -v -short ./integration-tests/

# Generate report from existing profiles
integration-report:
	go test -v ./integration-tests/ -args -report-only

# Clean integration test output
integration-clean:
	rm -rf ./integration-tests/output/
```

## Usage Examples

### Run All Tests

```bash
$ make integration-test

=== RUN   TestIntegration
=== RUN   TestIntegration/T01_single_producer_10Mbps
    integration_test.go:123: T01: Expected 10.0 Mb/s, Achieved 10.02 Mb/s (+0.2%)
--- PASS: TestIntegration/T01_single_producer_10Mbps (10.5s)
=== RUN   TestIntegration/T02_four_producers_10Mbps
    integration_test.go:123: T02: Expected 40.0 Mb/s, Achieved 39.85 Mb/s (-0.4%)
--- PASS: TestIntegration/T02_four_producers_10Mbps (10.3s)
...
--- PASS: TestIntegration (125.3s)
PASS
```

### Run with Profiling

```bash
$ make integration-test-profile

=== RUN   TestIntegration
=== RUN   TestIntegration/T01_single_producer_10Mbps
    integration_test.go:123: T01: Profiling enabled (cpu,mem,allocs)
    integration_test.go:145: T01: CPU profile saved to output/profiles/T01_cpu.prof
    integration_test.go:146: T01: Mem profile saved to output/profiles/T01_mem.prof
--- PASS: TestIntegration/T01_single_producer_10Mbps (12.5s)
...
    integration_test.go:200: HTML report generated: output/report.html
--- PASS: TestIntegration (180.3s)
PASS
```

### View Report

```bash
$ open ./integration-tests/output/report.html
# or
$ xdg-open ./integration-tests/output/report.html
```

## Open Questions

1. **Parallel test execution**: Should tests run in parallel? This could affect profiling accuracy due to resource contention.

2. **Baseline comparison**: Should we store baseline results and compare against them to detect performance regressions?

3. **CI/CD integration**: Should the report be published as a GitHub Actions artifact?

4. **Flame graphs**: Should we generate SVG flame graphs in addition to top-5 tables?

5. **Threshold configuration**: Should tolerance thresholds be configurable per-test or global?

6. **Long-running tests**: Should there be a separate "stress test" suite with longer durations (5min+)?

7. **Resource limits**: Should tests run with cgroups/resource limits to simulate constrained environments?

