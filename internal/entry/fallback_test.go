package entry

import (
	"strings"
	"testing"
)

// TestWrapAPIError400CarriesProviderDetail: the stock 400 hint tells the user
// to check model/api_type — which is wrong advice when the provider rejected
// something else (a retired model name, an oversized context). The provider's
// own rejection reason must ride along so the user can act on the real cause
// instead of guessing.
func TestWrapAPIError400CarriesProviderDetail(t *testing.T) {
	err := WrapAPIError(&statusError{
		status: 400,
		body:   `{"error":{"message":"Model Not Found: deepseek-chat had been deprecated","type":"invalid_request_error"}}`,
	})
	msg := err.Error()
	if !strings.Contains(msg, "400") {
		t.Fatalf("user message lost the status: %q", msg)
	}
	if !strings.Contains(msg, "Model Not Found") {
		t.Fatalf("user message missing the provider's reason: %q", msg)
	}
}

// TestWrapAPIError400SurvivesOddBodies: a non-JSON or empty body must degrade
// gracefully — the hint still reads cleanly, never a dangling separator.
func TestWrapAPIError400SurvivesOddBodies(t *testing.T) {
	for _, body := range []string{"", "   ", "plain text reason\nsecond line"} {
		msg := WrapAPIError(&statusError{status: 400, body: body}).Error()
		if strings.HasSuffix(msg, "：") || strings.HasSuffix(msg, ":") {
			t.Fatalf("dangling separator for body %q: %q", body, msg)
		}
	}
	msg := WrapAPIError(&statusError{status: 400, body: "plain text reason\nsecond line"}).Error()
	if !strings.Contains(msg, "plain text reason") {
		t.Fatalf("plain-text body should surface its first line: %q", msg)
	}
	if strings.Contains(msg, "second line") {
		t.Fatalf("only the first line of the body should surface: %q", msg)
	}
}

// TestWrapAPIError400TruncatesLongBodies: a provider verbose enough to fill a
// page must not flood the chat bubble — the excerpt stays bounded.
func TestWrapAPIError400TruncatesLongBodies(t *testing.T) {
	body := strings.Repeat("x", 5000)
	msg := WrapAPIError(&statusError{status: 400, body: body}).Error()
	if len(msg) > 500 {
		t.Fatalf("user message not bounded: %d chars", len(msg))
	}
}
