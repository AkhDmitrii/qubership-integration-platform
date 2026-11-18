package docgithub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
)

type publicationAPI struct {
	markDoneOnClose                                  bool
	changeHeadDuringFiles                            bool
	headComparison                                   string
	prFiles                                          []map[string]string
	viewerLogin                                      string
	dropComment                                      bool
	plan                                             Plan
	state, project, head, branch                     string
	comments                                         []issueComment
	pr                                               *pullRequest
	commentCreates, prCreates, closeCalls, doneCalls int
	uncertainComment, uncertainPR, foreignComment    bool
}

func apiFixture(t *testing.T, p Plan) (*Client, *publicationAPI) {
	t.Helper()
	a := &publicationAPI{viewerLogin: "agent-bot", headComparison: "ahead", prFiles: []map[string]string{{"filename": "help/docs/readme.md"}}, plan: p, state: "closed", project: "REVIEW", head: p.BaseSHA}
	server := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(server.Close)
	api, e := fixer.NewGitHubClient(p.Repository, "test", server.Client(), server.URL)
	if e != nil {
		t.Fatal(e)
	}
	return &Client{API: api, Repository: p.Repository}, a
}
func (a *publicationAPI) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	fail := func() { w.WriteHeader(http.StatusNotFound); write(map[string]string{"message": "not found"}) }
	if r.URL.Path == "/graphql" {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch {
		case strings.Contains(request.Query, "viewer"):
			write(map[string]any{"data": map[string]any{"viewer": map[string]any{"databaseId": 99, "login": a.viewerLogin}}})
		case strings.Contains(request.Query, "updateProjectV2ItemFieldValue"):
			if request.Variables["option"] != "DONE" || request.Variables["item"] != "ITEM" {
				w.WriteHeader(400)
				write(map[string]string{})
				return
			}
			a.project = "DONE"
			a.doneCalls++
			write(map[string]any{"data": map[string]any{"updateProjectV2ItemFieldValue": map[string]any{"projectV2Item": map[string]string{"id": "ITEM"}}}})
		case strings.Contains(request.Query, "projectV2(number"):
			write(map[string]any{"data": map[string]any{"repository": map[string]any{"owner": map[string]any{"projectV2": map[string]any{"id": "PROJECT", "fields": map[string]any{"pageInfo": map[string]bool{"hasNextPage": false}, "nodes": []any{map[string]any{"id": "FIELD", "name": "Status", "options": []any{map[string]string{"id": "REVIEW", "name": "In doc review"}, map[string]string{"id": "DONE", "name": "Done"}}}}}}}}, "node": map[string]any{"projectItems": map[string]any{"pageInfo": map[string]bool{"hasNextPage": false}, "nodes": []any{map[string]any{"id": "ITEM", "project": map[string]string{"id": "PROJECT"}, "fieldValueByName": map[string]any{"optionId": a.project, "field": map[string]string{"id": "FIELD"}}}}}}}})
		case strings.Contains(request.Query, "timelineItems"):
			pr := map[string]any{"number": 2, "merged": true, "baseRefName": "main", "repository": map[string]string{"nameWithOwner": "org/repo"}, "mergeCommit": map[string]string{"oid": a.plan.BaseSHA}, "closingIssuesReferences": map[string]any{"nodes": []any{map[string]int{"number": 1}}, "pageInfo": map[string]bool{"hasNextPage": false}}}
			write(map[string]any{"data": map[string]any{"repository": map[string]any{"issue": map[string]any{"timelineItems": map[string]any{"pageInfo": map[string]bool{"hasNextPage": false}, "nodes": []any{map[string]any{"closer": pr}}}}}}})
		default:
			fail()
		}
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/org/repo")
	switch {
	case path == "/actions/variables/AGENT_DOCS_ENABLED":
		write(map[string]string{"value": "true"})
	case path == "/actions/variables/AGENT_DOCS_PUBLISHER_LOGIN":
		write(map[string]string{"value": "agent-bot"})
	case path == "/actions/variables/AGENT_ALLOWED_ACTORS":
		write(map[string]string{"value": "alice"})
	case path == "/issues/1" && r.Method == "GET":
		write(map[string]any{"number": 1, "node_id": "ISSUE", "title": a.plan.IssueTitle, "body": "Issue body", "state": a.state})
	case path == "/issues/1" && r.Method == "PATCH":
		a.state = "closed"
		a.closeCalls++
		if a.markDoneOnClose {
			a.project = "DONE"
		}
		write(map[string]string{"state": "closed"})
	case path == "/issues/1/comments" && r.Method == "GET":
		if a.comments == nil {
			write([]any{})
		} else {
			write(a.comments)
		}
	case path == "/issues/1/comments" && r.Method == "POST":
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c := issueComment{ID: 5, Body: body.Body}
		c.User.ID = 99
		if a.foreignComment {
			c.User.ID = 12
		}
		a.commentCreates++
		if a.dropComment {
			w.WriteHeader(500)
			write(map[string]string{})
			return
		}
		a.comments = append(a.comments, c)
		if a.uncertainComment {
			a.uncertainComment = false
			w.WriteHeader(500)
			write(map[string]string{})
			return
		}
		write(c)
	case path == "/issues/comments/5" && r.Method == "PATCH":
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.comments[0].Body = body.Body
		write(a.comments[0])
	case path == "/git/ref/heads/main":
		write(map[string]any{"object": map[string]string{"sha": a.head}})
	case strings.HasPrefix(path, "/git/ref/heads/agent/docs/"):
		if a.branch == "" {
			fail()
		} else {
			write(map[string]any{"object": map[string]string{"sha": a.branch}})
		}
	case path == "/git/refs" && r.Method == "POST":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.branch = body["sha"]
		write(map[string]any{"object": map[string]string{"sha": a.branch}})
	case strings.HasPrefix(path, "/git/commits/"):
		write(map[string]any{"tree": map[string]string{"sha": strings.Repeat("a", 40)}})
	case path == "/git/blobs":
		write(map[string]string{"sha": strings.Repeat("b", 40)})
	case path == "/git/trees":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["base_tree"] != strings.Repeat("a", 40) {
			w.WriteHeader(400)
			write(map[string]string{})
			return
		}
		write(map[string]string{"sha": strings.Repeat("c", 40)})
	case path == "/git/commits" && r.Method == "POST":
		write(map[string]string{"sha": strings.Repeat("d", 40)})
	case path == "/pulls/3/files":
		write(a.prFiles)
		if a.changeHeadDuringFiles {
			a.pr.Head.SHA = strings.Repeat("1", 40)
		}
	case path == "/pulls/2/files":
		write([]any{map[string]string{"filename": "engine/file.go"}})
	case path == "/pulls" && r.Method == "GET":
		if a.pr == nil {
			write([]any{})
		} else {
			write([]pullRequest{*a.pr})
		}
	case path == "/pulls" && r.Method == "POST":
		var body struct {
			Body, Head, Base string
			Draft            bool
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Draft || hasClosingKeyword(body.Body) {
			w.WriteHeader(400)
			write(map[string]string{})
			return
		}
		a.pr = &pullRequest{ChangedFiles: 1, Number: 3, State: "open", Body: body.Body, HTMLURL: "https://github.com/org/repo/pull/3"}
		a.pr.Head.SHA = a.branch
		a.pr.Head.Ref = body.Head
		a.pr.Head.Repo.FullName = "org/repo"
		a.pr.Base.Ref = body.Base
		a.pr.Base.SHA = a.plan.BaseSHA
		a.pr.Base.Repo.FullName = "org/repo"
		a.prCreates++
		if a.uncertainPR {
			a.uncertainPR = false
			w.WriteHeader(500)
			write(map[string]string{})
			return
		}
		write(a.pr)
	case path == "/pulls/3":
		write(a.pr)
	case strings.HasPrefix(path, "/compare/"+strings.Repeat("d", 40)+"..."):
		write(map[string]string{"status": a.headComparison})
	case strings.HasPrefix(path, "/compare/"):
		write(map[string]string{"status": "ahead"})
	default:
		fmt.Println("unhandled fake request", r.Method, path)
		fail()
	}
}
func TestPublishNoChangeIsIdempotentForClosedIssue(t *testing.T) {
	plan, run, _ := publicationFixture(t, false)
	c, a := apiFixture(t, plan)
	a.uncertainComment = true
	for i := 0; i < 2; i++ {
		result, e := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true})
		if e != nil || result.Status != "completed" {
			t.Fatal(result, e)
		}
	}
	if a.commentCreates != 1 || a.prCreates != 0 || a.closeCalls != 0 || a.doneCalls != 1 || a.project != "DONE" {
		t.Fatalf("%+v", a)
	}
	const signature = "<sub>🤖 typesafe/jev-1.14 out=30 usd=0.0042</sub>"
	body := a.comments[0].Body
	if !strings.HasSuffix(body, "\n\n"+signature) || strings.Count(body, "<sub>🤖") != 1 {
		t.Fatalf("Jev-only comment does not carry exactly one last-line signature: %s", body)
	}
	result, e := Reconcile(context.Background(), c, plan, PublishOptions{Actor: "alice", Apply: true})
	if e != nil || result.Status != "completed" || a.comments[0].Body != body {
		t.Fatalf("reconciliation changed the signed result: %s, %v", result.Status, e)
	}
}
func TestPublishOpenIssueClosesAfterComment(t *testing.T) {
	plan, run, _ := publicationFixture(t, false)
	plan.IssueState = "open"
	plan.ID = ""
	plan.ID = Digest(plan)
	c, a := apiFixture(t, plan)
	a.state = "open"
	result, e := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true})
	if e != nil || result.Status != "completed" || a.closeCalls != 1 || a.commentCreates != 1 {
		t.Fatal(result, e, a.closeCalls)
	}
	a.state = "open"
	if _, e = Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true}); e == nil || a.closeCalls != 1 {
		t.Fatal("reopened issue was closed again", e)
	}
}
func TestProjectAutomationCompletesAfterIssueClose(t *testing.T) {
	plan, run, _ := publicationFixture(t, false)
	plan.IssueState = "open"
	plan.ID = ""
	plan.ID = Digest(plan)
	c, a := apiFixture(t, plan)
	a.state, a.markDoneOnClose = "open", true
	for i := 0; i < 2; i++ {
		result, err := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true})
		if err != nil || result.Status != "completed" || !result.CloseAttempted || result.ProjectDoneAttempted {
			t.Fatalf("publication %d: %+v, %v", i, result, err)
		}
	}
	if a.closeCalls != 1 || a.doneCalls != 0 || a.commentCreates != 1 {
		t.Fatalf("close=%d project updates=%d comments=%d", a.closeCalls, a.doneCalls, a.commentCreates)
	}
}
func TestPublicationStopsForStaleMovedOrUnauthorizedTask(t *testing.T) {
	for _, change := range []string{"main", "project", "actor"} {
		t.Run(change, func(t *testing.T) {
			plan, run, _ := publicationFixture(t, false)
			c, a := apiFixture(t, plan)
			opts := PublishOptions{Actor: "alice", Apply: true}
			switch change {
			case "main":
				a.head = strings.Repeat("e", 40)
			case "project":
				a.project = "OTHER"
			case "actor":
				opts.Actor = "outsider"
			}
			if _, e := Publish(context.Background(), c, plan, run, opts); e == nil {
				t.Fatal("invalid task published")
			}
			if a.commentCreates+a.prCreates+a.doneCalls != 0 {
				t.Fatal("created effects")
			}
		})
	}
}
func TestDocsPRWaitsForMergeAndRecoversUncertainCreate(t *testing.T) {
	plan, run, _ := publicationFixture(t, true)
	c, a := apiFixture(t, plan)
	a.uncertainPR = true
	for i := 0; i < 2; i++ {
		result, e := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true})
		if e != nil || result.Status != "awaiting-merge" {
			t.Fatal(result, e)
		}
	}
	if a.prCreates != 1 || a.doneCalls != 0 || a.closeCalls != 0 {
		t.Fatal("PR was duplicated or task completed before merge")
	}
	prBody := a.pr.Body
	const signature = "<sub>🤖 gpt-6.1-terra high out=60</sub>"
	if !strings.HasSuffix(prBody, marker(plan)+"\n\n"+signature) || strings.Count(prBody, "<sub>🤖") != 1 {
		t.Fatalf("missing author signature on the final PR line: %s", prBody)
	}
	a.pr.Merged = true
	a.pr.State = "closed"
	a.pr.MergeCommitSHA = strings.Repeat("f", 40)
	a.head = a.pr.MergeCommitSHA
	for i := 0; i < 2; i++ {
		result, e := Reconcile(context.Background(), c, plan, PublishOptions{Actor: "alice", Apply: true})
		if e != nil || result.Status != "completed" {
			t.Fatal(result, e)
		}
	}
	if a.doneCalls != 1 || a.pr.Body != prBody || strings.Contains(a.comments[0].Body, "<sub>🤖") {
		t.Fatal("merge completion repeated the signature or updated the PR")
	}
}
func TestDryRunAndClosedUnmergedPR(t *testing.T) {
	plan, run, _ := publicationFixture(t, true)
	c, a := apiFixture(t, plan)
	result, e := Publish(context.Background(), c, plan, run, PublishOptions{})
	if e != nil || result.Applied || a.commentCreates != 0 {
		t.Fatal(result, e)
	}
	if _, e = Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true}); e != nil {
		t.Fatal(e)
	}
	a.pr.State = "closed"
	result, e = Reconcile(context.Background(), c, plan, PublishOptions{Actor: "alice", Apply: true})
	if e != nil || result.Status != "needs-human" || a.doneCalls != 0 {
		t.Fatal(result, e)
	}
	if !strings.Contains(a.pr.Body, "<sub>🤖 gpt-6.1-terra high") || strings.Contains(a.comments[0].Body, "<sub>🤖") || a.commentCreates != 1 {
		t.Fatal("the closed PR status repeated or replaced the run signature")
	}
}

