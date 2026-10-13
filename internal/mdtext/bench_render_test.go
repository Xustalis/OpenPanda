// SPDX-License-Identifier: AGPL-3.0-or-later

package mdtext

import (
	"testing"

	"github.com/charmbracelet/glamour"
)

var benchSrc = `# Heading

Some **bold** and *italic* text with ` + "`code`" + ` inline.

- item one
- item two

` + "```go\nfunc main() {}\n```" + `

| a | b |
|---|---|
| 1 | 2 |
`

// Render is the TUI's per-frame hot path while an answer streams.
func BenchmarkRender(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := Render(benchSrc, 100); err != nil {
			b.Fatal(err)
		}
	}
}

// RendererConstruction is the cost termRenderer now pays once per
// (width, background) instead of once per Render call.
func BenchmarkRendererConstruction(b *testing.B) {
	for i := 0; i < b.N; i++ {
		r, err := glamour.NewTermRenderer(
			glamour.WithStyles(defaultStyle()),
			glamour.WithWordWrap(100),
		)
		if err != nil {
			b.Fatal(err)
		}
		_ = r
	}
}
