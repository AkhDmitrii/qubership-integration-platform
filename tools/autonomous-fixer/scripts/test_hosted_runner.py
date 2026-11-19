import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from urllib.request import Request

import hosted_issue


SCRIPTS = Path(__file__).resolve().parent
REPO = "Netcracker/qubership-integration-platform"
IMAGE = "https://github.com/user-attachments/assets/3ade1d33-b370-43be-adac-b98b2c276ce1"


def admitted_issue(body=None, sha="a" * 40, legacy=False):
    title = "Navigation keeps the selected folder"
    issue = {"number": 878, "state": "open", "title": title, "body": body, "labels": [{"name": "ready-for-agent"}]}
    requirements = f"{title}\n\n{body or ''}"
    contract = {"baseSha": sha, "scopePaths": ["ui"]}
    if legacy:
        contract["requirements"] = requirements
    else:
        contract["requirementsSha256"] = hashlib.sha256(requirements.encode("utf-8")).hexdigest()
    checkpoint = {"issueNumber": 878, "taskId": "qip-878", "contract": contract}
    return checkpoint, issue


class HostedIssueTest(unittest.TestCase):
    def test_captures_only_github_attachments_and_preserves_the_issue(self):
        body = f"![Preview]({IMAGE})\n{IMAGE}\n[local](https://127.0.0.1/a.png)"
        checkpoint, issue = admitted_issue(body)
        calls = []

        def fetch(url, repository, token):
            calls.append((url, repository, token))
            return "image/png", b"example PNG data"

        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "assignment"
            hosted_issue.snapshot(checkpoint, issue, REPO, directory, "read-token", fetch)
            self.assertEqual(calls, [(IMAGE, REPO, "read-token")])
            self.assertIn(f"{issue['title']}\n\n{body}", (directory / "task.md").read_text())
            self.assertEqual((directory / "attachment-01.png").read_bytes(), b"example PNG data")
            self.assertNotIn("read-token", (directory / "attachments.json").read_text())

    def test_rejects_stale_issue_before_fetching(self):
        checkpoint, issue = admitted_issue(IMAGE)
        issue["body"] += "\nUpdated after admission."
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "assignment"
            with self.assertRaisesRegex(ValueError, "changed since admission"):
                hosted_issue.snapshot(checkpoint, issue, REPO, directory, None, self.fail)
            self.assertFalse(directory.exists())

    def test_captures_large_unicode_requirements_from_the_issue(self):
        body = "Последовательность действий.\r\n" * 1800
        checkpoint, issue = admitted_issue(body)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "assignment"
            hosted_issue.snapshot(checkpoint, issue, REPO, directory, None)
            self.assertIn(body.encode("utf-8"), (directory / "task.md").read_bytes())

    def test_reads_a_legacy_checkpoint(self):
        checkpoint, issue = admitted_issue("Previous work", legacy=True)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "assignment"
            hosted_issue.snapshot(checkpoint, issue, REPO, directory, None)
            self.assertIn(checkpoint["contract"]["requirements"], (directory / "task.md").read_text())

    def test_binds_the_authorized_answers_to_the_assignment(self):
        checkpoint, issue = admitted_issue()
        answer = {
            "questionId": "qip-878/question/1",
            "questionVersion": 1,
            "contractVersion": 2,
            "question": "Where does Back return?",
            "answer": "Return to the last folder.\nKeep its selection.",
            "actor": "issue-owner",
            "source": "comment:20",
        }
        checkpoint.update(answers=[answer], contractVersion=2)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "assignment"
            hosted_issue.snapshot(checkpoint, issue, REPO, directory, None)
            self.assertEqual(json.loads((directory / "answers.json").read_text()), [answer])
            self.assertIn("> Return to the last folder.\n> Keep its selection.", (directory / "task.md").read_text())
            checkpoint["contractVersion"] += 1
            with self.assertRaisesRegex(ValueError, "different contract version"):
                hosted_issue.snapshot(checkpoint, issue, REPO, directory, None)

    def test_attachments_leave_a_receipt_on_download_failure(self):
        checkpoint, issue = admitted_issue(IMAGE)

        def unavailable(*args):
            raise OSError("network down")

        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "assignment"
            with self.assertRaisesRegex(ValueError, "attachments are unavailable"):
                hosted_issue.snapshot(checkpoint, issue, REPO, directory, None, unavailable)
            self.assertEqual(json.loads((directory / "attachments.json").read_text()), [{"index": 1, "status": "unavailable"}])
            self.assertFalse((directory / "task.md").exists())

    def test_redirects_strip_authorization_and_reject_unapproved_hosts(self):
        handler = hosted_issue.AttachmentRedirect(REPO)
        request = Request(IMAGE, headers={"Authorization": "Bearer private"})
        redirected = handler.redirect_request(request, None, 302, "", {}, "https://objects.githubusercontent.com/image")
        self.assertIsNone(redirected.get_header("Authorization"))
        with self.assertRaisesRegex(ValueError, "leaves GitHub's asset hosts"):
            handler.redirect_request(request, None, 302, "", {}, "https://github.com@127.0.0.1/x")