func TestReconcileEditsTheSignedNoChangeCommentInPlace(t *testing.T) {
	plan, _, _ := publicationFixture(t, false)
	c, api := apiFixture(t, plan)
	ctx := context.Background()
	const signature = "<sub>🤖 typesafe/jev-1.14 out=30 usd=0.0042</sub>"
	p := &publisher{client: c, plan: plan, state: Publication{
		Decision: "no_change", Status: "publishing", CandidateDigest: "candidate", InitialIssueState: "closed",
		IssueComment: "Help already describes the behavior.\n\n<sub>🤖 draft-model in=999</sub>", RunSignature: signature,
	}}
	if e := p.saveState(ctx, p.state); e != nil {
		t.Fatal(e)
	}
	recovered, e := RecoverPlan(ctx, c, plan.IssueNumber)
	if e != nil || recovered.ID != plan.ID {
		t.Fatalf("could not recover the signed plan: %v", e)
	}
	result, e := Reconcile(ctx, c, recovered, PublishOptions{Actor: "alice", Apply: true})
	body := api.comments[0].Body
	if e != nil || result.Status != "completed" || api.commentCreates != 1 || !strings.Contains(body, "**completed**") ||
		!strings.HasSuffix(body, "\n\n"+signature) || strings.Count(body, "<sub>🤖") != 1 {
		t.Fatalf("reconciliation lost or duplicated signed totals: %s, %v", body, e)
	}
}

