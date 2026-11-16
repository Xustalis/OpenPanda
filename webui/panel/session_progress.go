// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"github.com/Xustalis/OpenPanda/internal/activity"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// askProgress is the reconnectable display state, not model replay. Callbacks
// and the periodic checkpoint can run concurrently, so they share this lock.
type askProgress struct {
	activities   []activity.Record
	mu           sync.Mutex
	text         strings.Builder
	thought      strings.Builder
	progress     map[string]any
	taskID       string
	lastActivity time.Time
	tokens       int64
	estimated    bool
}

func (p *askProgress) delta(text string, thought bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastActivity = time.Now()
	if thought {
		p.thought.WriteString(text)
	} else {
		p.text.WriteString(text)
	}
}
func (p *askProgress) status(event map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.progress = event
	p.lastActivity = time.Now()
}
func (p *askProgress) task(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.taskID = id
	p.lastActivity = time.Now()
}
func (p *askProgress) usage(tokens int64, estimated bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens, p.estimated, p.lastActivity = tokens, estimated, time.Now()
}
func (p *askProgress) snapshot(op sessions.Operation) sessions.Operation {
	p.mu.Lock()
	defer p.mu.Unlock()
	op.LastActivity, op.Tokens, op.Estimated = p.lastActivity, p.tokens, p.estimated
	op.Text, op.Thought, op.Progress = p.text.String(), p.thought.String(), p.progress
	if p.taskID != "" {
		op.TaskID = p.taskID
	}
	op.Activities = append([]activity.Record(nil), p.activities...)
	return op
}

func (p *askProgress) activity(record activity.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activities = activity.Upsert(p.activities, record)
	p.lastActivity = time.Now()
}

func (p *askProgress) settle(status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activities = activity.Settle(p.activities, status)
}