class HostedRunnerTest(unittest.TestCase):
    def test_prepares_and_launches_with_separate_credentials(self):
        self.run_fixture("author-reported-fixed", 0)

    def test_releases_preview_when_the_controller_needs_an_owner(self):
        self.run_fixture("needs-human", 1)

    def run_fixture(self, status, exit_code):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            trusted = root / "trusted"
            workspace = root / "candidate"
            pilot = root / "pilot"
            assignment = pilot / "assignment"
            tools = root / "bin"
            downloads = root / "downloads"
            for path in (trusted, workspace, pilot, tools, downloads):
                path.mkdir()

            def file(path, contents, executable=False):
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(contents, encoding="utf-8")
                if executable:
                    path.chmod(0o755)

            def cli(name, contents):
                file(tools / name, "#!/usr/bin/env bash\nset -euo pipefail\n" + contents, executable=True)

            def execute(*argv, cwd=None, expected=0):
                result = subprocess.run(argv, cwd=cwd, env=environment, capture_output=True, text=True, timeout=35)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
                return result.stdout.strip()

            file(downloads / "kind", "#!/bin/sh\nexit 0\n")
            file(downloads / "istioctl", "#!/bin/sh\nexit 0\n", executable=True)
            with tarfile.open(downloads / "istioctl.tar.gz", "w:gz") as archive:
                archive.add(downloads / "istioctl", arcname="istioctl")
            pins = {name: hashlib.sha256((downloads / name).read_bytes()).hexdigest() for name in ("kind", "istioctl.tar.gz")}
            file(trusted / ".github/workflows/_e2e-kind.yaml", f"""
          KIND_VERSION: v0.33.0
          KIND_SHA256: {pins['kind']}
          ISTIO_VERSION: 1.31.1
          ISTIOCTL_SHA256: {pins['istioctl.tar.gz']}
""")
            file(trusted / ".github/maven-settings.xml", "<settings />\n")
            file(trusted / "e2e/k8s/bootstrap-cluster.sh", "set -eu\ntest -z \"${MAVEN_GITHUB_PASSWORD:-}\"\nprintf 'fixture' > \"$KUBECONFIG\"\n")
            provisioner = trusted / "tools/autonomous-fixer/scripts/hosted_provision.mjs"
            file(provisioner, (SCRIPTS / "hosted_provision.mjs").read_text())
            cli("curl", "while [[ $1 != -o ]]; do shift; done\ncp \"$FIXTURE_DOWNLOADS/$2\" \"$2\"\n")
            cli("npm", "test -z \"${MAVEN_GITHUB_PASSWORD:-}\"\n")
            cli("npx", "test -z \"${MAVEN_GITHUB_PASSWORD:-}\"\n")
            cli("opencode", "test -z \"${OPENROUTER_API_KEY:-}\"\necho 'opencode v2.2.6'\n")
            cli("work", """
test "$OPENROUTER_API_KEY" = provider-fixture
test -z "${GH_TOKEN:-}${GITHUB_TOKEN:-}${MAVEN_GITHUB_PASSWORD:-}${GITHUB_ENV:-}"
test "$E2E_PROVISION" = never
jq -e '.workflow == "fix-issue" and .hosted and .environment.CIP_TARGET == "k8s" and (.environment | has("OPENROUTER_API_KEY") | not)' "$1"
result=$(jq -r '.output_directory' "$1")
mkdir "$result"
""" + f"printf '%s\\n' '{json.dumps({'status': status})}' > \"$result/report.json\"\nexit {exit_code}\n")
            file(workspace / "e2e/package.json", '{"type":"module"}\n')
            file(workspace / "e2e/env/provision/check.ts", "export const expected = 'read-fixture';\n")
            file(workspace / "e2e/env/provision/k8s.ts", """
import assert from 'node:assert/strict';
import { expected } from './check.js';
export async function provisionK8s(mode: string) {
  assert.equal(mode, 'auto');
  assert.equal(process.env.MAVEN_GITHUB_PASSWORD, expected);
  assert.equal(process.env.E2E_BUILD, 'all');
  return { fixture: 'ready' };
}
""")
            file(workspace / "e2e/support/schemas-build.ts", "export async function ensureSchemas() {}\n")
            file(workspace / "e2e/support/ui-server.ts", """
import assert from 'node:assert/strict';
import { writeFileSync } from 'node:fs';
export async function ensureUiServer() {
  assert.equal(process.env.OPENROUTER_API_KEY, undefined);
  assert.equal(process.env.GH_TOKEN, undefined);
  assert.ok(Number(process.env.E2E_UI_PREVIEW_OWNER_PID) > 0);
  return { fixture: 'preview' };
}
export async function releaseUiServer(owner: number) {
  assert.equal(process.env.OPENROUTER_API_KEY, undefined);
  assert.equal(owner, Number(process.env.E2E_UI_PREVIEW_OWNER_PID));
  writeFileSync('preview-released', 'yes');
}
""")

            environment = {**os.environ, "PATH": f"{tools}:{os.environ['PATH']}", "FIXTURE_DOWNLOADS": str(downloads),
                           "HOME": str(root / "home"), "JAVA_HOME": str(root / "java"), "GITHUB_RUN_ID": "20",
                           "GITHUB_RUN_ATTEMPT": "1", "MAVEN_GITHUB_USERNAME": "fixture", "MAVEN_GITHUB_PASSWORD": "read-fixture"}
            execute("git", "init", "-q", cwd=workspace)
            execute("git", "add", ".", cwd=workspace)
            execute("git", "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture", "commit", "-qm", "Fixture", cwd=workspace)
            sha = execute("git", "rev-parse", "HEAD", cwd=workspace)
            checkpoint, issue = admitted_issue(sha=sha)
            hosted_issue.snapshot(checkpoint, issue, REPO, assignment, None)

            execute("bash", SCRIPTS / "hosted_prepare.sh", trusted, workspace, pilot)
            runner = (pilot / "runner.json").read_text()
            self.assertIn("kind-qip-20-1", runner)
            self.assertNotIn("read-fixture", runner)
            self.assertEqual((pilot / "kubeconfig").read_text(), "fixture")
            environment.update(OPENROUTER_API_KEY="provider-fixture", GH_TOKEN="must-not-leak", GITHUB_TOKEN="must-not-leak",
                               QIP_PILOT_MODEL="openrouter/openai/gpt-6-luna", QIP_PILOT_REVIEW_MODEL="openrouter/openai/gpt-6-luna")
            execute("python3", SCRIPTS / "hosted_launch.py", workspace, assignment, pilot, tools / "work", tools / "opencode", expected=exit_code)
            self.assertEqual(json.loads((pilot / "result/report.json").read_text())["status"], status)
            self.assertEqual((workspace / "preview-released").read_text(), "yes")
            self.assertNotIn("provider-fixture", (pilot / "launcher-home/config.json").read_text())


if __name__ == "__main__":
    unittest.main()
