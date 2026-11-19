package agentci

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
)

var (
	testHead      = strings.Repeat("a", 40)
	testBase      = strings.Repeat("b", 40)
	testMerge     = strings.Repeat("c", 40)
	testPriorHead = strings.Repeat("d", 40)
)

func testClient(t *testing.T, handler http.HandlerFunc) *fixer.GitHubClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := fixer.NewGitHubClient("org/repo", "test-token", server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func fixture(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func rawCheck(name, sha, status, conclusion, slug string, app, suite int) map[string]any {
	return map[string]any{"name": name, "head_sha": sha, "status": status, "conclusion": conclusion,
		"app": map[string]any{"id": app, "slug": slug}, "check_suite": map[string]int{"id": suite}}
}

func TestSnapshotReadsExactHeadAndChecksAllReportedCategories(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/commits/" + testHead + "/check-runs":
			if r.URL.Query().Get("filter") != "latest" {
				t.Error("check run query includes stale attempts")
			}
			fixture(w, map[string]any{"check_runs": []any{
				rawCheck("build", testHead, "completed", "success", "github-actions", 15368, 1),
				rawCheck("build", testHead, "completed", "failure", "github-actions", 15368, 2),
				rawCheck("deploy", testHead, "in_progress", "", "github-actions", 15368, 1),
				rawCheck("integration", testHead, "completed", "cancelled", "github-actions", 15368, 1),
				rawCheck("manual build", testHead, "completed", "success", "github-actions", 15368, 3),
				rawCheck("observe", testHead, "in_progress", "", "github-actions", 15368, 4),
				rawCheck("measure outcome", testHead, "in_progress", "", "github-actions", 15368, 5),
				rawCheck("format", testHead, "completed", "skipped", "github-actions", 15368, 1),
			}})
		case "/repos/org/repo/actions/runs":
			fixture(w, map[string]any{"workflow_runs": []actionRun{
				{Name: "UI CI", SHA: testHead, Event: "pull_request", Suite: 1},
				{Name: "Engine CI", SHA: testHead, Event: "pull_request", Suite: 2},
				{Name: "Manual", SHA: testHead, Event: "workflow_dispatch", Suite: 3},
				{Name: ObserverWorkflow, SHA: testHead, Event: "pull_request_target", Suite: 4},
				{Name: "QIP agent PR outcome", SHA: testHead, Event: "pull_request_target", Suite: 5},
			}})
		case "/repos/org/repo/commits/" + testHead + "/statuses":
			fixture(w, []map[string]string{{"context": "analysis", "state": "pending"}, {"context": "analysis", "state": "success"}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	})
	snapshot, err := Read(t.Context(), client, testHead, "", []Requirement{{Name: "build", AppID: 15368}, {Name: "manual build"}, {Name: "expected check", AppID: 10}})
	if err != nil || snapshot.Status != "failed" || len(snapshot.Diagnostics) > 0 {
		t.Fatalf("observation = %+v, %v", snapshot, err)
	}
	var statuses []string
	for _, check := range snapshot.Checks {
		statuses = append(statuses, fmt.Sprintf("%s/%s/%s/%v", check.Name, check.Workflow, check.Status, check.Required))
		if check.SHA != testHead {
			t.Fatal("check is unbound to the current head", check)
		}
	}
	want := []string{
		"analysis//pending/false", "build/Engine CI/failed/true", "build/UI CI/passed/true", "deploy/UI CI/pending/false",
		"expected check//missing/true", "format/UI CI/skipped/false", "integration/UI CI/cancelled/false", "manual build//missing/true",
	}
	if !slices.Equal(statuses, want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
}

func TestSnapshotUsesVerifiedTestMergeAndRejectsStaleTestMerge(t *testing.T) {
	for _, parent := range []string{testHead, testPriorHead} {
		t.Run(parent[:1], func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/org/repo/commits/" + testMerge:
					fixture(w, map[string]any{"sha": testMerge, "parents": []map[string]string{{"sha": testBase}, {"sha": parent}}})
				case "/repos/org/repo/commits/" + testHead + "/check-runs":
					fixture(w, map[string]any{"check_runs": []any{rawCheck("head check", testHead, "completed", "success", "service", 8, 1)}})
				case "/repos/org/repo/commits/" + testMerge + "/check-runs":
					if parent != testHead {
						t.Error("queried results from a stale test merge")
					}
					fixture(w, map[string]any{"check_runs": []any{rawCheck("merge check", testMerge, "completed", "success", "service", 8, 1)}})
				case "/repos/org/repo/commits/" + testHead + "/statuses", "/repos/org/repo/commits/" + testMerge + "/statuses":
					fixture(w, []string{})
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			})
			snapshot, err := Read(t.Context(), client, testHead, testMerge, []Requirement{{Name: "head check"}, {Name: "merge check"}})
			if err != nil {
				t.Fatal(err)
			}
			if parent != testHead {
				if len(snapshot.Checks) != 1 || snapshot.TestMergeSHA != "" || len(snapshot.Diagnostics) != 1 || snapshot.Status != "unknown" {
					t.Fatal("stale test merge changed the head observation", snapshot)
				}
				return
			}
			if len(snapshot.Checks) != 3 || snapshot.Status != "missing" || snapshot.TestMergeSHA != testMerge || !snapshot.Checks[2].Required {
				t.Fatal("test merge check did not establish the active result", snapshot)
			}
			if !slices.ContainsFunc(snapshot.Checks, func(check Check) bool {
				return check.Name == "head check" && check.Status == "missing" && check.SHA == testMerge
			}) {
				t.Fatal("head result incorrectly satisfied a test merge requirement", snapshot)
			}
		})
	}
}

func TestRequiredCheckDiscoveryAndUnavailableStatus(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/rules/branches/main":
			_, _ = fmt.Fprint(w, `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build","integration_id":15368}]}},{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build","integration_id":15368}]}}]`)
		case "/repos/org/repo/branches/main":
			_, _ = fmt.Fprint(w, `{"protected":true,"protection":{"enabled":true,"required_status_checks":{"contexts":["legacy","build"]}}}`)
		case "/repos/org/repo/commits/" + testHead + "/check-runs":
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		case "/repos/org/repo/commits/" + testHead + "/statuses":
			fixture(w, []map[string]string{{"context": "legacy", "state": "success"}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	})
	required, diagnostics := Required(t.Context(), client, "main", "build\ntest (one, two)\n")
	want := []Requirement{{Name: "build", AppID: 15368}, {Name: "legacy"}, {Name: "test (one, two)"}}
	if !slices.Equal(required, want) || len(diagnostics) != 0 {
		t.Fatal("wrong required contexts", required, diagnostics)
	}
	snapshot, err := Read(t.Context(), client, testHead, "", required)
	if err != nil || snapshot.Status != "unknown" || len(snapshot.Diagnostics) != 1 || len(snapshot.Checks) != 1 || !snapshot.Checks[0].Required {
		t.Fatal("an unavailable API was treated as a missing result", snapshot, err)
	}
}
