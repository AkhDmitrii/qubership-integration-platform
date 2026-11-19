package docgithub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/docpacket"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
)

type scheduleAPI struct {
	publicationAPI
	activeRuns                 []fixer.WorkflowRun
	attempts                   map[string]fixer.WorkflowRun
	jobs                       map[string][]any
	enabled, apply, openDocsPR bool
}

func scheduleFixture(t *testing.T) (*Client, *scheduleAPI, Plan) {
	t.Helper()
	base := strings.Repeat("a", 40)
	a := &scheduleAPI{
		publicationAPI: publicationAPI{
			viewerLogin: "agent-bot", plan: Plan{Repository: "org/repo", IssueTitle: "Document the change", BaseSHA: base},
			state: "closed", project: "REVIEW", head: base,
		},
		attempts: map[string]fixer.WorkflowRun{}, jobs: map[string][]any{}, enabled: true, apply: true,
	}
	server := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(server.Close)
	api, err := fixer.NewGitHubClient(a.plan.Repository, "test", server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{API: api, Repository: a.plan.Repository}
	prepared, err := Prepare(t.Context(), c, 1, 12)
	if err != nil {
		t.Fatal(err)
	}
	plan := prepared.Plan
	plan.PacketSHA256 = strings.Repeat("b", 64)
	plan.ID = ""
	plan.ID = Digest(plan)
	return c, a, plan
}

func (a *scheduleAPI) serve(w http.ResponseWriter, r *http.Request) {
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	graph := func(v any) { write(map[string]any{"data": v}) }
	if r.URL.Path == "/graphql" {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var query struct {
			Query     string
			Variables map[string]any
		}
		_ = json.Unmarshal(body, &query)
		if strings.Contains(query.Query, "items(first:100,after:") {
			item := func(number int, repo, option string, archived bool) any {
				return map[string]any{
					"isArchived":       archived,
					"fieldValueByName": map[string]any{"optionId": option, "field": map[string]string{"id": "FIELD"}},
					"content":          map[string]any{"__typename": "Issue", "number": number, "repository": map[string]string{"nameWithOwner": repo}},
				}
			}
			items := []any{item(2, "org/repo", "DONE", false), item(9, "other/repo", "REVIEW", false)}
			page := map[string]any{"hasNextPage": true, "endCursor": "NEXT"}
			if query.Variables["cursor"] == "NEXT" {
				items = []any{item(1, "org/repo", "REVIEW", false), item(3, "org/repo", "REVIEW", true)}
				page = map[string]any{"hasNextPage": false}
			}
			project := map[string]any{
				"id":    "PROJECT",
				"field": map[string]any{"id": "FIELD", "options": []any{map[string]string{"name": "In doc review", "id": "REVIEW"}}},
				"items": map[string]any{"nodes": items, "pageInfo": page},
			}
			graph(map[string]any{"repository": map[string]any{"owner": map[string]any{"projectV2": project}}})
			return
		}
		if a.openDocsPR && strings.Contains(query.Query, "timelineItems") {
			implementation := map[string]any{"number": 2, "merged": true, "baseRefName": "main", "repository": map[string]string{"nameWithOwner": "org/repo"}, "mergeCommit": map[string]string{"oid": a.plan.BaseSHA}}
			existing := map[string]any{"number": 3, "state": "OPEN", "baseRefName": "main", "repository": map[string]string{"nameWithOwner": "org/repo"}}
			timeline := map[string]any{"nodes": []any{map[string]any{"closer": implementation}, map[string]any{"source": existing}}}
			graph(map[string]any{"repository": map[string]any{"issue": map[string]any{"timelineItems": timeline}}})
			return
		}
	}
	switch path := strings.TrimPrefix(r.URL.Path, "/repos/org/repo"); {
	case path == "":
		write(map[string]string{"default_branch": "main"})
	case path == "/users/agent-bot":
		write(fixer.GitHubUser{Login: "agent-bot", ID: 99})
	case path == "/actions/variables/AGENT_DOCS_SCHEDULE_ENABLED":
		write(map[string]string{"value": strconv.FormatBool(a.enabled)})
	case path == "/actions/variables/AGENT_DOCS_SCHEDULE_APPLY":
		write(map[string]string{"value": strconv.FormatBool(a.apply)})
	case path == "/actions/workflows/agent-docs.yaml/runs":
		runs := a.activeRuns
		if runs == nil {
			runs = []fixer.WorkflowRun{}
		}
		write(map[string]any{"workflow_runs": runs})
	case strings.HasSuffix(path, "/jobs"):
		jobs := a.jobs[strings.TrimSuffix(path, "/jobs")]
		if jobs == nil {
			jobs = []any{}
		}
		write(map[string]any{"jobs": jobs})
	case strings.HasPrefix(path, "/actions/runs/"):
		run, ok := a.attempts[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		write(run)
	case path == "/pulls/3/files":
		write([]map[string]string{{"filename": "help/docs/topic.md"}})
	default:
		a.publicationAPI.serve(w, r)
	}
}

func TestScheduleDiscoveryReportsEligibleAndSkippedWithoutWriting(t *testing.T) {
	c, a, plan := scheduleFixture(t)
	for _, tc := range []struct {
		name   string
		active []fixer.WorkflowRun
		docsPR bool
		reason string
	}{
		{name: "eligible"},
		{name: "active manual work", active: []fixer.WorkflowRun{{Status: "queued", DisplayTitle: "qip-docs|1|run"}}, reason: "active-docs-run"},
		{name: "linked documentation PR", docsPR: true, reason: "existing-docs-pr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.activeRuns, a.openDocsPR = tc.active, tc.docsPR
			report, err := Discover(t.Context(), c, c.API, 12)
			if err != nil || a.commentCreates != 0 {
				t.Fatal(report, err, a.commentCreates)
			}
			wantSkipped := []ScheduleSkip{{Reason: "outside-repository"}, {Issue: 2, Reason: "outside-doc-review"}, {Issue: 3, Reason: "archived"}}
			wantEligible := []ScheduleCandidate{}
			if tc.reason == "" {
				wantEligible = append(wantEligible, ScheduleCandidate{Issue: 1, Basis: scheduleBasis(plan)})
			} else {
				wantSkipped = slices.Insert(wantSkipped, 1, ScheduleSkip{Issue: 1, Reason: tc.reason})
			}
			if !reflect.DeepEqual(report, ScheduleReport{Eligible: wantEligible, Skipped: wantSkipped}) {
				t.Fatalf("unexpected discovery result: %+v", report)
			}
		})
	}
}

func TestScheduledClaimDeduplicatesAndRecoversBeforeModelWork(t *testing.T) {
	c, a, plan := scheduleFixture(t)
	ctx := context.Background()
	basis := scheduleBasis(plan)
	differentBase := plan
	differentBase.BaseSHA = strings.Repeat("c", 40)
	if scheduleBasis(differentBase) != basis {
		t.Fatal("an unrelated default branch change replaced the schedule basis")
	}
	differentBase.ImplementingPRs = []docpacket.PR{{Number: 8, MergeSHA: strings.Repeat("d", 40), Target: "main"}}
	if scheduleBasis(differentBase) == basis {
		t.Fatal("changed implementation retained the previous schedule basis")
	}
	claim := func(run int64, applied bool, reason string, commentCount int) {
		t.Helper()
		claimed, got, err := ClaimSchedule(ctx, c, c.API, plan, basis, "alice", ScheduledAttempt{RunID: run, Attempt: 1})
		if err != nil || claimed != applied || got != reason || a.commentCreates != commentCount {
			t.Fatalf("claim %d = %v/%s/%v, comments %d", run, claimed, got, err, a.commentCreates)
		}
	}
	a.apply = false
	claim(500, false, "schedule-apply-disabled", 0)
	a.apply = true
	a.uncertainComment = true
	claim(500, true, "", 1)
	claim(500, true, "", 1)
	const firstPath = "/actions/runs/500/attempts/1"
	first := fixer.WorkflowRun{ID: 500, RunAttempt: 1, HeadRepository: fixer.Repository{FullName: "org/repo"}, HeadBranch: "main", Event: "schedule", Path: scheduleWorkflow, Status: "in_progress"}
	a.attempts[firstPath] = first
	claim(501, false, "active-docs-run", 1)
	first.Status = "completed"
	a.attempts[firstPath] = first
	job := func(outcome string) any {
		return map[string]any{"name": "Process documentation issue #1 / Work on documentation issue #1", "steps": []any{
			map[string]string{"name": "Run documentation work", "status": "completed", "conclusion": outcome},
		}}
	}
	a.jobs[firstPath] = []any{job("failure")}
	claim(501, false, "basis-already-attempted", 1)
	a.jobs[firstPath] = []any{job("skipped")}
	claim(501, true, "", 2)
}

func TestScheduledClaimRevalidatesIssueBeforeWriting(t *testing.T) {
	c, a, plan := scheduleFixture(t)
	a.project = "DONE"
	claimed, reason, err := ClaimSchedule(t.Context(), c, c.API, plan, scheduleBasis(plan), "alice", ScheduledAttempt{RunID: 500, Attempt: 1})
	if err != nil || claimed || reason != "outside-doc-review" || a.commentCreates != 0 {
		t.Fatal(claimed, reason, err, a.commentCreates)
	}
}
