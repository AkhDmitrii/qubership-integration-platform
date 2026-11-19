# Engineering task routing with Jev

## Pre-ready issue advice

Set the repository variable `AGENT_PRE_READY_ON_OPEN` to `true` to run **QIP issue triage** when an issue opens. Any issue author can trigger an automatic run. The workflow reads the issue from GitHub and uses code from the default branch.

For a manual run, select **QIP issue triage** in GitHub Actions on the default branch. Enter the issue number and record your assessment before reading Jev's recommendation. Set `AGENT_ALLOWED_ACTORS` to a comma-separated list of GitHub logins. The manual run checks both the original operator and the user requesting a rerun against that list.

Both modes use the repository secret `OPENROUTER_API_KEY` and make one classifier request per eligible run. The request uses the issue title and description. Pull requests, closed issues, and issues labeled `ready-for-agent` are ineligible. A rerun can incur another charge. The workflow uses a read-only GitHub token. Engineers decide whether to assign work or ask for more information.

Read the run summary or download the `pre-ready-triage` artifact. It includes the recommendation, the snapshot time and hash, unread comment count, policy version, provider request ID and cost when available, and classification time. Manual runs also show the recorded engineer assessment. If the issue changes during the run, repeat triage on the new snapshot. Inspect comments and attachments before acting on a recommendation.

For the pilot, sample consecutive eligible issues and keep disagreements, abstentions and failed calls. After the engineer assigns a task, check whether the author actually needed the predicted information. Track useful early questions and ready issues that the recommendation would have delayed.

Run this check before adding `ready-for-agent`. Save the issue as it appeared at intake, with its title and body and any access facts available at the time. Leave later comments, fixes, labels, and expected classification out of the packet. This example converts a GitHub CLI snapshot to a packet:

```bash
gh issue view <issue> --json number,title,body > issue-at-intake.json
jq '{task_id: (.number | tostring), phase: "pre-ready", task: (.title + "\n\n" + (.body // ""))}' \
  issue-at-intake.json > pre-ready.json
go -C tools/autonomous-fixer run ./cmd/jev-task \
  --packet /absolute/path/pre-ready.json \
  --output /absolute/path/new-output \
  --live --key-file /absolute/path/private-key-file
```

Use the packet's `context` field for known access and scope. Include issue text you have permission to send to OpenRouter. The request bounds the issue excerpt to 12000 bytes and the access excerpt to 2000 bytes. It redacts common credential patterns and local paths. The report recommends `ready`, `diagnose`, `request_info`, or `uncertain`. For `request_info` it also identifies a category of missing information. Review the issue before asking for the concrete fact or changing a label. The tool does not write to GitHub.

## Archived engineering experiments

The sections below describe historical experiments. Current engineering runs ignore `engineering_triage` in every workflow. Use the standalone command to study archived packets.

### Review verification observations with Jev

Use the same `verification_scenarios` in both experimental arms. Omit `engineering_triage` in the control. Configure the treatment with:

```json
{
  "engineering_triage": {
    "disable_routing": true,
    "coverage_audit": "recheck"
  }
}
```

The controller requests advice after the verifier supplies complete evidence for the required cases. The request includes explicit case descriptions and bounded, redacted observation text. It excludes raw evidence files, standalone test reports, and task history. Jev compares each reported action, consumer, and result with the requirement. A mismatch at probability 0.70 with a 0.30 margin triggers one fresh, independent verifier recheck limited to the flagged cases. The recheck decides those cases. The controller retains the other observations and does not audit the recheck again. Model errors and uncertainty leave the ordinary verification result in force. Set `coverage_audit` to `shadow` to record predictions without a recheck. Omit the option to skip the audit.

Reports record the Jev request and cost under `evidence/engineering-triage-*.json`. A recheck has its own verifier invocation and native usage. The audit cannot add required cases or decide that a case passes. Pre-ready and coverage advice use the `engineering-advice-v1` policy. Documentation triage keeps its own policy.

