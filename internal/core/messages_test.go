// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// msgCore builds the minimal Core the board paths need: a store, a node id,
// and a logger. Message delivery on a single node never touches transport.
func msgCore(t *testing.T) *Core {
	t.Helper()
	db := openTestDB(t)
	c := &Core{db: db, nodeID: "node-a", logger: testLogger()}
	c.store = NewTaskStore(db, testLogger())
	return c
}

// TestTaskMessageSlotSemantics pins the temporal-buffer contract: one slot per
// (scope, sender, rcpt), newest seq wins, a late-arriving older copy loses.
func TestTaskMessageSlotSemantics(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := TaskMessage{Scope: "plan:p1", Sender: "t1", Grade: MsgGradeStatus, Body: "first", SentAt: 100}
	if err := s.PutTaskMessage(ctx, m); err != nil {
		t.Fatalf("put: %v", err)
	}
	m.SenderSeq, m.Body = 2, "second"
	if err := s.PutTaskMessage(ctx, m); err != nil {
		t.Fatalf("put newer: %v", err)
	}
	m.SenderSeq, m.Body = 1, "stale"
	if err := s.PutTaskMessage(ctx, m); err != nil {
		t.Fatalf("put stale: %v", err)
	}
	msgs, err := s.TaskMessages(ctx, "plan:p1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "second" {
		t.Fatalf("slot = %+v, want single slot body=second", msgs)
	}
	// A directed slot to a second recipient is a different slot, not an
	// overwrite — the broadcast above must survive it.
	directed := m
	directed.Rcpt, directed.SenderSeq, directed.Body = "t2", 3, "for t2"
	if err := s.PutTaskMessage(ctx, directed); err != nil {
		t.Fatalf("put directed: %v", err)
	}
	msgs, _ = s.TaskMessages(ctx, "plan:p1")
	if len(msgs) != 2 {
		t.Fatalf("slots = %d, want 2 (broadcast + directed)", len(msgs))
	}
	// Scope isolation: another plan's board sees nothing.
	if other, _ := s.TaskMessages(ctx, "plan:p2"); len(other) != 0 {
		t.Fatalf("scope leak: %+v", other)
	}
}

// TestNextMsgSeqMonotone: a sender's sequence starts at 1 and only ever grows,
// so the receiver-side stale guard can trust it.
func TestNextMsgSeqMonotone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if got := s.NextMsgSeq(ctx, "plan:p1", "t1"); got != 1 {
		t.Fatalf("first seq = %d, want 1", got)
	}
	m := TaskMessage{Scope: "plan:p1", Sender: "t1", SenderSeq: 1, Grade: MsgGradeStatus, Body: "x"}
	if err := s.PutTaskMessage(ctx, m); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := s.NextMsgSeq(ctx, "plan:p1", "t1"); got != 2 {
		t.Fatalf("next seq = %d, want 2", got)
	}
	// A different sender's slots do not advance this sender's counter.
	if got := s.NextMsgSeq(ctx, "plan:p1", "t2"); got != 1 {
		t.Fatalf("other sender seq = %d, want 1", got)
	}
}

// TestTaskMessagesExpiredFiltered: an expired slot never reaches a reader —
// TTL is the board's garbage collector, not a render-time hint.
func TestTaskMessagesExpiredFiltered(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour).Unix()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO task_messages
		(scope, sender, rcpt, sender_seq, grade, body, sent_at, recv_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		"plan:p1", "t1", "", 1, MsgGradeStatus, "old", past, past, past); err != nil {
		t.Fatalf("seed expired: %v", err)
	}
	if msgs, _ := s.TaskMessages(ctx, "plan:p1"); len(msgs) != 0 {
		t.Fatalf("expired slot surfaced: %+v", msgs)
	}
}

