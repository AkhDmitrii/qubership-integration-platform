package docgithub

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func VerifyDocumentationPR(ctx context.Context, c *Client, number int, publisherLogin string) error {
	if number <= 0 || publisherLogin == "" {
		return fmt.Errorf("PR and trusted publisher login are required")
	}
	var pr pullRequest
	if e := c.Request(ctx, "GET", fmt.Sprintf("/pulls/%d", number), nil, &pr); e != nil {
		return e
	}
	m := regexp.MustCompile(`^agent/docs/([1-9][0-9]*)/([a-f0-9]{12})$`).FindStringSubmatch(pr.Head.Ref)
	if len(m) != 3 || !strings.EqualFold(pr.User.Login, publisherLogin) || pr.Head.Repo.FullName != c.Repository || pr.Base.Repo.FullName != c.Repository {
		return fmt.Errorf("PR is not a trusted documentation publication")
	}
	issue, _ := strconv.Atoi(m[1])
	comments, e := c.API.List(ctx, fmt.Sprintf("/issues/%d/comments", issue), "")
	if e != nil {
		return e
	}
	found := false
	originalSHA := ""
	for _, raw := range comments {
		var comment struct {
			Body string
			User struct{ Login string }
		}
		if e = json.Unmarshal(raw, &comment); e != nil {
			return e
		}
		if !strings.EqualFold(comment.User.Login, publisherLogin) || !strings.HasPrefix(comment.Body, "<!-- qip-docs:") {
			continue
		}
		state, err := decodePublicationState(comment.Body)
		if err != nil || state.Plan == nil || validatePublicationPlan(*state.Plan) != nil {
			continue
		}
		p := state.Plan
		if p.Repository != c.Repository || p.IssueNumber != issue || state.PlanID != p.ID || p.ID[:12] != m[2] {
			continue
		}
		if !strings.HasPrefix(comment.Body, marker(*p)+"\n") || !strings.Contains(pr.Body, marker(*p)) || !regexp.MustCompile(fmt.Sprintf(`(?m)^Related #%d\s*$`, issue)).MatchString(pr.Body) {
			continue
		}
		if state.Branch != pr.Head.Ref || state.CandidateDigest == "" || p.DefaultBranch != pr.Base.Ref || (state.PRNumber != number && !(state.PRNumber == 0 && state.Status == "publishing")) {
			return fmt.Errorf("PR differs from its recorded documentation publication")
		}
		if found {
			return fmt.Errorf("multiple matching documentation state comments")
		}
		found = true
		originalSHA = state.CommitSHA
	}
	if !found {
		return fmt.Errorf("no matching authenticated documentation state")
	}
	return verifyDocumentationHead(ctx, c, pr, originalSHA)
}