## Legacy engineering routing

Jev supplies five engineering recommendations: intake readiness, an initial work focus, relevant modules and skills, additional review topics, and failure categories, and the next action for an unresolved question. Intake can also compare a caller-supplied shortlist of possible duplicate issues. Documentation impact uses the separate [documentation policy](JEV.md).

These recommendations run locally. They do not change labels, reject an issue, close a duplicate, authorize edits, retry a command, or skip reproduction and final verification. The contract author checks the intake suggestions against source and asks for missing information when needed. A documentation recommendation does not move an engineering task into the post-merge documentation lifecycle.

### Configuration used in earlier local experiments

Add this field to the existing `work` configuration:

```json
{
  "engineering_triage": {
    "candidates": [
      {
        "id": "123",
        "text": "Title and relevant description of a possible duplicate."
      }
    ]
  }
}
```

Use an empty object when no duplicate candidates have been retrieved. Set `OPENROUTER_API_KEY` only in the controller process environment. The native Codex child does not inherit that environment variable. Do not put the key in `environment` or a task attachment. The caller supplies the shortlist; the classifier does not search GitHub for duplicate candidates.

The controller requests intake advice before the contract stage. It passes the task and required criteria, checks, and consumers. The controller searches committed source for up to eight code identifiers from the task. Named source files take priority; a test class also contributes its production class name. Results include nearby committed source lines, with implementation files before tests and documentation and at most two distinct files per module. Identifier lookup matches snake_case, kebab-case, and camelCase spellings. It matches identifier boundaries and prefers named definitions and references to the requested operation, such as deletion, over imports, log messages, POM URLs, and unrelated AI notifications. Known shared consumers are recorded in `consumer_hints`: a named UI definition points to its VS Code webview consumer. Incidental UI search hits do not create that hint. The search excludes ordinary compound prose such as `end-to-end` and `read-only`. Lines that explicitly address the classifier with instructions are excluded from classification text and lookup terms; the original task and excluded line numbers remain in the report. This filter is a heuristic, not a prompt-injection security boundary. Search results are inspection leads, not proof of ownership or an exhaustive dependency graph. The model recommends modules; explicit module names and documentation requests also produce deterministic inspection hints. These hints are recorded separately in `explicit_modules`. They are not evidence that a module needs editing. Installed repository instructions remain authoritative; suggested skills are candidates to read when applicable.

After the draft contract, Jev can recommend inspecting missing coverage for a known shared consumer. The author must confirm reachability before revising the contract. An existing extension scenario also covers the webview alias. A fully covered draft needs no classifier request.

Before each review round, the controller sends the current diff, changed paths, task, and contract. The diff includes untracked regular files. Its byte budget is shared across production files and hunks before tests and fixtures. `diff_coverage` records included and total bytes for every file, including omitted files. This keeps a late module visible when an earlier test fixture is large. The report records the candidate identity. After a repair, the controller requests fresh advice for the new candidate. The effective review set is the union of the existing mandatory topics and Jev's additional topics. The request asks only about topics outside `mandatory_reviews`. When every supported topic is already mandatory, it records a rule-only result and makes no provider call. Correctness review remains mandatory; negative classifications cannot remove required compatibility, data, security, or concurrency checks.

The controller requests failure advice when a cycle errors or asks for human input. Known native process-start and response-decoding failures have deterministic categories and need no classifier call. Other cases include the current stage, native process result, and available last-command or stderr excerpts. Credentials are redacted before those excerpts enter the request. An unresolved question has a separate `question_action`: `owner_decision`, `observable_probe`, `unavailable_access`, or `uncertain`. For example, comparing two available Git trees or checking product-path reachability is a factual probe, while choosing new intended behavior is an owner decision. Failure cause and next action are classified independently. The native controller decides whether an authorized bounded probe can run; the classifier grants no new access or scope and does not establish a root cause.

## Policy and fallback

