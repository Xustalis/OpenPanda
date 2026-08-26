package bus

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// A task result is the one payload whose size the user controls: it carries a
// child process's stdout, and executil.Capture allows 8 MiB of it while
// readLimit caps a frame at 4 MiB. An unclamped result therefore does not
// arrive large — it does not arrive at all, because the receiving side's read
// limit closes the connection, and the finished work is lost with the link.
func TestTaskResultClampedToFitAFrame(t *testing.T) {
	env, err := NewEnvelope(MsgTaskResult, "win", "m1", TaskResultPayload{
		TaskID: "t1", OK: true,
		Stdout: strings.Repeat("训练日志行\n", 900_000),  // ~14 MiB
		Stderr: strings.Repeat("warn\n", 2_000_000), // ~10 MiB
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	if len(env.Payload) >= readLimit {
		t.Fatalf("payload is %d bytes, transport rejects anything over %d", len(env.Payload), readLimit)
	}

	var got TaskResultPayload
	if err := env.PayloadInto(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !utf8.ValidString(got.Stdout) {
		t.Error("clamped stdout is not valid UTF-8 (cut mid-rune)")
	}
	// Head and tail both survive: a long log's answer is at its end.
	if !strings.HasPrefix(got.Stdout, "训练日志行") || !strings.HasSuffix(got.Stdout, "训练日志行\n") {
		t.Errorf("clamp dropped the head or the tail: %.60q…%.60q",
			got.Stdout, got.Stdout[max(0, len(got.Stdout)-60):])
	}
	if !strings.Contains(got.Stdout, "已截断") {
		t.Error("clamped output does not say it was truncated")
	}
	if got.TaskID != "t1" || !got.OK {
		t.Errorf("clamping changed the rest of the payload: %+v", got)
	}
}

// Output that already fits must travel byte-for-byte: the clamp is a ceiling,
// not a reformatter, and a task's result is what the user reads.
func TestTaskResultUnderLimitIsUntouched(t *testing.T) {
	out := "accuracy 0.93\n"
	env, err := NewEnvelope(MsgTaskResult, "win", "m2", TaskResultPayload{TaskID: "t2", Stdout: out})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	var got TaskResultPayload
	if err := env.PayloadInto(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Stdout != out {
		t.Errorf("stdout = %q, want %q unchanged", got.Stdout, out)
	}
}

// The clamp must not mutate the caller's struct: the same value is what the
// sender wrote its own task row from, and a local row silently replaced by the
// truncated copy would lose output that never needed to travel.
func TestClampDoesNotMutateCaller(t *testing.T) {
	p := TaskResultPayload{TaskID: "t3", Stdout: strings.Repeat("x", maxWireText+10)}
	before := len(p.Stdout)
	if _, err := NewEnvelope(MsgTaskResult, "win", "m3", p); err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	if len(p.Stdout) != before {
		t.Errorf("caller's stdout shrank from %d to %d bytes", before, len(p.Stdout))
	}
}

// Every payload still round-trips through the generic envelope path; the
// clamper is opt-in per type, not a change to how messages are encoded.
func TestNonClampedPayloadRoundTrips(t *testing.T) {
	env, err := NewEnvelope(MsgTaskDecline, "pi", "m4", TaskDeclinePayload{TaskID: "t4", Reason: "no capacity"})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	var got TaskDeclinePayload
	if err := env.PayloadInto(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reason != "no capacity" {
		t.Errorf("decline payload = %+v", got)
	}
	var probe map[string]any
	if err := json.Unmarshal(env.Payload, &probe); err != nil {
		t.Fatalf("payload is not a JSON object: %v", err)
	}
}
