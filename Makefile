# go-lock-free-ring Makefile

# Build targets
.PHONY: all build build-cmd build-cmd-race clean

# Test targets
.PHONY: test test-verbose test-race test-race-all test-race-stress test-coverage test-cmd test-datagen test-datagen-short
.PHONY: test-integration test-integration-smoke test-integration-standard test-integration-full test-integration-unit
.PHONY: test-integration-profile test-integration-profile-all test-integration-profile-200mbps test-integration-profile-400mbps test-integration-report
.PHONY: test-strategy-quick test-strategy-standard test-strategy-contention test-strategy-throughput

# Benchmark targets
.PHONY: bench bench-short bench-cpu bench-mem bench-pattern bench-falsesharing bench-padding

# Run targets
.PHONY: run-cmd run-cmd-custom

# Code quality targets
.PHONY: lint vet fmt tidy check help

# Default target
all: build

# Build the ring library
build:
	go build ./...

# Build the example command
build-cmd:
	go build -o bin/ring ./cmd/ring/

# Test the cmd/ring package
test-cmd:
	go test -v -count=1 ./cmd/ring/

# Test the data-generator package
test-datagen:
	go test -v -count=1 ./data-generator/

# Test data-generator in short mode (skips long-running rate tests)
test-datagen-short:
	go test -v -short -count=1 ./data-generator/

# Run integration tests (quick set - ~20s)
# Note: ^TestIntegration$$ anchors to exact match, avoiding TestIntegrationSmoke etc.
test-integration:
	go test -v -count=1 -run "^TestIntegration$$" ./integration-tests/ -args -testset=quick

# Run integration smoke test (~3s)
test-integration-smoke:
	go test -v -count=1 -run "^TestIntegrationSmoke$$" ./integration-tests/

# Run integration tests (standard set - ~100s)
test-integration-standard:
	go test -v -count=1 -run "^TestIntegration$$" ./integration-tests/ -args -testset=standard

# Run integration tests (full matrix - use with caution)
test-integration-full:
	go test -v -timeout=60m -count=1 -run "^TestIntegration$$" ./integration-tests/ -args -testset=full

# Run integration config unit tests only
test-integration-unit:
	go test -v -short -count=1 ./integration-tests/

# Run integration tests with CPU profiling (smoke test: 4p×10Mb)
test-integration-profile:
	go test -v -timeout=30m -count=1 -run TestIntegrationWithProfiling ./integration-tests/ -args -profile=cpu -testset=smoke -report

# Run integration tests with all profile types (smoke test: 4p×10Mb)
test-integration-profile-all:
	go test -v -timeout=60m -count=1 -run TestIntegrationWithProfiling ./integration-tests/ -args -profile=all -testset=smoke -report

# Profile the 4 producers × 50 Mb/s test (T003 from quick set = 200 Mb/s total)
# Runs all 6 profile types: cpu, mem, allocs, heap, mutex, block
# Duration: ~80s, generates HTML report
test-integration-profile-200mbps:
	go test -v -timeout=60m -count=1 -run "TestIntegrationWithProfiling/T003" ./integration-tests/ -args -profile=all -testset=quick -report

# Profile the 8 producers × 50 Mb/s test (T004 from quick set = 400 Mb/s total)
# Runs all 6 profile types: cpu, mem, allocs, heap, mutex, block
# Duration: ~80s, generates HTML report
test-integration-profile-400mbps:
	go test -v -timeout=60m -count=1 -run "TestIntegrationWithProfiling/T004" ./integration-tests/ -args -profile=all -testset=quick -report

# Profile the 8 producers × 125 Mb/s test (1 Gbps total)
# Great for demonstrating high-throughput capability
# Duration: ~15s, generates HTML report
test-integration-profile-1gbps:
	go test -v -timeout=60m -count=1 -run "TestIntegrationWithProfiling/T001" ./integration-tests/ -args -profile=all -testset=gbps -report

# Quick 1 Gbps throughput demo (no profiling, just shows throughput)
# Use this for quick validation of high-throughput performance
test-integration-1gbps:
	go test -v -timeout=30m -count=1 -run "^TestIntegration$$/T001" ./integration-tests/ -args -testset=gbps

# Generate integration test report from quick tests
test-integration-report:
	go test -v -timeout=30m -count=1 -run "^TestIntegration$$" ./integration-tests/ -args -testset=quick -report

# =============================================================================
# Strategy Comparison Tests
# =============================================================================

# Quick strategy comparison (~1 min): 3 strategies × 1 config
test-strategy-quick: build-cmd
	go test -v -timeout=10m -count=1 -run TestStrategyComparison ./integration-tests/ -args -testset=strategy-quick -report

# Standard strategy comparison (~5 min): all 6 strategies × 4 configs
test-strategy-standard: build-cmd
	go test -v -timeout=30m -count=1 -run TestStrategyComparison ./integration-tests/ -args -testset=strategy-standard -report

# Contention strategy comparison (~15 min): all strategies × GOMAXPROCS variations
test-strategy-contention: build-cmd
	go test -v -timeout=60m -count=1 -run TestStrategyComparison ./integration-tests/ -args -testset=strategy-contention -report

# High-throughput strategy comparison (~20 min): all strategies × high load
test-strategy-throughput: build-cmd
	go test -v -timeout=60m -count=1 -run TestStrategyComparison ./integration-tests/ -args -testset=strategy-throughput -report

# Run the example command with defaults for 5 seconds
run-cmd: build-cmd
	./bin/ring -duration=5s

