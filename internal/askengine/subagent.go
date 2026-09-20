package askengine

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// The sub-agent round (design: "调度变成子代理"): a classified task is one step
// of the conversation, not its end. The dispatch is replayed as the entry
// model's own words and the outcome fed back as an observation, so the next
// classification reports on it — the session survives the task instead of
// being terminated by it, whether the task ran on this node's agent, another
// node's agent, or the queue. The helpers here compose those turns; the loop
// that drives them is AskTurns.

// taskDispatchNote replays a dispatch as the entry model's own words, so the
// transcript reads as one continuous conversation around a sub-agent round:
// the model said it would run the task, the outcome arrives as the next
// observation, and the following call reports on it. Feeding the dispatch
// back this way (rather than a synthetic "task submitted" note) keeps the
// model's authorship of the plan visible to itself when it converges.
func taskDispatchNote(spec *entry.TaskSpec, loc ...i18n.Locale) string {
	targetLoc := i18n.Locale("")
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	if targetLoc == "" {
		if containsHan(spec.Title) || containsHan(spec.Spec.Target) {
			targetLoc = i18n.ChineseSimp
		} else {
			targetLoc = i18n.Detect()
		}
	}

	var b strings.Builder
	b.WriteString(i18n.Tf(targetLoc, "prompt.subagent.dispatch", "title", spec.Title))
	var subagentTypes []string
	if spec.Spec.Node != "" {
		subagentTypes = append(subagentTypes, i18n.Tf(targetLoc, "prompt.subagent.dispatch_node", "node", spec.Spec.Node))
	}
	for _, ab := range spec.Requires.Abilities {
		if strings.HasPrefix(ab, "agent:") {
			subagentTypes = append(subagentTypes, i18n.Tf(targetLoc, "prompt.subagent.dispatch_harness", "harness", strings.TrimPrefix(ab, "agent:")))
		}
	}
	if len(subagentTypes) > 0 {
		fmt.Fprintf(&b, " 【Subagent: %s】", strings.Join(subagentTypes, " · "))
	}
	if len(spec.Requires.Abilities) > 0 {
		sep := "、"
		if targetLoc == i18n.English {
			sep = ", "
		}
		b.WriteString(i18n.Tf(targetLoc, "prompt.subagent.dispatch_abilities", "abilities", strings.Join(spec.Requires.Abilities, sep)))
	}
	if spec.Spec.Target != "" {
		b.WriteString(i18n.Tf(targetLoc, "prompt.subagent.dispatch_target", "target", spec.Spec.Target))
	}
	if spec.Spec.Node != "" {
		b.WriteString(i18n.Tf(targetLoc, "prompt.subagent.dispatch_assigned_node", "node", spec.Spec.Node))
	}
	return b.String()
}

// taskBudgetNote is appended as a user turn when the loop refuses the model's
// latest dispatch because the task budget is spent: the final tool-free call
// then explains itself instead of silently dropping the model's intent.
func taskBudgetNote(maxTasks int, loc ...i18n.Locale) string {
	targetLoc := i18n.Detect()
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	return i18n.Tf(targetLoc, "prompt.subagent.budget_note", "n", strconv.Itoa(maxTasks))
}

// taskObservation formats one executed task's outcome as the observation the
// entry model reports on: state, exit code, and excerpts of the output. The
// agent transcript behind a task can be tens of thousands of tokens, so the
// observation carries an excerpt only — the full output travels in the Result
// (Stdout/Stderr) for the caller to surface on demand.
func taskObservation(res *Result, loc ...i18n.Locale) string {
	targetLoc := i18n.Locale("")
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	if targetLoc == "" {
		if containsHan(res.TaskTitle) || containsHan(res.Stdout) {
			targetLoc = i18n.ChineseSimp
		} else {
			targetLoc = i18n.Detect()
		}
	}

	var b strings.Builder
	b.WriteString(i18n.Tf(targetLoc, "prompt.subagent.header",
		"title", res.TaskTitle,
		"id", res.TaskID,
		"state", res.TaskState,
	))
	if res.Agent != "" {
		b.WriteString(i18n.Tf(targetLoc, "prompt.subagent.agent", "agent", res.Agent))
		if res.Model != "" {
			b.WriteString(i18n.Tf(targetLoc, "prompt.subagent.model", "model", res.Model))
		}
		if res.Injected {
			b.WriteString(i18n.T(targetLoc, "prompt.subagent.injected"))
		}
	}
	if res.ExitCode != 0 {
		b.WriteString(i18n.Tf(targetLoc, "prompt.subagent.exit_code", "code", strconv.Itoa(res.ExitCode)))
	}
	// Adaptive excerpting: 6000 runes for stdout and 2500 for stderr to bound prompt token growth
	if out := excerpt(res.Stdout, 6000, targetLoc); out != "" {
		fmt.Fprintf(&b, "\n%s\n%s", i18n.T(targetLoc, "prompt.subagent.stdout"), out)
	}
	if errText := excerpt(res.Stderr, 2500, targetLoc); errText != "" {
		fmt.Fprintf(&b, "\n%s\n%s", i18n.T(targetLoc, "prompt.subagent.stderr"), errText)
	}
	if res.OK && (res.TaskState == "done" || res.ExitCode == 0) {
		b.WriteString(i18n.T(targetLoc, "prompt.subagent.done_core_instruction"))
	} else {
		b.WriteString(i18n.T(targetLoc, "prompt.subagent.continue_instruction"))
	}
	return b.String()
}

// excerpt trims a log to at most limit runes keeping head and tail — the
// beginning says what was attempted, the end says how it finished.
func excerpt(s string, limit int, loc ...i18n.Locale) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= limit {
		return string(runes)
	}
	targetLoc := i18n.Detect()
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	omitted := i18n.T(targetLoc, "prompt.subagent.excerpt_omitted")
	head := limit * 2 / 3
	tail := limit / 3
	return string(runes[:head]) + omitted + string(runes[len(runes)-tail:])
}

// reportTaskOutcome converges on the model's report when the round budget
// leaves no room for another loop iteration after a task ran: the dispatch and
// its observation are appended to the accumulated history and one tool-free
// call produces the report the loop would have converged on. Without tools the
// model can only answer in text, so the ask ends in a report rather than raw
// output.
func (e *Engine) reportTaskOutcome(ctx context.Context, client *entry.Client, turns []entry.Turn, devices []ledger.Node, conversationMemory string, opts []entry.ClassifyOption, spec *entry.TaskSpec, res *Result) (string, error) {
	if client == nil {
		client = e.client.Load()
	}
	t := append(append([]entry.Turn{}, turns...),
		entry.Turn{Role: "assistant", Content: taskDispatchNote(spec, e.locale)},
		entry.Turn{Role: "user", Content: taskObservation(res, e.locale)},
	)
	out, err := entry.ClassifyTurns(ctx, client, devices, conversationMemory, t, opts...)
	if err != nil {
		return "", err
	}
	if out.Kind != entry.KindAnswer || out.Answer == "" {
		return "", fmt.Errorf("report call converged on %s instead of an answer", out.Kind)
	}
	return out.Answer, nil
}
