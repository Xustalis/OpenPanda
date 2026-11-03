// SPDX-License-Identifier: AGPL-3.0-or-later

package intake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyGitHubSignature(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	const secret = "s3cret"
	cases := []struct {
		name   string
		secret string
		header string
		want   bool
	}{
		{"valid", secret, sign(secret, body), true},
		{"wrong secret", "other", sign(secret, body), false},
		{"tampered body", secret, sign(secret, []byte(`{"action":"edited"}`)), false},
		{"missing prefix", secret, strings.TrimPrefix(sign(secret, body), "sha256="), false},
		{"sha1 is not accepted", secret, "sha1=abc", false},
		{"empty header", secret, "", false},
		{"empty secret refuses", "", sign(secret, body), false},
		{"malformed hex", secret, "sha256=zzzz", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyGitHubSignature(tc.secret, body, tc.header); got != tc.want {
				t.Fatalf("VerifyGitHubSignature() = %v, want %v", got, tc.want)
			}
		})
	}
}

const issueOpenedPayload = `{
  "action": "opened",
  "issue": {
    "number": 42,
    "title": "Crash on save",
    "body": "@panda-bot please fix this",
    "html_url": "https://github.com/acme/widgets/issues/42",
    "user": {"login": "alice"}
  },
  "repository": {"full_name": "acme/widgets"},
  "sender": {"login": "alice"}
}`

func TestParseGitHubIssueOpened(t *testing.T) {
	ev, ok, err := ParseGitHub("issues", []byte(issueOpenedPayload))
	if err != nil || !ok {
		t.Fatalf("ParseGitHub() = %v, %v", ok, err)
	}
	if ev.Kind != KindIssueOpened || ev.Repo != "acme/widgets" || ev.Thread != "acme/widgets#42" ||
		ev.Number != 42 || ev.Actor != "alice" || ev.Title != "Crash on save" || ev.URL == "" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestParseGitHubIssueComment(t *testing.T) {
	payload := `{
	  "action": "created",
	  "issue": {"number": 42, "title": "Crash on save", "body": "original", "user": {"login": "alice"}},
	  "comment": {"body": "@panda-bot try again", "html_url": "https://github.com/acme/widgets/issues/42#issuecomment-9", "user": {"login": "bob"}},
	  "repository": {"full_name": "acme/widgets"}
	}`
	ev, ok, err := ParseGitHub("issue_comment", []byte(payload))
	if err != nil || !ok {
		t.Fatalf("ParseGitHub() = %v, %v", ok, err)
	}
	if ev.Kind != KindIssueComment || ev.Actor != "bob" || ev.Body != "@panda-bot try again" ||
		ev.Title != "Crash on save" || ev.Thread != "acme/widgets#42" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestParseGitHubReviewComment(t *testing.T) {
	payload := `{
	  "action": "created",
	  "pull_request": {"number": 7, "title": "Add cache"},
	  "comment": {"body": "@panda-bot this line leaks", "html_url": "https://github.com/acme/widgets/pull/7#discussion_r1", "user": {"login": "alice"}},
	  "repository": {"full_name": "acme/widgets"}
	}`
	ev, ok, err := ParseGitHub("pull_request_review_comment", []byte(payload))
	if err != nil || !ok {
		t.Fatalf("ParseGitHub() = %v, %v", ok, err)
	}
	if ev.Kind != KindReviewNote || ev.Thread != "acme/widgets#7" || ev.Title != "Add cache" ||
		ev.Actor != "alice" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestParseGitHubIgnoresNonRequestActions(t *testing.T) {
	cases := []struct{ eventType, payload string }{
		{"ping", `{"zen":"hi"}`},
		{"issues", `{"action":"closed","issue":{"number":1,"title":"x"},"repository":{"full_name":"a/b"}}`},
		{"issues", `{"action":"labeled","issue":{"number":1,"title":"x"},"repository":{"full_name":"a/b"}}`},
		{"issue_comment", `{"action":"deleted","issue":{"number":1},"comment":{"body":"x"},"repository":{"full_name":"a/b"}}`},
	}
	for _, tc := range cases {
		if _, ok, err := ParseGitHub(tc.eventType, []byte(tc.payload)); ok || err != nil {
			t.Errorf("%s/%s: ok=%v err=%v, want ignored", tc.eventType, tc.payload, ok, err)
		}
	}
}

func TestParseGitHubMalformed(t *testing.T) {
	if _, _, err := ParseGitHub("issues", []byte(`{`)); err == nil {
		t.Fatal("malformed JSON must error")
	}
	if _, _, err := ParseGitHub("issues", []byte(`{"action":"opened"}`)); err == nil {
		t.Fatal("a delivery without a repository must error")
	}
}
