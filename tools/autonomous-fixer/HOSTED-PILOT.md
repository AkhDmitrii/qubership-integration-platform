# Run a fix-issue pilot in GitHub Actions

The hosted author runs OpenCode 2.2.6 on the GitHub runner and calls the configured models through OpenRouter. It reads the repository's fix-issue skill, changes the candidate checkout, runs product checks on the runner's kind cluster, and delegates source reviews to its OpenCode reviewer. Local engineering runs still use Codex and the existing local login. Jev is separate from this engineering workflow.

## Configure a pilot repository

Install the workflows, controller, product source and agent skills on the pilot repository's default branch. Enable Actions, create the `ready-for-agent` label, and use a repository that can restore or download the dependencies used by the QIP kind workflow. A new fork may have cold caches. Start with one issue and check disk use, the preparation log, the billed OpenRouter usage and the draft PR before assigning more.

| Repository variable | Pilot setting |
| --- | --- |
| `AGENT_ENABLED` | `true` |
| `AGENT_MODE` | `pilot`. The default is the model-free `fake` worker. |
| `AGENT_ALLOWED_ACTORS` | Comma-separated GitHub logins allowed to assign, answer and stop work |
| `AGENT_SCOPE_PATHS` | Paths the candidate may change, including relevant tests. For a UI issue, for example, use `ui,e2e` if both are in scope. |
| `QIP_PILOT_MODEL` (default) | `openrouter/openai/gpt-6-luna` |
| `QIP_PILOT_EFFORT` (default) | `high` |
| `QIP_PILOT_REVIEW_MODEL` (default) | `openrouter/openai/gpt-6.1-terra` |
| `QIP_PILOT_REVIEW_EFFORT` (default) | `medium` |
| `AGENT_AUTO_PUBLISH` | Set to `true` for label-to-draft-PR operation. Leave unset for manual publication of the selected worker run and attempt. |

| Repository secret | Where it is used |
| --- | --- |
| `OPENROUTER_API_KEY` | The inference step for this pilot. Use a dedicated key so its usage can be attributed and revoked independently. |
| `AGENT_VARIABLES_READ_TOKEN` | Trusted controller steps. Grant repository Variables read access so the controller can recheck the allowlist and enable flag. |
| `AGENT_PUBLICATION_TOKEN` | Trusted publication step. Grant Contents and Pull requests write and Issues read on the pilot repository. The publisher creates a branch and draft PR and reads the issue to confirm eligibility. |

If the fork token cannot read the required Maven packages, configure `QIP_PILOT_MAVEN_USERNAME` and the `QIP_PILOT_MAVEN_TOKEN` secret with Packages read access. Only the environment preparation step receives these credentials.

The inference process uses a loopback relay for its configured models. The real key is kept out of OpenCode's config and ordinary tool environment. Tools still have shell, Docker and Kubernetes access on that runner. The relay does not isolate secrets from a process controlling the host. The credential for publishing the PR belongs to a different job.

## First issue

1. Open a narrowly scoped issue in the pilot repository. If `AGENT_PRE_READY_ON_OPEN` is set to `true`, this starts **QIP issue triage**. You can also record your assessment and run the workflow manually. Use its English summary as advice before assigning work. See [Pre-ready issue advice](JEV-ENGINEERING.md#pre-ready-issue-advice).
2. Add `ready-for-agent` as an allowed operator. Follow the controller's issue comment to **QIP agent work**. Compare the registered worker run and attempt, base commit, issue text, and any captured attachments.
3. Inspect the worker's preparation and author result. An `author-reported-fixed` outcome means the author reports that the checks and reviews completed. The draft and evidence are available for human review. If the author needs a decision, answer the versioned question in the issue. A new execution starts with the recorded answer. Use `/agent stop` to stop work.
4. With automatic publication enabled, the trusted worker completion leads to a draft PR. For manual publication, dispatch **QIP agent publish** with the issue number and exact candidate run and attempt. Inspect the changed files, evidence and ordinary PR CI before merging.

## What crosses jobs

The worker seals its run identity and author report. For a claimed fix it also packages the candidate's regular-file edits relative to the authorized commit. The publisher verifies the recorded artifact identity and digest, reconstructs the actual tree in a temporary index against the trusted base, checks every changed path against the issue scope, and compares the tree returned by GitHub. It seeds a run-specific branch from an existing base commit and fast-forwards it to the validated candidate. When only the fixer tooling or its three control/work/publish workflows advanced, it can seed from the current tip and replay the same product edits. The PR body records both commits. A repeated publication reads back the same branch and PR. An unrelated commit on the default branch leaves the pinned candidate intact. If the default branch has been rewritten and no longer contains the admitted base, the publisher stops.

The runner is ephemeral. A restart gets a fresh checkout and stand. The first [fork run](https://github.com/AkhDmitrii/qubership-integration-platform/actions/runs/44315529424) exercised the real stand, author and reviewers. OpenRouter billed $0.35611274 for 74 requests. [Draft PR #2](https://github.com/AkhDmitrii/qubership-integration-platform/pull/2) used the original candidate after an operator reconciled the missing completion notification and retried publication. Test automatic delivery on a new issue before relying on it.

## GitHub result signatures

The result publication follows the [AI agent comment signature specification](https://github.com/Netcracker/qubership-ai-packages/blob/main/research/ai-autonomous-design/README.md#ai-agent-comment-signatures).
A completed fix reports its usage on the last line of the draft PR description. When an author stops for help, its issue result comment carries the footer. The state comment tracks control events. It does not repeat the PR's charges.

Each footer identifies the main model and sums the available runtime measurements for the execution, including reviewer and retry requests. Cache reads have their own counter. Output includes reasoning. A missing value means the runtime could not establish a complete total. API-equivalent price estimates are not provider charges.

To inspect the footer before delivery or recover it from a downloaded evidence artifact:

```bash
go -C tools/autonomous-fixer run ./cmd/agent-signature /absolute/path/to/result
```
