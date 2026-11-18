# Manual documentation workflow

The pilot runs model stages locally. GitHub Actions prepares input and reconciles merged documentation PRs without calling Jev or a native model. The workflow runs only on the default branch when `AGENT_DOCS_ENABLED` is `true`.

Select **Run workflow**, enter the issue number, and choose `prepare`. This operation reads GitHub and uploads a frozen issue and change packet. It never applies changes, even if `apply` is selected. Download the preparation artifact for local model work. Each dispatch gets a new plan identity; repeated processing of the same attempt keeps that identity.

After a documentation PR has merged, choose `reconcile` to validate the publication and its associated issue and project state. The `apply` input defaults to `false`. Inspect the dry-run output before applying changes. Reconciliation recovers the original plan from the publisher's canonical GitHub state and does not run preparation or model work. A changed merge basis or missing evidence can block completion.

Use `QIP agent control` with operation `check` to build and test the controller without model credentials. Its tests use fake adapters and local HTTP servers. The demo exercises dispatch, duplicate delivery, admission, stop, and stale completion. These checks do not establish that live workflow callbacks and publication work correctly.

## Repository setup

The workflow requires these repository variables and secrets. Configure them outside the repository; do not commit credentials or upload runtime authentication files as artifacts.

- Variable `AGENT_DOCS_ENABLED`: set to `true` to enable the workflow.
- Variable `AGENT_DOCS_PUBLISHER_LOGIN`: GitHub login of the account or App bot that publishes state comments and documentation PRs. The PR gate verifies this identity.
- Variable `AGENT_ALLOWED_ACTORS` contains comma-separated GitHub logins permitted to publish. The publisher rereads this allowlist and the enable switch from GitHub before publication.

The hosted pilot does not use model secrets. Jev runs locally with an OpenRouter key; native stages run locally through Codex.

Choose one GitHub credential mode:

- **GitHub App:** set variable `AGENT_DOCS_APP_ID` to the App ID and secret `AGENT_DOCS_APP_PRIVATE_KEY` to its private key. Install the App on this repository in the organization that owns project 12. Grant repository Contents, Issues, and Pull requests read and write, repository Variables read, and organization Projects read and write. Each preparation job requests a read-only installation token; reconciliation requests write access for their required operations. Tokens are generated for the current repository and organization project, expire after one hour, and are revoked at job completion. No web server is required. The App login must match `AGENT_DOCS_PUBLISHER_LOGIN`.
- **Personal access token:** leave `AGENT_DOCS_APP_ID` unset. Set secret `AGENT_DOCS_GITHUB_TOKEN` to a dedicated token with access to the repository, issues, pull requests, repository variables, and owner project, including the write access needed to create branches and PRs, comment, close issues, and update project items. Its account must match `AGENT_DOCS_PUBLISHER_LOGIN`. Preparation uses this token for read operations, so the credential in the preparation job can have write access. Optionally set `AGENT_DOCS_READ_TOKEN` to a separate read-only token for preparation.

If `AGENT_DOCS_APP_ID` is set, the workflow uses the App; a failed token creation stops the job. Model work runs locally and receives no GitHub credential from these jobs. The publisher reads the enable switch, actor allowlist, and publisher identity from GitHub again before applying effects. The pinned token action reads `INPUT_PERMISSION-VARIABLES` from its environment to request repository Variables read: its metadata does not declare that permission input, so review this compatibility detail when updating the pinned action.

## Model execution and evidence

Keep subscription authentication on the local machine. This public repository's workflow does not accept or restore Codex `auth.json`. Hosted model execution remains unavailable until a separately billed API credential and its adapter are configured and tested. See [Codex account authentication in CI/CD](https://learn.chatgpt.com/docs/auth/ci-cd-auth) for the private-automation restriction.

Preparation artifacts contain the frozen plan, context packet, and preparation log. Reconciliation uploads its log. Both jobs save evidence after failures when files exist. Controller checks upload test, vet, and demo logs. Local model outputs and transcripts remain in their local run directories; authentication files must stay outside shared evidence.

The workflow does not configure secrets or variables, enable itself, or run on a schedule. Validate repository settings and permissions before an applied reconciliation. A successful local test does not establish that the hosted workflow is ready.

## Local operation and feedback

`docs-agent prepare` reads the issue, its project status and merged implementing PRs. Strong closing relationships select implementations; ambiguous production references remain explicit limitations. It accepts an already-closed issue in documentation review. The finalized plan binds the source revision, initial issue state and exact evidence packet. A reopened issue invalidates a plan prepared while it was closed.

`docs-agent work` uses the prepared packet. Mechanically incomplete input skips the classifier and uses the assessor directly; no OpenRouter request or charge is fabricated. Completed output includes an immutable feedback record. A result that still needs an owner decision is preserved as an unresolved run and cannot publish completion.

A local operator can publish the exact completed candidate with `docs-agent publish --prepared ... --run ... --source ... --actor ... --apply`; this reuses the work instead of running the model again. Omit `--apply` to validate the local publication without writes. Local applied publication requires a token whose login matches the declared actor and configured publisher. The initiating workflow actor may differ from the App publisher identity.

Human reviewers can append documentation-only corrections to the published branch. The gate and merge reconciliation check ancestry and the complete PR file list, including rename origins. The original candidate identity remains recorded, with the later merge head marked separately; added commits are an observation, not automatically a classifier error. A rewritten unrelated head or a product-code change needs separate validation.

`docs-feedback import --run <completed-directory> --output <new-record.json>` validates a completed archive. `docs-feedback adjudicate` adds an explicit later judgment with actor, basis and evidence. `summary` separates independent observations of one dataset case from corrections to the same observation. Human and model judgments remain separate. Records use the current `docs-feedback-v2` schema and never overwrite earlier exports. Standalone downloaded archives can be validated after controlled path relocation; relocated resume chains need their original evidence layout.

The pilot records proposed/observed results and reports. It does not infer that a merged PR is flawless or that any reviewer edit proves the classifier was wrong. No S3 service or database is required.
