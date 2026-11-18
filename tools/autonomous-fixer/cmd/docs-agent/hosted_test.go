package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/agentsignature"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/docgithub"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/docpacket"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/doctriage"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/jev"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestHostedDocumentationHandoffRestoresFreshCandidate(t *testing.T) {
	t.Run("patch", func(t *testing.T) { checkHostedHandoff(t, true) })
	t.Run("automatic-no-change", func(t *testing.T) { checkHostedHandoff(t, false) })
}

func checkHostedHandoff(t *testing.T, needed bool) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("OPENROUTER_API_KEY", "fixture-openrouter-key")
	t.Setenv("GITHUB_TOKEN", "fixture-github-token")
	source := filepath.Join(root, "source")
	workspace := filepath.Join(root, "candidate")
	prepared := filepath.Join(root, "prepared")
	output := filepath.Join(root, "output")
	handoff := filepath.Join(root, "handoff")
	for name, content := range map[string]string{
		"AGENTS.md":                                       "Follow project conventions.\n",
		".agents/skills/docs-authoring/SKILL.md":          "Document shipped behavior.\n",
		".agents/skills/english-developer-style/SKILL.md": "Write for developers.\n",
		"help/README.md":                                  "# Help\n",
		"help/docs/readme.md":                             "# Help\n\nOld instruction.\n",
	} {
		writeFixtureFile(t, filepath.Join(source, name), content, 0600)
	}
	for _, args := range [][]string{{"init", "-q", "--initial-branch=main"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-qm", "baseline"}} {
		if err := git(ctx, source, args...); err != nil {
			t.Fatal(err)
		}
	}
	base, err := exec.Command("git", "-C", source, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(base))
	plan := docgithub.Plan{
		Version: "docs-plan-v2", IssueState: "closed", Repository: "org/repo", IssueNumber: 1, IssueNodeID: "ISSUE",
		IssueTitle: "Document behavior", IssueBodyHash: docgithub.Digest("Issue body"), DefaultBranch: "main", BaseSHA: sha,
		Project:         docgithub.Project{ID: "PROJECT", Number: 12, ItemID: "ITEM", StatusFieldID: "FIELD", ReviewOptionID: "REVIEW", DoneOptionID: "DONE", ReviewStatus: "In doc review", DoneStatus: "Done"},
		ImplementingPRs: []docpacket.PR{{Number: 2, MergeSHA: sha, Target: "main"}},
	}
	issue, err := json.Marshal(docpacket.Issue{Number: 1, Title: plan.IssueTitle, Body: "Issue body", PRs: plan.ImplementingPRs})
	if err != nil {
		t.Fatal(err)
	}
	packet := doctriage.Packet{
		ID: "issue-1", Scope: "Complete issue change", Repository: plan.Repository, Issue: issue,
		BaseSHA: sha, HeadSHA: sha, DocsSHA: sha, ChangedPaths: []string{"engine/file.go"},
		Diff: "implementation diff", Documentation: []doctriage.Document{{Path: "help/docs/readme.md", Content: "Old instruction."}}, Complete: true,
	}
	plan.PacketSHA256 = docgithub.Digest(packet)
	plan.ID = docgithub.Digest(plan)
	writeFixtureFile(t, filepath.Join(prepared, "task.md"), "Update the outdated instruction.\n", 0600)
	if err := save(filepath.Join(prepared, "plan.json"), plan); err != nil {
		t.Fatal(err)
	}
	if err := save(filepath.Join(prepared, "packet.json"), packet); err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 8)
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	http.DefaultTransport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "openrouter.ai" || r.Header.Get("Authorization") != "Bearer fixture-openrouter-key" {
			return nil, fmt.Errorf("invalid upstream request")
		}
		defer r.Body.Close()
		var body jev.Request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		var data []byte
		switch r.URL.Path {
		case "/api/alpha/decisions":
			choices := map[string]string{"documentation_action": "no_change", "documentation_reason": "outside_help_scope", "change_attribution": "unchanged"}
			if needed {
				choices = map[string]string{"documentation_action": "needed", "documentation_reason": "stale_guidance", "change_attribution": "change_related"}
			}
			answers := make(map[string]jev.Answer)
			for name, question := range body.Questions {
				probabilities := make(map[string]float64)
				for option := range question.Criteria {
					probabilities[option] = 0
				}
				probabilities[choices[name]] = 1
				answers[name] = jev.Answer{Type: "choice", Choice: choices[name], Probabilities: probabilities}
			}
			data, _ = json.Marshal(map[string]any{"id": "decision", "model": body.Model, "answers": answers, "usage": map[string]any{"output_tokens": 30, "cost": .004}})
		case "/api/v1/chat/completions":
			data = fmt.Appendf(nil, `{"id":"native-%d","model":%q,"usage":{"prompt_tokens":100,"completion_tokens":10,"cost":0.001,"prompt_tokens_details":{"cached_tokens":0}}}`, len(requests), body.Model)
		default:
			return nil, fmt.Errorf("unexpected OpenRouter path %s", r.URL.Path)
		}
		requests <- body.Model
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})
	runtime := filepath.Join(root, "opencode")
	writeFixtureFile(t, runtime, hostedRuntimeFixture, 0700)
	if err := run(ctx, []string{
		"work", "--hosted", "--prepared", prepared, "--source", source, "--workspace", workspace, "--output", output,
		"--runtime", runtime, "--runtime-version", "opencode v2.2.6", "--model", "openrouter/openai/author", "--effort", "high",
		"--review-model", "openrouter/openai/reviewer", "--review-effort", "medium", "--handoff", handoff,
	}); err != nil {
		t.Fatal(err)
	}
	models := []string{jev.DefaultModel}
	if needed {
		models = append(models, "openai/author", "openai/reviewer", "openai/author")
	}
	for _, want := range models {
		select {
		case got := <-requests:
			if got != want {
				t.Fatalf("requested %s, want %s", got, want)
			}
		default:
			t.Fatalf("missing OpenRouter request for %s", want)
		}
	}
	if len(requests) != 0 {
		t.Fatalf("unexpected extra model requests: %d", len(requests))
	}
	expected := []string{"feedback.json"}
	excluded := []string{"work/step-0001-docs-write/home", "work/step-0001-docs-write/stdout.jsonl", "work/evidence/step-0001-docs-write/unlisted"}
	if needed {
		expected = append(expected, "work/candidate-files/help/docs/readme.md", "work/provider-usage.json", "work/step-0002-docs-review/observed-agent.json")
	} else {
		excluded = append(excluded, "work/candidate-files", "work/provider-usage.json")
	}
	for _, name := range expected {
		if _, err := os.Stat(filepath.Join(handoff, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range excluded {
		if _, err := os.Lstat(filepath.Join(handoff, name)); !os.IsNotExist(err) {
			t.Fatalf("private or unlisted file in handoff: %s (%v)", name, err)
		}
	}
	if err := git(ctx, source, "worktree", "remove", "--force", workspace); err != nil {
		t.Fatal(err)
	}
	publisherWorkspace := filepath.Join(root, "publisher")
	publishedRun := filepath.Join(handoff, "work")
	if err := restore(ctx, prepared, publishedRun, source, publisherWorkspace); err != nil {
		t.Fatal(err)
	}
	patched, err := os.ReadFile(filepath.Join(publisherWorkspace, "help/docs/readme.md"))
	wantPage, wantSignature := "# Help\n\nOld instruction.\n", "<sub>🤖 typesafe/jev-1.14 out=30 usd=0.004</sub>"
	if needed {
		wantPage, wantSignature = "# Help\n\nUpdated instruction.\n", "<sub>🤖 openai/author high out=60 usd=0.007</sub>"
	}
	if err != nil || string(patched) != wantPage {
		t.Fatal(string(patched), err)
	}
	signature, err := agentsignature.ReadRun(publishedRun)
	if err != nil || signature.Line() != wantSignature {
		t.Fatal(signature.Line(), err)
	}
	leaked := filepath.Join(output, "task/task.md")
	writeFixtureFile(t, leaked, "fixture-openrouter-key", 0600)
	rejected := filepath.Join(root, "rejected-handoff")
	if err := exportHandoff(ctx, plan, output, publisherWorkspace, rejected); err == nil || !strings.Contains(err.Error(), "controller credential") {
		t.Fatal("handoff accepted a controller credential", err)
	}
	if _, err := os.Lstat(rejected); !os.IsNotExist(err) {
		t.Fatal("rejected handoff left transferable files", err)
	}
}

func writeFixtureFile(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

const hostedRuntimeFixture = `#!/usr/bin/env python3
import json, os, sqlite3, sys, urllib.request
from pathlib import Path

assert 'OPENROUTER_API_KEY' not in os.environ
assert 'GITHUB_TOKEN' not in os.environ
if sys.argv[1] == '--version':
    print('opencode v2.2.6')
    sys.exit(0)

model, variant = sys.argv[sys.argv.index('--model') + 1].split('#')
prompt = Path(sys.argv[sys.argv.index('--file') + 1]).read_text()
invocation = json.loads(prompt.split('Controller input:\n', 1)[1].split('\n\nSource base commit:', 1)[0])
stage = invocation['stage']
config = json.loads(os.environ['OPENCODE_CONFIG_CONTENT'])
assert config['experimental']['subagent_depth'] == 0
assert {'action': 'subagent', 'resource': '*', 'effect': 'deny'} in config['permissions']
provider = config['providers']['openrouter']['settings']
assert provider['baseURL'].startswith('http://127.0.0.1:')
assert provider['apiKey'] == 'qip-local-provider-relay'
remote_model = model.removeprefix('openrouter/')
request = urllib.request.Request(provider['baseURL'] + '/chat/completions', data=json.dumps({'model': remote_model}).encode(),
    headers={'Authorization': 'Bearer ' + provider['apiKey'], 'Content-Type': 'application/json'})
with urllib.request.urlopen(request, timeout=10) as reply:
    assert json.load(reply)['model'] == remote_model

db_path = Path(os.environ['XDG_DATA_HOME']) / 'opencode/opencode.db'
db_path.parent.mkdir(parents=True)
db = sqlite3.connect(db_path)
db.executescript('CREATE TABLE session_v2(id TEXT, parent_id TEXT); CREATE TABLE session_message(id INTEGER, session_id TEXT, type TEXT, data TEXT);')
db.execute("INSERT INTO session_v2 VALUES ('fixture', NULL)")
db.execute("INSERT INTO session_message VALUES (1, 'fixture', 'assistant', ?)", [json.dumps({'model': {'providerID': 'openrouter', 'id': remote_model, 'variant': variant}})])
db.commit()
db.close()

home = Path(os.environ['HOME'])
evidence = home.parent.parent / 'evidence' / home.parent.name
proof = evidence / 'proof.txt'
proof.write_text('Checked the documentation against the implementation.\n')
(evidence / 'unlisted').write_text('fixture-openrouter-key')
refs = [str(proof)]
doc = {'decision': 'needed', 'reason': 'stale_guidance', 'summary': 'Update the outdated instruction.',
       'claims': [{'id': 'instruction', 'behavior': 'Reader instruction', 'attribution': 'change_related',
                   'needs_change': True, 'source_evidence': refs, 'documentation_evidence': refs, 'action': 'Update the existing page.'}],
       'pr_title': 'docs: update the instruction', 'pr_body': 'Document the shipped behavior. Related #1'}
response = {'evidence': refs}
if stage == 'docs-write':
    Path('help/docs/readme.md').write_text('# Help\n\nUpdated instruction.\n')
    response['documentation'] = doc
if stage == 'docs-verify':
    response['checks'] = [{'id': check['id'], 'outcome': 'pass', 'evidence': refs} for check in invocation['request']['required_checks']]
print(json.dumps({'type': 'text', 'sessionID': 'fixture', 'part': {'type': 'text', 'messageID': 'fixture', 'text': json.dumps(response)}}))
`
