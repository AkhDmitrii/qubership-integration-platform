package hostedrevision

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/agentpr"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/fixer"
)

var ErrClosed = errors.New("the pull request closed before publication")
var ErrStale = errors.New("the pull request head or target changed")
var ErrUnauthorized = errors.New("the revision is no longer authorized")
var ErrAlreadyActive = errors.New("another revision is already running for this pull request")

type Config struct {
	Client, Variables                           *fixer.GitHubClient
	Repository, Ref, Event, Actor, TriggerActor string
	WorkflowSHA                                 string
	Run                                         fixer.RunIdentity
	Payload                                     fixer.GitHubPayload
}

type Controller struct {
	cfg  Config
	repo fixer.Repository
	bot  fixer.GitHubUser
}

type Assignment struct {
	RepositoryID  int64             `json:"repositoryId"`
	PullID        int64             `json:"pullId"`
	PullNumber    int64             `json:"pullNumber"`
	Issue         int64             `json:"issue"`
	Branch        string            `json:"branch"`
	Head          string            `json:"head"`
	CommentID     int64             `json:"commentId"`
	Actor         fixer.GitHubUser  `json:"actor"`
	CreatedAt     string            `json:"createdAt"`
	CommandHash   string            `json:"commandHash"`
	WorkflowSHA   string            `json:"workflowSha"`
	Run           fixer.RunIdentity `json:"run"`
	OriginRun     fixer.RunIdentity `json:"originRun"`
	Feedback      []Feedback        `json:"feedback"`
	Instruction   string            `json:"instruction,omitempty"`
	checkpointSHA string
}

func Open(ctx context.Context, cfg Config) (*Controller, error) {
	p := cfg.Payload
	if cfg.Client == nil || cfg.Variables == nil || cfg.Event != "issue_comment" || p.Action != "created" ||
		p.Repository.ID <= 0 || p.Repository.FullName != cfg.Repository || cfg.Run.RunID <= 0 ||
		cfg.Run.RunAttempt < 1 || p.Issue.Number <= 0 || len(p.Issue.PullRequest) == 0 || string(p.Issue.PullRequest) == "null" ||
		p.Comment.ID <= 0 || p.Comment.User.ID <= 0 || p.Comment.User.Type != "User" || p.Comment.User != p.Sender ||
		!strings.EqualFold(p.Comment.User.Login, cfg.Actor) || !strings.EqualFold(p.Comment.User.Login, cfg.TriggerActor) {
		return nil, ErrUnauthorized
	}
	c := &Controller{cfg: cfg}
	if err := cfg.Client.Request(ctx, "GET", "", nil, &c.repo); err != nil {
		return nil, err
	}
	if !agentpr.SameRepository(c.repo, p.Repository) || c.repo.DefaultBranch == "" || cfg.Ref != "refs/heads/"+c.repo.DefaultBranch {
		return nil, ErrUnauthorized
	}
	var err error
	c.bot, err = agentpr.Bot(ctx, cfg.Client)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func pullURL(repo string, pull int64) string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", repo, pull)
}

func evidenceURL(repo string, run fixer.RunIdentity) string {
	return fmt.Sprintf("https://github.com/%s/actions/runs/%d/attempts/%d", repo, run.RunID, run.RunAttempt)
}

func (c *Controller) commandCurrent(ctx context.Context) error {
	payload := c.cfg.Payload.Comment
	var response struct {
		fixer.Comment
		IssueURL string `json:"issue_url"`
	}
	if err := c.cfg.Client.Request(ctx, "GET", fmt.Sprintf("/issues/comments/%d", payload.ID), nil, &response); err != nil {
		var httpErr *fixer.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 404 {
			return ErrUnauthorized
		}
		return err
	}
	if response.Comment != payload || payload.CreatedAt == "" || payload.CreatedAt != payload.UpdatedAt ||
		response.IssueURL != fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", c.repo.FullName, c.cfg.Payload.Issue.Number) {
		return ErrUnauthorized
	}
	if _, err := time.Parse(time.RFC3339, payload.CreatedAt); err != nil {
		return ErrUnauthorized
	}
	return nil
}

func (c *Controller) operatorCurrent(ctx context.Context) error {
	for name, want := range map[string]string{"AGENT_ENABLED": "true", "AGENT_MODE": "pilot"} {
		value, err := c.cfg.Variables.RepositoryVariable(ctx, name)
		if err != nil {
			return err
		}
		if value != want {
			return ErrUnauthorized
		}
	}
	actors, err := c.cfg.Variables.RepositoryVariable(ctx, "AGENT_ALLOWED_ACTORS")
	if err != nil {
		return err
	}
	actor := c.cfg.Payload.Comment.User
	if !fixer.Allowed(actors, actor.Login) {
		return ErrUnauthorized
	}
	var permission struct {
		Permission string           `json:"permission"`
		User       fixer.GitHubUser `json:"user"`
	}
	if err := c.cfg.Client.Request(ctx, "GET", "/collaborators/"+url.PathEscape(actor.Login)+"/permission", nil, &permission); err != nil {
		var response *fixer.HTTPError
		if errors.As(err, &response) && response.Status == 404 {
			return ErrUnauthorized
		}
		return err
	}
	if permission.User != actor || (permission.Permission != "admin" && permission.Permission != "write") {
		return ErrUnauthorized
	}
	return nil
}

