// SPDX-License-Identifier: AGPL-3.0-or-later
package askengine

import (
	"context"
	"errors"
	"fmt"
	"github.com/Xustalis/OpenPanda/internal/activity"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"time"
)

func startToolActivity(cb StreamCallbacks, call *entry.ToolCall, round, index int) activity.Record {
	input, clipped := activity.Input(call.Arguments)
	record := activity.Record{ID: fmt.Sprintf("%d:%d:%s", round, index, call.ID), Sequence: round*10000 + index, Round: round, ToolID: call.ID, Name: call.Tool, Status: "running", Input: input, StartedAt: time.Now().UnixMilli(), Truncated: clipped}
	if cb.OnActivity != nil {
		cb.OnActivity(record)
	}
	return record
}

func finishToolActivity(cb StreamCallbacks, record activity.Record, result string, err error) {
	if record.Status == "running" {
		record.Status = "success"
	}
	if errors.Is(err, context.Canceled) {
		record.Status = "cancelled"
	} else if err != nil {
		record.Status = "failed"
	}
	output, clipped := activity.Bound(result)
	record.Output, record.Truncated, record.FinishedAt = output, record.Truncated || clipped, time.Now().UnixMilli()
	if cb.OnActivity != nil {
		cb.OnActivity(record)
	}
}

// An invocation-local outcome keeps the legacy tool error contract intact.
type toolActivityStatusKey struct{}

func toolActivityOutcome(ctx context.Context, status string) error {
	if target, ok := ctx.Value(toolActivityStatusKey{}).(*string); ok {
		*target = status
	}
	return nil
}
