// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// BriefModelConfig bounds auxiliary verdict/digest generation independently
// of the model's conversational capacity. It preserves the selected endpoint
// and credentials and never changes the user's configured agent model.
func BriefModelConfig(model config.ModelConfig) config.ModelConfig {
	if model.MaxTokens <= 0 || model.MaxTokens > 2048 {
		model.MaxTokens = 2048
	}
	model.Thinking = "off"
	model.ThinkingBudget = 0
	if model.MaxRetries == 0 || model.MaxRetries > 1 {
		model.MaxRetries = 1
	}
	return model
}

// CompleteBrief gives a digest its own bounded client, reusing the serving
// client's transport and live credential resolver. Concurrent conversation
// requests keep their normal output/reasoning settings and usage is charged
// back exactly once (this auxiliary client serves just this call).
func (c *Client) CompleteBrief(ctx context.Context, system, user string) (string, error) {
	brief, err := NewClient(BriefModelConfig(c.sourceModel))
	if err != nil {
		return "", err
	}
	brief.hc, brief.hcStream = c.hc, c.hcStream
	brief.keyFn, brief.tokenFn = c.keyFn, c.tokenFn
	brief.sessionID = c.sessionID
	brief.promptCache.Store(c.promptCache.Load())
	brief.thinkingDenied.Store(c.thinkingDenied.Load())
	defer func() {
		u := brief.Usage()
		c.addUsage(u.InputTokens, u.OutputTokens)
	}()
	return brief.Complete(ctx, system, user)
}
