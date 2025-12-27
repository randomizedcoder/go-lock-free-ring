package integration_tests

import (
	"context"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StrategyComparisonResult aggregates metrics across strategies and GOMAXPROCS
type StrategyComparisonResult struct {
	StartTime   time.Time
	EndTime     time.Time
	Duration    time.Duration
	TestResults []TestResult

	// Aggregated metrics by strategy
	ByStrategy map[string]*StrategyMetrics

	// Aggregated metrics by GOMAXPROCS
	ByGOMAXPROCS map[int]*StrategyMetrics

	// Cross-dimensional: strategy × GOMAXPROCS
	Matrix map[string]map[int]*StrategyMetrics
}

// StrategyMetrics holds aggregated metrics for a strategy or configuration
type StrategyMetrics struct {
	Strategy   string
	GOMAXPROCS int
	TestCount  int
	PassCount  int
	FailCount  int

	// Throughput metrics
	TotalExpectedRate float64 // Sum of expected rates
	TotalAchievedRate float64 // Sum of achieved rates
	AvgRateDeviation  float64 // Average rate deviation percentage

	// Quality metrics
	TotalDropped   uint64
	TotalProduced  uint64
	AvgDropRate    float64
	MaxDropRate    float64

	// Timing metrics
	AvgShutdownTime time.Duration
	MaxShutdownTime time.Duration

	// Raw data for percentile calculations
	rateDeviations []float64
	dropRates      []float64
	shutdownTimes  []time.Duration
}

// PassRate returns the percentage of passed tests
func (sm *StrategyMetrics) PassRate() float64 {
	if sm.TestCount == 0 {
		return 0
	}
	return float64(sm.PassCount) / float64(sm.TestCount) * 100
}

// ThroughputEfficiency returns achieved/expected rate percentage
func (sm *StrategyMetrics) ThroughputEfficiency() float64 {
	if sm.TotalExpectedRate == 0 {
		return 0
	}
	return sm.TotalAchievedRate / sm.TotalExpectedRate * 100
}

// RunStrategyComparison runs all strategy test cases and aggregates results
func RunStrategyComparison(ctx context.Context, executor *Executor, tests []TestCase) (*StrategyComparisonResult, error) {
	result := &StrategyComparisonResult{
		StartTime:    time.Now(),
		ByStrategy:   make(map[string]*StrategyMetrics),
		ByGOMAXPROCS: make(map[int]*StrategyMetrics),
		Matrix:       make(map[string]map[int]*StrategyMetrics),
	}

	// Initialize metrics containers
	for _, tc := range tests {
		if _, ok := result.ByStrategy[tc.Strategy]; !ok {
			result.ByStrategy[tc.Strategy] = &StrategyMetrics{Strategy: tc.Strategy}
		}
		if _, ok := result.ByGOMAXPROCS[tc.GOMAXPROCS]; !ok {
			result.ByGOMAXPROCS[tc.GOMAXPROCS] = &StrategyMetrics{GOMAXPROCS: tc.GOMAXPROCS}
		}
		if _, ok := result.Matrix[tc.Strategy]; !ok {
			result.Matrix[tc.Strategy] = make(map[int]*StrategyMetrics)
		}
		if _, ok := result.Matrix[tc.Strategy][tc.GOMAXPROCS]; !ok {
			result.Matrix[tc.Strategy][tc.GOMAXPROCS] = &StrategyMetrics{
				Strategy:   tc.Strategy,
				GOMAXPROCS: tc.GOMAXPROCS,
			}
		}
	}

	// Run each test
	for i, tc := range tests {
		fmt.Printf("[%d/%d] Running: %s (%s)\n", i+1, len(tests), tc.ID, tc.ConfigString())

		execResult, err := executor.Run(ctx, tc, "")
		if err != nil {
			fmt.Printf("  Error: %v\n", err)
			continue
		}

		// Parse and validate results
		combinedOutput := execResult.Stdout + "\n" + execResult.Stderr
		metrics, parseErr := ParseOutput(combinedOutput)
		
		var validation *ValidationResult
		if metrics != nil && metrics.HasValidStats() {
			validation = Validate(tc, metrics, DefaultValidationConfig())
		}

		testResult := TestResult{
			TestCase:   tc,
			Execution:  execResult,
			Metrics:    metrics,
			Validation: validation,
			Passed:     validation != nil && validation.AllValid,
			Error:      parseErr,
		}

		result.TestResults = append(result.TestResults, testResult)

		// Update aggregated metrics
		updateStrategyMetrics(result.ByStrategy[tc.Strategy], testResult)
		updateStrategyMetrics(result.ByGOMAXPROCS[tc.GOMAXPROCS], testResult)
		updateStrategyMetrics(result.Matrix[tc.Strategy][tc.GOMAXPROCS], testResult)

		// Print brief status
		status := "PASS"
		if !testResult.Passed {
			status = "FAIL"
		}
		if validation != nil {
			fmt.Printf("  %s: %.2f Mb/s (%.1f%% of expected), drop=%.2f%%\n",
				status, validation.AchievedRate, validation.AchievedRate/validation.ExpectedRate*100, metrics.DropRate)
		} else {
			fmt.Printf("  %s: could not parse metrics\n", status)
		}
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	// Finalize aggregated metrics (compute averages)
	for _, sm := range result.ByStrategy {
		finalizeStrategyMetrics(sm)
	}
	for _, sm := range result.ByGOMAXPROCS {
		finalizeStrategyMetrics(sm)
	}
	for _, byGMP := range result.Matrix {
		for _, sm := range byGMP {
			finalizeStrategyMetrics(sm)
		}
	}

	return result, nil
}

// updateStrategyMetrics updates aggregated metrics with a test result
func updateStrategyMetrics(sm *StrategyMetrics, tr TestResult) {
	sm.TestCount++
	if tr.Passed {
		sm.PassCount++
	} else {
		sm.FailCount++
	}

	if tr.Validation != nil {
		sm.TotalExpectedRate += tr.Validation.ExpectedRate
		sm.TotalAchievedRate += tr.Validation.AchievedRate
		sm.rateDeviations = append(sm.rateDeviations, tr.Validation.RateDeviation)
	}

	if tr.Metrics != nil {
		sm.TotalDropped += tr.Metrics.Dropped
		sm.TotalProduced += tr.Metrics.Produced
		sm.dropRates = append(sm.dropRates, tr.Metrics.DropRate)
		if tr.Metrics.DropRate > sm.MaxDropRate {
			sm.MaxDropRate = tr.Metrics.DropRate
		}
		sm.shutdownTimes = append(sm.shutdownTimes, tr.Metrics.ShutdownDuration)
		if tr.Metrics.ShutdownDuration > sm.MaxShutdownTime {
			sm.MaxShutdownTime = tr.Metrics.ShutdownDuration
		}
	}
}

// finalizeStrategyMetrics computes averages from accumulated data
func finalizeStrategyMetrics(sm *StrategyMetrics) {
	if len(sm.rateDeviations) > 0 {
		sum := 0.0
		for _, v := range sm.rateDeviations {
			sum += v
		}
		sm.AvgRateDeviation = sum / float64(len(sm.rateDeviations))
	}

	if len(sm.dropRates) > 0 {
		sum := 0.0
		for _, v := range sm.dropRates {
			sum += v
		}
		sm.AvgDropRate = sum / float64(len(sm.dropRates))
	}

	if len(sm.shutdownTimes) > 0 {
		var sum time.Duration
		for _, v := range sm.shutdownTimes {
			sum += v
		}
		sm.AvgShutdownTime = sum / time.Duration(len(sm.shutdownTimes))
	}
}

// =============================================================================
// Strategy Comparison Report Generation
// =============================================================================

// StrategyReportData contains all data for the strategy comparison report
type StrategyReportData struct {
	Timestamp   string
	Duration    string
	TotalTests  int
	PassedTests int
	FailedTests int
	PassRate    float64

	// Strategy comparison tables
	StrategyTable   []StrategyTableRow
	GOMAXPROCSTable []GOMAXPROCSTableRow
	MatrixTable     []MatrixTableRow

	// Recommendations
	Recommendations []string
	BestStrategy    string
	BestForContention string
}

// StrategyTableRow represents one row in the strategy comparison table
type StrategyTableRow struct {
	Strategy           string
	TestCount          int
	PassRate           float64
	ThroughputEff      float64 // Achieved/Expected %
	AvgRateDeviation   float64
	AvgDropRate        float64
	MaxDropRate        float64
	AvgShutdownTimeMs  float64
}

// GOMAXPROCSTableRow represents one row in the GOMAXPROCS comparison table
type GOMAXPROCSTableRow struct {
	GOMAXPROCS        int
	GOMAXPROCSLabel   string
	TestCount         int
	PassRate          float64
	ThroughputEff     float64
	AvgDropRate       float64
}

// MatrixTableRow represents strategy × GOMAXPROCS combination
type MatrixTableRow struct {
	Strategy      string
	GOMAXPROCS    int
	GOMAXPROCSLabel string
	PassRate      float64
	ThroughputEff float64
	AvgDropRate   float64
}

// GenerateStrategyComparisonReport creates an HTML report for strategy comparison
func GenerateStrategyComparisonReport(result *StrategyComparisonResult, outputDir string) (string, error) {
	data := buildStrategyReportData(result)

	tmpl, err := template.New("strategy_report").Parse(strategyReportTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	filename := fmt.Sprintf("strategy-comparison-%s.html", time.Now().Format("20060102-150405"))
	reportPath := filepath.Join(outputDir, filename)

	f, err := os.Create(reportPath)
	if err != nil {
		return "", fmt.Errorf("failed to create report file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	// Create latest symlink
	latestPath := filepath.Join(outputDir, "strategy-comparison-latest.html")
	os.Remove(latestPath)
	os.Symlink(filename, latestPath)

	return reportPath, nil
}

// buildStrategyReportData converts comparison results to report data
func buildStrategyReportData(result *StrategyComparisonResult) StrategyReportData {
	data := StrategyReportData{
		Timestamp:  time.Now().Format("2006-01-02 15:04:05"),
		Duration:   result.Duration.Round(time.Second).String(),
		TotalTests: len(result.TestResults),
	}

	for _, tr := range result.TestResults {
		if tr.Passed {
			data.PassedTests++
		} else {
			data.FailedTests++
		}
	}
	if data.TotalTests > 0 {
		data.PassRate = float64(data.PassedTests) / float64(data.TotalTests) * 100
	}

	// Build strategy table (sorted by throughput efficiency)
	var strategyRows []StrategyTableRow
	for strategy, sm := range result.ByStrategy {
		row := StrategyTableRow{
			Strategy:          strategy,
			TestCount:         sm.TestCount,
			PassRate:          sm.PassRate(),
			ThroughputEff:     sm.ThroughputEfficiency(),
			AvgRateDeviation:  sm.AvgRateDeviation,
			AvgDropRate:       sm.AvgDropRate,
			MaxDropRate:       sm.MaxDropRate,
			AvgShutdownTimeMs: float64(sm.AvgShutdownTime.Milliseconds()),
		}
		strategyRows = append(strategyRows, row)
	}
	sort.Slice(strategyRows, func(i, j int) bool {
		return strategyRows[i].ThroughputEff > strategyRows[j].ThroughputEff
	})
	data.StrategyTable = strategyRows

	// Build GOMAXPROCS table
	var gmpRows []GOMAXPROCSTableRow
	for gmp, sm := range result.ByGOMAXPROCS {
		label := fmt.Sprintf("%d", gmp)
		if gmp == 0 {
			label = "default"
		}
		row := GOMAXPROCSTableRow{
			GOMAXPROCS:      gmp,
			GOMAXPROCSLabel: label,
			TestCount:       sm.TestCount,
			PassRate:        sm.PassRate(),
			ThroughputEff:   sm.ThroughputEfficiency(),
			AvgDropRate:     sm.AvgDropRate,
		}
		gmpRows = append(gmpRows, row)
	}
	sort.Slice(gmpRows, func(i, j int) bool {
		// Sort: default (0) first, then ascending
		if gmpRows[i].GOMAXPROCS == 0 {
			return true
		}
		if gmpRows[j].GOMAXPROCS == 0 {
			return false
		}
		return gmpRows[i].GOMAXPROCS < gmpRows[j].GOMAXPROCS
	})
	data.GOMAXPROCSTable = gmpRows

	// Build matrix table
	var matrixRows []MatrixTableRow
	for strategy, byGMP := range result.Matrix {
		for gmp, sm := range byGMP {
			label := fmt.Sprintf("%d", gmp)
			if gmp == 0 {
				label = "default"
			}
			row := MatrixTableRow{
				Strategy:        strategy,
				GOMAXPROCS:      gmp,
				GOMAXPROCSLabel: label,
				PassRate:        sm.PassRate(),
				ThroughputEff:   sm.ThroughputEfficiency(),
				AvgDropRate:     sm.AvgDropRate,
			}
			matrixRows = append(matrixRows, row)
		}
	}
	sort.Slice(matrixRows, func(i, j int) bool {
		if matrixRows[i].Strategy != matrixRows[j].Strategy {
			return matrixRows[i].Strategy < matrixRows[j].Strategy
		}
		return matrixRows[i].GOMAXPROCS < matrixRows[j].GOMAXPROCS
	})
	data.MatrixTable = matrixRows

	// Generate recommendations
	data.Recommendations = generateStrategyRecommendations(result)
	if len(strategyRows) > 0 {
		data.BestStrategy = strategyRows[0].Strategy
	}

	// Find best for contention (lowest drop rate at GOMAXPROCS=1)
	if byGMP1, ok := result.Matrix["NextShard"][1]; ok && byGMP1.AvgDropRate < 1.0 {
		data.BestForContention = "NextShard"
	} else if byGMP1, ok := result.Matrix["SpinThenYield"][1]; ok && byGMP1.AvgDropRate < 1.0 {
		data.BestForContention = "SpinThenYield"
	}

	return data
}

// generateStrategyRecommendations creates actionable recommendations
func generateStrategyRecommendations(result *StrategyComparisonResult) []string {
	var recs []string

	// Find best overall strategy
	var bestStrategy string
	var bestEff float64
	for strategy, sm := range result.ByStrategy {
		eff := sm.ThroughputEfficiency()
		if eff > bestEff {
			bestEff = eff
			bestStrategy = strategy
		}
	}
	if bestStrategy != "" {
		recs = append(recs, fmt.Sprintf("Best overall throughput: %s (%.1f%% efficiency)", bestStrategy, bestEff))
	}

	// Find lowest drop rate
	var lowestDropStrategy string
	lowestDrop := 100.0
	for strategy, sm := range result.ByStrategy {
		if sm.AvgDropRate < lowestDrop {
			lowestDrop = sm.AvgDropRate
			lowestDropStrategy = strategy
		}
	}
	if lowestDropStrategy != "" && lowestDrop < 1.0 {
		recs = append(recs, fmt.Sprintf("Lowest drop rate: %s (%.2f%% avg)", lowestDropStrategy, lowestDrop))
	}

	// GOMAXPROCS impact
	if sm1, ok := result.ByGOMAXPROCS[1]; ok {
		if smDefault, ok := result.ByGOMAXPROCS[0]; ok {
			if sm1.AvgDropRate > smDefault.AvgDropRate*2 {
				recs = append(recs, "High contention impact: GOMAXPROCS=1 shows significantly higher drop rates")
			}
		}
	}

	// Strategy-specific recommendations
	if nextShard, ok := result.ByStrategy["NextShard"]; ok {
		if sleepBackoff, ok := result.ByStrategy["SleepBackoff"]; ok {
			if nextShard.ThroughputEfficiency() > sleepBackoff.ThroughputEfficiency()+5 {
				recs = append(recs, "NextShard outperforms SleepBackoff - consider for high-throughput scenarios")
			}
		}
	}

	if spinYield, ok := result.ByStrategy["SpinThenYield"]; ok {
		if spinYield.AvgDropRate < 0.5 {
			recs = append(recs, "SpinThenYield shows very low drop rates - good for latency-sensitive workloads")
		}
	}

	return recs
}

// GenerateSimpleStrategyReport generates a text report
func GenerateSimpleStrategyReport(result *StrategyComparisonResult) string {
	var sb strings.Builder

	sb.WriteString("=" + strings.Repeat("=", 79) + "\n")
	sb.WriteString("Strategy Comparison Report\n")
	sb.WriteString("Generated: " + time.Now().Format("2006-01-02 15:04:05") + "\n")
	sb.WriteString("Duration: " + result.Duration.Round(time.Second).String() + "\n")
	sb.WriteString("=" + strings.Repeat("=", 79) + "\n\n")

	// Summary
	passed := 0
	for _, tr := range result.TestResults {
		if tr.Passed {
			passed++
		}
	}
	sb.WriteString(fmt.Sprintf("Total Tests: %d, Passed: %d, Failed: %d\n\n",
		len(result.TestResults), passed, len(result.TestResults)-passed))

	// Strategy comparison table
	sb.WriteString("== Strategy Comparison ==\n")
	sb.WriteString(fmt.Sprintf("%-15s %6s %10s %10s %10s\n",
		"Strategy", "Tests", "Pass%", "Throughput", "Drop%"))
	sb.WriteString(strings.Repeat("-", 55) + "\n")

	// Sort by throughput efficiency
	type stratRow struct {
		name string
		sm   *StrategyMetrics
	}
	var rows []stratRow
	for name, sm := range result.ByStrategy {
		rows = append(rows, stratRow{name, sm})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].sm.ThroughputEfficiency() > rows[j].sm.ThroughputEfficiency()
	})

	for _, r := range rows {
		sb.WriteString(fmt.Sprintf("%-15s %6d %9.1f%% %9.1f%% %9.2f%%\n",
			r.name, r.sm.TestCount, r.sm.PassRate(),
			r.sm.ThroughputEfficiency(), r.sm.AvgDropRate))
	}
	sb.WriteString("\n")

	// GOMAXPROCS comparison
	if len(result.ByGOMAXPROCS) > 1 {
		sb.WriteString("== GOMAXPROCS Impact ==\n")
		sb.WriteString(fmt.Sprintf("%-12s %6s %10s %10s\n",
			"GOMAXPROCS", "Tests", "Throughput", "Drop%"))
		sb.WriteString(strings.Repeat("-", 42) + "\n")

		for gmp, sm := range result.ByGOMAXPROCS {
			label := fmt.Sprintf("%d", gmp)
			if gmp == 0 {
				label = "default"
			}
			sb.WriteString(fmt.Sprintf("%-12s %6d %9.1f%% %9.2f%%\n",
				label, sm.TestCount, sm.ThroughputEfficiency(), sm.AvgDropRate))
		}
		sb.WriteString("\n")
	}

	// Recommendations
	recs := generateStrategyRecommendations(result)
	if len(recs) > 0 {
		sb.WriteString("== Recommendations ==\n")
		for i, rec := range recs {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, rec))
		}
	}

	return sb.String()
}

// HTML template for strategy comparison report
const strategyReportTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Strategy Comparison Report - {{.Timestamp}}</title>
    <style>
        :root {
            --bg-primary: #0a0a0f;
            --bg-secondary: #12121a;
            --bg-card: #1a1a24;
            --text-primary: #e8e8ed;
            --text-secondary: #8888a0;
            --accent-green: #00d68f;
            --accent-red: #ff6b6b;
            --accent-blue: #4da6ff;
            --accent-purple: #b48eff;
            --accent-orange: #ffaa00;
            --accent-cyan: #00e5cc;
            --border-color: #2a2a3a;
        }
        
        * { margin: 0; padding: 0; box-sizing: border-box; }
        
        body {
            font-family: 'IBM Plex Mono', 'Fira Code', monospace;
            background: var(--bg-primary);
            color: var(--text-primary);
            line-height: 1.6;
            padding: 2rem;
        }
        
        .container { max-width: 1400px; margin: 0 auto; }
        
        h1 {
            font-size: 1.75rem;
            font-weight: 600;
            margin-bottom: 0.25rem;
            background: linear-gradient(135deg, var(--accent-cyan), var(--accent-purple));
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }
        
        .subtitle { color: var(--text-secondary); font-size: 0.875rem; margin-bottom: 2rem; }
        
        h2 {
            font-size: 1.1rem;
            color: var(--accent-cyan);
            margin: 2rem 0 1rem;
            padding-bottom: 0.5rem;
            border-bottom: 1px solid var(--border-color);
        }
        
        .summary-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
            gap: 1rem;
            margin-bottom: 2rem;
        }
        
        .summary-card {
            background: var(--bg-card);
            border-radius: 8px;
            padding: 1.25rem;
            border: 1px solid var(--border-color);
        }
        
        .summary-card h3 {
            font-size: 0.7rem;
            text-transform: uppercase;
            letter-spacing: 0.1em;
            color: var(--text-secondary);
            margin-bottom: 0.5rem;
        }
        
        .summary-card .value {
            font-size: 1.75rem;
            font-weight: 600;
        }
        
        .value.pass { color: var(--accent-green); }
        .value.fail { color: var(--accent-red); }
        .value.neutral { color: var(--accent-blue); }
        
        table {
            width: 100%;
            border-collapse: collapse;
            margin: 1rem 0;
            font-size: 0.8rem;
        }
        
        th, td {
            padding: 0.75rem;
            text-align: left;
            border-bottom: 1px solid var(--border-color);
        }
        
        th {
            background: var(--bg-secondary);
            color: var(--text-secondary);
            font-weight: 500;
            text-transform: uppercase;
            font-size: 0.7rem;
            letter-spacing: 0.05em;
        }
        
        tr:hover { background: var(--bg-secondary); }
        
        .strategy-name {
            color: var(--accent-orange);
            font-weight: 500;
        }
        
        .good { color: var(--accent-green); }
        .bad { color: var(--accent-red); }
        .neutral { color: var(--text-secondary); }
        
        .bar-container {
            background: var(--bg-secondary);
            border-radius: 4px;
            height: 8px;
            overflow: hidden;
        }
        
        .bar {
            height: 100%;
            border-radius: 4px;
            transition: width 0.3s;
        }
        
        .bar.green { background: var(--accent-green); }
        .bar.blue { background: var(--accent-blue); }
        .bar.orange { background: var(--accent-orange); }
        
        .recommendations {
            background: var(--bg-card);
            border-radius: 8px;
            padding: 1.25rem;
            border-left: 3px solid var(--accent-purple);
        }
        
        .recommendations li {
            padding: 0.5rem 0;
            list-style: none;
            padding-left: 1.5rem;
            position: relative;
        }
        
        .recommendations li::before {
            content: "→";
            position: absolute;
            left: 0;
            color: var(--accent-purple);
        }

        .best-badge {
            display: inline-block;
            background: var(--accent-green);
            color: var(--bg-primary);
            font-size: 0.65rem;
            padding: 0.15rem 0.4rem;
            border-radius: 3px;
            margin-left: 0.5rem;
            font-weight: 600;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>⚡ Strategy Comparison Report</h1>
        <p class="subtitle">Generated: {{.Timestamp}} | Duration: {{.Duration}}</p>
        
        <div class="summary-grid">
            <div class="summary-card">
                <h3>Total Tests</h3>
                <p class="value neutral">{{.TotalTests}}</p>
            </div>
            <div class="summary-card">
                <h3>Passed</h3>
                <p class="value pass">{{.PassedTests}}</p>
            </div>
            <div class="summary-card">
                <h3>Failed</h3>
                <p class="value fail">{{.FailedTests}}</p>
            </div>
            <div class="summary-card">
                <h3>Pass Rate</h3>
                <p class="value {{if ge .PassRate 90.0}}pass{{else if ge .PassRate 70.0}}neutral{{else}}fail{{end}}">{{printf "%.1f" .PassRate}}%</p>
            </div>
            {{if .BestStrategy}}
            <div class="summary-card">
                <h3>Best Strategy</h3>
                <p class="value" style="font-size: 1.25rem; color: var(--accent-orange);">{{.BestStrategy}}</p>
            </div>
            {{end}}
        </div>
        
        <h2>📊 Strategy Performance Comparison</h2>
        <table>
            <thead>
                <tr>
                    <th>Strategy</th>
                    <th>Tests</th>
                    <th>Pass Rate</th>
                    <th>Throughput Efficiency</th>
                    <th>Avg Drop %</th>
                    <th>Max Drop %</th>
                    <th>Avg Shutdown (ms)</th>
                </tr>
            </thead>
            <tbody>
                {{range $i, $row := .StrategyTable}}
                <tr>
                    <td><span class="strategy-name">{{$row.Strategy}}</span>{{if eq $i 0}}<span class="best-badge">BEST</span>{{end}}</td>
                    <td>{{$row.TestCount}}</td>
                    <td class="{{if ge $row.PassRate 90.0}}good{{else if ge $row.PassRate 70.0}}neutral{{else}}bad{{end}}">{{printf "%.1f" $row.PassRate}}%</td>
                    <td>
                        <div style="display: flex; align-items: center; gap: 0.5rem;">
                            <span class="{{if ge $row.ThroughputEff 95.0}}good{{else if ge $row.ThroughputEff 80.0}}neutral{{else}}bad{{end}}">{{printf "%.1f" $row.ThroughputEff}}%</span>
                            <div class="bar-container" style="width: 60px;">
                                <div class="bar {{if ge $row.ThroughputEff 95.0}}green{{else if ge $row.ThroughputEff 80.0}}blue{{else}}orange{{end}}" style="width: {{printf "%.0f" $row.ThroughputEff}}%;"></div>
                            </div>
                        </div>
                    </td>
                    <td class="{{if lt $row.AvgDropRate 1.0}}good{{else if lt $row.AvgDropRate 5.0}}neutral{{else}}bad{{end}}">{{printf "%.2f" $row.AvgDropRate}}%</td>
                    <td class="{{if lt $row.MaxDropRate 5.0}}good{{else if lt $row.MaxDropRate 10.0}}neutral{{else}}bad{{end}}">{{printf "%.2f" $row.MaxDropRate}}%</td>
                    <td>{{printf "%.1f" $row.AvgShutdownTimeMs}}</td>
                </tr>
                {{end}}
            </tbody>
        </table>
        
        {{if gt (len .GOMAXPROCSTable) 1}}
        <h2>🔄 GOMAXPROCS Impact</h2>
        <p style="color: var(--text-secondary); margin-bottom: 1rem; font-size: 0.85rem;">
            Lower GOMAXPROCS values simulate higher contention scenarios
        </p>
        <table>
            <thead>
                <tr>
                    <th>GOMAXPROCS</th>
                    <th>Tests</th>
                    <th>Pass Rate</th>
                    <th>Throughput Efficiency</th>
                    <th>Avg Drop %</th>
                </tr>
            </thead>
            <tbody>
                {{range .GOMAXPROCSTable}}
                <tr>
                    <td><strong>{{.GOMAXPROCSLabel}}</strong></td>
                    <td>{{.TestCount}}</td>
                    <td class="{{if ge .PassRate 90.0}}good{{else if ge .PassRate 70.0}}neutral{{else}}bad{{end}}">{{printf "%.1f" .PassRate}}%</td>
                    <td class="{{if ge .ThroughputEff 95.0}}good{{else if ge .ThroughputEff 80.0}}neutral{{else}}bad{{end}}">{{printf "%.1f" .ThroughputEff}}%</td>
                    <td class="{{if lt .AvgDropRate 1.0}}good{{else if lt .AvgDropRate 5.0}}neutral{{else}}bad{{end}}">{{printf "%.2f" .AvgDropRate}}%</td>
                </tr>
                {{end}}
            </tbody>
        </table>
        {{end}}
        
        {{if .Recommendations}}
        <h2>💡 Recommendations</h2>
        <div class="recommendations">
            <ul>
                {{range .Recommendations}}
                <li>{{.}}</li>
                {{end}}
            </ul>
        </div>
        {{end}}
        
    </div>
</body>
</html>`