func TestUnknownCommentCreationDoesNotBlindlyRetry(t *testing.T) {
	plan, run, _ := publicationFixture(t, false)
	c, a := apiFixture(t, plan)
	a.dropComment = true
	for i := 0; i < 2; i++ {
		if _, e := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true}); e == nil {
			t.Fatal("unknown creation reported success")
		}
	}
	if a.commentCreates != 1 || a.doneCalls != 0 || a.prCreates != 0 {
		t.Fatal("uncertain creation repeated", a.commentCreates)
	}
}
func TestRecoverPlanAndRejectForeignMarker(t *testing.T) {
	plan, run, _ := publicationFixture(t, false)
	c, a := apiFixture(t, plan)
	if _, e := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true}); e != nil {
		t.Fatal(e)
	}
	recovered, e := RecoverPlan(context.Background(), c, 1)
	if e != nil || recovered.ID != plan.ID {
		t.Fatal(recovered, e)
	}
	a.comments[0].User.ID = 12
	if _, e = Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true}); e == nil {
		t.Fatal("foreign state accepted")
	}
	if _, e = RecoverPlan(context.Background(), c, 1); !errors.Is(e, ErrNoPublication) {
		t.Fatal("foreign state recovered", e)
	}
}

func TestReconcileDoesNotCompleteAnInterruptedPRPublication(t *testing.T) {
	plan, _, _ := publicationFixture(t, true)
	c, a := apiFixture(t, plan)
	p := &publisher{client: c, plan: plan, state: Publication{PlanID: plan.ID, Decision: "needed", Status: "publishing", CandidateDigest: "candidate", InitialIssueState: "closed"}}
	if _, e := p.readState(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := p.saveState(context.Background(), p.state); e != nil {
		t.Fatal(e)
	}
	if _, e := Reconcile(context.Background(), c, plan, PublishOptions{Actor: "alice", Apply: true}); e == nil {
		t.Fatal("interrupted PR publication treated as no-change")
	}
	if a.doneCalls != 0 || a.closeCalls != 0 {
		t.Fatal("task completed without a PR")
	}
}

func TestReadIdentityCanAuthorizeActorButCannotPublish(t *testing.T) {
	plan, run, _ := publicationFixture(t, false)
	c, a := apiFixture(t, plan)
	a.viewerLogin = "reader-bot"
	if err := AuthorizeActor(context.Background(), c, "alice"); err != nil {
		t.Fatal("read identity could not authorize initiator", err)
	}
	if err := Authorize(context.Background(), c, "alice"); err == nil {
		t.Fatal("read identity passed publisher authorization")
	}
	if _, err := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true}); err == nil {
		t.Fatal("read identity published")
	}
	if a.commentCreates+a.prCreates+a.closeCalls+a.doneCalls != 0 {
		t.Fatal("publication performed effects")
	}
	if err := AuthorizeActor(context.Background(), c, "outsider"); err == nil {
		t.Fatal("unauthorized actor admitted")
	}
}

