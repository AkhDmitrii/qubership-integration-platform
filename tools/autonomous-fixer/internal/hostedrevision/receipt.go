package hostedrevision

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/agentsignature"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
)

const receiptMarker = "<!-- qip-agent-revision:v1 "
const resultMarker = "<!-- qip-agent-revision-result:v1 "

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)
var publishedMarker = regexp.MustCompile(`^` + regexp.QuoteMeta(resultMarker) + `([1-9][0-9]*):([1-9][0-9]*):[1-9][0-9]*:[1-9][0-9]*:1:([0-9a-f]{40}):published:([0-9a-f]{40}) -->$`)

func PublishedCommits(comments []fixer.Comment, bot fixer.GitHubUser, repoID, pullNumber int64) map[string]int64 {
	published := map[string]int64{}
	repository, pull := strconv.FormatInt(repoID, 10), strconv.FormatInt(pullNumber, 10)
	for _, comment := range comments {
		if comment.ID <= 0 || comment.User != bot || bot.ID <= 0 || bot.Login != "github-actions[bot]" || bot.Type != "Bot" {
			continue
		}
		for _, line := range strings.Split(comment.Body, "\n") {
			if fields := publishedMarker.FindStringSubmatch(line); fields != nil && fields[1] == repository && fields[2] == pull && fields[3] != fields[4] {
				published[fields[4]] = comment.ID
			}
		}
	}
	return published
}

type receipt struct {
	Assignment
	CheckpointSHA string `json:"checkpointSha256"`
}

func (c *Controller) botComments(ctx context.Context) ([]fixer.Comment, error) {
	raw, err := c.cfg.Client.List(ctx, fmt.Sprintf("/issues/%d/comments", c.cfg.Payload.Issue.Number), "")
	if err != nil {
		return nil, err
	}
	comments := make([]fixer.Comment, 0, len(raw))
	for _, item := range raw {
		var comment fixer.Comment
		if err := json.Unmarshal(item, &comment); err != nil {
			return nil, err
		}
		if comment.User == c.bot {
			comments = append(comments, comment)
		}
	}
	return comments, nil
}

func (c *Controller) receipts(ctx context.Context) (map[int64]Assignment, error) {
	comments, err := c.botComments(ctx)
	if err != nil {
		return nil, err
	}
	receipts := map[int64]Assignment{}
	for _, comment := range comments {
		for _, line := range strings.Split(comment.Body, "\n") {
			if !strings.HasPrefix(line, receiptMarker) || !strings.HasSuffix(line, " -->") {
				continue
			}
			encoded := strings.TrimSuffix(strings.TrimPrefix(line, receiptMarker), " -->")
			data, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil {
				return nil, err
			}
			var stored receipt
			if err := fixer.DecodeJSON(data, &stored); err != nil {
				return nil, err
			}
			a := stored.Assignment
			a.checkpointSHA = stored.CheckpointSHA
			if !digestSHA.MatchString(a.checkpointSHA) || a.RepositoryID != c.repo.ID || a.PullNumber != c.cfg.Payload.Issue.Number || a.PullID <= 0 ||
				a.CommentID <= 0 || a.Issue <= 0 || !commitSHA.MatchString(a.Head) || !commitSHA.MatchString(a.WorkflowSHA) ||
				a.Run.RunID <= 0 || a.Run.RunAttempt != 1 || a.OriginRun.RunID <= 0 || a.OriginRun.RunAttempt <= 0 {
				return nil, fmt.Errorf("the revision receipt does not match this pull request")
			}
			if _, ok := receipts[a.CommentID]; ok {
				return nil, fmt.Errorf("the pull request has duplicate revision receipts")
			}
			receipts[a.CommentID] = a
		}
	}
	return receipts, nil
}

func (c *Controller) post(ctx context.Context, body string) error {
	var result fixer.Comment
	path := fmt.Sprintf("/issues/%d/comments", c.cfg.Payload.Issue.Number)
	postErr := c.cfg.Client.Request(ctx, "POST", path, map[string]string{"body": body}, &result)
	if postErr == nil && result.ID > 0 && result.User == c.bot && result.Body == body {
		return nil
	}
	comments, err := c.botComments(ctx)
	for _, comment := range comments {
		if comment.Body == body {
			return nil
		}
	}
	return errors.Join(postErr, err, fmt.Errorf("GitHub did not confirm the revision comment"))
}

func (c *Controller) postReceipt(ctx context.Context, a Assignment) error {
	data, err := json.Marshal(receipt{Assignment: a, CheckpointSHA: a.checkpointSHA})
	if err != nil {
		return err
	}
	body := fmt.Sprintf("Accepted [revision request](#issuecomment-%d) from @%s at `%s`. Follow the [revision run](%s).\n\n%s%s -->",
		a.CommentID, a.Actor.Login, a.Head, evidenceURL(c.repo.FullName, a.Run), receiptMarker, base64.StdEncoding.EncodeToString(data))
	if len(body) > 65536 {
		return fmt.Errorf("the selected feedback exceeds the revision receipt limit")
	}
	return c.post(ctx, body)
}

type Result struct {
	Status    string
	Commit    string
	Signature *agentsignature.Signature
}

func (c *Controller) resultPrefix(a Assignment) string {
	return fmt.Sprintf("%s%d:%d:%d:%d:%d:%s:", resultMarker, a.RepositoryID, a.PullNumber, a.CommentID, a.Run.RunID, a.Run.RunAttempt, a.Head)
}

func (c *Controller) HasResult(ctx context.Context, a Assignment) (bool, error) {
	comments, err := c.botComments(ctx)
	if err != nil {
		return false, err
	}
	prefix := c.resultPrefix(a)
	for _, comment := range comments {
		for _, line := range strings.Split(comment.Body, "\n") {
			if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, " -->") {
				return true, nil
			}
		}
	}
	return false, nil
}

func (c *Controller) Report(ctx context.Context, a Assignment, result Result) error {
	if a.Run.RunID != c.cfg.Run.RunID || a.CommentID != c.cfg.Payload.Comment.ID {
		return ErrUnauthorized
	}
	published := "-"
	var outcome string
	switch result.Status {
	case "published":
		if !commitSHA.MatchString(result.Commit) {
			return fmt.Errorf("the revision result needs a published commit")
		}
		published = result.Commit
		outcome = fmt.Sprintf("Published [commit `%s`](https://github.com/%s/commit/%s) to this pull request.", result.Commit[:12], c.repo.FullName, result.Commit)
	case "stale":
		outcome = "The pull request head or target changed. The revision left the branch untouched."
	case "closed":
		outcome = "The pull request closed before publication. The revision left the branch untouched."
	case "withdrawn":
		outcome = "The revision's authorization changed. The revision left the branch untouched."
	case "needs-human":
		outcome = "The author requested a maintainer decision. Read the report in the run evidence."
	case "blocked":
		outcome = "The author reported a blocker. Read the report in the run evidence."
	case "failed":
		outcome = "The workflow ended before producing a sealed result. See the run for details."
	default:
		return fmt.Errorf("unknown revision result")
	}
	done, err := c.HasResult(ctx, a)
	if err != nil || done {
		return err
	}
	body := fmt.Sprintf("### Agent revision\n\n[Revision request](#issuecomment-%d)\n\n%s\n\n[Run and verification evidence](%s)\n\n%s%s:%s -->",
		a.CommentID, outcome, evidenceURL(c.repo.FullName, a.Run), c.resultPrefix(a), result.Status, published)
	if result.Signature != nil {
		body = result.Signature.Append(body)
	}
	return c.post(ctx, body)
}
