import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pre_ready


class IntakeTest(unittest.TestCase):
    def setUp(self):
        self.issue = dict(number=12, state="open", labels=[], comments=3, title="Nested imports", body="Observed behavior")

    def test_skip_before_call(self):
        for change in [dict(state="closed"), dict(labels=[dict(name="ready-for-agent")]), dict(pull_request={})]:
            packet, reason = pre_ready.prepare({**self.issue, **change}, "12", "org/repo")
            self.assertIsNone(packet)
            self.assertTrue(reason)

    def test_summary_never_recommends_from_a_stale_snapshot(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            (base / "issue.json").write_text(json.dumps(self.issue))
            (base / "current.json").write_text(json.dumps({**self.issue, "body": "Different requirements"}))
            (base / "jev").mkdir()
            (base / "jev/report.json").write_text(json.dumps(dict(version="test", duration_ms=15, response=dict(id="test-456"), selection=dict(pre_ready_action="ready"))))
            summary = base / "summary.md"
            with patch.dict(os.environ, dict(ISSUE_NUMBER="12", GITHUB_REPOSITORY="org/repo",
                                GITHUB_EVENT_NAME="workflow_dispatch",
                                ENGINEER_DECISION="diagnose", GITHUB_STEP_SUMMARY=str(summary))), patch.object(sys, "argv", ["pilot", "summary", tmp]):
                pre_ready.main()
            self.assertIn("changed", summary.read_text())
            self.assertNotIn("Jev recommendation", summary.read_text())
            self.assertIn("test-456", summary.read_text())
            self.assertIn("Comments excluded from this snapshot: 3", summary.read_text())
            self.assertIn("Snapshot taken at ", summary.read_text())

    def test_request_metadata_and_unread_discussion(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            (base / "issue.json").write_text(json.dumps(self.issue))
            (base / "current.json").write_text(json.dumps({**self.issue, "comments": 4}))
            (base / "jev").mkdir()
            report = {"version": "pilot", "duration_ms": 11,
                      "selection": {"pre_ready_action": "request_info", "missing_information": "access"},
                      "response": {"id": "request-42", "usage": {"cost": 0.00004}}}
            (base / "jev/report.json").write_text(json.dumps(report))
            summary = base / "summary.md"
            env = dict(ISSUE_NUMBER="12", GITHUB_REPOSITORY="org/repo", ENGINEER_DECISION="diagnose",
                       GITHUB_EVENT_NAME="workflow_dispatch",
                       GITHUB_STEP_SUMMARY=str(summary))
            with patch.dict(os.environ, env), patch.object(sys, "argv", ["pilot", "summary", tmp]):
                pre_ready.main()
            result = summary.read_text()
            self.assertIn("now has 4 comments", result)
            self.assertIn("request-42", result)
            self.assertIn("Missing information: `access`", result)
            self.assertIn("Jev recommendation: **Ask for missing information**", result)
            self.assertIn("Engineer assessment before this run: **Start with a bounded investigation**.", result)

    def test_automatic_summary_uses_no_engineer_assessment(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            for name in ("issue.json", "current.json"):
                (base / name).write_text(json.dumps(self.issue))
            (base / "jev").mkdir()
            report = {"version": "pilot", "duration_ms": 11, "selection": {"pre_ready_action": "ready"},
                      "response": {"id": "request-42\nUnexpected markup", "usage": {"cost": 0.00004}}}
            (base / "jev/report.json").write_text(json.dumps(report))
            summary = base / "summary.md"
            env = dict(ISSUE_NUMBER="12", GITHUB_REPOSITORY="org/repo", GITHUB_EVENT_NAME="issues",
                       GITHUB_STEP_SUMMARY=str(summary))
            with patch.dict(os.environ, env, clear=True), patch.object(sys, "argv", ["pilot", "summary", tmp]):
                pre_ready.main()
            result = summary.read_text()
            self.assertIn("Automatic triage for a newly opened issue.", result)
            self.assertIn("Jev recommendation: **Ready for implementation**.", result)
            self.assertNotIn("Engineer assessment", result)
            self.assertNotIn(self.issue["body"], result)
            self.assertNotIn("Unexpected markup", result)
            self.assertEqual((base / "recommendation.md").read_text(), result)

    def test_ready_issue_emits_ineligible_output_without_classifier_packet(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            issue = {**self.issue, "labels": [{"name": "ready-for-agent"}]}
            (base / "issue.json").write_text(json.dumps(issue))
            output = base / "output"
            summary = base / "summary.md"
            env = dict(ISSUE_NUMBER="12", GITHUB_REPOSITORY="org/repo", GITHUB_EVENT_NAME="issues",
                       GITHUB_OUTPUT=str(output), GITHUB_STEP_SUMMARY=str(summary))
            with patch.dict(os.environ, env, clear=True):
                with patch.object(sys, "argv", ["pilot", "prepare", tmp]):
                    pre_ready.main()
                with patch.object(sys, "argv", ["pilot", "summary", tmp]):
                    pre_ready.main()
            self.assertEqual(output.read_text(), "eligible=false\n")
            self.assertFalse((base / "packet.json").exists())
            self.assertIn("The issue already has the ready-for-agent label.", summary.read_text())
            self.assertNotIn("Jev recommendation", summary.read_text())

    def test_id_validation(self):
        with self.assertRaises(ValueError):
            pre_ready.prepare(self.issue, "../12", "org/repo")