func TestReconcileAllowsAppendedHelpCorrections(t *testing.T) {
	plan, run, _ := publicationFixture(t, true)
	c, a := apiFixture(t, plan)
	original, err := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	corrected := strings.Repeat("e", 40)
	a.pr.Head.SHA = corrected
	a.branch = corrected
	observed, err := Reconcile(context.Background(), c, plan, PublishOptions{Actor: "alice", Apply: true})
	if err != nil || observed.Status != "awaiting-merge" || !observed.UpdatedAfterPublication || observed.MergedHeadSHA != "" || observed.CommitSHA != original.CommitSHA || observed.CandidateDigest != original.CandidateDigest || a.doneCalls != 0 {
		t.Fatal(observed, err)
	}
	a.pr.Merged = true
	a.pr.State = "closed"
	a.pr.MergeCommitSHA = strings.Repeat("f", 40)
	a.head = a.pr.MergeCommitSHA
	result, err := Reconcile(context.Background(), c, plan, PublishOptions{Actor: "alice", Apply: true})
	if err != nil || result.Status != "completed" || !result.UpdatedAfterPublication || result.MergedHeadSHA != corrected || result.CommitSHA != original.CommitSHA || result.CandidateDigest != original.CandidateDigest || a.doneCalls != 1 {
		t.Fatal(result, err)
	}
	again, err := Reconcile(context.Background(), c, plan, PublishOptions{Actor: "alice", Apply: true})
	if err != nil || again.MergedHeadSHA != corrected || a.doneCalls != 1 {
		t.Fatal(again, err)
	}
}

