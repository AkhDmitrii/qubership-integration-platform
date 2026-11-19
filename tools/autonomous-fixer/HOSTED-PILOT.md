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

If the fork token cannot read the required Maven packages for the first issue, configure `QIP_PILOT_MAVEN_USERNAME` and the `QIP_PILOT_MAVEN_TOKEN` secret with Packages read access. The first issue's environment preparation and the trusted revision cache workflow use these credentials.

The inference process uses a loopback relay for its configured models. The real key is kept out of OpenCode's config and ordinary tool environment. Tools still have shell, Docker and Kubernetes access on that runner. The relay does not isolate secrets from a process controlling the host. The credential for publishing the PR belongs to a different job.

## First issue

1. Open a narrowly scoped issue in the pilot repository. If `AGENT_PRE_READY_ON_OPEN` is set to `true`, this starts **QIP issue triage**. You can also record your assessment and run the workflow manually. Use its English summary as advice before assigning work. See [Pre-ready issue advice](JEV-ENGINEERING.md#pre-ready-issue-advice).
2. Add `ready-for-agent` as an allowed operator. Follow the controller's issue comment to **QIP agent work**. Compare the registered worker run and attempt, base commit, issue text, and any captured attachments.
3. Inspect the worker's preparation and author result. An `author-reported-fixed` outcome means the author reports that the checks and reviews completed. The draft and evidence are available for human review. If the author needs a decision, answer the versioned question in the issue. A new execution starts with the recorded answer. Use `/agent stop` to stop work.
4. With automatic publication enabled, the trusted worker completion leads to a draft PR. For manual publication, dispatch **QIP agent publish** with the issue number and exact candidate run and attempt. Inspect the changed files, evidence and ordinary PR CI before merging.

The draft PR draws its verification steps and observed results from the sealed author report. **How to verify** ends with the end-to-end decision. It names the regression test and target when the run needs that coverage, or explains why the change needs no end-to-end case. If an older or incomplete report lacks the decision, the draft says so and links to the worker evidence. A publication retry keeps edits to an existing draft description.

## Request changes in the same PR

Leave a new top-level comment on the published engineering PR. Copy a link to each review comment, submitted review, or PR conversation comment that the author should address. For example:

```text
/agent revise #discussion_r123456 #issuecomment-234567
Keep the existing behavior for a chain opened directly from a link.
```

Select up to five feedback messages from that PR. A link to a submitted review includes its summary and inline comments. Link individual comments to request a smaller batch. Put any extra instruction on the next line. The operator needs repository write access and a place in `AGENT_ALLOWED_ACTORS`. Keep the source issue open and labeled `ready-for-agent`.

Run **QIP agent revision cache** on the pilot repository's default branch to prepare a new fork or recover from a missing private Maven dependency. An allowed operator can run it from Actions. It uses the default-branch checkout and the same read-only Maven package access as the first issue. The workflow stores the four Java services' Maven dependencies under `maven-Linux-agent-revision-<default-branch POM hash>` in that repository's default-branch cache. Revisions can download from public repositories and use this cache. If a revision adds a private dependency unavailable from the default-branch build, preparation stops before the author starts.

The receipt identifies the run and the PR head that the author will work from. Check the resulting commit and its evidence before resolving review threads. If someone pushes a change while the author works, review the new head before requesting another attempt. The branch update requires the expected head and rechecks that the PR is open immediately before pushing. GitHub handles the branch update and PR closure separately. If the publication job fails temporarily, the operator who posted the command can rerun the failed publisher job to reuse the first worker artifact. The maintainer decides when to mark the PR ready and merge it.

## Read CI and the final outcome

**QIP agent CI observation** updates one comment on the open engineering PR. It reads GitHub check runs and commit statuses for the current head and, when available, the test merge. Check the SHA in the report when a new commit arrives. You can set `AGENT_CI_REQUIRED_CHECKS` to a list of expected job or status names, one per line, to supplement the branch rules available to Actions. The observer runs on PR changes and CI completions. A daily scan and manual dispatch can refresh the report if an event was missed. Observation uses no model calls.

**QIP agent PR outcome** records both merged and closed-unmerged PRs. Its comment and Actions artifact identify the published candidate, the last PR head, and the merge commit when applicable. The report timestamps the CI observation because checks can finish or rerun after closure. The comparison uses each version's patch against its own base. Missing historical evidence produces an unknown result. A changed patch can include work by the author, a maintainer, or both. Use the commit and discussion history when attributing a change.

## What crosses jobs

The worker seals its run identity and author report. For a claimed fix it also packages the candidate's regular-file edits relative to the authorized commit. The publisher verifies the recorded artifact identity and digest, reconstructs the actual tree in a temporary index against the trusted base, checks every changed path against the issue scope, and compares the tree returned by GitHub. It seeds a run-specific branch from an existing base commit and fast-forwards it to the validated candidate. When only the fixer tooling or its engineering workflows advanced, it can seed from the current tip and replay the same product edits. The PR body records both commits. A repeated publication reads back the same branch and PR. An unrelated commit on the default branch leaves the pinned candidate intact. If the default branch has been rewritten and no longer contains the admitted base, the publisher stops.

The runner is ephemeral. A restart gets a fresh checkout and stand. The first [fork run](https://github.com/AkhDmitrii/qubership-integration-platform/actions/runs/44315529424) exercised the real stand, author and reviewers. OpenRouter billed $0.35611274 for 74 requests. [Draft PR #2](https://github.com/AkhDmitrii/qubership-integration-platform/pull/2) used the original candidate after an operator reconciled the missing completion notification and retried publication. A later run on [issue #8](https://github.com/AkhDmitrii/qubership-integration-platform/issues/8) published [draft PR #9](https://github.com/AkhDmitrii/qubership-integration-platform/pull/9). Keep label-to-draft delivery in the smoke test when deploying a new controller version.

## GitHub result signatures

The result publication follows the [AI agent comment signature specification](https://github.com/Netcracker/qubership-ai-packages/blob/main/research/ai-autonomous-design/README.md#ai-agent-comment-signatures).
A completed fix reports its usage on the last line of the draft PR description. When an author stops for help, its issue result comment carries the footer. The state comment tracks control events. It does not repeat the PR's charges.

Each footer identifies the main model and sums the available runtime measurements for the execution, including reviewer and retry requests. Cache reads have their own counter. Output includes reasoning. A missing value means the runtime could not establish a complete total. API-equivalent price estimates are not provider charges.

To inspect the footer before delivery or recover it from a downloaded evidence artifact:

```bash
go -C tools/autonomous-fixer run ./cmd/agent-signature /absolute/path/to/result
```