// TestParseMessages covers the marker protocol: inline JSON, a pretty-printed
// payload, and the preservation of anything that fails to parse.
func TestParseMessages(t *testing.T) {
	inline := "working\nPANDA_MSG {\"to\":\"parent\",\"grade\":\"status\",\"body\":\"halfway\"}\n"
	reqs, rest := parseMessages(inline)
	if len(reqs) != 1 || reqs[0].To != "parent" || reqs[0].Body != "halfway" || reqs[0].Grade != "status" {
		t.Fatalf("inline parse = %+v", reqs)
	}
	if strings.Contains(rest, msgMarker) || !strings.Contains(rest, "working") {
		t.Fatalf("stdout after strip = %q", rest)
	}

	pretty := "PANDA_MSG {\n  \"to\": \"siblings\",\n  \"body\": \"two lines\"\n}\ndone"
	reqs, _ = parseMessages(pretty)
	if len(reqs) != 1 || reqs[0].Body != "two lines" {
		t.Fatalf("multiline parse = %+v", reqs)
	}

	malformed := "PANDA_MSG {\"to\":\"parent\"}\nPANDA_MSG not json\nPANDA_MSG\n"
	reqs, rest = parseMessages(malformed)
	if len(reqs) != 0 {
		t.Fatalf("malformed accepted: %+v", reqs)
	}
	if strings.Count(rest, msgMarker) != 3 {
		t.Fatalf("malformed markers dropped from stdout: %q", rest)
	}

	// A mention in prose ("use PANDA_MSG to ...") is not a marker line.
	prose := "You can use PANDA_MSG to talk to siblings.\n"
	reqs, rest = parseMessages(prose)
	if len(reqs) != 0 || rest != prose {
		t.Fatalf("prose consumed: reqs=%+v rest=%q", reqs, rest)
	}
}

// TestMessageScopes: a plan stage's board is the plan, a child's is its
// parent's, and a standalone task has none — but every task can host and read
// its own children board.
func TestMessageScopes(t *testing.T) {
	stage := Task{TaskID: "t1", PlanID: "p1"}
	if got := messageScope(stage); got != "plan:p1" {
		t.Fatalf("stage scope = %q", got)
	}
	child := Task{TaskID: "t2", ParentID: "p9"}
	if got := messageScope(child); got != "task:p9" {
		t.Fatalf("child scope = %q", got)
	}
	standalone := Task{TaskID: "t3"}
	if got := messageScope(standalone); got != "" {
		t.Fatalf("standalone scope = %q", got)
	}
	scopes := BoardScopes(standalone)
	if len(scopes) != 1 || scopes[0] != "task:t3" {
		t.Fatalf("standalone boards = %v", scopes)
	}
}

// TestResolveMsgRcpt: addressing resolves against the roster, refuses
// strangers and self-posts, and only a parent may address "children".
func TestResolveMsgRcpt(t *testing.T) {
	members := []scopeMember{
		{taskID: "a", stage: "build"},
		{taskID: "b", stage: "test"},
		{taskID: "p"},
	}
	cases := []struct {
		name, scope, to, sender string
		want                    string
		wantErr                 bool
	}{
		{name: "broadcast", scope: "plan:p1", to: "", sender: "a", want: ""},
		{name: "siblings", scope: "plan:p1", to: "siblings", sender: "a", want: ""},
		{name: "stage id", scope: "plan:p1", to: "stage:build", sender: "b", want: "a"},
		{name: "task id", scope: "plan:p1", to: "b", sender: "a", want: "b"},
		{name: "task prefix", scope: "plan:p1", to: "task:b", sender: "a", want: "b"},
		{name: "parent", scope: "task:p", to: "parent", sender: "a", want: "p"},
		{name: "parent on plan board", scope: "plan:p1", to: "parent", sender: "a", wantErr: true},
		{name: "self post", scope: "plan:p1", to: "a", sender: "a", wantErr: true},
		{name: "stranger", scope: "plan:p1", to: "zz", sender: "a", wantErr: true},
		{name: "unknown stage", scope: "plan:p1", to: "stage:deploy", sender: "a", wantErr: true},
		{name: "children broadcast", scope: "task:a", to: "children", sender: "a", want: ""},
		{name: "children not on own board", scope: "plan:p1", to: "children", sender: "a", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := TaskMessage{Scope: tc.scope, Sender: tc.sender}
			got, err := resolveMsgRcpt(m, tc.to, members, tc.sender)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolve(%q) = %q, want error", tc.to, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("resolve(%q) = %q, %v; want %q", tc.to, got, err, tc.want)
			}
		})
	}

	// Board lines render senders shortened; the copied prefix must resolve.
	long := []scopeMember{
		{taskID: "01a13309-aaaa-7d92-849d-7c3fd525b56a", stage: "build"},
		{taskID: "01a13309-aaaa-7e02-9adc-ec08c77cd7c9"},
		{taskID: "01a1330a-bbbb-7e02-9adc-ec08c77cd7c9"},
	}
	m := TaskMessage{Scope: "plan:p1", Sender: "other"}
	if got, err := resolveMsgRcpt(m, "01a1330a-bbbb", long, "other"); err != nil || got != long[2].taskID {
		t.Fatalf("prefix resolve = %q, %v; want %q", got, err, long[2].taskID)
	}
	// A prefix matching two members is refused, not guessed.
	if _, err := resolveMsgRcpt(m, "01a13309-aaaa", long, "other"); err == nil {
		t.Fatal("ambiguous prefix resolved")
	}
	// A prefix shorter than the floor never resolves.
	if _, err := resolveMsgRcpt(m, "01a13", long, "other"); err == nil {
		t.Fatal("short prefix resolved")
	}
}

