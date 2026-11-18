package hostedcandidate

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T) (Options, Candidate, string) {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	must := func(args ...string) string {
		t.Helper()
		out, e := git(ctx, dir, "", nil, args...)
		if e != nil {
			t.Fatal(e)
		}
		return strings.TrimSpace(string(out))
	}
	must("init", "--quiet", "-b", "main")
	if err := os.Mkdir(filepath.Join(dir, "ui"), 0700); err != nil {
		t.Fatal(err)
	}
	file := func(path, value string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(value), mode); err != nil {
			t.Fatal(err)
		}
	}
	file("ui/remove", "old", 0644)
	file("ui/file", "before", 0644)
	must("add", ".")
	must("-c", "user.name=Test", "-c", "user.email=test@example.org", "commit", "-qm", "baseline")
	base := must("rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(dir, "ui/remove")); err != nil {
		t.Fatal(err)
	}
	file("ui/file", "after\x00\xff", 0644)
	file("ui/new", "new", 0755)
	file("ui/empty", "", 0644)
	c, err := Pack(ctx, dir, base)
	if err != nil {
		t.Fatal(err)
	}
	// Stage the same edits independently to obtain Git's expected tree.
	must("add", "-A")
	tree := must("write-tree")
	return Options{Repository: "org/qip", DefaultBranch: "main", Workspace: dir, Base: base, Issue: 12, Run: 345, Attempt: 1, Scope: []string{"ui"}}, c, tree
}

type githubFixture struct {
	targetSHA, comparisonStatus      string
	o                                Options
	tree, tip, branch, body, message string
	pulls                            []json.RawMessage
	writes, created                  int
	loseCreateReply, removed         bool
}

func assign(out, v any) error {
	if out == nil {
		return nil
	}
	d, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(d, out)
}
func (g *githubFixture) Request(ctx context.Context, method, path string, body, target any) error {
	if method == "POST" {
		g.writes++
	}
	switch {
	case path == "/issues/12":
		labels := []map[string]string{{"name": "ready-for-agent"}}
		if g.removed {
			labels = nil
		}
		return assign(target, map[string]any{"title": "Handle binary input", "state": "open", "labels": labels})
	case path == "":
		return assign(target, map[string]string{"default_branch": "main"})
	case path == "/git/ref/heads/main":
		if g.targetSHA != "" {
			return assign(target, ref{object{g.targetSHA}})
		}
		return assign(target, ref{object{g.o.Base}})
	case path == "/compare/"+g.o.Base+"..."+g.targetSHA:
		return assign(target, map[string]string{"status": g.comparisonStatus})
	case strings.HasPrefix(path, "/git/ref/heads/"):
		if g.branch == "" {
			return &fixer.HTTPError{Status: 404}
		}
		return assign(target, ref{object{g.tip}})
	case path == "/git/commits/"+g.o.Base:
		return assign(target, commit{Tree: object{g.o.Base}})
	case path == "/git/commits/"+g.tip:
		return assign(target, commit{Tree: object{g.tree}, Parents: []object{{g.o.Base}}, Message: g.message})
	case path == "/git/blobs":
		raw, _ := base64.StdEncoding.DecodeString(body.(map[string]string)["content"])
		h := sha1.New()
		fmt.Fprintf(h, "blob %d%c", len(raw), 0)
		h.Write(raw)
		return assign(target, object{fmt.Sprintf("%x", h.Sum(nil))})
	case path == "/git/trees":
		return assign(target, object{g.tree})
	case path == "/git/commits":
		g.message = body.(map[string]any)["message"].(string)
		g.tip = strings.Repeat("b", 40)
		return assign(target, object{g.tip})
	case path == "/git/refs":
		g.branch = strings.TrimPrefix(body.(map[string]string)["ref"], "refs/heads/")
		return nil
	case path == "/pulls":
		g.created++
		b := body.(map[string]any)
		g.body = b["body"].(string)
		r := map[string]string{"full_name": g.o.Repository}
		raw, _ := json.Marshal(map[string]any{"number": 27, "state": "open", "draft": b["draft"], "head": map[string]any{"ref": g.branch, "sha": g.tip, "repo": r}, "base": map[string]any{"ref": "main", "repo": r}, "body": g.body, "html_url": "https://github.com/org/qip/pull/27"})
		g.pulls = []json.RawMessage{raw}
		if g.loseCreateReply {
			return fmt.Errorf("reply lost")
		}
		return nil
	}
	return fmt.Errorf("unexpected request %s %s", method, path)
}
func (g *githubFixture) List(ctx context.Context, path, key string) ([]json.RawMessage, error) {
	if !strings.Contains(path, "state=all") {
		return nil, fmt.Errorf("must check closed PRs too")
	}
	return g.pulls, nil
}
func TestCandidateRoundTripAndRepeatPublication(t *testing.T) {
	o, c, tree := repo(t)
	if len(c.Files) != 4 {
		t.Fatal(c.Files)
	}
	data, _ := json.Marshal(c)
	path := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	c, err := Read(path, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Read(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted different candidate digest")
	}
	g := &githubFixture{o: o, tree: tree, loseCreateReply: true}
	for i := 0; i < 2; i++ {
		p, e := Publish(context.Background(), g, c, o)
		if e != nil || p.Number != 27 || p.SHA != g.tip {
			t.Fatal(p, e)
		}
	}
	if g.created != 1 || !strings.Contains(g.body, "Closes #12") {
		t.Fatal(g.created, g.body)
	}
}
func TestRejectBeforePublishing(t *testing.T) {
	o, c, tree := repo(t)
	for _, path := range []string{"ui/../escape", "ui/.git/config", ".github/workflows/deploy.yml", "tools/autonomous-fixer/cmd/agent/main.go", "engine/change"} {
		other := c
		other.Files = append([]File{{Path: path, Mode: "100644", Content: []byte("x")}}, c.Files...)
		g := &githubFixture{o: o, tree: tree}
		_, e := Publish(context.Background(), g, other, o)
		if e == nil || g.writes != 0 {
			t.Fatal(path, e, g.writes)
		}
	}
	for _, bad := range []File{{Path: "ui/new", Mode: "120000"}, {Path: "ui/missing", Mode: "delete"}, {Path: "ui/file", Mode: "100644", Content: []byte("before")}} {
		other := c
		other.Files = []File{bad}
		g := &githubFixture{o: o, tree: tree}
		_, e := Publish(context.Background(), g, other, o)
		if e == nil || g.writes != 0 {
			t.Fatal(bad.Path, e, g.writes)
		}
	}
	g := &githubFixture{o: o, tree: tree, removed: true}
	if _, err := Publish(context.Background(), g, c, o); err == nil || g.writes != 0 {
		t.Fatal("removed label allowed writes")
	}
}

func TestDefaultBranchAdvanceKeepsPinnedParent(t *testing.T) {
	o, c, tree := repo(t)
	for _, status := range []string{"ahead", "diverged", "behind"} {
		g := &githubFixture{o: o, tree: tree, targetSHA: strings.Repeat("c", 40), comparisonStatus: status}
		_, err := Publish(context.Background(), g, c, o)
		if (err == nil) != (status == "ahead") || (status != "ahead" && g.writes != 0) {
			t.Fatal(status, err, g.writes)
		}
	}
}
