package agentci

import (
	"net/http"
	"slices"
	"testing"
)

func TestRepairKeepsDistinctESLintFilesAndSites(t *testing.T) {
	check := failedAction(testHead, 42, 43, 44, 1)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/actions/jobs/42":
			fixture(w, workflowJob{ID: 42, RunID: 43, SHA: testHead, Status: "completed", Conclusion: "failure"})
		case "/repos/org/repo/check-runs/42/annotations":
			fixture(w, []annotation{
				{Level: "failure", Path: "ui/src/Nav.ts", Message: "15:3  error  untrusted text  @typescript-eslint/no-unused-vars"},
				{Level: "failure", Message: `/home/runner/work/repo/repo/ui/src/Nav.ts
  15:3  error  untrusted text  @typescript-eslint/no-unused-vars
  23:3  error  untrusted text  @typescript-eslint/no-unused-vars

/home/runner/work/repo/repo/ui/src/Report.ts
  15:3  error  untrusted text  @typescript-eslint/no-unused-vars`},
			})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	})
	plan, err := PlanRepair(t.Context(), client, Snapshot{HeadSHA: testHead, Checks: []Check{check}})
	if err != nil || !plan.Ready {
		t.Fatalf("repair plan = %+v, %v", plan, err)
	}
	var excerpts []string
	for _, failure := range plan.Failures {
		excerpts = append(excerpts, failure.Excerpt)
	}
	want := []string{
		"ESLint reported @typescript-eslint/no-unused-vars at ui/src/Nav.ts:15:3.",
		"ESLint reported @typescript-eslint/no-unused-vars at ui/src/Nav.ts:23:3.",
		"ESLint reported @typescript-eslint/no-unused-vars at ui/src/Report.ts:15:3.",
	}
	if !slices.Equal(excerpts, want) {
		t.Fatalf("diagnostics = %q, want %q", excerpts, want)
	}
}

func TestRepairIdentifiesGoSubtestsAndIgnoresMavenTotals(t *testing.T) {
	var dependency dependencyErrors
	got := readDiagnostics(nil, &dependency, `[ERROR] Tests run: 12, Failures: 2, Errors: 0
--- FAIL: TestReport/request_a (0.03s)
--- FAIL: TestReport/request_b (0.01s)`, "")
	want := []string{"Go reported a failure in TestReport/request_a.", "Go reported a failure in TestReport/request_b."}
	if !slices.Equal(got, want) {
		t.Fatalf("diagnostics = %q, want %q", got, want)
	}
}
