package localwork

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/doctriage"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/workcycle"
)

func documentationPrompt(inv workcycle.Invocation, evidence, base string) string {
	guidance := inv.Request.Context
	inv.Request.Context = ""
	input, _ := json.MarshalIndent(inv, "", "  ")
	stages := map[workcycle.Stage]string{
		"docs-assess": "Investigate the whole supplied issue and its implementing merged changes against current help. Read relevant current source and every affected help page in full. Include changed consumers even when their help pages were not edited by the original PR. Treat the Jev classification, if supplied, as a hypothesis only. Keep source and docs unchanged. Return documentation with your decision, concrete summary, and claims linking source facts to existing help. Each claim needs an id, behavior, attribution, needs_change, source_evidence, documentation_evidence, and action. Compare the behavior before and after the implementing PR. Read whole help sections, configuration notes, and cross-references before declaring a gap. Separate a necessary correction from an optional explanation. Put independent preexisting documentation defects in separate_findings, with their own evidence and preexisting_independent attribution; they do not make this issue need a documentation patch. For no_change also prepare an English issue_comment with source/doc permalinks and the precise reason no help edit is needed. Uncertainty can be resolved by investigation; return investigate plus a specific question only when evidence or a real owner decision is missing. Do not force the case into its expected test branch.",
		"docs-write":  "Make the smallest correct change only under help/docs, using only the change-related claims that have needs_change=true. Preserve unrelated current documentation and record separate_findings without implementing them. Existing page edits are authorized. If a genuinely new page or section is necessary, return a question instead of silently changing the information structure. Reproduce reviewer findings and fix the confirmed ones. Return documentation with current claims/evidence and a Conventional Commits PR title plus a concise English PR body describing the reader problem, changed pages, and verification. Use Related #N, never Closes #N. Run focused checks as you edit; final checks are a separate controller stage. Leave changes uncommitted; do not create a real PR. If investigation shows no edit is needed, stop with a concrete question rather than inventing a patch.",
		"docs-review": "Review this documentation candidate independently against the issue, merged implementation, and current source. Verify factual correctness, reader scope, affected consumers, wording, links, and whether the patch is sufficient and minimal. Verify that it addresses the implementing change and does not repair separate preexisting findings. A renamed claim must not conceal a scope change. The assessment is evidence to challenge, not an authoritative verdict. Read actual files and diff against the base. Keep all files in the checkout unchanged. Return findings with id, location, scenario_id (the relevant documentation claim id), description and evidence. Return an empty findings list when no confirmed problem remains, together with fresh review evidence. Do not read the author's conversation or other review reports. The controller will return findings to the writer.",
		"docs-verify": "Verify the exact final documentation candidate after review. Run every required check from the controller request; report checks with exact id, outcome pass or fail, and new evidence. Inspect the changed text against actual source one more time. Check changed-page structure and local links/images. Use a scratch directory outside the checkout for generated indexes or temporary scripts. Do not change source or documentation. Product startup, screenshots and A/B data are not required for prose whose accuracy is established by source inspection; if a claim genuinely needs product measurement, report the specific missing verification. Return documentation with the final PR title/body and issue comment drafts and refreshed claim evidence. The drafts remain local.",
	}
	if inv.Stage == "docs-write" && inv.Documentation == nil {
		stages[inv.Stage] = "Investigate the whole issue and the alleged help gap, then make the smallest necessary help/docs change in this same session. The Jev recommendation is a hypothesis, not a requirement to produce a patch. Read the implementing before/after changes, current consumers, whole affected help sections, and cross-references. Return claims with fresh source/help evidence, attribution, and needs_change. Keep old independent defects in separate_findings. If current help is sufficient, return decision no_change, a valid reason, and an English issue_comment without changing any source or help file. If an edit is needed, return needed with missing_guidance or stale_guidance, the changed claims, and a Conventional Commits PR title/body using Related #N. Edit only existing help pages. Return a structured question when genuinely blocked or a new page/section needs an owner decision. Run focused checks as you edit. The controller runs an independent review and final checks after a needed result."
	}
	return fmt.Sprintf(`You are executing one stage of a controller-owned QIP documentation task.
%s

The project guidance below is provided in full. The frozen issue and merged-change packet replace GitHub reads for this local test. Read their complete contents from the named files. Apply docs-authoring and English wording conventions. Current committed source and help are the authority for shipped behavior; a historic diff or issue title alone is insufficient. This controller owns the stage order and review sessions. Do not delegate or launch another model. Do not modify product code, run the product stack without a task-specific verification need, commit, push, comment, change labels/status, or create a pull request in GitHub. Save the result locally. Existing doctrine requiring a patch or product A/B for a code fix does not apply to a no-change documentation outcome. Do not ask a chat-only question and continue waiting: return the structured question below when blocked.

%s

Controller input:
%s

Source base commit: %s
Current stage: %s
%s

When controller input contains documentation claims, preserve EVERY incoming claim ID, attribution, needs_change value, and collection (claims or separate_findings) in any returned documentation declaration. This includes already-covered behavior and unaffected consumers that need no edit. Keep their no-change action and refresh their evidence. Preserve separate_findings with fresh evidence, without adding their fixes to the patch. Add claims when necessary, but never silently drop an existing one. Protocol findings about a missing claim require correcting the declaration even when the text patch is already correct.

Write fresh evidence under %s. Every returned evidence reference, including claim source_evidence and documentation_evidence, must name a nonempty file under this invocation's evidence directory. Record source/doc paths and line numbers or permalinks INSIDE those files. Earlier evidence under %s may inform investigation, but return fresh measurements and notes. All generated reports/scripts/check output belong outside the checkout.

Return exactly one JSON object, without a Markdown fence. Allowed fields: documentation, findings, question, checks, evidence. The controller attaches candidate identity from its own snapshot; do not transcribe or calculate a candidate hash. Documentation shape:
{"decision":"needed|no_change|investigate","reason":"reason from the allowed pairs below","summary":"specific conclusion","claims":[{"id":"claim-id","behavior":"reader-visible behavior","attribution":"change_related|unchanged|unclear","needs_change":true,"source_evidence":["absolute current evidence file"],"documentation_evidence":["absolute current evidence file"],"action":"minimal change or precise no-change reason"}],"separate_findings":[{"id":"independent-finding-id","behavior":"unrelated older documentation error","attribution":"preexisting_independent","needs_change":true,"source_evidence":["absolute current evidence file"],"documentation_evidence":["absolute current evidence file"],"action":"record for separate triage"}],"pr_title":"...","pr_body":"...","issue_comment":"..."}.
Allowed decision/reason pairs: needed uses missing_guidance or stale_guidance; no_change uses already_covered, restored_behavior, no_explanation_needed, internal_only, outside_help_scope, or preexisting_only; investigate uses insufficient_evidence or scope_unclear. Needed requires at least one change_related claim with needs_change=true. A no_change decision has no main claim needing a change, even when separate_findings contains an older defect. Unclear main-claim attribution requires investigate. The controller selects the entry stage. A direct docs-write entry combines the initial investigation and the edit; a docs-assess entry performs the investigation separately.
Findings: [{"id":"...","location":"help/docs/path/readme.md:line","scenario_id":"claim-id","description":"reproducible problem","evidence":["absolute current evidence file"]}].
Checks: [{"id":"exact configured check id","outcome":"pass|fail","evidence":["absolute current evidence file"]}].
Question when blocked: {"kind":"needs-human|verification-blocked","text":"specific question","needed":"answer, access, or owner decision needed","evidence":["absolute current evidence file"]}.
Omit unused optional fields. Never report an unavailable check as passed. A no-change decision is a valid successful documentation result and does not require a synthetic edit.
`, doctriage.HelpScope, guidance, input, base, inv.Stage, stages[inv.Stage], evidence, filepath.Dir(evidence))
}