The policy version is `engineering-triage-v12`. Readiness, focus, and semantic module hints require a selected probability of at least 0.80 and a margin of 0.50 over the next option. A strong question action or missing-consumer coverage recommendation also requires 0.80 and 0.50. Additional review topics require 0.70 and 0.30. Duplicate candidates require 0.85 and 0.60 because they are suggestions for inspection, not closure decisions. These are pilot thresholds, not calibrated success probabilities.

Weaker positive module and review suggestions appear separately as `module_candidates` and `review_candidates` at probability 0.50 and margin 0.15. They are visible to the native agent but never add scheduled reviewers. `readiness_candidate` uses 0.45 and 0.15; `route_candidate` uses 0.50 and 0.15. The accepted readiness or route remains `uncertain` until the original threshold passes. `question_action_candidate` uses 0.50 and 0.15 and cannot authorize a probe. Candidate fields are not counted as confident decisions. Partial task evidence suppresses readiness and route candidates as well as duplicate suggestions.

Readiness refers to the requested deliverable. Source-only diagnosis can start with verified repository access even when the external service is unavailable. Missing requirements for an implementation remain a reason to ask the owner. Committed source availability is recorded by the source lookup; a positive classification never grants permissions.

Missing or conflicting evidence can produce `uncertain`. Native investigation still runs. Provider errors are saved and leave the existing cycle in control. Missing credentials for an explicitly enabled integration are configuration errors. An invalid or changed evidence record stops processing instead of silently accepting altered recommendations.

The worker limits task text to 12000 bytes and review diff text to 48000 bytes for this classifier. It records partial per-file coverage and truncation in `limitations`; the native agent still receives the original task and source. Partial input cannot establish readiness or a duplicate. Review advice from partial input can add checks but cannot remove the mandatory set. Large diffs may therefore need additional investigation beyond what the classifier suggests.

## Inspect a standalone recommendation

Create a packet with `task_id`, `phase`, and `task`. Supported phases are `intake`, `contract`, `review`, and `failure`. Contract routing needs a draft contract and known consumer hints. Review also requires `candidate_id` and `diff`; failure requires `failure.message`. Optional fields include context, contract, changed paths, consumer hints, mandatory reviews, diff coverage, limitations, and duplicate candidates. A failure can carry stage, process exit, process classification, sanitized log excerpts, and a structured question.

```bash
go -C tools/autonomous-fixer run ./cmd/jev-task \
  --packet /absolute/path/packet.json \
  --output /absolute/path/new-output
```

This writes the request without inference. Add `--live --key-file /absolute/path/private-key-file` for an explicit provider call. Use `--source /absolute/path/repository` to include the same committed-source lookup used by the local worker. Set `base_sha` to replay an archived issue against its recorded pre-fix revision; otherwise the command uses HEAD. Output directories must be new, so later trials preserve earlier evidence. Reference judgments belong in a separate evaluation manifest, never in the classifier packet.

Each report preserves the exact request, packet and request hashes, policy version, raw answers, selected recommendations, timing, and reported token usage and cost. Local work archives these reports under `evidence/engineering-triage-*.json`, includes their hashes in the completion receipt, and passes recommendations through the stage invocation record. `work-metrics` includes these provider costs, deduplicates request IDs, and keeps unknown charges unknown. Rule-only classifications record zero inference cost.

## Validation boundaries

Unit and local integration tests cover changed evidence, stale candidate recommendations, mandatory review preservation, repeated classification after repair, duplicate thresholds, provider failure, credential isolation, and cost accounting. The local integration test uses a fake native runtime and exercises reproduction, a faulty candidate, review, repair, and verification on different test data.

Live classifier evaluations use archived issues and patches plus controlled contrasts. Their expectations are provisional development labels, not independent proof of accuracy. Evaluate explicit module hints separately from semantic Jev recommendations. Do not call an abstention a correct classification or use five issues to estimate production accuracy.

No product E2E case is needed: these changes stay inside the controller, and its module integration tests cover the behavior. Hosted execution and permissions still require separate GitHub testing.
