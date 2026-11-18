import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import hosted_evidence


class HostedEvidenceTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.pilot = self.root / "pilot"
        self.pilot.mkdir()
        self.archive = self.root / "archive"

    def file(self, relative, data):
        path = self.pilot / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return path

    def test_stages_product_evidence_and_retains_provider_cost(self):
        evidence = b"browser verified folder navigation\n"
        self.file("result/report.json", b'{"status":"author-reported-fixed"}\n')
        self.file("result/provider-usage.json", b'{"reported_cost_usd":0.02}\n')
        self.file("result/config.json", b'{"task_id":"qip-878","workflow":"fix-issue","environment":{"OPENROUTER_API_KEY":"private"}}\n')
        self.file("result/step-0001-author/process.json", b"{}\n")
        self.file("result/step-0001-author/profile.json", b"{}\n")
        self.file("result/step-0001-author/response-raw.txt", b"Outcome: fixed\n")
        self.file("result/step-0001-author/stdout.jsonl", b"full native stream\n")
        self.file("result/evidence/browser/baseline.txt", evidence)
        self.file("preparation/stand.log", b"kind stand ready\n")
        self.file("preparation/extra.log", b"outside intended logs\n")
        hosted_evidence.stage(self.pilot, self.archive)
        manifest = json.loads((self.archive / "manifest.json").read_text())
        files = {file["path"]: file for file in manifest["files"]}
        self.assertEqual(manifest["reported_status"], "author-reported-fixed")
        self.assertEqual(files["result/evidence/browser/baseline.txt"]["sha256"], hashlib.sha256(evidence).hexdigest())
        self.assertEqual(json.loads((self.archive / "result/provider-usage.json").read_text())["reported_cost_usd"], 0.02)
        self.assertEqual(set(json.loads((self.archive / "result/config.json").read_text())), {"task_id", "workflow"})
        self.assertIn("result/step-0001-author/process.json", files)
        self.assertIn("preparation/stand.log", files)
        self.assertNotIn("preparation/extra.log", files)
        self.assertNotIn("result/step-0001-author/stdout.jsonl", files)

    def test_omits_links_auth_homes_databases_and_named_pipes(self):
        outside = self.root / "outside"
        outside.mkdir()
        secret = outside / "secret.txt"
        secret.write_text("private")
        folder = self.pilot / "result/evidence"
        folder.mkdir(parents=True)
        (folder / "file-link.txt").symlink_to(secret)
        (folder / "directory-link").symlink_to(outside, target_is_directory=True)
        (self.pilot / "preparation").symlink_to(outside, target_is_directory=True)
        os.link(secret, folder / "hard-link.txt")
        os.mkfifo(folder / "pipe.txt")
        self.file("result/author-home/credentials.json", b"private")
        self.file("result/evidence/author-home/tokens.txt", b"private")
        self.file("result/evidence/opencode.db", b"sqlite")
        self.file("result/evidence/renamed.txt", b"SQLite format 3\x00" + bytes(4096))
        hosted_evidence.stage(self.pilot, self.archive)
        self.assertEqual(sorted(file.name for file in self.archive.iterdir()), ["manifest.json"])
        manifest = json.loads((self.archive / "manifest.json").read_text())
        self.assertEqual(manifest["omissions"]["non_regular_or_linked"]["count"], 4)
        self.assertEqual(manifest["omissions"]["unsafe_directory"]["count"], 1)
        self.assertEqual(manifest["omissions"]["private_path"]["count"], 2)
        self.assertEqual(manifest["omissions"]["sqlite"]["count"], 1)

    def test_rejects_symlinked_source_and_destination_parents(self):
        link = self.root / "linked"
        link.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(OSError):
            hosted_evidence.stage(link / "pilot", self.archive)
        with self.assertRaises(OSError):
            hosted_evidence.stage(self.pilot, link / "archive-two")
        self.assertFalse((self.root / "archive-two").exists())

    def test_bounds_files_report_and_total_bytes(self):
        self.file("result/report.json", b"unterminated report")
        self.file("result/evidence/huge.txt", b"a" * 65)
        for name in ("first.txt", "second.txt"):
            self.file(f"result/evidence/{name}", b"b" * 40)
        with patch.multiple(hosted_evidence, MAX_FILE_BYTES=64, MAX_ARCHIVE_BYTES=60):
            hosted_evidence.stage(self.pilot, self.archive)
        manifest = json.loads((self.archive / "manifest.json").read_text())
        self.assertIsNone(manifest["reported_status"])
        self.assertEqual(len(manifest["files"]), 1)
        self.assertEqual(manifest["omissions"]["size_limit"]["count"], 2)
        self.assertEqual(manifest["omissions"]["invalid_json"]["count"], 1)


if __name__ == "__main__":
    unittest.main()
