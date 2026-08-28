package entry

import (
	"context"
	"strings"
	"testing"
)

// Test tag literals. open1/close1 are built by concatenation on purpose: a
// bare tag in the source would otherwise be stripped from the file's own
// documentation by future tooling that runs this code over it.
var (
	open1  = "<" + "think>"
	close1 = "</" + "think>"
)

// ---- stripThinkingBlock (whole-text backstop) ----

func TestStripThinkingBlock(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"no think passthrough", "普通回答", "普通回答"},
		{"complete block", "导入" + open1 + "推理过程" + close1 + "答案是 42", "导入答案是 42"},
		{"block with leading text", "前置思考" + open1 + "推理" + close1 + "答案", "前置思考答案"},
		{"case-insensitive tags", "导入" + "<THINK>推理</Think>" + "答案", "导入答案"},
		{"reasoning variant", "<reasoning>秘密</reasoning>\n可见", "可见"},
		{"thinking variant", "<thinking>秘密</thinking>可见", "可见"},
		{"mismatched names", "导入" + open1 + "推理</reasoning>" + "答案", "导入答案"},
		{"attributes tolerated", "导入" + "<think scope='x'>推理</think>" + "答案", "导入答案"},
		{"multiple blocks", "a" + open1 + "r1" + close1 + " b" + open1 + "r2" + close1 + " c", "a b c"},
		{"unterminated drops tail", "答案前" + open1 + "之后的推理", "答案前"},
		{"multiline reasoning", "行1\n行2" + open1 + "多行\n推理" + close1 + "答案", "行1\n行2答案"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripThinkingBlock(tc.in); got != tc.want {
				t.Fatalf("stripThinkingBlock(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestStripThinkingBlockJSONUntouched pins that structured task payloads —
// which contain braces and quotes but never think tags — survive the
// backstop byte-for-byte.
func TestStripThinkingBlockJSONUntouched(t *testing.T) {
	jsonPayload := `{"kind":"task","task":{"title":"跑测试","spec":{"scope":"全量"}}}`
	if got := stripThinkingBlock(jsonPayload); got != jsonPayload {
		t.Fatalf("task JSON must pass through unchanged, got %q", got)
	}
}

// ---- thinkStripper (streaming state machine) ----

// feedAll drives the stripper one delta at a time and returns everything it
// released.
func feedAll(t *testing.T, ts *thinkStripper, deltas ...string) string {
	t.Helper()
	var b strings.Builder
	for _, d := range deltas {
		b.WriteString(ts.feed(d))
	}
	return b.String()
}

func TestThinkStripperRemovesBlock(t *testing.T) {
	ts := &thinkStripper{}
	got := feedAll(t, ts, "导入"+open1, "推理过程", close1+"答案", "是 42")
	if got != "导入答案是 42" {
		t.Fatalf("released %q, want 导入答案是 42", got)
	}
}

// TestThinkStripperSplitTags pins the core streaming hazard: the opening
// and closing tags each split across delta boundaries.
func TestThinkStripperSplitTags(t *testing.T) {
	ts := &thinkStripper{}
	got := feedAll(t, ts, "前<"+"thi", "nk>秘密</th", "ink>后")
	if got != "前后" {
		t.Fatalf("released %q, want 前后", got)
	}
}

// TestThinkStripperSplitCloseTagOnly covers prose that runs into a reasoning
// block whose close tag is split.
func TestThinkStripperSplitCloseTagOnly(t *testing.T) {
	ts := &thinkStripper{}
	got := feedAll(t, ts, "前"+open1, "推理", "</rea", "soning>后")
	if got != "前后" {
		t.Fatalf("released %q, want 前后", got)
	}
}

// TestThinkStripperThinkingOnlyStream: a stream that is entirely reasoning
// releases nothing.
func TestThinkStripperThinkingOnlyStream(t *testing.T) {
	ts := &thinkStripper{}
	got := feedAll(t, ts, open1+"全程推理", "没有输出")
	if got != "" {
		t.Fatalf("released %q, want nothing", got)
	}
	if tail := ts.flush(); tail != "" {
		t.Fatalf("flush released %q from inside a think block", tail)
	}
}

// TestThinkStripperUnterminatedAtFlush: a stream cut mid-think (max_tokens)
// must drop the partial reasoning, not flush it as prose.
func TestThinkStripperUnterminatedAtFlush(t *testing.T) {
	ts := &thinkStripper{}
	got := feedAll(t, ts, open1+"导入", "被截断的推理")
	if got != "" {
		t.Fatalf("released %q, want nothing", got)
	}
	if tail := ts.flush(); tail != "" {
		t.Fatalf("flush leaked partial reasoning %q", tail)
	}
}

// TestThinkStripperFlushReleasesLiteralTail: bytes held back as a possible
// split tag that never completed are literal prose once the stream ends.
func TestThinkStripperFlushReleasesLiteralTail(t *testing.T) {
	ts := &thinkStripper{}
	got := feedAll(t, ts, "a < th", "inking about it")
	if got != "a < thinking about it" {
		t.Fatalf("released %q", got)
	}
}

// TestThinkStripperReasoningVariant covers the <reasoning> tag family split
// across deltas.
func TestThinkStripperReasoningVariant(t *testing.T) {
	ts := &thinkStripper{}
	got := feedAll(t, ts, "<rea", "soning>秘密</", "reasoning>可见")
	if got != "可见" {
		t.Fatalf("released %q, want 可见", got)
	}
}

// TestThinkStripperProseWithAngleBrackets: ordinary '<' bytes in prose must
// not be swallowed once proven not to start a think tag.
func TestThinkStripperProseWithAngleBrackets(t *testing.T) {
	ts := &thinkStripper{}
	got := feedAll(t, ts, "比较 a<b 和 c>d", " 以及 x<3")
	if got != "比较 a<b 和 c>d 以及 x<3" {
		t.Fatalf("released %q", got)
	}
}

// ---- deltaGuard integration ----

// TestDeltaGuardStripsThinkBeforeShapeDecision: a stream that opens with a
// reasoning block must reach the structured-shape decision as pure JSON, so
// the task spec stays suppressed instead of streaming to the user.
func TestDeltaGuardStripsThinkBeforeShapeDecision(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	g.on(open1)
	g.on("推理")
	g.on(close1)
	g.on(`{"kind":`)
	g.on(`"task":{}}`)
	if len(got) != 0 || g.delivered {
		t.Fatalf("forwarded %q delivered=%v, want reasoning-wrapped JSON fully suppressed", got, g.delivered)
	}
	if !g.structured {
		t.Fatal("guard must latch structured on the post-think JSON")
	}
}

// TestDeltaGuardStreamsProseAfterThink: the answer after a think block
// streams live to the user.
func TestDeltaGuardStreamsProseAfterThink(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	g.on(open1 + "推理")
	g.on(close1 + "你好")
	g.on("，世界")
	if strings.Join(got, "") != "你好，世界" {
		t.Fatalf("forwarded %q, want the post-think prose only", got)
	}
	if !g.delivered {
		t.Fatal("post-think prose counts as delivered")
	}
}

// TestStreamStripsThinkEndToEnd runs the full streaming path: reasoning
// inlined in content must reach neither onDelta nor Response.Text.
func TestStreamStripsThinkEndToEnd(t *testing.T) {
	c := startStreamServer(t, []string{open1 + "推理", close1 + "最终答案。"})
	var got []string
	resp, err := c.StreamTurnsWithTools(context.Background(), "", []Turn{{Role: "user", Content: "你好"}}, nil, func(s string) { got = append(got, s) }, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if joined := strings.Join(got, ""); joined != "最终答案。" {
		t.Fatalf("onDelta got %q, want 最终答案。", joined)
	}
	if resp.Text != "最终答案。" {
		t.Fatalf("Response.Text = %q, want the stripped answer", resp.Text)
	}
}
