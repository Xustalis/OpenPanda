// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// A background completion is a handoff, not merely a state notification.
// Read existing evidence only: no additional provider call and no invented tests.
func taskHandoff(t core.Task, loc i18n.Locale) (body, remaining string) {
	var payload map[string]any
	_ = json.Unmarshal([]byte(t.ResultJSON), &payload)
	for _, key := range []string{"result", "answer", "stdout", "stderr", "failed"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			body = strings.TrimSpace(value)
			break
		}
	}
	if body == "" {
		key := "tui.task.noSummary"
		if t.State != core.StateDone {
			key = "tui.task.noResult"
		}
		body = i18n.T(loc, key)
	}
	if runes := []rune(body); len(runes) > 24000 {
		body = string(runes[:24000]) + "\n\n" + i18n.Tf(loc, "tui.task.rawLogHint", "id", t.TaskID)
	}
	for _, key := range []string{"followup", "verdict_reason"} {
		if key == "verdict_reason" && t.State == core.StateDone && payload["needs_followup"] != true {
			continue
		}
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			remaining = strings.TrimSpace(value)
			break
		}
	}
	return body, remaining
}

func taskRemainingNote(loc i18n.Locale, state, verification string) string {
	key := "tui.task.remainingUnreported"
	switch {
	case state == core.StateReview:
		key = "tui.task.remainingReview"
	case state != core.StateDone:
		key = "tui.task.remainingFailed"
	case verification == "failed" || verification == "partial":
		key = "tui.task.remainingFollowup"
	}
	return i18n.T(loc, key)
}

// Short action evidence for an out-of-band TUI completion. Provider thoughts
// and narration are deliberately excluded from the execution checklist.
func taskHandoffSteps(events []core.Event) []string {
	var steps []string
	for _, event := range events {
		if event.Type != "agent_event" {
			continue
		}
		var data struct {
			Ev    string         `json:"ev"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		if json.Unmarshal([]byte(event.DataJSON), &data) != nil || data.Ev != "tool_use" {
			continue
		}
		line := strings.TrimSpace(data.Name)
		for _, key := range []string{"command", "file_path", "path", "query", "url"} {
			if value, ok := data.Input[key].(string); ok && strings.TrimSpace(value) != "" {
				if line != "" {
					line += " · "
				}
				line += strings.Join(strings.Fields(value), " ")
				break
			}
		}
		if line == "" {
			continue
		}
		if runes := []rune(line); len(runes) > 180 {
			line = string(runes[:180]) + "…"
		}
		if len(steps) > 0 && steps[len(steps)-1] == line {
			continue
		}
		steps = append(steps, line)
	}
	if len(steps) > 6 {
		steps = steps[len(steps)-6:]
	}
	return steps
}
