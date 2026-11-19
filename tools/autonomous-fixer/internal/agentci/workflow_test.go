package agentci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestObserverFollowsOrdinaryPullRequestWorkflows(t *testing.T) {
	directory := filepath.Join("..", "..", "..", "..", ".github", "workflows")
	data, err := os.ReadFile(filepath.Join(directory, "agent-ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, triggers, ok := strings.Cut(string(data), "    workflows:\n")
	if !ok {
		t.Fatal("observer has no explicit workflow allowlist")
	}
	triggers, _, _ = strings.Cut(triggers, "    types: [completed]\n")
	allowed := make(map[string]bool)
	for line := range strings.SplitSeq(triggers, "\n") {
		if name, ok := strings.CutPrefix(line, "      - "); ok {
			allowed[name] = true
		}
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	pullRequestTrigger := regexp.MustCompile(`(?m)^  (pull_request|pull_request_target|pull_request_review):`)
	workflowName := regexp.MustCompile(`(?m)^name: (.+)$`)
	for _, file := range files {
		if ext := filepath.Ext(file.Name()); ext != ".yaml" && ext != ".yml" {
			continue
		}
		switch file.Name() {
		case "agent-ci.yaml", "agent-docs-reconcile-merge.yaml", "agent-outcome.yaml":
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !pullRequestTrigger.Match(contents) {
			continue
		}
		match := workflowName.FindSubmatch(contents)
		if len(match) != 2 {
			t.Errorf("missing name in %s", file.Name())
			continue
		}
		name := strings.Trim(string(match[1]), `"'`)
		if !allowed[name] {
			t.Errorf("observer does not follow PR workflow %q in %s", name, file.Name())
		}
		delete(allowed, name)
	}
	for name := range allowed {
		t.Errorf("observer follows workflow %q outside ordinary PR CI", name)
	}
}
