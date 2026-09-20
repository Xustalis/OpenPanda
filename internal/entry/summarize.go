package entry

import (
	"context"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

const summarizeCacheNS = "summarize"

// SummarizeResult asks the entry model to produce a user-facing summary of a
// task's outcome. It is the dedicated "report after execution" call: the
// engine invokes it after every inline task (success or failure) so the user
// sees a human-readable summary instead of raw stdout/stderr. A model
// failure degrades gracefully — the caller falls back to raw output, so the
// summary never blocks result delivery (review: LLM 汇报必须可降级).
//
// Identical task outcomes are served from the client's disk cache when present.
func SummarizeResult(ctx context.Context, c *Client, title, intent string, ok bool, exitCode int, stdout, stderr string, loc ...i18n.Locale) (string, error) {
	if c == nil {
		return "", fmt.Errorf("no model client")
	}

	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}

	systemPrompt := i18n.T(targetLoc, "prompt.summarize.system")

	k1 := hashString(fmt.Sprintf("%s\n%s\n%t:%d\n%s", title, intent, ok, exitCode, targetLoc))
	k2 := hashString(stdout + "\n---\n" + stderr)
	if dc := c.diskCache(); dc != nil {
		var cached string
		if dc.Get(ctx, summarizeCacheNS, k1, k2, &cached) && cached != "" {
			return cached, nil
		}
	}

	var b strings.Builder
	b.WriteString(i18n.Tf(targetLoc, "prompt.summarize.title", "title", title) + "\n")
	if intent != "" {
		b.WriteString(i18n.Tf(targetLoc, "prompt.summarize.intent", "intent", truncate(intent, 500)) + "\n")
	}
	if ok {
		b.WriteString(i18n.T(targetLoc, "prompt.summarize.success") + "\n")
	} else {
		b.WriteString(i18n.Tf(targetLoc, "prompt.summarize.failure", "code", fmt.Sprintf("%d", exitCode)) + "\n")
	}
	if out := truncate(strings.TrimSpace(stdout), 3000); out != "" {
		b.WriteString(i18n.T(targetLoc, "prompt.summarize.stdout_excerpt") + "\n" + out + "\n")
	}
	if errText := truncate(strings.TrimSpace(stderr), 1500); errText != "" {
		b.WriteString(i18n.T(targetLoc, "prompt.summarize.stderr_excerpt") + "\n" + errText + "\n")
	}
	text, err := c.Complete(ctx, systemPrompt, b.String())
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("empty summary")
	}
	if dc := c.diskCache(); dc != nil {
		dc.Put(ctx, summarizeCacheNS, k1, k2, text)
	}
	return text, nil
}
