package hostedcandidate

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
)

type API interface {
	Request(context.Context, string, string, any, any) error
	List(context.Context, string, string) ([]json.RawMessage, error)
}
type Options struct {
	Repository, DefaultBranch, Workspace, Base string
	Issue, Run, Attempt                        int64
	Scope                                      []string
}
type Published struct {
	Number int64  `json:"number"`
	SHA    string `json:"sha"`
	Branch string `json:"branch"`
	URL    string `json:"url"`
}
type object struct {
	SHA string `json:"sha"`
}
type ref struct {
	Object object `json:"object"`
}
type commit struct {
	Tree    object   `json:"tree"`
	Parents []object `json:"parents"`
	Message string   `json:"message"`
}

func missing(err error) bool { var e *fixer.HTTPError; return errors.As(err, &e) && e.Status == 404 }

func eligible(ctx context.Context, api API, o Options) (string, error) {
	var issue struct {
		Title, State string
		PullRequest  json.RawMessage `json:"pull_request"`
		Labels       []struct{ Name string }
	}
	if err := api.Request(ctx, "GET", fmt.Sprintf("/issues/%d", o.Issue), nil, &issue); err != nil {
		return "", err
	}
	ready := false
	for _, l := range issue.Labels {
		if l.Name == "ready-for-agent" {
			ready = true
		}
	}
	if len(issue.PullRequest) > 0 || issue.State != "open" || !ready {
		return "", fmt.Errorf("issue is no longer open and ready for the agent")
	}
	return issue.Title, nil
}