func TestPublishRejectsAnExistingPRWithAnEditedClosingKeyword(t *testing.T) {
	ctx := context.Background()
	plan, run, _ := publicationFixture(t, true)
	c, a := apiFixture(t, plan)
	if _, err := Publish(ctx, c, plan, run, PublishOptions{Actor: "alice", Apply: true}); err != nil {
		t.Fatal(err)
	}
	a.pr.Body += "\nResolves: #1"
	if _, err := Publish(ctx, c, plan, run, PublishOptions{Actor: "alice", Apply: true}); err == nil || !strings.Contains(err.Error(), "issue-closing keyword") {
		t.Fatal("publication accepted an edited closing keyword", err)
	}
	if a.prCreates != 1 || a.doneCalls != 0 || a.closeCalls != 0 {
		t.Fatal("invalid PR triggered publication effects")
	}
}

func TestReconcileRejectsRewrittenOrOutOfScopeCorrections(t *testing.T) {
	for _, change := range []string{"rewritten", "outside", "rename_outside", "changed_during_check", "closing_keyword"} {
		t.Run(change, func(t *testing.T) {
			plan, run, _ := publicationFixture(t, true)
			c, a := apiFixture(t, plan)
			if _, err := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true}); err != nil {
				t.Fatal(err)
			}
			a.pr.Head.SHA = strings.Repeat("e", 40)
			a.pr.Merged = true
			a.pr.State = "closed"
			a.pr.MergeCommitSHA = strings.Repeat("f", 40)
			a.head = a.pr.MergeCommitSHA
			switch change {
			case "closing_keyword":
				a.pr.Body += "\nCloses: #1"
			case "changed_during_check":
				a.changeHeadDuringFiles = true
			case "rewritten":
				a.headComparison = "diverged"
			case "outside":
				a.prFiles = []map[string]string{{"filename": "engine/source.go"}}
			case "rename_outside":
				a.prFiles = []map[string]string{{"filename": "help/docs/readme.md", "previous_filename": "engine/source.go"}}
			}
			if _, err := Reconcile(context.Background(), c, plan, PublishOptions{Actor: "alice", Apply: true}); err == nil {
				t.Fatal("invalid reviewer correction accepted")
			}
			if a.doneCalls != 0 || a.closeCalls != 0 {
				t.Fatal("invalid PR completed the documentation task")
			}
		})
	}
}