# Run the example command with custom settings
# Usage: make run-cmd-custom ARGS="-producers=8 -rate=50 -duration=10s"
run-cmd-custom: build-cmd
	./bin/ring $(ARGS)

# Clean build artifacts
clean:
	rm -rf bin/
	go clean ./...

# Run all tests (ring library only)
test:
	go test -count=1 .

# Run tests with verbose output
test-verbose:
	go test -v -count=1 .

# Run tests with race detector
test-race:
	go test -race -count=1 .

# Run tests with race detector (all packages)
test-race-all:
	go test -race -count=1 ./...

# Run race detector with stress testing (runs concurrent tests multiple times)
test-race-stress:
	go test -race -count=10 -run "Concurrent" .

# Build command with race detector (for debugging race conditions)
build-cmd-race:
	go build -race -o bin/ring-race ./cmd/ring/

# Run tests with coverage
test-coverage:
	go test -cover -coverprofile=coverage.out .
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Run all benchmarks (ring library)
bench:
	go test -bench=. -benchmem -run=^$$ .

# Run benchmarks with shorter duration
bench-short:
	go test -bench=. -benchmem -benchtime=1s -run=^$$ .

# Run benchmarks with CPU profile
bench-cpu:
	go test -bench=. -benchmem -cpuprofile=cpu.prof -run=^$$ .
	@echo "CPU profile: cpu.prof"
	@echo "View with: go tool pprof cpu.prof"

# Run benchmarks with memory profile
bench-mem:
	go test -bench=. -benchmem -memprofile=mem.prof -run=^$$ .
	@echo "Memory profile: mem.prof"
	@echo "View with: go tool pprof mem.prof"

# Run specific benchmark by pattern (usage: make bench-pattern PATTERN=Write)
bench-pattern:
	go test -bench=$(PATTERN) -benchmem -run=^$$ .

# Run false sharing benchmarks (demonstrates cache line effects)
bench-falsesharing:
	go test -bench='FalseSharing' -benchmem -benchtime=2s -run=^$$ .

# Run padding optimization benchmarks (find optimal cache line padding)
bench-padding:
	go test -bench='ShardPadding' -benchmem -benchtime=2s -run=^$$ .

# Run linter (requires golangci-lint)
lint:
	golangci-lint run ./...

# Run go vet
vet:
	go vet ./...

# Format code
fmt:
	go fmt ./...

# Tidy go modules
tidy:
	go mod tidy

# Run all checks (fmt, vet, test, bench-short)
check: fmt vet test bench-short

# Show help
help:
	@echo "Available targets:"
	@echo ""
	@echo "Build:"
	@echo "  build          - Build the ring library"
	@echo "  build-cmd      - Build the example command to bin/ring"
	@echo "  clean          - Remove build artifacts"
	@echo ""
	@echo "Test (ring library):"
	@echo "  test           - Run all tests"
	@echo "  test-verbose   - Run tests with verbose output"
	@echo "  test-race      - Run tests with race detector"
	@echo "  test-coverage  - Run tests with coverage report"
	@echo "  test-cmd       - Run cmd/ring tests"
	@echo "  test-datagen   - Run data-generator tests"
	@echo "  test-datagen-short - Run data-generator tests (short mode)"
	@echo "  test-integration - Run integration tests (quick set, ~20s)"
	@echo "  test-integration-smoke - Run smoke test only (~3s)"
	@echo "  test-integration-standard - Run standard test set (~100s)"
	@echo "  test-integration-full - Run full test matrix (long)"
	@echo "  test-integration-unit - Run config unit tests only"
	@echo "  test-integration-profile - Run with CPU profiling (4p×10Mb)"
	@echo "  test-integration-profile-all - Run with all profile types (4p×10Mb)"
	@echo "  test-integration-profile-200mbps - Profile 4p×50Mb (200Mb/s)"
	@echo "  test-integration-profile-400mbps - Profile 8p×50Mb (400Mb/s)"
	@echo "  test-integration-profile-1gbps - Profile 8p×125Mb (1 Gbps) with HTML report"
	@echo "  test-integration-1gbps - Quick 1 Gbps throughput demo"
	@echo "  test-integration-report - Generate HTML report"
	@echo ""
	@echo "Strategy comparison:"
	@echo "  test-strategy-quick      - Quick comparison (~1 min)"
	@echo "  test-strategy-standard   - Standard comparison (~5 min)"
	@echo "  test-strategy-contention - Contention test with GOMAXPROCS (~15 min)"
	@echo "  test-strategy-throughput - High-throughput comparison (~20 min)"
	@echo ""
	@echo "Benchmark:"
	@echo "  bench          - Run all benchmarks"
	@echo "  bench-short    - Run benchmarks with 1s duration"
	@echo "  bench-cpu      - Run benchmarks with CPU profiling"
	@echo "  bench-mem      - Run benchmarks with memory profiling"
	@echo "  bench-pattern  - Run specific benchmark (PATTERN=Write)"
	@echo "  bench-falsesharing - Run false sharing benchmarks"
	@echo "  bench-padding  - Run padding size benchmarks"
	@echo ""
	@echo "Run example:"
	@echo "  run-cmd        - Run example command for 5s with defaults"
	@echo "  run-cmd-custom - Run with custom args (ARGS=\"-producers=8 -rate=50\")"
	@echo ""
	@echo "Code quality:"
	@echo "  lint           - Run golangci-lint"
	@echo "  vet            - Run go vet"
	@echo "  fmt            - Format code"
	@echo "  tidy           - Tidy go modules"
	@echo "  check          - Run fmt, vet, test, and bench-short"
	@echo ""
	@echo "  help           - Show this help"
