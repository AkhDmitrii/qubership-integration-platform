"""Prepare a GitHub issue for pre-ready triage and write its Actions summary."""
import hashlib
import json
import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path

ACTIONS = {
    "ready": "Ready for implementation",
    "diagnose": "Start with a bounded investigation",
    "request_info": "Ask for missing information",
    "uncertain": "Engineer judgment needed",
}
MISSING = {
    "expected_behavior": "Which observable behavior should the fix preserve or change?",
    "integration_contract": "Which external contract or downstream implementation can we inspect?",
    "reproduction_context": "Which input or setup produced the reported symptom?",
    "access": "How can we access the required environment or source?",
}


def prepare(issue, number, repo):
    if not re.fullmatch(r"[1-9][0-9]*", number) or issue["number"] != int(number):
        raise ValueError("Issue number differs from the requested number")
    if "pull_request" in issue:
        return None, "The selected number belongs to a pull request."
    if issue["state"] != "open":
        return None, "The issue is closed."
    if any(label["name"] == "ready-for-agent" for label in issue["labels"]):
        return None, "The issue already has the ready-for-agent label."
    return {"task_id": f"{repo}#{number}", "phase": "pre-ready",
            "task": issue["title"] + "\n\n" + (issue["body"] or "")}, None


def main():
    mode, folder = sys.argv[1:]
    root = Path(folder)
    issue = json.loads((root / "issue.json").read_text())
    number = os.environ["ISSUE_NUMBER"]
    packet, skip = prepare(issue, number, os.environ["GITHUB_REPOSITORY"])
    basis = json.dumps(packet, ensure_ascii=False, sort_keys=True).encode()
    digest = hashlib.sha256(basis).hexdigest()
    if mode == "prepare":
        if packet:
            (root / "packet.json").write_bytes(basis)
        with open(os.environ["GITHUB_OUTPUT"], "a") as f:
            print(f"eligible={'true' if packet else 'false'}", file=f)
        return
    if mode != "summary":
        raise ValueError("Unknown operation")
    human = os.environ["ENGINEER_DECISION"]
    if human not in ACTIONS:
        raise ValueError("Unknown engineer decision")
    lines = [f"## Pre-ready triage for issue #{number}", ""]
    if skip:
        lines.append(skip)
    else:
        captured = datetime.fromtimestamp((root / "issue.json").stat().st_mtime, timezone.utc)
        comments = issue["comments"]
        lines += [f"Engineer assessment before this run: **{ACTIONS[human]}**.",
                  f"Snapshot taken at {captured.isoformat(timespec='seconds')}.",
                  f"Title and description snapshot: `{digest}`.",
                  f"Comments excluded from this snapshot: {comments}. Attachment contents were not inspected.", ""]
        report = root / "jev" / "report.json"
        current = root / "current.json"
        data = json.loads(report.read_text()) if report.exists() else {}
        response = data.get("response") or {}
        if data:
            request_id = response.get("id")
            if request_id:
                lines.append(f"Provider request: `{request_id}`.")
            cost = (response.get("usage") or {}).get("cost")
            lines.append(f"Provider cost: ${cost:.8f}." if isinstance(cost, (int, float)) else "Provider cost unavailable.")
            lines.append(f"Policy: `{data['version']}`. Classifier time: {data['duration_ms']} ms.")
        if current.exists():
            current_issue = json.loads(current.read_text())
            latest, reason = prepare(current_issue, number, os.environ["GITHUB_REPOSITORY"])
            if current_issue["comments"] != comments:
                lines.append(f"Discussion changed during triage. The issue now has {current_issue['comments']} comments.")
        else:
            latest, reason = None, "The workflow could not recheck the issue."
        if latest != packet or reason:
            lines.append("The issue changed or could not be rechecked. Run triage again before using this result.")
        elif not data:
            lines.append("No recommendation is available. Check the classification step.")
        else:
            if data.get("error"):
                lines.append("The classification failed. An engineer can still triage this issue.")
            else:
                choice = data["selection"]["pre_ready_action"]
                lines += [f"Jev recommendation: **{ACTIONS[choice]}**."]
                missing = data["selection"].get("missing_information")
                if choice == "request_info" and missing in MISSING:
                    lines.append(f"Missing information: `{missing}`.")
                    lines.append("Suggested clarification: " + MISSING[missing])
        lines += ["", "Review any comments or attachments before assigning work."]
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as f:
        print("\n".join(lines), file=f)


if __name__ == "__main__":
    main()