func (c *Controller) jobCurrent(ctx context.Context, run fixer.RunIdentity, pull, comment int64, trustedSHA string) (bool, error) {
	var actual fixer.WorkflowRun
	path := fmt.Sprintf("/actions/runs/%d/attempts/%d", run.RunID, run.RunAttempt)
	if err := c.cfg.Client.Request(ctx, "GET", path, nil, &actual); err != nil {
		return false, err
	}
	workflow := ".github/workflows/agent-work.yaml"
	branch := c.repo.DefaultBranch
	validPath := actual.Path == workflow || actual.Path == workflow+"@"+branch || actual.Path == workflow+"@refs/heads/"+branch
	if actual.ID != run.RunID || actual.RunAttempt != run.RunAttempt || !agentpr.SameRepository(actual.HeadRepository, c.repo) ||
		actual.Event != "issue_comment" || !validPath || actual.HeadBranch != branch || actual.HeadSHA != trustedSHA ||
		actual.DisplayTitle != fmt.Sprintf("qip-agent-revise|%d|%d", pull, comment) {
		return false, fmt.Errorf("the revision workflow differs from the recorded run")
	}
	return actual.Status != "completed", nil
}

func (c *Controller) pull(ctx context.Context) (agentpr.PullRequest, error) {
	var pr agentpr.PullRequest
	err := c.cfg.Client.Request(ctx, "GET", fmt.Sprintf("/pulls/%d", c.cfg.Payload.Issue.Number), nil, &pr)
	return pr, err
}

func (c *Controller) completedHead(pr agentpr.PullRequest, a Assignment, publishedSHA string) (bool, error) {
	if pr.ID != a.PullID || pr.Number != a.PullNumber || !agentpr.SameRepository(pr.Base.Repo, c.repo) ||
		pr.Base.Ref != c.repo.DefaultBranch || pr.Head.Ref != a.Branch {
		return false, ErrStale
	}
	if publishedSHA != "" && pr.Head.SHA == publishedSHA && agentpr.SameRepository(pr.Head.Repo, c.repo) {
		return true, nil
	}
	if pr.State != "open" || pr.Merged {
		return false, ErrClosed
	}
	if pr.Head.SHA != a.Head || !agentpr.SameRepository(pr.Head.Repo, c.repo) {
		return false, ErrStale
	}
	return false, nil
}

func (c *Controller) CheckHead(ctx context.Context, a Assignment, publishedSHA string) (bool, error) {
	if publishedSHA != "" && !commitSHA.MatchString(publishedSHA) {
		return false, fmt.Errorf("invalid revision commit")
	}
	pr, err := c.pull(ctx)
	if err != nil {
		return false, err
	}
	already, err := c.completedHead(pr, a, publishedSHA)
	if publishedSHA == "" || !isHeadChange(err) || !commitSHA.MatchString(pr.Head.SHA) ||
		!agentpr.SameRepository(pr.Head.Repo, c.repo) || pr.Head.Ref != a.Branch {
		return already, err
	}
	var comparison struct {
		Status string `json:"status"`
	}
	if compareErr := c.cfg.Client.Request(ctx, "GET", "/compare/"+publishedSHA+"..."+pr.Head.SHA, nil, &comparison); compareErr != nil {
		var response *fixer.HTTPError
		if errors.As(compareErr, &response) && response.Status == 404 {
			return false, err
		}
		return false, compareErr
	}
	if comparison.Status == "ahead" || comparison.Status == "identical" {
		return true, nil
	}
	return false, err
}

func isHeadChange(err error) bool {
	return errors.Is(err, ErrStale) || errors.Is(err, ErrClosed)
}

