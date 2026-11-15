// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// AuxiliaryVersion fingerprints public model configuration, never credentials.
func (c *Client) AuxiliaryVersion() string {
	b, _ := json.Marshal([]any{"taskflow-v1", c.apiType, c.baseURL, c.model, c.sourceModel.Params})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// CompleteBounded makes exactly one tool-free provider request. Callers reserve
// input bytes plus output tokens before calling; bytes conservatively bound
// UTF-8 input tokens, with a separate allowance for protocol overhead. No
// transport, reasoning, passback or network-wait retry may spend that reserve
// again. The serving client's endpoint, transport and live credentials survive.
func (c *Client) CompleteBounded(ctx context.Context, system, user string, output int) (string, error) {
	if output < 1 || output > 8192 || len(system)+len(user) > 7500 {
		return "", errors.New("auxiliary completion exceeds its budget")
	}
	cfg := c.sourceModel
	cfg.MaxTokens, cfg.Thinking, cfg.ThinkingBudget = output, "off", 0
	b, err := NewClient(cfg)
	if err != nil {
		return "", err
	}
	b.hc, b.hcStream, b.keyFn, b.tokenFn = c.hc, c.hcStream, c.keyFn, c.tokenFn
	b.oauth, b.sessionID = c.oauth, c.sessionID
	b.maxTokens, b.maxTokensCap = output, output
	// Configured built-in tools/search cannot bypass this tool-free boundary.
	b.extraParams = nil
	b.thinkingDenied.Store(true)
	defer func() { u := b.Usage(); c.addUsage(u.InputTokens, u.OutputTokens) }()
	if b.noCredential() {
		return "", ErrNoKey
	}
	turns := []Turn{{Role: "user", Content: user}}
	var r Response
	switch b.apiType {
	case config.APITypeOpenAI:
		r, err = b.completeOnceOpenAI(ctx, system, turnsToOpenAI(system, turns), nil)
	case config.APITypeResponses:
		if strings.Contains(b.baseURL, "chatgpt.com/backend-api/codex") {
			var respBodyErr error
			resp, e := b.sendResponsesStream(ctx, system, turns, nil)
			if e != nil {
				return "", e
			}
			r, respBodyErr = b.readResponsesStream(ctx, resp.Body, nil, nil, nil)
			err = respBodyErr
		} else {
			r, err = b.completeOnceResponses(ctx, system, turns, nil)
		}
	case config.APITypeGoogle:
		r, err = b.completeOnceGoogle(ctx, system, turns, nil)
	default:
		r, err = b.completeOnce(ctx, messagesRequest{Model: b.model, MaxTokens: output, System: b.systemPayload(system), Messages: turnsToMessages(turns)})
	}
	if err != nil {
		return "", err
	}
	if len(r.ToolUses) != 0 || strings.TrimSpace(r.Text) == "" {
		return "", errors.New("auxiliary completion did not produce plain text")
	}
	return r.Text, nil
}
