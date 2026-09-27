package panel

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// ---- Memory graph ---------------------------------------------------------
//
// GET /api/memory/graph derives the console's knowledge-graph view: every
// memory surface (USER.md / MEMORY.md / DREAMS.md / topics / project memory /
// the warm-layer diary) plus every skill becomes a node, and two cheap,
// honest edge kinds connect them:
//
//   - "promotion": an entry carrying the Dreamer's [from:日志] tag — the
//     warm-layer diary feeding long-term memory.
//   - "reference": one surface naming another by name inside its text —
//     MEMORY.md mentioning a topic, a project memory mentioning a skill.
//
// No inference, no embeddings: every edge a reader sees is either a provenance
// marker the Dreamer wrote or a literal name mention, so the graph can never
// claim a relationship that isn't in the files.

// graphNodeJSON / graphEdgeJSON are the wire shapes of the graph response.
type graphNodeJSON struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // core | topic | project | daily | skill
	Label string `json:"label"`
	// Chars is the surface size — the view scales the node with it.
	Chars int `json:"chars,omitempty"`
	// Status carries skill approval state (pending skills render dimmed).
	Status string `json:"status,omitempty"`
}

type graphEdgeJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // promotion | reference
}

// graphText is one readable surface's (id, name-in-text, content) triple the
// mention scan runs over.
type graphText struct {
	id      string
	content string
}

// getMemoryGraph serves GET /api/memory/graph. Every source is optional — a
// fresh node returns a sparse graph, never an error.
func (h *handler) getMemoryGraph(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	var root string
	h.readCfg(func(c *config.Config) { root = c.Storage.MemoryPath })

	hermes := h.memoryHermes()
	nodes := []graphNodeJSON{}
	var texts []graphText
	// mentionCandidates are the (node id → display name) pairs the reference
	// scan looks for inside other surfaces. Kept apart from `nodes` so a
	// file's own text cannot self-link.
	type candidate struct{ id, name string }
	var candidates []candidate

	addCore := func(id, label, path string) {
		content := readFileCapped(filepath.Join(root, path))
		nodes = append(nodes, graphNodeJSON{ID: id, Kind: "core", Label: label, Chars: len([]rune(content))})
		texts = append(texts, graphText{id: id, content: content})
	}
	addCore("core:user", "USER.md", "USER.md")
	addCore("core:memory", "MEMORY.md", "MEMORY.md")
	addCore("core:dreams", "DREAMS.md", "DREAMS.md")

	if names, err := hermes.ListTopics(); err == nil {
		for _, name := range names {
			content := readFileCapped(filepath.Join(hermes.TopicsDir(), name+".md"))
			id := "topic:" + name
			nodes = append(nodes, graphNodeJSON{ID: id, Kind: "topic", Label: name, Chars: len([]rune(content))})
			texts = append(texts, graphText{id: id, content: content})
			candidates = append(candidates, candidate{id: id, name: name})
		}
	}

	// The warm layer aggregates to one diary node: promotion tags do not name
	// a specific daily file, so per-day nodes would invent edges the data
	// does not carry.
	daily := listDaily(hermes.WarmDir())
	{
		var diary strings.Builder
		chars := 0
		for _, d := range daily {
			diary.WriteString(d.Content)
			diary.WriteString("\n")
			chars += len([]rune(d.Content))
		}
		nodes = append(nodes, graphNodeJSON{ID: "daily", Kind: "daily", Label: "daily", Chars: chars})
		texts = append(texts, graphText{id: "daily", content: diary.String()})
	}

	if h.projects != nil {
		if names, err := h.projects.List(); err == nil {
			for _, name := range names {
				mf, err := h.projects.Load(name)
				if err != nil {
					continue
				}
				id := "project:" + name
				nodes = append(nodes, graphNodeJSON{ID: id, Kind: "project", Label: name, Chars: mf.Chars()})
				texts = append(texts, graphText{id: id, content: string(mf.Bytes())})
				candidates = append(candidates, candidate{id: id, name: name})
			}
		}
	}

	if h.skillStore != nil {
		if index, err := h.skillStore.Index(); err == nil {
			for _, e := range index {
				id := "skill:" + e.Name
				nodes = append(nodes, graphNodeJSON{
					ID: id, Kind: "skill", Label: e.Name, Status: string(e.Status),
				})
				candidates = append(candidates, candidate{id: id, name: e.Name})
			}
		}
	}

	// Edges. Promotion markers and name mentions; both dedup'd by (from,to,kind).
	edgeSet := map[string]graphEdgeJSON{}
	addEdge := func(from, to, kind string) {
		if from == to {
			return
		}
		edgeSet[from+"\x00"+to+"\x00"+kind] = graphEdgeJSON{From: from, To: to, Kind: kind}
	}
	for _, g := range texts {
		if strings.Contains(g.content, memory.PromotionSourceTag) {
			addEdge("daily", g.id, "promotion")
		}
		lower := strings.ToLower(g.content)
		for _, cand := range candidates {
			if cand.id == g.id || !mentionable(cand.name) {
				continue
			}
			if mentionsName(lower, strings.ToLower(cand.name)) {
				addEdge(g.id, cand.id, "reference")
			}
		}
	}

	edges := make([]graphEdgeJSON, 0, len(edgeSet))
	for _, e := range edgeSet {
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	writeJSON(w, map[string]any{"nodes": nodes, "edges": edges})
}

// mentionable reports whether a name is worth scanning for — single/double
// character names mention-match too aggressively to be honest edges.
func mentionable(name string) bool {
	return len([]rune(name)) >= 3
}

// mentionsName reports whether content names candidate: a case-insensitive
// substring match that for ASCII names also requires non-alphanumeric
// boundaries, so "go" inside "golang" never links. CJK names match plainly
// (Han script has no word-boundary convention).
func mentionsName(content, name string) bool {
	idx := 0
	for {
		i := strings.Index(content[idx:], name)
		if i < 0 {
			return false
		}
		i += idx
		beforeOK := i == 0 || !isASCIILetter(rune(content[i-1]))
		end := i + len(name)
		afterOK := end >= len(content) || !isASCIILetter(rune(content[end]))
		if beforeOK && afterOK {
			return true
		}
		idx = i + 1
	}
}

func isASCIILetter(r rune) bool {
	return unicode.IsLetter(r) && r < utf8RuneSelf || unicode.IsDigit(r)
}

// utf8RuneSelf is the ASCII cutoff — bytes below it are single-byte runes.
const utf8RuneSelf = 0x80
