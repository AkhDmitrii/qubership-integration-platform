package autofollow

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/autorequest"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
)

func TestRepeatedDiagnosticDoesNotBlockNewDiagnosticInSameJob(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			f := newFixture(t)
			f.check, f.annotation = "failure", "src/nav.ts(15,3): error TS2322: incompatible type"
			f.options.Apply = false
			first, err := f.reconcile()
			if err != nil || len(first.Failures) != 1 {
				t.Fatalf("initial CI plan = %+v, %v", first, err)
			}
			prior := first.Failures[0]
			if legacy {
				prior.Excerpt += "\nTypeScript reported TS2323 at src/nav.ts:18:3."
				identity := "2\x002\x00Test frontend\x00TypeScript reported TS2322 at src/nav.ts.\nTypeScript reported TS2323 at src/nav.ts."
				prior.Key = fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
			}
			f.addRequest(autorequest.Request{RepositoryID: 1, PullID: 54, PullNumber: 2, Head: headSHA, Failures: []autorequest.Failure{prior}}, time.Now().Add(-10*time.Minute))
			f.admit(200, 400, true, headSHA, prior.Key)
			f.comments = append(f.comments, fixer.Comment{ID: 240, User: f.bot, Body: fmt.Sprintf(
				"<!-- qip-agent-revision-result:v1 1:2:200:400:1:%s:published:%s -->", headSHA, nextSHA)})
			f.pr.Head.SHA = nextSHA
			f.annotation += "\nsrc/nav.ts(23,3): error TS2322: incompatible type"
			f.options.Apply = true
			report, err := f.reconcile()
			if err != nil || report.Status != "dispatched" || len(report.Failures) != 1 || len(report.Human) != 1 || f.dispatches != 1 || len(f.comments) != 5 {
				t.Fatalf("CI follow-up = %+v, %v", report, err)
			}
			if note := f.comments[3].Body; !strings.Contains(note, "src/nav.ts&#58;15&#58;3") || !strings.Contains(note, "#issuecomment-200") {
				t.Fatalf("maintainer note = %s", note)
			}
			request, err := autorequest.Decode(f.comments[4].Body)
			if err != nil || len(request.Failures) != 1 || request.Failures[0].Excerpt != "TypeScript reported TS2322 at src/nav.ts:23:3." {
				t.Fatalf("new revision request = %+v, %v", request, err)
			}
		})
	}
}
