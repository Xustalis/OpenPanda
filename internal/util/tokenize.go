package util

import (
	"strings"
	"unicode"
)

// Tokenize splits text into lowercase word tokens for lexical matching: runs of
// Latin letters/digits become one token, and each CJK ideograph stands alone
// (Chinese has no word boundaries, so single ideographs give the overlap signal
// needed for short-entry retrieval). It is a retrieval heuristic, not a
// linguistic tokenizer.
func Tokenize(s string) map[string]struct{} {
	out := make(map[string]struct{})
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out[strings.ToLower(cur.String())] = struct{}{}
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.Is(unicode.Han, r) {
				flush()
				out[string(r)] = struct{}{}
			} else {
				cur.WriteRune(r)
			}
		default:
			flush()
		}
	}
	flush()
	return out
}

// cjkFunctionWords are high-frequency Chinese function characters that carry
// little concept or retrieval signal on their own. Shared by the Dreaming
// engine's concept signal (internal/memory) and the memory/skill retrievers.
var cjkFunctionWords = map[string]bool{
	"的": true, "了": true, "是": true, "在": true, "和": true, "与": true,
	"就": true, "都": true, "也": true, "还": true, "不": true, "没": true,
	"有": true, "我": true, "你": true, "他": true, "她": true, "它": true,
	"们": true, "这": true, "那": true, "哪": true, "很": true, "会": true,
	"要": true, "能": true, "个": true, "把": true, "被": true, "从": true,
	"到": true, "对": true, "为": true, "让": true, "给": true, "跟": true,
	"上": true, "下": true, "中": true, "内": true, "里": true, "等": true,
	"说": true, "讲": true, "看": true, "做": true, "用": true, "去": true,
	"来": true, "之": true, "其": true, "些": true, "或": true, "但": true,
	"而": true, "并": true, "及": true, "着": true, "过": true, "得": true,
	"地": true, "啊": true, "吗": true, "呢": true, "吧": true, "一": true,
	"几": true, "最": true, "更": true, "只": true, "又": true, "再": true,
	"才": true, "刚": true, "已": true, "却": true, "则": true, "因": true,
	"所": true, "以": true, "于": true,
}

// IsCJKFunctionWord reports whether w is a single-ideograph Chinese function
// word (a high-frequency word with little retrieval signal). Multi-character
// strings are never function words.
func IsCJKFunctionWord(w string) bool {
	if len([]rune(w)) != 1 {
		return false
	}
	return cjkFunctionWords[w]
}

// latinStopwords are high-frequency English function words with little retrieval
// signal. Shared by the memory and skill retrievers so a match on "the", "and",
// or "for" alone never drives ranking.
var latinStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"of": true, "to": true, "for": true, "with": true, "in": true, "on": true,
	"at": true, "by": true, "from": true, "as": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"this": true, "that": true, "these": true, "those": true, "it": true,
	"its": true, "do": true, "does": true, "did": true, "not": true, "no": true,
	"yes": true, "if": true, "then": true, "else": true, "so": true, "can": true,
	"could": true, "should": true, "would": true, "will": true, "may": true,
	"might": true, "must": true, "we": true, "you": true, "he": true, "she": true,
	"they": true, "them": true, "my": true, "your": true, "our": true, "their": true,
	"me": true, "him": true, "her": true, "us": true, "who": true, "what": true,
	"which": true, "how": true, "why": true, "when": true, "where": true,
}

// IsStopword reports whether a lowercase token is a function word (Latin or
// CJK) that carries no retrieval signal on its own. Retrievers skip these so a
// match on a common word alone does not trigger a false positive.
func IsStopword(w string) bool {
	return latinStopwords[w] || IsCJKFunctionWord(w)
}
