// SPDX-License-Identifier: AGPL-3.0-or-later

package intake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// VerifyGitHubSignature checks the X-Hub-Signature-256 header against the raw
// request body. An empty secret is a refusal, not a skip: a listener that
// accepts unsigned deliveries lets anyone who can reach the port open tasks.
func VerifyGitHubSignature(secret string, body []byte, header string) bool {
	if secret == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(header[len(prefix):])
	if err != nil || len(want) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// githubPayload is the subset of a GitHub webhook body intake reads. Fields a
// delivery may omit stay nil; a missing pointer is how "not this shape" is
// distinguished from an empty string.
type githubPayload struct {
	Action string `json:"action"`
	Issue  *struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"issue"`
	PullRequest *struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
	} `json:"pull_request"`
	Comment *struct {
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// githubEvents are the delivery types intake models. Everything else — ping,
// installation, stars — answers "unsupported" and costs no queue work.
const (
	eventIssues        = "issues"
	eventIssueComment  = "issue_comment"
	eventReviewComment = "pull_request_review_comment"
)

// ParseGitHub normalizes one delivery. ok=false means the delivery is valid
// but not one intake acts on; an error means the body could not be read as the
// shape its event type promises.
func ParseGitHub(eventType string, payload []byte) (Event, bool, error) {
	switch eventType {
	case eventIssues, eventIssueComment, eventReviewComment:
	default:
		return Event{}, false, nil
	}
	var p githubPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return Event{}, false, fmt.Errorf("github payload: %w", err)
	}
	repo := strings.TrimSpace(p.Repository.FullName)
	if repo == "" {
		return Event{}, false, fmt.Errorf("github payload: repository.full_name is empty")
	}

	ev := Event{Source: "github", Repo: repo}
	switch eventType {
	case eventIssues:
		// Only a new or reopened issue is a request. Edits, closes and labels
		// are housekeeping: replaying them would open work nobody asked for.
		if p.Action != "opened" && p.Action != "reopened" {
			return Event{}, false, nil
		}
		if p.Issue == nil {
			return Event{}, false, fmt.Errorf("github payload: issues delivery without issue")
		}
		ev.Kind = KindIssueOpened
		ev.Title = p.Issue.Title
		ev.Body = p.Issue.Body
		ev.URL = p.Issue.HTMLURL
		ev.Actor = p.Issue.User.Login
		ev.Number = p.Issue.Number
		ev.Thread = threadOf(repo, p.Issue.Number)

	case eventIssueComment:
		if p.Action != "created" {
			return Event{}, false, nil
		}
		if p.Issue == nil || p.Comment == nil {
			return Event{}, false, fmt.Errorf("github payload: issue_comment delivery without issue or comment")
		}
		ev.Kind = KindIssueComment
		ev.Title = p.Issue.Title
		ev.Body = p.Comment.Body
		ev.URL = p.Comment.HTMLURL
		ev.Actor = p.Comment.User.Login
		ev.Number = p.Issue.Number
		ev.Thread = threadOf(repo, p.Issue.Number)

	case eventReviewComment:
		if p.Action != "created" {
			return Event{}, false, nil
		}
		if p.PullRequest == nil || p.Comment == nil {
			return Event{}, false, fmt.Errorf("github payload: review_comment delivery without pull request or comment")
		}
		ev.Kind = KindReviewNote
		ev.Title = p.PullRequest.Title
		ev.Body = p.Comment.Body
		ev.URL = p.Comment.HTMLURL
		ev.Actor = p.Comment.User.Login
		ev.Number = p.PullRequest.Number
		ev.Thread = threadOf(repo, p.PullRequest.Number)
	}
	if ev.Actor == "" {
		// The sender is the fallback for a payload variant that omits the
		// author: without an actor the gate has nothing to allow or refuse.
		ev.Actor = p.Sender.Login
	}
	return ev, true, nil
}

func threadOf(repo string, number int) string {
	if number <= 0 {
		return repo
	}
	return fmt.Sprintf("%s#%d", repo, number)
}
