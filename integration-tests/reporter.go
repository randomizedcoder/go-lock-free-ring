package integration_tests

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReportData contains all data needed to generate the HTML report
type ReportData struct {
	// Metadata
	Timestamp   string
	Duration    string
	GeneratedAt time.Time

	// Test summary
	TotalTests   int
	PassedTests  int
	FailedTests  int
	SkippedTests int
	PassRate     float64

	// Profile summary
	TotalProfiles int
	ProfileTypes  []string

	// Detailed results
	Results        []TestResultView
	FailedResults  []TestResultView
	ProfileResults []ProfileResultView

	// Recommendations
	Recommendations []string
}

// TestResultView is a view model for test results in the HTML report
type TestResultView struct {
	TestID        string
	TestName      string
	Config        string
	ExpectedRate  string
	AchievedRate  string
	RateDeviation string
	DropRate      string
	ShutdownTime  string
	Passed        bool
	ErrorMessage  string
	LogPreview    string // First N lines of log
}

// ProfileResultView is a view model for profile results in the HTML report
type ProfileResultView struct {
	TestID       string
	ProfileType  string
	TotalSamples string
	TopEntries   []ProfileEntryView
	Error        string
}

// ProfileEntryView is a view model for a single profile entry
type ProfileEntryView struct {
	Rank     int
	Function string
	Flat     string
	FlatPct  string
	Cum      string
	CumPct   string
}

// HTMLReporter generates HTML reports for integration tests
type HTMLReporter struct {
	OutputDir string
	Template  *template.Template
}

// NewHTMLReporter creates a new HTML reporter
func NewHTMLReporter(outputDir string) (*HTMLReporter, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output dir: %w", err)
	}

	tmpl, err := template.New("report").Parse(reportTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	return &HTMLReporter{
		OutputDir: outputDir,
		Template:  tmpl,
	}, nil
}

