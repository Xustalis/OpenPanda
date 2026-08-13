package ledger

import "testing"

// BenchmarkMatches exercises the routing hot path: a capability match against
// a node with a representative card. The pre-normalized implementation does
// O(A) normalizations regardless of how many required ids are checked.
func BenchmarkMatches(b *testing.B) {
	n := Node{
		Native: []NativeAbility{{ID: "build:macos"}, {ID: "lint"}, {ID: "serve:dev"}},
		Agents: map[string]Agent{
			"claude_code": {Capabilities: []string{"code:modify", "code:review", "code:debug", "code:refactor", "file:analyze", "test:generate", "docs:generate"}},
			"opencode":    {Capabilities: []string{"code:modify", "code:review", "web:search", "web:fetch"}},
		},
		Manual: []ManualAbility{{ID: "design:figma"}},
	}
	required := []string{"sys:info", "build:macos", "code:modify"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.Matches(required)
	}
}
