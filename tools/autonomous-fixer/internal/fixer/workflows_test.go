package fixer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowsBuildGoBeforeTokenUse(t *testing.T) {
	for _, name := range []string{"control", "work", "publish"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".github", "workflows", "agent-"+name+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			for _, required := range []string{"permissions: {}", "persist-credentials: false", "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", "go-version-file: tools/autonomous-fixer/go.mod", "cache: false", "GOTOOLCHAIN: local", "working-directory: tools/autonomous-fixer", "go build -trimpath -o \"$RUNNER_TEMP/qip-agent\" ./cmd/agent", "AGENT_MODE: ${{ vars.AGENT_MODE || 'fake' }}", "group: qip-agent-control", "queue: max", "cancel-in-progress: false"} {
				if !strings.Contains(source, required) {
					t.Errorf("missing %q", required)
				}
			}
			for _, forbidden := range []string{"node tools/", "npm ", "AGENT_MODE: live", "pull_request_target", "contents: write"} {
				if strings.Contains(source, forbidden) {
					t.Errorf("unexpected %q", forbidden)
				}
			}
			if strings.Index(source, "name: Build agent") > strings.Index(source, "GITHUB_TOKEN:") {
				t.Fatal("token use precedes agent build")
			}
			if !strings.Contains(source, "          AGENT_VARIABLES_READ_TOKEN:") || strings.Contains(source, "AGENT_ALLOWED_ACTORS:") {
				t.Fatal("the workflow must read current variables in the agent step")
			}
			if name == "control" {
				for _, required := range []string{".github/workflows/agent-*.yaml", ".github/workflows/agent-*.yml", ".github/workflows/pr-linked-issue.yaml", ".github/workflows/pr-linked-issue.yml", "python3 -B -W error -m unittest discover", "gofmt -l .", "go vet ./...", "go test -race ./...", "qip-agent\" route", "qip-agent\" control"} {
					if !strings.Contains(source, required) {
						t.Errorf("missing %q", required)
					}
				}
			}
			if name == "work" {
				for _, required := range []string{"group: qip-agent-work", "qip-agent\" admit", "qip-agent\" work", "needs.admit.outputs.admitted == 'true'", "INPUT_TRUSTED_SHA:", "if: inputs.mode == 'fake'", "if: inputs.mode == 'pilot'"} {
					if !strings.Contains(source, required) {
						t.Errorf("missing %q", required)
					}
				}
				if strings.Count(source, "name: Build agent") != 2 {
					t.Fatal("each work job must build its binary")
				}
				for _, setup := range []string{"actions/setup-node@", "actions/setup-java@", "name: Prepare the product environment", "name: Run the author and reviewer"} {
					_, rest, found := strings.Cut(source, setup)
					step, _, _ := strings.Cut(rest, "\n      - ")
					if !found || !strings.Contains(step, "\n        if: inputs.mode == 'pilot'") {
						t.Errorf("product step %q must run only in pilot mode", setup)
					}
				}
				_, author, _ := strings.Cut(source, "- name: Run the author and reviewer")
				author, _, _ = strings.Cut(author, "- name: Stage pilot evidence")
				if !strings.Contains(author, "if: inputs.mode == 'pilot'") || !strings.Contains(author, "OPENROUTER_API_KEY:") || strings.Contains(author, "github.token") || strings.Contains(author, "AGENT_VARIABLES_READ_TOKEN") || strings.Contains(source, "AGENT_PUBLICATION_TOKEN") {
					t.Fatal("pilot execution must receive only its model credential")
				}
			}
			if name == "publish" && (!strings.Contains(source, "workflow_call:") || !strings.Contains(source, "steps.authorize.outputs.process == 'true'") || strings.Contains(source, "OPENROUTER_API_KEY")) {
				t.Fatal("publication requires the trusted authorization step and no model credentials")
			}
		})
	}
}
