package agentci

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
)

type workflowJob struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	SHA        string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Steps      []struct {
		Name       string `json:"name"`
		Number     int    `json:"number"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"steps"`
}

type annotation struct {
	Level   string `json:"annotation_level"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

type failureEvidence struct {
	excerpt        string
	infrastructure bool
	unavailable    bool
}

func inspectFailure(ctx context.Context, client *fixer.GitHubClient, check Check) failureEvidence {
	if check.ID <= 0 || check.action.ID <= 0 || check.action.WorkflowID <= 0 || check.action.SHA != check.SHA {
		return failureEvidence{}
	}
	var job workflowJob
	if err := client.Request(ctx, http.MethodGet, fmt.Sprintf("/actions/jobs/%d", check.ID), nil, &job); err != nil ||
		job.ID != check.ID || job.RunID != check.action.ID || job.SHA != check.SHA || job.Status != "completed" || job.Conclusion != check.Conclusion {
		return failureEvidence{unavailable: true}
	}
	var annotations []annotation
	_ = client.Request(ctx, http.MethodGet, fmt.Sprintf("/check-runs/%d/annotations?per_page=30", check.ID), nil, &annotations)
	if beforeRepositorySteps(job) {
		for _, note := range annotations {
			if note.Level == "failure" && (note.Path == "" || note.Path == ".github") && runnerFailure(note.Title+"\n"+note.Message) {
				return failureEvidence{infrastructure: true}
			}
		}
	}
	if check.Conclusion != "failure" {
		return failureEvidence{}
	}
	var diagnostics []string
	var dependencyFailure dependencyErrors
	for _, note := range annotations {
		if note.Level == "failure" {
			diagnostics = readDiagnostics(diagnostics, &dependencyFailure, note.Title+"\n"+note.Message)
		}
	}
	var readSteps int
	var logUnavailable bool
	for _, step := range job.Steps {
		if len(diagnostics) >= maxDiagnostics {
			break
		}
		if step.Status != "completed" || step.Conclusion != "failure" {
			continue
		}
		if readSteps == 3 {
			logUnavailable = true
			break
		}
		readSteps++
		if log, err := client.ActionsStepLog(ctx, job.ID, step.Number); err == nil {
			if beforeRepositorySteps(job) && runnerFailure(log) {
				return failureEvidence{infrastructure: true}
			}
			diagnostics = readDiagnostics(diagnostics, &dependencyFailure, log)
		} else {
			logUnavailable = true
		}
	}
	if len(diagnostics) == 0 && dependencyFailure.transient && !dependencyFailure.configuration && !logUnavailable {
		return failureEvidence{infrastructure: true}
	}
	return failureEvidence{excerpt: client.RedactGitHubToken(redactProviderTokens(strings.Join(diagnostics, "\n"))), unavailable: logUnavailable}
}

func beforeRepositorySteps(job workflowJob) bool {
	for _, step := range job.Steps {
		if (step.Number != 1 || step.Name != "Set up job") && step.Conclusion != "skipped" && step.Status != "queued" {
			return false
		}
	}
	return true
}

func runnerFailure(text string) bool {
	text = strings.ToLower(text)
	for _, message := range []string{
		"the job was not acquired by runner of type hosted even after multiple attempts",
		"the hosted runner encountered an error while running your job",
		"the runner has received a shutdown signal",
		"lost communication with the server",
		"our servers are currently too busy to handle your request",
	} {
		if strings.Contains(text, message) {
			return true
		}
	}
	return false
}

const maxDiagnostics = 3

var ansiSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
var logTimestamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\S*\s+`)
var providerToken = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{12,}|gh[pousr]_[a-z0-9_]{12,}|github_pat_[a-z0-9_]+|xox[bpars]-[a-z0-9-]{12,}|(?:AKIA|ASIA)[A-Z0-9]{16})\b`)

func redactProviderTokens(value string) string {
	return providerToken.ReplaceAllString(value, "[redacted]")
}

type diagnosticPattern struct {
	pattern *regexp.Regexp
	format  string
}

const sourcePath = `[a-zA-Z0-9_.@/\\-]+`

var diagnosticPatterns = []diagnosticPattern{
	{regexp.MustCompile(`^(` + sourcePath + `\.tsx?)\((\d+),(\d+)\):\s+error (TS\d+):`), `TypeScript reported $4 at $1:$2:$3.`},
	{regexp.MustCompile(`^(` + sourcePath + `\.go):(\d+):(\d+):\s+(?:undefined:|cannot |declared and not used:|syntax error:|missing |too many |not enough )`), `Go reported a source error at $1:$2:$3.`},
	{regexp.MustCompile(`^\[ERROR\]\s+(` + sourcePath + `\.java):\[(\d+),(\d+)\]`), `The Java compiler reported a source error at $1:$2:$3.`},
	{regexp.MustCompile(`^(?:\[ERROR\]\s+)?(` + sourcePath + `\.java):(\d+)(?::\d+)?: .*\[([A-Za-z][A-Za-z0-9]+)\]$`), `Checkstyle reported $3 at $1:$2.`},
	{regexp.MustCompile(`^(` + sourcePath + `\.go):(\d+):(\d+): .*\(([a-z][a-z0-9]+)\)$`), `golangci-lint reported $4 at $1:$2:$3.`},
	{regexp.MustCompile(`^(\d+):(\d+)\s+error\s+.+\s+(@?[a-z][a-z0-9/-]+)\s*$`), `ESLint reported $3 at line $1, column $2.`},
	{regexp.MustCompile(`^--- FAIL:\s+(Test[A-Za-z0-9_]+)(?:\s+\(|/)`), `Go reported a failure in $1.`},
	{regexp.MustCompile(`^FAIL\s+(` + sourcePath + `\.(?:test|spec)\.[cm]?[jt]sx?)\s*(?:$|\()`), `Jest reported a failure in $1.`},
	{regexp.MustCompile(`^\d+\)\s+\[[a-zA-Z0-9_-]+\]\s+›\s+(` + sourcePath + `\.spec\.[jt]s):(\d+):(\d+)\s+›`), `Playwright reported a failed test at $1:$2:$3.`},
	{regexp.MustCompile(`^\[ERROR\]\s+([A-Za-z0-9_.$]+Test[A-Za-z0-9_.$]*)\s*»`), `Maven reported a failure in $1.`},
	{regexp.MustCompile(`^\[ERROR\]\s+Tests run: \d+, Failures: ([1-9]\d*), Errors: \d+`), `Maven reported $1 failed tests.`},
}

type dependencyErrors struct {
	transient     bool
	configuration bool
}

var mavenDownloadError = regexp.MustCompile(`^\[ERROR\].*Could not transfer artifact .*\(https://(?:repo\.maven\.apache\.org|repo1\.maven\.org)/[^)]*\).*status code: ([45]\d\d)\b`)
var npmDownloadError = regexp.MustCompile(`(?i)^npm (?:error|ERR!) ([45]\d\d)\s+.* - GET https://(?:registry\.npmjs\.org|npm\.pkg\.github\.com)/`)

func readDiagnostics(diagnostics []string, dependency *dependencyErrors, output string) []string {
	for line := range strings.SplitSeq(output, "\n") {
		if len(diagnostics) >= maxDiagnostics {
			break
		}
		line = ansiSequence.ReplaceAllString(line, "")
		line = logTimestamp.ReplaceAllString(line, "")
		line = strings.TrimSpace(strings.TrimPrefix(line, "##[error]"))
		for _, downloadError := range []*regexp.Regexp{mavenDownloadError, npmDownloadError} {
			if match := downloadError.FindStringSubmatch(line); match != nil {
				switch match[1] {
				case "502", "503", "504":
					dependency.transient = true
				default:
					dependency.configuration = true
				}
			}
		}
		for _, diagnostic := range diagnosticPatterns {
			match := diagnostic.pattern.FindStringSubmatchIndex(line)
			if match == nil {
				continue
			}
			value := string(diagnostic.pattern.ExpandString(nil, diagnostic.format, line, match))
			if len(value) <= 200 && !slices.Contains(diagnostics, value) {
				diagnostics = append(diagnostics, value)
			}
			break
		}
	}
	return diagnostics
}
