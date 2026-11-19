package fixer

import "fmt"

func RevisionCheckpoint(state *State, issue Issue, head string, run RunIdentity, command int64, assignmentSHA string) (Checkpoint, error) {
	if state == nil || state.PilotPublication == nil || state.PilotCandidate == nil ||
		state.Desired != "running" || state.Phase != "waiting-ci" ||
		issue.Number != state.IssueNumber || issue.State != "open" || !issue.ReadyForAgent() ||
		!state.Contract.matchesRequirements(issue.Requirements()) {
		return Checkpoint{}, fmt.Errorf("the original engineering assignment no longer authorizes work")
	}
	if !commitPattern.MatchString(head) || !digestPattern.MatchString(assignmentSHA) || run.RunID <= 0 || run.RunAttempt != 1 || command <= 0 {
		return Checkpoint{}, fmt.Errorf("invalid revision checkpoint identity")
	}
	contract := state.Contract
	contract.BaseSHA = head
	attempt := fmt.Sprintf("revision-%d", command)
	return Checkpoint{
		SchemaVersion: SchemaVersion, Mode: "pilot", Repository: state.Repository,
		TaskID: fmt.Sprintf("%s-revise-%d", state.TaskID, command), IssueNumber: state.IssueNumber,
		AttemptID: attempt, ExecutionID: attempt, Generation: state.Generation, ContractVersion: contract.Version,
		Run: run, TrustedSHA: state.Contract.TrustedSHA, Contract: contract, Answers: pilotAnswers(state),
		RevisionSHA256: assignmentSHA,
	}, nil
}