func (c *Controller) OpenRevision(ctx context.Context) (bool, error) {
	p := c.cfg.Payload
	if c.cfg.Run.RunAttempt != 1 {
		return false, nil
	}
	seen, err := c.receipts(ctx)
	if err != nil {
		return false, err
	}
	if _, exists := seen[p.Comment.ID]; exists {
		return false, nil
	}
	if err := c.operatorCurrent(ctx); err != nil {
		return false, err
	}
	if err := c.commandCurrent(ctx); err != nil {
		return false, err
	}
	selected, instruction, err := parseCommand(p.Comment.Body, pullURL(c.repo.FullName, p.Issue.Number))
	if err != nil {
		return false, err
	}
	pr, state, err := agentpr.Read(ctx, c.cfg.Client, c.repo, p.Issue.Number)
	if err != nil {
		return false, err
	}
	if pr.State != "open" || pr.Merged {
		return false, ErrClosed
	}
	if state.PilotCandidate == nil {
		return false, ErrUnauthorized
	}
	if pr.Base.Ref != c.repo.DefaultBranch {
		return false, ErrStale
	}
	for _, existing := range seen {
		active, err := c.jobCurrent(ctx, existing.Run, existing.PullNumber, existing.CommentID, existing.WorkflowSHA)
		if err != nil {
			return false, err
		}
		if active {
			return false, ErrAlreadyActive
		}
	}
	if pr.Head.SHA != state.PilotPublication.SHA {
		var comparison struct {
			Status string `json:"status"`
		}
		endpoint := "/compare/" + url.PathEscape(state.PilotPublication.SHA+"..."+pr.Head.SHA)
		if err := c.cfg.Client.Request(ctx, "GET", endpoint, nil, &comparison); err != nil {
			return false, err
		}
		if comparison.Status != "ahead" {
			return false, ErrStale
		}
	}
	feedback, err := loadFeedback(ctx, c.cfg.Client, c.repo.FullName, pr.Number, selected)
	if err != nil {
		return false, err
	}
	a := Assignment{
		RepositoryID: c.repo.ID, PullID: pr.ID, PullNumber: pr.Number, Issue: state.IssueNumber,
		Branch: pr.Head.Ref, Head: pr.Head.SHA, CommentID: p.Comment.ID, Actor: p.Comment.User, CreatedAt: p.Comment.CreatedAt,
		CommandHash: fmt.Sprintf("%x", sha256.Sum256([]byte(p.Comment.Body))), WorkflowSHA: c.cfg.WorkflowSHA,
		Run: c.cfg.Run, OriginRun: state.PilotCandidate.Run, Feedback: feedback, Instruction: instruction,
	}
	checkpoint, err := c.checkpoint(ctx, state, a)
	if err != nil {
		return false, err
	}
	a.checkpointSHA, err = checkpointDigest(checkpoint)
	if err != nil {
		return false, err
	}
	if _, err := c.jobCurrent(ctx, a.Run, a.PullNumber, a.CommentID, a.WorkflowSHA); err != nil {
		return false, err
	}
	if _, err := c.CheckHead(ctx, a, ""); err != nil {
		return false, err
	}
	return true, c.postReceipt(ctx, a)
}

func (c *Controller) checkpoint(ctx context.Context, state *fixer.State, a Assignment) (fixer.Checkpoint, error) {
	var issue fixer.Issue
	if err := c.cfg.Client.Request(ctx, "GET", fmt.Sprintf("/issues/%d", a.Issue), nil, &issue); err != nil {
		return fixer.Checkpoint{}, err
	}
	if state.IssueNumber != a.Issue || state.PilotCandidate == nil || state.PilotCandidate.Run != a.OriginRun {
		return fixer.Checkpoint{}, ErrUnauthorized
	}
	data, err := json.Marshal(a)
	if err != nil {
		return fixer.Checkpoint{}, err
	}
	checkpoint, err := fixer.RevisionCheckpoint(state, issue, a.Head, a.Run, a.CommentID, fmt.Sprintf("%x", sha256.Sum256(data)))
	if err != nil {
		return checkpoint, errors.Join(ErrUnauthorized, err)
	}
	return checkpoint, nil
}

func (c *Controller) Assignment(ctx context.Context) (Assignment, bool, error) {
	receipts, err := c.receipts(ctx)
	if err != nil {
		return Assignment{}, false, err
	}
	p := c.cfg.Payload
	a, ok := receipts[p.Comment.ID]
	if !ok || a.Run.RunID != c.cfg.Run.RunID {
		return Assignment{}, false, nil
	}
	if a.Actor != p.Comment.User || a.CreatedAt != p.Comment.CreatedAt || a.WorkflowSHA != c.cfg.WorkflowSHA ||
		a.CommandHash != fmt.Sprintf("%x", sha256.Sum256([]byte(p.Comment.Body))) {
		return Assignment{}, false, ErrUnauthorized
	}
	return a, true, nil
}

func (c *Controller) Verify(ctx context.Context, a Assignment) (fixer.Checkpoint, error) {
	if err := c.operatorCurrent(ctx); err != nil {
		return fixer.Checkpoint{}, err
	}
	if err := c.commandCurrent(ctx); err != nil {
		return fixer.Checkpoint{}, err
	}
	_, state, err := agentpr.Read(ctx, c.cfg.Client, c.repo, a.PullNumber)
	if errors.Is(err, agentpr.ErrNotPublished) {
		return fixer.Checkpoint{}, ErrUnauthorized
	}
	if err != nil {
		return fixer.Checkpoint{}, err
	}
	checkpoint, err := c.checkpoint(ctx, state, a)
	if err != nil {
		return fixer.Checkpoint{}, err
	}
	if !a.MatchesCheckpoint(checkpoint) {
		return fixer.Checkpoint{}, ErrUnauthorized
	}
	return checkpoint, nil
}

func checkpointDigest(checkpoint fixer.Checkpoint) (string, error) {
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (a Assignment) MatchesCheckpoint(checkpoint fixer.Checkpoint) bool {
	digest, err := checkpointDigest(checkpoint)
	return err == nil && digest == a.checkpointSHA
}
