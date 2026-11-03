// SPDX-License-Identifier: AGPL-3.0-or-later

package intake

import (
	"strings"
	"testing"
)

func testPolicy() Policy {
	return Policy{
		Enabled:        true,
		Repos:          []string{"Acme/Widgets"},
		Actors:         []string{"alice"},
		Mention:        "@panda-bot",
		Self:           "panda-bot",
		RequireMention: true,
	}
}

func TestPolicyAdmit(t *testing.T) {
	valid := Event{
		Source: "github", Kind: KindIssueComment, Repo: "acme/widgets",
		Actor: "alice", Title: "Crash on save", Body: "@panda-bot please fix this",
	}
	cases := []struct {
		name   string
		policy Policy
		mutate func(*Event)
		want   Decision
	}{
		{
			name:   "addressed request from an allowed actor",
			policy: testPolicy(),
			want:   Decision{Admit: true},
		},
		{
			name:   "disabled listener refuses everything",
			policy: Policy{},
			want:   Decision{Code: CodeDisabled},
		},
		{
			name:   "repository is case-insensitive",
			policy: testPolicy(),
			mutate: func(ev *Event) { ev.Repo = "ACME/widgets" },
			want:   Decision{Admit: true},
		},
		{
			name:   "foreign repository",
			policy: testPolicy(),
			mutate: func(ev *Event) { ev.Repo = "acme/other" },
			want:   Decision{Code: CodeRepoNotAllowed},
		},
		{
			name:   "the node's own message",
			policy: testPolicy(),
			mutate: func(ev *Event) { ev.Actor = "panda-bot" },
			want:   Decision{Code: CodeSelfActor},
		},
		{
			name:   "unknown actor",
			policy: testPolicy(),
			mutate: func(ev *Event) { ev.Actor = "mallory" },
			want:   Decision{Code: CodeActorNotAllowed},
		},
		{
			name:   "not addressed",
			policy: testPolicy(),
			mutate: func(ev *Event) { ev.Body = "just a drive-by comment" },
			want:   Decision{Code: CodeNotAddressed},
		},
		{
			name:   "a longer handle is a different node",
			policy: testPolicy(),
			mutate: func(ev *Event) { ev.Body = "@panda-bot-2 please fix this" },
			want:   Decision{Code: CodeNotAddressed},
		},
		{
			name:   "addressing may land in the title",
			policy: testPolicy(),
			mutate: func(ev *Event) { ev.Title = "@panda-bot save is broken"; ev.Body = "" },
			want:   Decision{Admit: true},
		},
		{
			name:   "unmodelled kind",
			policy: testPolicy(),
			mutate: func(ev *Event) { ev.Kind = "issue_closed" },
			want:   Decision{Code: CodeUnsupported},
		},
		{
			name: "mention check can be turned off",
			policy: func() Policy {
				p := testPolicy()
				p.RequireMention = false
				return p
			}(),
			mutate: func(ev *Event) { ev.Body = "no handle here" },
			want:   Decision{Admit: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := valid
			if tc.mutate != nil {
				tc.mutate(&ev)
			}
			got := tc.policy.Admit(ev)
			if got != tc.want {
				t.Fatalf("Admit() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAdmitEmptyPayload(t *testing.T) {
	ev := Event{Kind: KindIssueOpened, Repo: "acme/widgets", Actor: "alice"}
	if got := testPolicy().Admit(ev); got.Code != CodeNotAddressed {
		t.Fatalf("Admit() = %+v, want not_addressed before the emptiness check", got)
	}
	p := testPolicy()
	p.RequireMention = false
	if got := p.Admit(ev); got.Code != CodeEmpty {
		t.Fatalf("Admit() = %+v, want empty_event", got)
	}
}

func TestAdmitRefusesWithoutAllowlists(t *testing.T) {
	ev := Event{
		Kind: KindIssueOpened, Repo: "acme/widgets", Actor: "alice",
		Title: "x", Body: "@panda-bot go",
	}
	if got := (Policy{Enabled: true}).Admit(ev); got.Code != CodeRepoNotAllowed {
		t.Fatalf("empty repos: got %+v", got)
	}
	p := testPolicy()
	p.Actors = nil
	if got := p.Admit(ev); got.Code != CodeActorNotAllowed {
		t.Fatalf("empty actors: got %+v", got)
	}
}

func TestMentionsBoundary(t *testing.T) {
	cases := []struct {
		text, handle string
		want         bool
	}{
		{"@panda-bot please", "panda-bot", true},
		{"hi @PANDA-BOT", "@panda-bot", true},
		{"@panda-bot.", "panda-bot", true},
		{"@panda-bot", "panda-bot", true},
		{"@panda-bot-2 go", "panda-bot", false},
		{"@panda-bot_x go", "panda-bot", false},
		{"panda-bot go", "panda-bot", false},
		{"anything", "", true},
	}
	for _, tc := range cases {
		if got := mentions(tc.text, tc.handle); got != tc.want {
			t.Errorf("mentions(%q, %q) = %v, want %v", tc.text, tc.handle, got, tc.want)
		}
	}
}

func TestEventIntent(t *testing.T) {
	ev := Event{
		Thread: "acme/widgets#7",
		Title:  "Crash on save",
		Body:   "@panda-bot please fix",
		URL:    "https://github.com/acme/widgets/issues/7#issuecomment-1",
		Actor:  "alice",
	}
	got := ev.Intent()
	for _, want := range []string{"acme/widgets#7: Crash on save", "@panda-bot please fix", "by @alice", "https://github.com"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Intent() = %q, missing %q", got, want)
		}
	}
}

func TestEventTaskTitle(t *testing.T) {
	if got := (Event{Title: "Crash"}).TaskTitle(); got != "Crash" {
		t.Fatalf("TaskTitle() = %q", got)
	}
	if got := (Event{Thread: "acme/widgets#7"}).TaskTitle(); got != "acme/widgets#7" {
		t.Fatalf("TaskTitle() = %q", got)
	}
	if got := (Event{}).TaskTitle(); got != "inbound event" {
		t.Fatalf("TaskTitle() = %q", got)
	}
}