func TestIssueReopenedAfterPreparationStopsBeforeEffects(t *testing.T) {
	plan, run, _ := publicationFixture(t, false)
	c, a := apiFixture(t, plan)
	a.state = "open"
	if _, e := Publish(context.Background(), c, plan, run, PublishOptions{Actor: "alice", Apply: true}); e == nil {
		t.Fatal("reopened issue accepted")
	}
	if a.commentCreates != 0 || a.closeCalls != 0 || a.doneCalls != 0 || a.prCreates != 0 {
		t.Fatal("effects occurred before reopened-issue check")
	}
}

func TestRecoverPlanRejectsAppliedStateWithoutPacketBinding(t *testing.T) {
	plan, _, _ := publicationFixture(t, false)
	c, a := apiFixture(t, plan)
	plan.PacketSHA256 = ""
	plan.ID = ""
	plan.ID = Digest(plan)
	state := Publication{Plan: &plan, PlanID: plan.ID, Status: "completed", Decision: "no_change", Applied: true}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	comment := issueComment{ID: 5, Body: marker(plan) + "\n<!-- qip-docs-state:" + base64.StdEncoding.EncodeToString(data) + " -->"}
	comment.User.ID = 99
	a.comments = []issueComment{comment}
	if _, err = RecoverPlan(context.Background(), c, 1); err == nil {
		t.Fatal("recovered an applied plan without trusted preparation binding")
	}
}
