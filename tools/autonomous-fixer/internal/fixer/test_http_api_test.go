package fixer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type testAPI struct {
	mu                   sync.Mutex
	Server               *httptest.Server
	Repository           string
	Bot                  GitHubUser
	Issue                Issue
	Comments             []Comment
	Events               []json.RawMessage
	Runs                 []WorkflowRun
	Artifacts            []map[string]any
	Trace                []string
	FailDispatchResponse bool
	CancellationPending  bool
	CurrentSHA           string
	Labels               map[string]bool
	Variables            map[string]string
	DraftPullRequest     map[string]any
}

func testGitHubServer(t *testing.T) *testAPI {
	t.Helper()
	body := "Repair the reported behavior"
	a := &testAPI{Repository: "org/repo", Bot: GitHubUser{ID: 99, Login: "github-actions[bot]", Type: "Bot"}, Issue: Issue{ID: 101, Number: 1, Title: "Repair behavior", Body: &body, Labels: []Label{{Name: "ready-for-agent"}}, User: GitHubUser{ID: 12, Login: "alice", Type: "User"}, UpdatedAt: "2026-11-06T10:00:00Z", State: "open"}, CurrentSHA: strings.Repeat("a", 40), Labels: map[string]bool{"ready-for-agent": true}}
	a.Variables = map[string]string{"AGENT_ENABLED": "true", "AGENT_ALLOWED_ACTORS": "alice,bob"}
	a.Server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.Server.Close)
	return a
}
func (a *testAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Trace = append(a.Trace, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { json.NewEncoder(w).Encode(v) }
	path := strings.TrimPrefix(r.URL.Path, "/repos/"+a.Repository)
	if r.URL.Path == "/users/github-actions[bot]" {
		write(a.Bot)
		return
	}
	switch {
	case strings.HasPrefix(path, "/actions/variables/"):
		if r.Header.Get("Authorization") != "Bearer variables-test-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		value, ok := a.Variables[strings.TrimPrefix(path, "/actions/variables/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		write(map[string]string{"value": value})
	case path == "":
		write(Repository{FullName: a.Repository, DefaultBranch: "main"})
	case path == "/commits/main":
		write(map[string]string{"sha": a.CurrentSHA})
	case path == "/actions/workflows/agent-work.yaml":
		write(map[string]any{"id": 42, "path": ".github/workflows/agent-work.yaml", "state": "active"})
	case path == "/issues/1":
		write(a.Issue)
	case path == "/issues/1/events":
		if a.Events == nil {
			write([]json.RawMessage{})
		} else {
			write(a.Events)
		}
	case path == "/pulls/10" && a.DraftPullRequest != nil:
		write(a.DraftPullRequest)
	case path == "/issues/1/comments" && r.Method == "GET":
		if a.Comments == nil {
			write([]Comment{})
		} else {
			write(a.Comments)
		}
	case path == "/issues/1/comments" && r.Method == "POST":
		var input struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		comment := Comment{ID: int64(200 + len(a.Comments)), Body: input.Body, User: a.Bot}
		a.Comments = append(a.Comments, comment)
		write(comment)
	case strings.HasPrefix(path, "/issues/comments/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/issues/comments/"), 10, 64)
		for i := range a.Comments {
			if a.Comments[i].ID == id {
				if r.Method == "PATCH" {
					var input struct {
						Body string `json:"body"`
					}
					json.NewDecoder(r.Body).Decode(&input)
					a.Comments[i].Body = input.Body
				}
				write(a.Comments[i])
				return
			}
		}
		http.NotFound(w, r)
	case path == "/actions/workflows/agent-work.yaml/runs" || path == "/actions/runs":
		if a.Runs == nil {
			write(map[string]any{"workflow_runs": []WorkflowRun{}})
		} else {
			write(map[string]any{"workflow_runs": a.Runs})
		}
	case path == "/actions/workflows/agent-work.yaml/dispatches" && r.Method == "POST":
		var input struct {
			Ref    string            `json:"ref"`
			Inputs map[string]string `json:"inputs"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		issue, _ := strconv.ParseInt(input.Inputs["issue"], 10, 64)
		generation, _ := strconv.Atoi(input.Inputs["generation"])
		version, _ := strconv.Atoi(input.Inputs["contract_version"])
		run := WorkflowRun{ID: int64(500 + len(a.Runs)), RunAttempt: 1, WorkflowID: 42, HeadRepository: Repository{FullName: a.Repository}, Event: "workflow_dispatch", Path: ".github/workflows/agent-work.yaml", HeadSHA: a.CurrentSHA, HeadBranch: input.Ref, DisplayTitle: WorkRunName(issue, input.Inputs["execution"], generation, version), Status: "in_progress", CreatedAt: "2026-11-06T10:00:00Z"}
		a.Runs = append(a.Runs, run)
		if a.FailDispatchResponse {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		write(map[string]int64{"workflow_run_id": run.ID})
	case strings.HasPrefix(path, "/actions/runs/") && strings.HasSuffix(path, "/artifacts"):
		if a.Artifacts == nil {
			write(map[string]any{"artifacts": []any{}})
		} else {
			write(map[string]any{"artifacts": a.Artifacts})
		}
	case strings.HasPrefix(path, "/actions/runs/"):
		parts := strings.Split(strings.TrimPrefix(path, "/actions/runs/"), "/")
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		for i := range a.Runs {
			if a.Runs[i].ID != id {
				continue
			}
			if len(parts) > 1 && parts[1] == "cancel" {
				if !a.CancellationPending {
					a.Runs[i].Status = "completed"
					a.Runs[i].Conclusion = "cancelled"
				}
				w.WriteHeader(202)
				return
			}
			write(a.Runs[i])
			return
		}
		http.NotFound(w, r)
	case path == "/issues/1/labels" && r.Method == "GET":
		write(a.Issue.Labels)
	case path == "/issues/1/labels" && r.Method == "POST":
		var input struct {
			Labels []string `json:"labels"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		for _, name := range input.Labels {
			found := false
			for _, label := range a.Issue.Labels {
				found = found || label.Name == name
			}
			if !found {
				a.Issue.Labels = append(a.Issue.Labels, Label{Name: name})
			}
		}
		write(a.Issue.Labels)
	case strings.HasPrefix(path, "/issues/1/labels/") && r.Method == "DELETE":
		name := strings.TrimPrefix(path, "/issues/1/labels/")
		labels := []Label{}
		for _, label := range a.Issue.Labels {
			if label.Name != name {
				labels = append(labels, label)
			}
		}
		a.Issue.Labels = labels
		w.WriteHeader(204)
	case path == "/labels" && r.Method == "POST":
		var input Label
		json.NewDecoder(r.Body).Decode(&input)
		a.Labels[input.Name] = true
		write(input)
	case strings.HasPrefix(path, "/labels/"):
		name := strings.TrimPrefix(path, "/labels/")
		if !a.Labels[name] {
			http.NotFound(w, r)
			return
		}
		write(Label{Name: name})
	default:
		http.Error(w, fmt.Sprintf("unhandled %s %s", r.Method, path), 404)
	}
}
func (a *testAPI) count(method, path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := 0
	for _, entry := range a.Trace {
		if entry == method+" "+path {
			count++
		}
	}
	return count
}
func (a *testAPI) adapter(t *testing.T) *GitHubAdapter {
	t.Helper()
	client, err := NewGitHubClient(a.Repository, "test-token", a.Server.Client(), a.Server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &GitHubAdapter{Client: client, Store: NewCommentStore(client, a.Repository, 1, a.Bot.ID, "task"), Task: Seed{TaskID: "task", Repository: a.Repository, IssueNumber: 1, BaseSHA: a.CurrentSHA, TrustedSHA: a.CurrentSHA, Requirements: "Repair behavior", ScopePaths: []string{"engine/"}}, DefaultBranch: "main", WorkflowID: 42, AllowedActors: func(_ context.Context) (string, error) { return "alice", nil }}
}