func Publish(ctx context.Context, api API, c Candidate, o Options) (Published, error) {
	var out Published
	if o.Issue <= 0 || o.Run <= 0 || o.Attempt <= 0 || o.DefaultBranch == "" || strings.Count(o.Repository, "/") != 1 {
		return out, fmt.Errorf("invalid publication identity")
	}
	if err := Validate(c, o.Base, o.Scope); err != nil {
		return out, err
	}
	tree, err := candidateTree(ctx, o.Workspace, c)
	if err != nil {
		return out, err
	}
	title, err := eligible(ctx, api, o)
	if err != nil {
		return out, err
	}
	var repository struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err = api.Request(ctx, "GET", "", nil, &repository); err != nil {
		return out, err
	}
	if repository.DefaultBranch != o.DefaultBranch {
		return out, fmt.Errorf("repository default branch changed")
	}
	var target ref
	if err = api.Request(ctx, "GET", "/git/ref/heads/"+url.PathEscape(o.DefaultBranch), nil, &target); err != nil {
		return out, err
	}
	if target.Object.SHA != o.Base {
		if !sha.MatchString(target.Object.SHA) {
			return out, fmt.Errorf("invalid default branch identity")
		}
		var comparison struct {
			Status string `json:"status"`
		}
		if err = api.Request(ctx, "GET", "/compare/"+o.Base+"..."+target.Object.SHA, nil, &comparison); err != nil {
			return out, err
		}
		if comparison.Status != "ahead" {
			return out, fmt.Errorf("default branch no longer contains the admitted base")
		}
	}
	branch := fmt.Sprintf("agent/fix/%d/%d-%d", o.Issue, o.Run, o.Attempt)
	reference := "/git/ref/heads/" + branch
	var head ref
	marker := fmt.Sprintf("<!-- qip-fix:%d:%d:%d -->", o.Issue, o.Run, o.Attempt)
	message := fmt.Sprintf("fix: resolve issue #%d\n\n%s", o.Issue, marker)
	err = api.Request(ctx, "GET", reference, nil, &head)
	if missing(err) {
		var base commit
		if err = api.Request(ctx, "GET", "/git/commits/"+o.Base, nil, &base); err != nil {
			return out, err
		}
		if !sha.MatchString(base.Tree.SHA) {
			return out, fmt.Errorf("GitHub returned an invalid base tree")
		}
		entries := make([]map[string]any, 0, len(c.Files))
		for _, f := range c.Files {
			entry := map[string]any{"path": f.Path, "type": "blob", "mode": f.Mode}
			if f.Mode == "delete" {
				entry["sha"] = nil
				entry["mode"] = "100644"
			} else {
				var blob object
				if err = api.Request(ctx, "POST", "/git/blobs", map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString(f.Content)}, &blob); err != nil {
					return out, err
				}
				h := sha1.New()
				fmt.Fprintf(h, "blob %d%c", len(f.Content), 0)
				h.Write(f.Content)
				if blob.SHA != fmt.Sprintf("%x", h.Sum(nil)) {
					return out, fmt.Errorf("GitHub returned a different blob for %q", f.Path)
				}
				entry["sha"] = blob.SHA
			}
			entries = append(entries, entry)
		}
		var built object
		if err = api.Request(ctx, "POST", "/git/trees", map[string]any{"base_tree": base.Tree.SHA, "tree": entries}, &built); err != nil {
			return out, err
		}
		if built.SHA != tree {
			return out, fmt.Errorf("GitHub tree differs from the validated candidate")
		}
		var tip object
		if err = api.Request(ctx, "POST", "/git/commits", map[string]any{"message": message, "parents": []string{o.Base}, "tree": tree}, &tip); err != nil {
			return out, err
		}
		if !sha.MatchString(tip.SHA) {
			return out, fmt.Errorf("GitHub returned an invalid commit")
		}
		_, err = eligible(ctx, api, o)
		if err != nil {
			return out, err
		}
		createErr := api.Request(ctx, "POST", "/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": tip.SHA}, nil)
		if err = api.Request(ctx, "GET", reference, nil, &head); err != nil {
			return out, errors.Join(createErr, err)
		}
	} else if err != nil {
		return out, err
	}
	var stored commit
	if !sha.MatchString(head.Object.SHA) {
		return out, fmt.Errorf("invalid publication branch")
	}
	if err = api.Request(ctx, "GET", "/git/commits/"+head.Object.SHA, nil, &stored); err != nil {
		return out, err
	}
	if stored.Tree.SHA != tree || len(stored.Parents) != 1 || stored.Parents[0].SHA != o.Base || stored.Message != message {
		return out, fmt.Errorf("existing branch differs from this candidate")
	}
	owner := strings.SplitN(o.Repository, "/", 2)[0]
	query := "/pulls?state=all&head=" + url.QueryEscape(owner+":"+branch) + "&base=" + url.QueryEscape(o.DefaultBranch)
	prs, err := api.List(ctx, query, "")
	if err != nil {
		return out, err
	}
	if len(prs) == 0 {
		if _, err = eligible(ctx, api, o); err != nil {
			return out, err
		}
		title = strings.Join(strings.Fields(title), " ")
		if len([]rune(title)) > 90 {
			title = string([]rune(title)[:90])
		}
		body := fmt.Sprintf("Closes #%d\n\n[Agent report and evidence](https://github.com/%s/actions/runs/%d/attempts/%d)\n\n%s", o.Issue, o.Repository, o.Run, o.Attempt, marker)
		createErr := api.Request(ctx, "POST", "/pulls", map[string]any{"title": "fix: " + title, "head": branch, "base": o.DefaultBranch, "draft": true, "body": body}, nil)
		prs, err = api.List(ctx, query, "")
		if err != nil || len(prs) == 0 {
			return out, errors.Join(createErr, err, fmt.Errorf("draft PR creation could not be confirmed"))
		}
	}
	if len(prs) != 1 {
		return out, fmt.Errorf("ambiguous PR for publication branch")
	}
	var pr struct {
		Number      int64
		State, Body string
		Draft       bool
		URL         string `json:"html_url"`
		Head, Base  struct {
			SHA, Ref string
			Repo     struct {
				FullName string `json:"full_name"`
			}
		}
	}
	if err = json.Unmarshal(prs[0], &pr); err != nil {
		return out, err
	}
	if pr.Number <= 0 || pr.State != "open" || !pr.Draft || pr.Head.SHA != head.Object.SHA || pr.Head.Ref != branch || pr.Base.Ref != o.DefaultBranch || pr.Head.Repo.FullName != o.Repository || pr.Base.Repo.FullName != o.Repository || !strings.Contains(pr.Body, marker) {
		return out, fmt.Errorf("pull request differs from the recorded draft")
	}
	return Published{Number: pr.Number, SHA: head.Object.SHA, Branch: branch, URL: pr.URL}, nil
}
