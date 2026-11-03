// SPDX-License-Identifier: AGPL-3.0-or-later

// Package intake turns an inbound event from an external source into queued
// work on this node.
//
// It is the inbound half of the loop the hooks package closes on the way out.
// Hooks fire a command when a task crosses an edge; intake creates a task when
// the outside world asks for one. Together they let a node answer GitHub or a
// chat channel without a human typing into the terminal:
//
//	webhook -> intake (admission) -> queue -> agent -> hooks (reply)
//
// Admission is deterministic and runs before any model call. A source event
// reaches the queue only after a code rule says it is addressed to this node
// and came from somebody allowed. That is the same position the entry model's
// fast path holds for chat: the cheap check runs first, and only what survives
// it pays for an agent.
package intake

import (
	"strings"
)

// Event kinds. A source maps its own vocabulary onto these; nothing downstream
// needs to know whether the text arrived as an Issue, a comment or a review
// thread.
const (
	KindIssueOpened  = "issue_opened"
	KindIssueComment = "issue_comment"
	KindReviewNote   = "review_comment"
)

// Decision codes. They are stable: they appear in the webhook response, the
// daemon log and any later audit trail, so an operator can tell "not addressed
// to us" apart from "that repo is not ours" without reading prose.
const (
	CodeDisabled        = "disabled"
	CodeUnsupported     = "unsupported_event"
	CodeRepoNotAllowed  = "repo_not_allowed"
	CodeSelfActor       = "self_actor"
	CodeActorNotAllowed = "actor_not_allowed"
	CodeNotAddressed    = "not_addressed"
	CodeDuplicate       = "duplicate"
	CodeEmpty           = "empty_event"
)

// Event is one inbound event, normalized across sources.
//
// Delivery is the source's own delivery id and doubles as the idempotence key:
// GitHub retries a delivery when a webhook answers slowly, and a replay must
// not open a second task for the same comment.
type Event struct {
	Source   string
	Delivery string
	Kind     string
	Repo     string
	// Number is the source-side thread number (the issue or pull request).
	Number int
	Thread string
	Actor  string
	Title  string
	Body   string
	URL    string
}

// Intent renders the event as the instruction an agent receives. The source
// block stays at the end so the body reads as the work, not as metadata.
func (ev Event) Intent() string {
	var b strings.Builder
	if ev.Thread != "" {
		b.WriteString(ev.Thread)
		b.WriteString(": ")
	}
	title := strings.TrimSpace(ev.Title)
	if title != "" {
		b.WriteString(title)
		b.WriteString("\n\n")
	}
	b.WriteString(strings.TrimSpace(ev.Body))
	if ev.URL != "" || ev.Actor != "" {
		b.WriteString("\n\n—")
		if ev.Actor != "" {
			b.WriteString(" by @")
			b.WriteString(ev.Actor)
		}
		if ev.URL != "" {
			b.WriteString("\n")
			b.WriteString(ev.URL)
		}
	}
	return strings.TrimSpace(b.String())
}

// TaskTitle is the one-line title the queue board and the task row show. A
// comment event carries the thread's title, so the board groups a conversation
// under the issue it belongs to instead of under "comment".
func (ev Event) TaskTitle() string {
	title := strings.TrimSpace(ev.Title)
	if title == "" {
		title = ev.Thread
	}
	if title == "" {
		return "inbound event"
	}
	return title
}

// Decision is the admission verdict. Admit=false with an empty Code means the
// caller should treat the event as malformed, not as refused policy.
type Decision struct {
	Admit bool
	Code  string
}

// Policy is the deterministic gate in front of the executor.
//
// Every list is a requirement, not a hint: an enabled listener with an empty
// Repos or Actors list refuses everything. A gate that defaults to open is one
// configuration typo away from letting a stranger spend this node's tokens.
type Policy struct {
	Enabled bool
	// Repos are "owner/name" entries, matched case-insensitively.
	Repos []string
	// Actors are the logins allowed to originate work.
	Actors []string
	// Mention is the handle a request must address, with or without the "@".
	// Empty disables the check entirely.
	Mention string
	// Self is this node's own login on the source. Its messages are dropped:
	// an agent that answers in a thread must not read its own answer as a new
	// request.
	Self string
	// RequireMention demands the handle in the text. A source that only
	// delivers addressed events (a bot mention, a dedicated inbox) can turn
	// this off.
	RequireMention bool
}

// Admit applies the gate in cheapest-first order: an unsupported event costs
// no string work, a foreign repo costs one membership test, and only an event
// from an allowed actor is scanned for the mention.
func (p Policy) Admit(ev Event) Decision {
	if !p.Enabled {
		return Decision{Code: CodeDisabled}
	}
	switch ev.Kind {
	case KindIssueOpened, KindIssueComment, KindReviewNote:
	default:
		return Decision{Code: CodeUnsupported}
	}
	if !containsFold(p.Repos, ev.Repo) {
		return Decision{Code: CodeRepoNotAllowed}
	}
	if p.Self != "" && strings.EqualFold(strings.TrimPrefix(p.Self, "@"), ev.Actor) {
		return Decision{Code: CodeSelfActor}
	}
	if !containsFold(p.Actors, ev.Actor) {
		return Decision{Code: CodeActorNotAllowed}
	}
	if p.RequireMention && !mentions(ev.Title+"\n"+ev.Body, p.Mention) {
		return Decision{Code: CodeNotAddressed}
	}
	if strings.TrimSpace(ev.Body) == "" && strings.TrimSpace(ev.Title) == "" {
		return Decision{Code: CodeEmpty}
	}
	return Decision{Admit: true}
}

func containsFold(list []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, entry := range list {
		if strings.EqualFold(strings.TrimSpace(entry), want) {
			return true
		}
	}
	return false
}

// mentions reports whether text addresses handle. The character after the
// handle must not extend it: "@panda" must not match "@panda-bot", or one
// node's requests would wake another's. An empty handle imposes no
// requirement.
func mentions(text, handle string) bool {
	handle = strings.TrimPrefix(strings.TrimSpace(handle), "@")
	if handle == "" {
		return true
	}
	needle := "@" + strings.ToLower(handle)
	lower := strings.ToLower(text)
	for start := 0; ; {
		i := strings.Index(lower[start:], needle)
		if i < 0 {
			return false
		}
		end := start + i + len(needle)
		if end >= len(lower) || !isHandleByte(lower[end]) {
			return true
		}
		start = end
	}
}

// isHandleByte reports whether b may continue a login. Logins are ASCII, so a
// byte test is exact even when the surrounding text is multi-byte.
func isHandleByte(b byte) bool {
	return b >= 'a' && b <= 'z' ||
		b >= '0' && b <= '9' ||
		b == '-' || b == '_'
}