// GenerateReport generates an HTML report from test and profile results
func (hr *HTMLReporter) GenerateReport(suiteResult *TestSuiteResult, profileAnalyses []ProfileAnalysisResult) (string, error) {
	data := hr.buildReportData(suiteResult, profileAnalyses)

	// Generate filename with timestamp
	filename := fmt.Sprintf("report-%s.html", time.Now().Format("20060102-150405"))
	reportPath := filepath.Join(hr.OutputDir, filename)

	f, err := os.Create(reportPath)
	if err != nil {
		return "", fmt.Errorf("failed to create report file: %w", err)
	}
	defer f.Close()

	if err := hr.Template.Execute(f, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	// Also create a "latest" symlink
	latestPath := filepath.Join(hr.OutputDir, "report-latest.html")
	os.Remove(latestPath) // Ignore error if doesn't exist
	os.Symlink(filename, latestPath)

	return reportPath, nil
}

// buildReportData converts test results into the report view model
func (hr *HTMLReporter) buildReportData(suiteResult *TestSuiteResult, profileAnalyses []ProfileAnalysisResult) ReportData {
	data := ReportData{
		Timestamp:   time.Now().Format("2006-01-02 15:04:05"),
		GeneratedAt: time.Now(),
	}

	if suiteResult != nil {
		data.Duration = suiteResult.Duration.Round(time.Second).String()
		data.TotalTests = suiteResult.TotalTests
		data.PassedTests = suiteResult.PassedTests
		data.FailedTests = suiteResult.FailedTests
		data.SkippedTests = suiteResult.SkippedTests
		data.PassRate = suiteResult.PassRate()

		// Convert test results to view models
		for _, r := range suiteResult.Results {
			view := hr.convertTestResult(r)
			data.Results = append(data.Results, view)
			if !r.Passed {
				data.FailedResults = append(data.FailedResults, view)
			}
		}
	}

	// Convert profile analyses to view models
	profileTypes := make(map[string]bool)
	for _, pa := range profileAnalyses {
		view := hr.convertProfileAnalysis(pa)
		data.ProfileResults = append(data.ProfileResults, view)
		profileTypes[string(pa.ProfileType)] = true
		data.TotalProfiles++
	}

	for pt := range profileTypes {
		data.ProfileTypes = append(data.ProfileTypes, pt)
	}

	// Generate recommendations
	data.Recommendations = GenerateRecommendations(profileAnalyses)

	return data
}

// convertTestResult converts a TestResult to a view model
func (hr *HTMLReporter) convertTestResult(r TestResult) TestResultView {
	view := TestResultView{
		TestID:   r.TestCase.ID,
		TestName: r.TestCase.Name,
		Config:   r.TestCase.ConfigString(),
		Passed:   r.Passed,
	}

	if r.Validation != nil {
		view.ExpectedRate = fmt.Sprintf("%.0f Mb/s", r.Validation.ExpectedRate)
		view.AchievedRate = fmt.Sprintf("%.2f Mb/s", r.Validation.AchievedRate)
		view.RateDeviation = fmt.Sprintf("%+.2f%%", r.Validation.RateDeviation)
	}

	if r.Metrics != nil {
		view.DropRate = fmt.Sprintf("%.2f%%", r.Metrics.DropRate)
		view.ShutdownTime = r.Metrics.ShutdownDuration.Round(time.Millisecond).String()
	}

	if r.Error != nil {
		view.ErrorMessage = r.Error.Error()
	}

	if r.Execution != nil && r.Execution.Stderr != "" {
		// Get first 500 chars of log for preview
		log := r.Execution.Stderr
		if len(log) > 500 {
			log = log[:500] + "..."
		}
		view.LogPreview = log
	}

	return view
}

// convertProfileAnalysis converts a ProfileAnalysisResult to a view model
func (hr *HTMLReporter) convertProfileAnalysis(pa ProfileAnalysisResult) ProfileResultView {
	view := ProfileResultView{
		TestID:       pa.TestID,
		ProfileType:  string(pa.ProfileType),
		TotalSamples: pa.TotalSamples,
	}

	if pa.Error != nil {
		view.Error = pa.Error.Error()
	}

	for _, e := range pa.TopEntries {
		ev := ProfileEntryView{
			Rank:     e.Rank,
			Function: e.Function,
			Flat:     e.Flat,
			FlatPct:  fmt.Sprintf("%.1f%%", e.FlatPct),
			Cum:      e.Cum,
			CumPct:   fmt.Sprintf("%.1f%%", e.CumPct),
		}
		view.TopEntries = append(view.TopEntries, ev)
	}

	return view
}

// GenerateSimpleReport generates a simpler text report
func GenerateSimpleReport(suiteResult *TestSuiteResult, profileAnalyses []ProfileAnalysisResult) string {
	var sb strings.Builder

	sb.WriteString("=" + strings.Repeat("=", 79) + "\n")
	sb.WriteString("Integration Test Report\n")
	sb.WriteString("Generated: " + time.Now().Format("2006-01-02 15:04:05") + "\n")
	sb.WriteString("=" + strings.Repeat("=", 79) + "\n\n")

	if suiteResult != nil {
		sb.WriteString("== Test Summary ==\n")
		sb.WriteString(fmt.Sprintf("Total:   %d\n", suiteResult.TotalTests))
		sb.WriteString(fmt.Sprintf("Passed:  %d\n", suiteResult.PassedTests))
		sb.WriteString(fmt.Sprintf("Failed:  %d\n", suiteResult.FailedTests))
		sb.WriteString(fmt.Sprintf("Pass Rate: %.1f%%\n", suiteResult.PassRate()))
		sb.WriteString(fmt.Sprintf("Duration: %v\n\n", suiteResult.Duration.Round(time.Second)))

		// List failed tests
		if len(suiteResult.FailedResults()) > 0 {
			sb.WriteString("== Failed Tests ==\n")
			for _, r := range suiteResult.FailedResults() {
				sb.WriteString(fmt.Sprintf("- %s: %v\n", r.TestCase.ID, r.Error))
			}
			sb.WriteString("\n")
		}
	}

	// Profile analysis
	if len(profileAnalyses) > 0 {
		sb.WriteString("== Profile Analysis ==\n")
		for _, pa := range profileAnalyses {
			sb.WriteString(fmt.Sprintf("\n%s - %s:\n", pa.TestID, pa.ProfileType))
			if pa.Error != nil {
				sb.WriteString(fmt.Sprintf("  Error: %v\n", pa.Error))
				continue
			}
			sb.WriteString(fmt.Sprintf("  Total: %s\n", pa.TotalSamples))
			for _, e := range pa.TopEntries {
				sb.WriteString(fmt.Sprintf("  %d. %s: %s (%.1f%%)\n",
					e.Rank, e.Function, e.Flat, e.FlatPct))
			}
		}
		sb.WriteString("\n")
	}

	// Recommendations
	recs := GenerateRecommendations(profileAnalyses)
	if len(recs) > 0 {
		sb.WriteString("== Recommendations ==\n")
		for i, rec := range recs {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, rec))
		}
	}

	return sb.String()
}

