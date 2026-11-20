package hostedrevision

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/agentci"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/autorequest"
)

func TestAutomaticRevisionResolvesLegacyFailureOnce(t *testing.T) {
	f, request := automaticFixture(t)
	f.change(func() { f.ciConclusion = "failure" })
	ctx := t.Context()
	legacyKey := fmt.Sprintf("%x", sha256.Sum256([]byte("1234\x0083\x00Compile\x00TypeScript reported TS1234 at ui/a.ts.")))
	request.Reviews = nil
	request.Failures = []autorequest.Failure{{Key: legacyKey, Name: "UI build / Compile", Excerpt: "TypeScript reported TS1234 at ui/a.ts:20:30."}}
	first := openFixture(t, f.dispatch(t, request, 123, 13))
	a := accepted(t, first)
	verified, _, err := first.VerifyWork(ctx, a)
	if err != nil || !slices.Equal(verified.FailureKeys, []string{legacyKey}) || len(verified.Feedback) != 1 {
		t.Fatalf("legacy assignment = %+v, %v", verified, err)
	}
	f.change(func() {
		f.comments = append(f.comments, f.commands[13])
		run := f.runs[123]
		run.Status = "completed"
		f.runs[123] = run
	})
	snapshot, err := agentci.Read(ctx, f.cfg.Client, request.Head, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := agentci.PlanRepair(ctx, f.cfg.Client, snapshot)
	if err != nil || len(plan.Failures) != 1 || plan.Failures[0].Key == legacyKey {
		t.Fatalf("current plan = %+v, %v", plan, err)
	}
	request.Failures = plan.Failures
	second := openFixture(t, f.dispatch(t, request, 124, 14))
	if ok, err := second.OpenRevision(ctx); ok || !errors.Is(err, ErrUnauthorized) || f.writes != 1 {
		t.Fatalf("duplicate failure admission = %t, %v, writes = %d", ok, err, f.writes)
	}
}