// TestMsgBoardOwner: plan boards belong to the chain root; children boards
// belong to the parent's executor — the local spawner for a local row, the
// dispatching hop for a row that arrived over the wire.
func TestMsgBoardOwner(t *testing.T) {
	c := &Core{nodeID: "node-c", logger: testLogger()}
	cases := []struct {
		name  string
		scope string
		task  Task
		want  string
	}{
		{name: "plan origin", scope: "plan:p1", task: Task{Chain: []string{"node-a", "node-c"}}, want: "node-a"},
		{name: "plan local", scope: "plan:p1", task: Task{Chain: []string{"node-c"}}, want: "node-c"},
		{name: "own children board", scope: "task:t1", task: Task{TaskID: "t1"}, want: "node-c"},
		{name: "remote child", scope: "task:p9", task: Task{TaskID: "t2", ParentID: "p9", Remote: true, Chain: []string{"node-a", "node-b", "node-c"}}, want: "node-b"},
		{name: "local child", scope: "task:p9", task: Task{TaskID: "t2", ParentID: "p9", Chain: []string{"node-a", "node-c"}}, want: "node-c"},
		{name: "root chain fallback", scope: "task:p9", task: Task{TaskID: "t2", ParentID: "p9", Remote: true, Chain: []string{"node-c"}}, want: "node-c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.msgBoardOwner(tc.scope, tc.task); got != tc.want {
				t.Fatalf("owner = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMessageScopeFor: a parent addressing a child by id (or "children") posts
// on the children board it hosts; everyone else posts on their own scope.
func TestMessageScopeFor(t *testing.T) {
	c := msgCore(t)
	ctx := context.Background()
	parent := createTask(t, c.store, "", "parent", c.nodeID)
	child := createTask(t, c.store, parent.TaskID, "child", c.nodeID)

	scope, err := c.messageScopeFor(ctx, parent, child.TaskID)
	if err != nil || scope != "task:"+parent.TaskID {
		t.Fatalf("parent->child scope = %q, %v", scope, err)
	}
	scope, err = c.messageScopeFor(ctx, parent, "children")
	if err != nil || scope != "task:"+parent.TaskID {
		t.Fatalf("children scope = %q, %v", scope, err)
	}
	// The child posting to "parent" stays on its own scope — the same board.
	scope, err = c.messageScopeFor(ctx, child, "parent")
	if err != nil || scope != "task:"+parent.TaskID {
		t.Fatalf("child->parent scope = %q, %v", scope, err)
	}
	// A standalone task with no parent and no plan has nowhere to post.
	lone := createTask(t, c.store, "", "lone", c.nodeID)
	if _, err := c.messageScopeFor(ctx, lone, "siblings"); err == nil {
		t.Fatal("standalone task accepted a sibling post")
	}
}

// TestBoardDeliveryAndDrain is the local end-to-end: siblings post, the board
// holds one slot each, drain renders lines for everyone but the sender, and
// directed slots reach only the member they name.
func TestBoardDeliveryAndDrain(t *testing.T) {
	c := msgCore(t)
	ctx := context.Background()
	parent := createTask(t, c.store, "", "parent", c.nodeID)
	c1 := createTask(t, c.store, parent.TaskID, "child-one", c.nodeID)
	c2 := createTask(t, c.store, parent.TaskID, "child-two", c.nodeID)

	if err := c.deliverTaskMessage(ctx, c1, msgRequest{To: "siblings", Grade: MsgGradeStatus, Body: "halfway"}); err != nil {
		t.Fatalf("deliver broadcast: %v", err)
	}
	if err := c.deliverTaskMessage(ctx, c1, msgRequest{To: c2.TaskID, Grade: MsgGradeReport, Body: "for two only"}); err != nil {
		t.Fatalf("deliver directed: %v", err)
	}

	// Child two sees the broadcast and the directed note.
	lines := c.drainTaskMessages(ctx, c2, msgSeen{})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "halfway") || !strings.Contains(joined, "for two only") {
		t.Fatalf("c2 board = %q", joined)
	}
	// The parent sees the broadcast but not the directed note.
	lines = c.drainTaskMessages(ctx, parent, msgSeen{})
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "halfway") {
		t.Fatalf("parent board = %q", joined)
	}
	if strings.Contains(joined, "for two only") {
		t.Fatalf("directed note leaked to parent: %q", joined)
	}
	// The sender never reads its own words back.
	if lines := c.drainTaskMessages(ctx, c1, msgSeen{}); len(lines) != 0 {
		t.Fatalf("sender echoed: %q", lines)
	}

	// Incremental drain: a second read with the same seen-map is empty; a
	// newer post on the same slot surfaces alone.
	seen := msgSeen{}
	c.drainTaskMessages(ctx, c2, seen)
	if again := c.drainTaskMessages(ctx, c2, seen); len(again) != 0 {
		t.Fatalf("re-drained unchanged board: %q", again)
	}
	if err := c.deliverTaskMessage(ctx, c1, msgRequest{To: "siblings", Grade: MsgGradeStatus, Body: "halfway+"}); err != nil {
		t.Fatalf("deliver update: %v", err)
	}
	again := c.drainTaskMessages(ctx, c2, seen)
	if len(again) != 1 || !strings.Contains(again[0], "halfway+") {
		t.Fatalf("incremental drain = %q", again)
	}
}

// TestSendAgentMessagesBudget: the per-episode budget meters sends — the
// over-budget request is refused with a note the agent reads next round, not
// silently dropped.
func TestSendAgentMessagesBudget(t *testing.T) {
	c := msgCore(t)
	ctx := context.Background()
	parent := createTask(t, c.store, "", "parent", c.nodeID)
	child := createTask(t, c.store, parent.TaskID, "child", c.nodeID)

	sent := 0
	reqs := []msgRequest{
		{To: "parent", Grade: MsgGradeStatus, Body: "one"},
		{To: "parent", Grade: MsgGradeStatus, Body: "two"},
	}
	notes := c.sendAgentMessages(ctx, child, reqs, &sent, 1)
	if len(notes) != 2 || !strings.Contains(notes[0], "posted") || !strings.Contains(notes[1], "budget") {
		t.Fatalf("budget notes = %q", notes)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want 1", sent)
	}

	// A refused grade is a per-request failure, not a budget spend.
	sent = 0
	notes = c.sendAgentMessages(ctx, child, []msgRequest{{To: "parent", Grade: "shout", Body: "x"}}, &sent, 4)
	if len(notes) != 1 || !strings.Contains(notes[0], "failed") {
		t.Fatalf("grade note = %q", notes)
	}
}

// TestHandleTaskMessageFanout stores an owner-resolved copy for a local
// member, and drops copies that name a task this node cannot vouch for.
func TestHandleTaskMessageFanout(t *testing.T) {
	c := msgCore(t)
	ctx := context.Background()
	parent := createTask(t, c.store, "", "parent", c.nodeID)
	child := createTask(t, c.store, parent.TaskID, "child", c.nodeID)

	env := func(p bus.TaskMessagePayload) bus.Envelope {
		e, err := bus.NewEnvelope(bus.MsgTaskMessage, "node-b", "m1", p)
		if err != nil {
			t.Fatalf("envelope: %v", err)
		}
		return e
	}
	base := bus.TaskMessagePayload{
		Scope: "task:" + parent.TaskID, Sender: "remote-task", Member: child.TaskID,
		Grade: MsgGradeStatus, Body: "from the wire", Seq: 1,
		SentAt: time.Now().Unix(), Fanout: true,
	}
	c.handleTaskMessage(ctx, env(base))
	msgs, err := c.store.TaskMessages(ctx, "task:"+parent.TaskID)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "from the wire" {
		t.Fatalf("fanout not stored: %+v, %v", msgs, err)
	}

	// A copy naming a task of another scope is a misroute or forgery.
	forged := base
	forged.Member = "some-other-task"
	c.handleTaskMessage(ctx, env(forged))
	if msgs, _ := c.store.TaskMessages(ctx, "task:"+parent.TaskID); len(msgs) != 1 {
		t.Fatalf("forged fanout stored: %+v", msgs)
	}
}

// TestMessageBudgetDefault: an unset cap falls back to the package default.
func TestMessageBudgetDefault(t *testing.T) {
	c := &Core{}
	if got := c.msgBudget(); got != defaultMsgBudget {
		t.Fatalf("default budget = %d, want %d", got, defaultMsgBudget)
	}
	c.SetMessageBudget(3)
	if got := c.msgBudget(); got != 3 {
		t.Fatalf("set budget = %d, want 3", got)
	}
}
