package fixer

import (
	"fmt"
	"regexp"
	"strings"
)

var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func nonempty(s string) bool { return strings.TrimSpace(s) != "" }
func NewState(seed Seed, actor string) (*State, error) {
	s := &State{SchemaVersion: SchemaVersion, TaskID: seed.TaskID, Repository: seed.Repository, IssueNumber: seed.IssueNumber, Phase: "queued", Desired: "running", Generation: 1, Initiator: strings.ToLower(actor), Contract: Contract{Version: 1, BaseSHA: seed.BaseSHA, TrustedSHA: seed.TrustedSHA, Requirements: seed.Requirements, ScopePaths: append([]string{}, seed.ScopePaths...), Criteria: []string{}}, Attempt: Attempt{ID: seed.TaskID + "/attempt/1", Number: 1, BaseSHA: seed.BaseSHA}, Executions: []Execution{}, WorkItems: []WorkItem{}, Decisions: []Decision{}, StopEpisodes: []StopEpisode{}, Effects: []Effect{}, Receipts: []string{}}
	return s, ValidateState(s)
}
func ValidateState(s *State) error {
	bad := func(field string) error { return fmt.Errorf("invalid task field: %s", field) }
	if s == nil {
		return bad("task")
	}
	if s.SchemaVersion != SchemaVersion || !nonempty(s.TaskID) || !regexp.MustCompile(`^[^/\s]+/[^/\s]+$`).MatchString(s.Repository) || s.IssueNumber <= 0 {
		return bad("identity")
	}
	if s.Revision < 0 || s.Generation <= 0 || !validLogin(s.Initiator) || !validExecutionMode(s.Mode) || !contains([]string{"queued", "working", "verifying", "publishing", "awaiting-publication", "waiting-ci", "waiting-review", "waiting-human", "stopping", "stopped", "recovery-required", "completed"}, s.Phase) || !contains([]string{"running", "stopped"}, s.Desired) {
		return bad("lifecycle")
	}
	c := s.Contract
	if c.Version <= 0 || !shaPattern.MatchString(c.BaseSHA) || !shaPattern.MatchString(c.TrustedSHA) || !nonempty(c.Requirements) || len(c.ScopePaths) == 0 {
		return bad("contract")
	}
	if s.ControllerSHA != "" && !shaPattern.MatchString(s.ControllerSHA) {
		return bad("controllerSha")
	}
	for _, p := range c.ScopePaths {
		if !canonicalPath(strings.TrimSuffix(p, "/")) {
			return bad("scopePaths")
		}
	}
	if s.Attempt.Number <= 0 || s.Attempt.ID != fmt.Sprintf("%s/attempt/%d", s.TaskID, s.Attempt.Number) || s.Attempt.BaseSHA != c.BaseSHA {
		return bad("attempt")
	}
	ids := map[string]bool{}
	unique := func(id string) bool {
		if !nonempty(id) || ids[id] {
			return false
		}
		ids[id] = true
		return true
	}
	executions := map[string]Execution{}
	for _, e := range s.Executions {
		if !unique(e.ID) || !validExecutionMode(e.Mode) || !strings.HasPrefix(e.AttemptID, s.TaskID+"/attempt/") || e.Generation <= 0 || e.Generation > s.Generation || e.ContractVersion <= 0 || e.ContractVersion > c.Version || !shaPattern.MatchString(e.TrustedSHA) || e.Stage != "work" || !contains([]string{"planned", "running", "completed", "cancelled"}, e.Status) {
			return bad("execution")
		}
		if e.Run != nil && (e.Run.RunID <= 0 || e.Run.RunAttempt <= 0) {
			return bad("execution.run")
		}
		if e.Status == "running" && e.Run == nil {
			return bad("execution.run")
		}
		executions[e.ID] = e
	}
	ids = map[string]bool{}
	items := map[string]bool{}
	for _, w := range s.WorkItems {
		if !unique(w.ID) || !contains([]string{"review", "diagnosis"}, w.Role) {
			return bad("workItem")
		}
		items[w.ID] = true
	}
	for _, w := range s.WorkItems {
		if _, ok := executions[w.ParentID]; !ok && !items[w.ParentID] {
			return bad("workItem.parent")
		}
		for _, d := range w.Dependencies {
			if !items[d] || d == w.ID {
				return bad("workItem.dependencies")
			}
		}
	}
	ids = map[string]bool{}
	decisions := map[string]Decision{}
	for _, d := range s.Decisions {
		if !unique(d.ID) || d.Version <= 0 || d.ContractVersion <= 0 || d.ContractVersion > c.Version || !contains(StopReasons, d.Reason) || !contains([]string{"decision", "authorization", "no-change"}, d.Kind) || !contains([]string{"open", "answered", "superseded"}, d.Status) || !nonempty(d.Question) || !nonempty(d.ResumeCondition) {
			return bad("decision")
		}
		if d.Status == "answered" && (d.Answer == nil || !validLogin(d.Answer.Actor) || !nonempty(d.Answer.Text) || !nonempty(d.Answer.Source)) {
			return bad("decision.answer")
		}
		if d.Status == "open" && d.Answer != nil {
			return bad("decision.answer")
		}
		if d.Kind == "no-change" && (!contains([]string{"already-fixed", "expected-behavior", "duplicate", "unreachable"}, d.Basis) || !nonempty(d.EvidenceRef)) {
			return bad("decision.evidence")
		}
		decisions[d.ID] = d
	}
	ids = map[string]bool{}
	for _, e := range s.StopEpisodes {
		d, ok := decisions[e.DecisionID]
		if !unique(e.ID) || !ok || e.Reason != d.Reason || !contains([]string{"open", "resolved", "closed", "superseded"}, e.Status) {
			return bad("stopEpisode")
		}
	}
	ids = map[string]bool{}
	for _, e := range s.Effects {
		x, ok := executions[e.ExecutionID]
		if !unique(e.ID) || !ok || e.Generation <= 0 || e.Generation > s.Generation || !contains([]string{"dispatch", "cancel", "publish"}, e.Kind) || !contains([]string{"pending", "confirmed", "superseded"}, e.Status) {
			return bad("effect")
		}
		if e.Status == "pending" && e.Kind != "cancel" && e.Generation != s.Generation {
			return bad("effect.generation")
		}
		if e.Kind == "dispatch" && (e.Payload.ContractVersion != x.ContractVersion || e.Payload.TrustedSHA != x.TrustedSHA || e.Generation != x.Generation) {
			return bad("effect.dispatch")
		}
		if e.Status == "confirmed" {
			if e.Result == nil {
				return bad("effect.result")
			}
			if e.Kind == "dispatch" && (e.Result.RunID <= 0 || e.Result.RunAttempt <= 0) {
				return bad("effect.run")
			}
		}
		if e.Kind == "publish" && (s.Candidate == nil || e.Payload.CandidateSHA != s.Candidate.CandidateSHA) && e.Status == "pending" {
			return bad("effect.candidate")
		}
	}
	ids = map[string]bool{}
	for _, r := range s.Receipts {
		if !unique(r) {
			return bad("receipts")
		}
	}
	if s.PilotCandidate != nil && validatePilotCandidate(s, *s.PilotCandidate) != nil {
		return bad("pilotCandidate")
	}
	if s.Phase == "awaiting-publication" && (s.PilotCandidate == nil || s.Desired != "running") {
		return bad("pilotCandidate.phase")
	}
	if s.PilotPublication != nil && (s.PilotPublication.Number <= 0 || !commitPattern.MatchString(s.PilotPublication.SHA) || !strings.HasPrefix(s.PilotPublication.Branch, fmt.Sprintf("agent/fix/%d/", s.IssueNumber))) {
		return bad("pilotPublication")
	}
	return nil
}