// HTML template for the report
const reportTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Integration Test Report - {{.Timestamp}}</title>
    <style>
        :root {
            --bg-primary: #0f172a;
            --bg-secondary: #1e293b;
            --bg-card: #334155;
            --text-primary: #f8fafc;
            --text-secondary: #94a3b8;
            --accent-green: #22c55e;
            --accent-red: #ef4444;
            --accent-blue: #3b82f6;
            --accent-purple: #a855f7;
            --accent-orange: #f97316;
            --border-color: #475569;
        }
        
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            font-family: 'JetBrains Mono', 'Fira Code', 'SF Mono', Consolas, monospace;
            background: var(--bg-primary);
            color: var(--text-primary);
            line-height: 1.6;
            padding: 2rem;
            min-height: 100vh;
        }
        
        .container {
            max-width: 1400px;
            margin: 0 auto;
        }
        
        h1 {
            font-size: 2rem;
            font-weight: 600;
            margin-bottom: 0.5rem;
            color: var(--text-primary);
            display: flex;
            align-items: center;
            gap: 0.75rem;
        }
        
        h2 {
            font-size: 1.25rem;
            font-weight: 500;
            color: var(--text-primary);
            margin: 2rem 0 1rem;
            padding-bottom: 0.5rem;
            border-bottom: 2px solid var(--border-color);
            display: flex;
            align-items: center;
            gap: 0.5rem;
        }
        
        h3 {
            font-size: 1rem;
            color: var(--text-secondary);
            margin-bottom: 0.75rem;
        }
        
        .timestamp {
            color: var(--text-secondary);
            font-size: 0.875rem;
            margin-bottom: 2rem;
        }
        
        .summary-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
            gap: 1rem;
            margin-bottom: 2rem;
        }
        
        .summary-card {
            background: var(--bg-secondary);
            border-radius: 8px;
            padding: 1.25rem;
            border: 1px solid var(--border-color);
        }
        
        .summary-card h3 {
            font-size: 0.75rem;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            color: var(--text-secondary);
            margin-bottom: 0.5rem;
        }
        
        .summary-card .value {
            font-size: 2rem;
            font-weight: 600;
        }
        
        .summary-card .value.pass { color: var(--accent-green); }
        .summary-card .value.fail { color: var(--accent-red); }
        .summary-card .value.neutral { color: var(--accent-blue); }
        
        table {
            width: 100%;
            border-collapse: collapse;
            margin: 1rem 0;
            font-size: 0.875rem;
        }
        
        th, td {
            padding: 0.75rem 1rem;
            text-align: left;
            border-bottom: 1px solid var(--border-color);
        }
        
        th {
            background: var(--bg-secondary);
            color: var(--text-secondary);
            font-weight: 500;
            text-transform: uppercase;
            font-size: 0.75rem;
            letter-spacing: 0.05em;
        }
        
        tr:hover {
            background: var(--bg-secondary);
        }
        
        .status {
            display: inline-flex;
            align-items: center;
            gap: 0.25rem;
            padding: 0.25rem 0.5rem;
            border-radius: 4px;
            font-size: 0.75rem;
            font-weight: 500;
        }
        
        .status.pass {
            background: rgba(34, 197, 94, 0.2);
            color: var(--accent-green);
        }
        
        .status.fail {
            background: rgba(239, 68, 68, 0.2);
            color: var(--accent-red);
        }
        
        .deviation {
            font-family: inherit;
        }
        
        .deviation.positive { color: var(--accent-red); }
        .deviation.negative { color: var(--accent-green); }
        
        .profile-section {
            background: var(--bg-secondary);
            border-radius: 8px;
            padding: 1.25rem;
            margin: 1rem 0;
            border: 1px solid var(--border-color);
        }
        
        .profile-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 1rem;
        }
        
        .profile-type {
            display: inline-block;
            padding: 0.25rem 0.5rem;
            background: var(--accent-purple);
            border-radius: 4px;
            font-size: 0.75rem;
            font-weight: 500;
        }
        
        .function-name {
            font-family: inherit;
            color: var(--accent-orange);
        }
        
        .recommendations {
            background: var(--bg-secondary);
            border-radius: 8px;
            padding: 1.25rem;
            border-left: 4px solid var(--accent-blue);
        }
        
        .recommendations ul {
            list-style: none;
            padding: 0;
        }
        
        .recommendations li {
            padding: 0.5rem 0;
            padding-left: 1.5rem;
            position: relative;
        }
        
        .recommendations li::before {
            content: "→";
            position: absolute;
            left: 0;
            color: var(--accent-blue);
        }
        
        .error-block {
            background: rgba(239, 68, 68, 0.1);
            border: 1px solid var(--accent-red);
            border-radius: 8px;
            padding: 1rem;
            margin: 1rem 0;
        }
        
        .error-block h4 {
            color: var(--accent-red);
            margin-bottom: 0.5rem;
        }
        
        pre {
            background: var(--bg-primary);
            border-radius: 4px;
            padding: 1rem;
            overflow-x: auto;
            font-size: 0.8rem;
            line-height: 1.4;
            border: 1px solid var(--border-color);
        }
        
        .no-data {
            color: var(--text-secondary);
            font-style: italic;
            text-align: center;
            padding: 2rem;
        }
        
        @media (max-width: 768px) {
            body {
                padding: 1rem;
            }
            
            table {
                font-size: 0.75rem;
            }
            
            th, td {
                padding: 0.5rem;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🔬 Integration Test Report</h1>
        <p class="timestamp">Generated: {{.Timestamp}} | Duration: {{.Duration}}</p>
        
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
            <div class="summary-card">
                <h3>Profiles</h3>
                <p class="value neutral">{{.TotalProfiles}}</p>
            </div>
        </div>
        
        <h2>📊 Test Results</h2>
        {{if .Results}}
        <table>
            <thead>
                <tr>
                    <th>Test</th>
                    <th>Configuration</th>
                    <th>Expected</th>
                    <th>Achieved</th>
                    <th>Deviation</th>
                    <th>Drop %</th>
                    <th>Shutdown</th>
                    <th>Status</th>
                </tr>
            </thead>
            <tbody>
                {{range .Results}}
                <tr>
                    <td>{{.TestID}}</td>
                    <td>{{.Config}}</td>
                    <td>{{.ExpectedRate}}</td>
                    <td>{{.AchievedRate}}</td>
                    <td class="deviation {{if gt (len .RateDeviation) 0}}{{if eq (index .RateDeviation 0) 43}}positive{{else}}negative{{end}}{{end}}">{{.RateDeviation}}</td>
                    <td>{{.DropRate}}</td>
                    <td>{{.ShutdownTime}}</td>
                    <td><span class="status {{if .Passed}}pass{{else}}fail{{end}}">{{if .Passed}}✓ PASS{{else}}✗ FAIL{{end}}</span></td>
                </tr>
                {{end}}
            </tbody>
        </table>
        {{else}}
        <p class="no-data">No test results available</p>
        {{end}}
        
        {{if .ProfileResults}}
        <h2>🔍 Profile Analysis</h2>
        {{range .ProfileResults}}
        <div class="profile-section">
            <div class="profile-header">
                <h3>{{.TestID}} <span class="profile-type">{{.ProfileType}}</span></h3>
                <span>Total: {{.TotalSamples}}</span>
            </div>
            {{if .Error}}
            <div class="error-block">
                <p>{{.Error}}</p>
            </div>
            {{else if .TopEntries}}
            <table>
                <thead>
                    <tr>
                        <th>#</th>
                        <th>Function</th>
                        <th>Flat</th>
                        <th>Flat %</th>
                        <th>Cum</th>
                        <th>Cum %</th>
                    </tr>
                </thead>
                <tbody>
                    {{range .TopEntries}}
                    <tr>
                        <td>{{.Rank}}</td>
                        <td><span class="function-name">{{.Function}}</span></td>
                        <td>{{.Flat}}</td>
                        <td>{{.FlatPct}}</td>
                        <td>{{.Cum}}</td>
                        <td>{{.CumPct}}</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
            {{else}}
            <p class="no-data">No profile data</p>
            {{end}}
        </div>
        {{end}}
        {{end}}
        
        {{if .FailedResults}}
        <h2>❌ Failed Test Details</h2>
        {{range .FailedResults}}
        <div class="error-block">
            <h4>{{.TestID}} - {{.TestName}}</h4>
            <p><strong>Error:</strong> {{.ErrorMessage}}</p>
            {{if .LogPreview}}
            <pre>{{.LogPreview}}</pre>
            {{end}}
        </div>
        {{end}}
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

