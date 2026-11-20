package agentci

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/autorequest"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
)

type RepairPlan struct {
	Ready     bool
	Failures  []autorequest.Failure
	RetryRuns []int64
	Human     []string
}

func PlanRepair(ctx context.Context, client *fixer.GitHubClient, snapshot Snapshot) (RepairPlan, error) {
	var plan RepairPlan
	if len(snapshot.Diagnostics) != 0 || len(snapshot.Checks) == 0 {
		return plan, nil
	}
	sha := snapshot.HeadSHA
	if snapshot.TestMergeSHA != "" && slices.ContainsFunc(snapshot.Checks, func(check Check) bool { return check.SHA == snapshot.TestMergeSHA }) {
		sha = snapshot.TestMergeSHA
	}
	hasRequired := slices.ContainsFunc(snapshot.Checks, func(check Check) bool { return check.SHA == sha && check.Required })
	var selected []Check
	for _, check := range snapshot.Checks {
		if check.SHA != sha {
			continue
		}
		if !terminalCheck(check.Status) || check.Workflow != "" && check.action.Status != "completed" {
			if check.Required || !hasRequired {
				return plan, nil
			}
			continue
		}
		selected = append(selected, check)
	}
	if len(selected) == 0 {
		return plan, nil
	}
	plan.Ready = true

	seen := make(map[string]bool)
	retry := make(map[int64]string)
	blocked := make(map[int64]bool)
	for _, check := range selected {
		if check.Status != "failed" && check.Status != "cancelled" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return RepairPlan{}, err
		}
		name := repairName(client.RedactGitHubToken(check.Name))
		if check.Workflow != "" && check.Workflow != check.Name {
			name = repairName(client.RedactGitHubToken(check.Workflow)) + " / " + name
		}
		evidence := inspectFailure(ctx, client, check)
		if err := ctx.Err(); err != nil {
			return RepairPlan{}, err
		}
		switch {
		case evidence.infrastructure && retryableRun(check.action):
			retry[check.action.ID] = name
		case evidence.infrastructure:
			plan.Human = append(plan.Human, name+" needs a runner or service investigation.")
			blocked[check.action.ID] = true
		case evidence.excerpt != "" && check.Conclusion == "failure":
			key := failureKey(check, evidence.excerpt)
			if !seen[key] {
				seen[key] = true
				plan.Failures = append(plan.Failures, autorequest.Failure{Key: key, Name: name,
					URL: client.ActionsJobURL(check.action.ID, check.ID), Excerpt: evidence.excerpt})
			}
			blocked[check.action.ID] = true
		case evidence.unavailable:
			plan.Human = append(plan.Human, "The failure evidence for "+name+" could not be read.")
			blocked[check.action.ID] = true
		default:
			plan.Human = append(plan.Human, name+" needs a manual diagnosis.")
			blocked[check.action.ID] = true
		}
	}
	for run, name := range retry {
		if blocked[run] {
			plan.Human = append(plan.Human, name+" shares a run with other jobs that need a diagnosis or fix.")
		} else {
			plan.RetryRuns = append(plan.RetryRuns, run)
		}
	}
	slices.Sort(plan.RetryRuns)
	slices.Sort(plan.Human)
	plan.Human = slices.Compact(plan.Human)
	return plan, nil
}

func terminalCheck(status string) bool {
	switch status {
	case "passed", "skipped", "neutral", "failed", "cancelled":
		return true
	default:
		return false
	}
}

var diagnosticLocation = regexp.MustCompile(`(?::\d+(?::\d+)?| at line \d+, column \d+)\.$`)
var testFailureCount = regexp.MustCompile(`Maven reported \d+ failed tests\.`)

func failureKey(check Check, excerpt string) string {
	signatures := strings.Split(excerpt, "\n")
	for i, signature := range signatures {
		signature = diagnosticLocation.ReplaceAllString(signature, ".")
		signatures[i] = testFailureCount.ReplaceAllString(signature, "Maven reported failed tests.")
	}
	slices.Sort(signatures)
	identity := fmt.Sprintf("%d\x00%d\x00%s\x00%s", check.AppID, check.action.WorkflowID, check.Name, strings.Join(slices.Compact(signatures), "\n"))
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}

func retryableRun(run actionRun) bool {
	return run.Attempt == 1 && (run.Event == "push" || run.Event == "pull_request") && run.Conclusion == "failure"
}

func RerunFailedJobs(ctx context.Context, client *fixer.GitHubClient, runID int64) error {
	return client.RerunFailedActionsJobs(ctx, runID)
}

func repairName(name string) string {
	name = strings.Join(strings.Fields(redactProviderTokens(name)), " ")
	if name == "" {
		return "Unnamed check"
	}
	runes := []rune(name)
	if len(runes) > 160 {
		return string(runes[:160]) + "…"
	}
	return name
}
