package entry

import (
	"regexp"
	"strings"
)

// Reasoning-block removal. Some models — DeepSeek-R1-style reasoners, and the
// OpenAI-compatible relays in front of them — inline chain-of-thought into
// the *content* field wrapped in  tags. That is model-private scratch
// space: it must never reach the user, the session history, or a task result
// (D14), where it would otherwise leak reasoning and compound into every
// later prompt. Two layers remove it:
//
//   - Streaming deltas flow through thinkStripper (below), the stateful tag
//     recognizer deltaGuard runs in front of every other decision, which
//     recognizes tags split across chunk boundaries.
//   - Texts assembled whole — non-streaming parses and end-of-stream
//     accumulator joins — go through the stateless stripThinkingBlock here.
//
// Providers that put reasoning in a *separate* field (OpenAI-compat
// delta.reasoning_content, Anthropic thinking blocks) never leak: the wire
// structs parse those fields only to discard them.

// thinkBlockRE matches one complete inlined reasoning block: paired
//
//	tags (case-insensitive, spanning newlines). The
//
// <thinking>/<reasoning> variants cover relays and models that renamed the
// tag; a short attribute tail after the tag name is tolerated; open and
// close names may differ (relay relabeling). Surrounding whitespace is left
// alone so prose on either side of the block never glues; leading/trailing
// residue is cleaned by TrimSpace.
var thinkBlockRE = regexp.MustCompile(`(?is)<(?:think|thinking|reasoning)[^>]*>.*?</(?:think|thinking|reasoning)[^>]*>`)

// thinkTagPrefix is the shortest opening-tag prefix openThinkIndex looks for.
const thinkTagPrefix = "<think"

// thinkOpenRE / thinkCloseRE match one complete opening/closing reasoning
// tag, used by the streaming stripper on its pending buffer.
var (
	thinkOpenRE  = regexp.MustCompile(`(?i)<(?:think|thinking|reasoning)[^>]*>`)
	thinkCloseRE = regexp.MustCompile(`(?i)</(?:think|thinking|reasoning)[^>]*>`)
)

// thinkClosePrefixes are the lowercased prefixes a partial closing tag can
// start with; thinkOpenPrefixes the opening equivalents. They double as the
// candidate sets for split-tag holdback in partialTagLen: "<think" /
// "<reasoning" cover the <thinking> variant too, since every split of
// "<thinking" either is a prefix of "<think" or extends past it, and the
// full "<thinking" tail is covered once its trailing bytes arrive.
var (
	thinkOpenPrefixes  = []string{"<think", "<reasoning"}
	thinkClosePrefixes = []string{"</think", "</reasoning"}
)

// stripThinkingBlock removes provider-inlined chain-of-thought from a
// complete answer string. It is the backstop for every path that assembles
// text whole; the fast path returns untouched strings unchanged.
func stripThinkingBlock(s string) string {
	low := strings.ToLower(s)
	if !strings.Contains(low, thinkTagPrefix) && !strings.Contains(low, "<reasoning") {
		return s
	}
	s = thinkBlockRE.ReplaceAllString(s, "")
	// An unterminated opening tag (e.g. a stream cut at max_tokens mid-think)
	// leaves the remainder as reasoning: drop it rather than leak a partial
	// chain of thought.
	if i := openThinkIndex(s); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// StripThinking is the exported form of stripThinkingBlock for callers
// outside the entry package: the last-chance backstop that runs where model
// text is about to be persisted (the ask engine's Result assembly), so a
// provider path added without the per-parse strip still cannot leak
// chain-of-thought into session history or a stored task result.
func StripThinking(s string) string {
	return stripThinkingBlock(s)
}

// openThinkIndex reports the offset of the first opening think/reasoning tag
// in s (case-insensitive), or -1.
func openThinkIndex(s string) int {
	low := strings.ToLower(s)
	i := strings.Index(low, thinkTagPrefix)
	j := strings.Index(low, "<reasoning")
	switch {
	case i < 0:
		return j
	case j < 0:
		return i
	case i < j:
		return i
	default:
		return j
	}
}

// thinkStripper is the streaming counterpart of stripThinkingBlock: a
// stateful filter that removes reasoning blocks from a delta sequence,
// recognizing the opening/closing tags even when a tag is split across
// deltas. Bytes that could still grow into a tag are held back until the
// next delta; a stream that ends mid-think drops the partial reasoning
// entirely rather than leak it.
type thinkStripper struct {
	in   bool            // inside a reasoning block
	held strings.Builder // undelivered bytes: partial tag, or reasoning tail
}

// feed consumes one delta and returns the prose it makes visible.
func (t *thinkStripper) feed(text string) string {
	t.held.WriteString(text)
	s := t.held.String()
	var out strings.Builder
	for {
		if t.in {
			loc := thinkCloseRE.FindStringIndex(s)
			if loc == nil {
				// No closing tag yet: everything up to a trailing partial
				// close-tag tail is droppable reasoning.
				keep := partialTagLen(s, thinkClosePrefixes)
				t.held.Reset()
				if keep > 0 {
					t.held.WriteString(s[len(s)-keep:])
				}
				return out.String()
			}
			s = s[loc[1]:]
			t.in = false
			continue
		}
		loc := thinkOpenRE.FindStringIndex(s)
		if loc == nil {
			// No opening tag yet: release everything except a tail that
			// might still grow into one with the next delta.
			keep := partialTagLen(s, thinkOpenPrefixes)
			out.WriteString(s[:len(s)-keep])
			t.held.Reset()
			if keep > 0 {
				t.held.WriteString(s[len(s)-keep:])
			}
			return out.String()
		}
		out.WriteString(s[:loc[0]])
		s = s[loc[1]:]
		t.in = true
	}
}

// flush releases the bytes held back as a possible partial tag once the
// stream has ended: outside a reasoning block they are literal prose;
// inside one they are a truncated chain-of-thought and are dropped.
func (t *thinkStripper) flush() string {
	s := t.held.String()
	t.held.Reset()
	if t.in {
		return ""
	}
	return s
}

// partialTagLen reports the length of the longest suffix of s that is a
// prefix of one of the candidate tag prefixes — bytes that may still grow
// into a complete tag when the next delta arrives. The full candidate length
// counts too: "<think" is still missing its '>', so it stays held back.
func partialTagLen(s string, candidates []string) int {
	keep := 0
	for _, c := range candidates {
		for k := min(len(c), len(s)); k > keep; k-- {
			if strings.EqualFold(s[len(s)-k:], c[:k]) {
				keep = k
				break
			}
		}
	}
	return keep
}
