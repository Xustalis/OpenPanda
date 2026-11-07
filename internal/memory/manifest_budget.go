// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RenderManifestFor keeps the profile/general index and the six most relevant
// topic files. The data fence and discovery path count toward the byte budget;
// omitted files remain available on disk, never silently deleted from memory.
func RenderManifestFor(files []FileSummary, query string, budget int) string {
	if len(files) == 0 || budget <= 0 {
		return ""
	}
	var candidates []FileSummary
	var topics []string
	byText := make(map[string]FileSummary)
	for _, f := range files {
		if f.Name == "USER.md" || f.Name == "MEMORY.md" {
			candidates = append(candidates, f)
			continue
		}
		text := f.Name + " | " + f.Summary
		topics = append(topics, text)
		byText[text] = f
	}
	for _, text := range (Retriever{}).Rank(query, topics, 6) {
		candidates = append(candidates, byText[text])
	}
	root := filepath.Dir(files[0].Path)
	render := func(selected []FileSummary) string {
		var b strings.Builder
		b.WriteString("Memory file manifest / 记忆文件清单 — read matching files on demand.\n")
		for _, f := range selected {
			fmt.Fprintf(&b, "- %s (%d entries) %s | %s\n", f.Name, f.Entries, f.Path, f.Summary)
		}
		if n := len(files) - len(selected); n > 0 {
			fmt.Fprintf(&b, "%d files omitted from this index; discover more under %s.\n", n, root)
		}
		return fenceMemoryData(b.String())
	}
	var selected []FileSummary
	if len(render(nil)) > budget {
		return ""
	}
	for _, f := range candidates {
		trial := append(append([]FileSummary(nil), selected...), f)
		if len(render(trial)) <= budget {
			selected = trial
		}
	}
	return render(selected)
}
