// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"bufio"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// ---- Memory graph ---------------------------------------------------------
//
// GET /api/memory/graph derives the console's knowledge-graph view in two
// layers:
//
//   - Provenance ("file"): every populated memory surface (USER.md / MEMORY.md /
//     DREAMS.md / topics / project memory / the warm-layer diary) plus every
//     skill becomes a node, and two cheap, honest edge kinds connect them:
//
//     · "promotion": a Dreamer receipt in DREAMS.md whose promoted entry
//       still exists in MEMORY.md, even after the daily logs are pruned.
//     · "reference": one surface naming another by name inside its text —
//       MEMORY.md mentioning a topic, a project memory mentioning a skill.
//
//   - Knowledge ("entity"): terms that literally recur inside memory content
//     (see graph_terms.go) — repeated Chinese phrases, Latin words and quoted
//     names — tied back to the surfaces by "mention" edges and to each other
//     by "co" edges (co-occurrence in the same entry/line).
//
// No inference, no embeddings: every edge a reader sees is either a provenance
// marker the Dreamer wrote or a literal string the files contain, so the graph
// can never claim a relationship that isn't in the files.

// graphNodeJSON / graphEdgeJSON are the wire shapes of the graph response.
type graphNodeJSON struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // core | topic | project | daily | skill | entity
	// Layer separates provenance from knowledge: "file" nodes are the memory
	// surfaces themselves, "entity" nodes are terms recurring inside them.
	Layer string `json:"layer"`
	Label string `json:"label"`
	// Chars is the surface size — the view scales the node with it.
	Chars int `json:"chars,omitempty"`
	// Entries is the §-entry count on entry-shaped surfaces (user/memory,
	// topics, projects). DREAMS.md is prose and leaves it unset.
	Entries int `json:"entries,omitempty"`
	// Files is the daily-log count on the aggregated diary node.
	Files int `json:"files,omitempty"`
	// Status carries skill approval state (pending skills render dimmed).
	Status string `json:"status,omitempty"`
	// Weight is the node's evidence mass — entries/files/characters for
	// surfaces, occurrence count for entities — and scales the rendered node.
	Weight int `json:"weight,omitempty"`
	// FirstSeen/LastSeen are YYYY-MM-DD dates the data honestly carries
	// (diary filenames, DREAMS.md sections, file mtimes, per-day entity
	// sightings). Empty when the surface has no dated evidence; the timeline
	// then treats the node as always visible.
	FirstSeen string `json:"first_seen,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
	// Evidence carries a few literal source lines an entity term was seen in
	// — the detail card shows them so the node explains itself.
	Evidence []string `json:"evidence,omitempty"`
}

type graphEdgeJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // promotion | reference | mention | co
	// Weight is the evidence count behind the edge — retained diary promotion
	// receipts, name mentions in the source, occurrences of an
	// entity in a surface, or lines two entities share — so the view can
	// render stronger links heavier. Capped, never zero.
	Weight int `json:"weight,omitempty"`
}

// graphText is a readable surface's ID and content for the mention scan.
type graphText struct {
	id      string
	content string
}

// Knowledge-layer caps: enough terms and links to fill the view without
// turning the payload (or the picture) into a hairball.
const (
	entityCapDefault = 20        // default entity nodes — a readable first view
	entityCap        = 60        // hard ceiling, reachable with ?entities=60
	mentionEdgeCap   = 120       // entity→surface links kept, highest evidence first
	coMinCount       = 2         // lines two entities must share to earn a co edge
	coEdgeCap        = 200       // entity↔entity links kept, highest evidence first
	extractBudget    = 256 << 10 // daily-log bytes the term scan will read
)

// getMemoryGraph serves GET /api/memory/graph. Every source is optional — a
// fresh node returns a sparse graph, never an error.
func (h *handler) getMemoryGraph(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	var root string
	h.readCfg(func(c *config.Config) { root = c.Storage.MemoryPath })
	var projectsRoot, skillsRoot string
	if h.projects != nil {
		projectsRoot = h.projects.Root()
	}
	if h.skillStore != nil {
		skillsRoot = h.skillStore.Root()
	}

	// The default view is the readable one: few entities, no statistical
	// co-occurrence edges. Clients widen it explicitly with ?entities=N and
	// ?co=1 when the reader asks for the full term cloud.
	q := r.URL.Query()
	entityLimit := entityCapDefault
	if v := q.Get("entities"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > entityCap {
				n = entityCap
			}
			entityLimit = n
		}
	}
	includeCo := q.Get("co") == "1"

	// The scan reads every surface and enumerates CJK n-grams. A conditional
	// request whose sources did not change skips all of that work.
	etag := memoryGraphETag(entityLimit, includeCo, root, projectsRoot, skillsRoot)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	hermes := h.memoryHermes()
	nodes := []graphNodeJSON{}
	var texts []graphText
	// extractDocs feed the knowledge layer: one doc per dated daily file (so
	// entity dates land on the day they were actually seen) plus real memory
	// surfaces. The Dreamer report is excluded: its repeated section labels
	// and copied promotions are not independent facts or entity evidence.
	var extractDocs []graphText
	// mentionCandidates are the (node id → display name) pairs the reference
	// scan looks for inside other surfaces. Kept apart from `nodes` so a
	// file's own text cannot self-link. `lower` is the precomputed lowercase
	// name — one lowering per candidate instead of one per surface scan.
	type candidate struct{ id, name, lower string }
	var candidates []candidate

	addCore := func(id, label, path string, countEntries bool) {
		content := readFileCapped(filepath.Join(root, path))
		if strings.TrimSpace(content) == "" {
			return
		}
		n := graphNodeJSON{ID: id, Kind: "core", Layer: "file", Label: label, Chars: len([]rune(content))}
		if countEntries {
			n.Entries = len(memory.ParseMem([]byte(content)).Entries)
		}
		n.Weight = surfaceWeight(n)
		n.LastSeen = fileDate(filepath.Join(root, path))
		nodes = append(nodes, n)
		if id != "core:dreams" {
			texts = append(texts, graphText{id: id, content: content})
			extractDocs = append(extractDocs, graphText{id: id, content: stripTags(content)})
		}
		// Core filenames ("USER.md") are distinctive mentions — a surface
		// naming one earns an honest reference edge like any other.
		candidates = append(candidates, candidate{id: id, name: label, lower: strings.ToLower(label)})
	}
	addCore("core:user", "USER.md", "USER.md", true)
	addCore("core:memory", "MEMORY.md", "MEMORY.md", true)
	addCore("core:dreams", "DREAMS.md", "DREAMS.md", false)

	if names, err := hermes.ListTopics(); err == nil {
		for _, name := range names {
			path := filepath.Join(hermes.TopicsDir(), name+".md")
			content := readFileCapped(path)
			if strings.TrimSpace(content) == "" {
				continue
			}
			id := "topic:" + name
			n := graphNodeJSON{
				ID: id, Kind: "topic", Layer: "file", Label: name,
				Chars:   len([]rune(content)),
				Entries: len(memory.ParseMem([]byte(content)).Entries),
			}
			n.Weight = surfaceWeight(n)
			n.LastSeen = fileDate(path)
			nodes = append(nodes, n)
			texts = append(texts, graphText{id: id, content: content})
			extractDocs = append(extractDocs, graphText{id: id, content: stripTags(content)})
			candidates = append(candidates, candidate{id: id, name: name, lower: strings.ToLower(name)})
		}
	}

	// The warm layer aggregates to one diary node: promotion tags do not name
	// a specific daily file, so per-day nodes would invent edges the data
	// does not carry. The knowledge layer still sees the days themselves (as
	// "daily:<date>" docs) so entity dates stay honest. The extraction budget
	// bounds the scan on a long history — newest days first, oldest dropped.
	daily, err := listDaily(hermes.WarmDir())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	{
		var diary strings.Builder
		chars := 0
		budget := extractBudget
		for _, d := range daily {
			diary.WriteString(d.Content)
			diary.WriteString("\n")
			chars += len([]rune(d.Content))
			if isDayName(d.Name) && budget > 0 {
				extractDocs = append(extractDocs, graphText{id: "daily:" + d.Name, content: stripTags(d.Content)})
				budget -= len(d.Content)
			}
		}
		if len(daily) > 0 {
			n := graphNodeJSON{ID: "daily", Kind: "daily", Layer: "file", Label: "daily", Chars: chars, Files: len(daily)}
			n.Weight = surfaceWeight(n)
			n.FirstSeen, n.LastSeen = dailyDates(daily)
			nodes = append(nodes, n)
			texts = append(texts, graphText{id: "daily", content: diary.String()})
		}
	}

	if h.projects != nil {
		if names, err := h.projects.List(); err == nil {
			for _, name := range names {
				mf, err := h.projects.Load(name)
				if err != nil {
					continue
				}
				if len(mf.Entries) == 0 {
					continue
				}
				id := "project:" + name
				n := graphNodeJSON{ID: id, Kind: "project", Layer: "file", Label: name, Chars: mf.Chars(), Entries: len(mf.Entries)}
				n.Weight = surfaceWeight(n)
				if path, err := h.projects.Path(name); err == nil {
					n.LastSeen = fileDate(path)
				}
				nodes = append(nodes, n)
				texts = append(texts, graphText{id: id, content: string(mf.Bytes())})
				extractDocs = append(extractDocs, graphText{id: id, content: stripTags(string(mf.Bytes()))})
				candidates = append(candidates, candidate{id: id, name: name, lower: strings.ToLower(name)})
			}
		}
	}

	if h.skillStore != nil {
		if index, err := h.skillStore.Index(); err == nil {
			for _, e := range index {
				id := "skill:" + e.Name
				nodes = append(nodes, graphNodeJSON{
					ID: id, Kind: "skill", Layer: "file", Label: e.Name, Status: string(e.Status), Weight: 1,
				})
				candidates = append(candidates, candidate{id: id, name: e.Name, lower: strings.ToLower(e.Name)})
				// A skill's own name+description is a text surface too: a skill
				// describing work on a topic or project earns the same honest
				// reference edge a memory file would.
				content := e.Name + " " + e.Description
				texts = append(texts, graphText{id: id, content: content})
				extractDocs = append(extractDocs, graphText{id: id, content: content})
			}
		}
	}

	// Edges. Dream-diary promotion receipts and name mentions; both dedup'd by (from,to,kind)
	// and carrying their evidence count as weight.
	edgeSet := map[string]graphEdgeJSON{}
	addEdge := func(from, to, kind string, weight int) {
		if from == to {
			return
		}
		if weight > edgeWeightCap {
			weight = edgeWeightCap
		}
		edgeSet[from+"\x00"+to+"\x00"+kind] = graphEdgeJSON{From: from, To: to, Kind: kind, Weight: weight}
	}
	// The Dreamer's receipts survive warm-log retention. Stream the whole
	// diary instead of relying on its 64 KiB graph preview; only entries
	// still present in long-term memory earn a promotion edge.
	if diary, err := os.Open(filepath.Join(root, "DREAMS.md")); err == nil {
		mem, readErr := os.ReadFile(filepath.Join(root, "MEMORY.md"))
		if os.IsNotExist(readErr) {
			readErr = nil
		}
		if readErr == nil {
			n, first, last, scanErr := dreamPromotionEvidence(diary, string(mem))
			if scanErr == nil {
				for i := range nodes {
					if nodes[i].ID == "core:dreams" && first != "" {
						nodes[i].FirstSeen, nodes[i].LastSeen = first, last
					}
				}
				if n > 0 {
					addEdge("core:dreams", "core:memory", "promotion", n)
				}
			}
		}
		_ = diary.Close()
	}
	for _, g := range texts {
		lower := strings.ToLower(g.content)
		for _, cand := range candidates {
			if cand.id == g.id || !mentionable(cand.name) {
				continue
			}
			if n := countMentions(lower, cand.lower, edgeWeightCap); n > 0 {
				addEdge(g.id, cand.id, "reference", n)
			}
		}
	}

	// Knowledge layer: the terms recurring in the surfaces become nodes; the
	// names already carrying a node (surface labels, topic/project/skill
	// names) are excluded so the two layers never duplicate each other.
	exclude := map[string]bool{}
	for _, n := range nodes {
		exclude[strings.ToLower(n.Label)] = true
		exclude[strings.ToLower(strings.TrimSuffix(n.Label, ".md"))] = true
	}
	terms := extractTermsWithCancel(extractDocs, exclude, entityLimit, func() bool {
		return r.Context().Err() != nil
	})
	if terms == nil {
		return // client disconnected during the n-gram scan
	}
	collectEvidence(terms, extractDocs)

	// seenBy is the dated-evidence roll-up per surface: entities inherit the
	// first/last sighting of the surfaces they appear in ("daily:<date>" docs
	// carry the day itself).
	seenBy := map[string][2]string{}
	for _, n := range nodes {
		seenBy[n.ID] = [2]string{n.FirstSeen, n.LastSeen}
	}
	for _, t := range terms {
		first, last := "", ""
		for id := range t.Docs {
			f, l := seenBy[id][0], seenBy[id][1]
			if day, ok := strings.CutPrefix(id, "daily:"); ok {
				f, l = day, day
			}
			first = minDay(first, f)
			last = maxDay(last, l)
		}
		nodes = append(nodes, graphNodeJSON{
			ID: "entity:" + t.Term, Kind: "entity", Layer: "entity",
			Label: t.Term, Weight: t.Count, FirstSeen: first, LastSeen: last,
			Evidence: t.Samples,
		})
	}

	// Mention edges: surface → entity, weighted by occurrences in that
	// surface. Daily docs collapse back onto the aggregated diary node.
	var mentions []edgeHit
	for _, t := range terms {
		per := map[string]int{}
		for docID, c := range t.Docs {
			sid := docID
			if strings.HasPrefix(sid, "daily:") {
				sid = "daily"
			}
			per[sid] += c
		}
		for _, sid := range sortedIDs(per) {
			mentions = append(mentions, edgeHit{from: sid, to: "entity:" + t.Term, weight: per[sid]})
		}
	}
	sortEdgeHits(mentions)
	if len(mentions) > mentionEdgeCap {
		mentions = mentions[:mentionEdgeCap]
	}
	for _, m := range mentions {
		addEdge(m.from, m.to, "mention", m.weight)
	}

	// Co-occurrence edges are statistical, not relational: two terms sharing
	// a line says nothing about how they relate, and at the old 200-edge cap
	// these chords crossed the whole canvas. They are opt-in (?co=1); the
	// default graph omits them entirely, including their scan cost.
	if includeCo {
		termLower := make([]string, len(terms))
		for i, t := range terms {
			termLower[i] = strings.ToLower(t.Term)
		}
		coCount := map[string]int{}
		for _, d := range extractDocs {
			if r.Context().Err() != nil {
				return
			}
			lower := strings.ToLower(d.content)
			for line := range strings.Lines(lower) {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var present []string
				for i, tl := range termLower {
					if strings.Contains(line, tl) {
						present = append(present, terms[i].Term)
					}
				}
				for i := 0; i < len(present); i++ {
					for j := i + 1; j < len(present); j++ {
						a, b := present[i], present[j]
						if strings.Contains(a, b) || strings.Contains(b, a) {
							continue
						}
						coCount[a+"\x00"+b]++
					}
				}
			}
		}
		var co []edgeHit
		for key, n := range coCount {
			if n < coMinCount {
				continue
			}
			from, to, _ := strings.Cut(key, "\x00")
			co = append(co, edgeHit{from: "entity:" + from, to: "entity:" + to, weight: n})
		}
		sortEdgeHits(co)
		if len(co) > coEdgeCap {
			co = co[:coEdgeCap]
		}
		for _, e := range co {
			addEdge(e.from, e.to, "co", e.weight)
		}
	}

	edges := make([]graphEdgeJSON, 0, len(edgeSet))
	for _, e := range edgeSet {
		edges = append(edges, e)
	}
	// Full (from,to,kind) sort: map iteration order is random, so without the
	// kind tiebreak two edges between the same pair could swap per request.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Kind < edges[j].Kind
	})
	writeJSON(w, map[string]any{"nodes": nodes, "edges": edges})
}

// dreamPromotionCount is a convenience wrapper for small, in-memory receipts.
func dreamPromotionCount(diary, memoryContent string) int {
	n, _, _, _ := dreamPromotionEvidence(strings.NewReader(diary), memoryContent)
	return n
}

// dreamPromotionEvidence scans every dated receipt without loading the entire
// DREAMS.md into memory. Only unique, currently retained long-term entries
// count; neither a free-form marker nor an old promotion later forgotten does.
func dreamPromotionEvidence(diary io.Reader, memoryContent string) (count int, first, last string, err error) {
	entries := map[string]bool{}
	for _, entry := range memory.ParseMem([]byte(memoryContent)).Entries {
		entries[entry] = true
	}
	seen := map[string]bool{}
	promotedSection := false
	scan := bufio.NewScanner(diary)
	scan.Buffer(make([]byte, 64<<10), maxDreamsEdit+1)
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "## ") {
			promotedSection = false
			if day := strings.TrimSpace(strings.TrimPrefix(line, "## ")); isDayName(day) {
				first, last = minDay(first, day), maxDay(last, day)
			}
		}
		if strings.HasPrefix(line, "- Promoted to long-term memory / ") {
			promotedSection = true
			continue
		}
		if strings.HasPrefix(line, "- ") {
			promotedSection = false
		}
		if promotedSection && strings.HasPrefix(line, "  - ") {
			entry := strings.TrimSpace(strings.TrimPrefix(line, "  - "))
			if entries[entry] && !seen[entry] {
				count++
				seen[entry] = true
			}
		}
	}
	return count, first, last, scan.Err()
}

// edgeWeightCap bounds the evidence count riding on one edge — plenty to tell
// a stray mention from a recurring one without long scans.
const edgeWeightCap = 50

// memoryGraphETag fingerprints the graph's source files before any parsing
// happens. Every writer in this package replaces files atomically, so a
// content change always moves mtime or size. The query options join the
// fingerprint because they change the response body.
func memoryGraphETag(entityLimit int, includeCo bool, roots ...string) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "entities=%d;co=%t;", entityLimit, includeCo)
	for _, root := range roots {
		if root == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				fmt.Fprintf(h, "error:%s|%v;", path, err)
				return nil
			}
			if !d.IsDir() && !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				fmt.Fprintf(h, "stat:%s|%v;", path, err)
				return nil
			}
			fmt.Fprintf(h, "%s|%s|%o|%d|%d;", path, fi.Mode().Type(), fi.Mode().Perm(), fi.ModTime().UnixNano(), fi.Size())
			return nil
		})
	}
	return `"` + strconv.FormatUint(h.Sum64(), 16) + `"`
}

// surfaceWeight turns a surface's size into the evidence mass the view scales
// nodes with: § entries where the file has them, daily files for the diary,
// and a character-derived floor for prose.
func surfaceWeight(n graphNodeJSON) int {
	w := n.Entries
	if n.Files > w {
		w = n.Files
	}
	if c := n.Chars / 300; c > w {
		w = c
	}
	if w < 1 {
		w = 1
	}
	return w
}

// dateLayout is the wire format of the graph's dated evidence.
const dateLayout = "2006-01-02"

// fileDate returns the file's mtime as a date; "" when unreadable.
func fileDate(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fi.ModTime().Format(dateLayout)
}

// isDayName reports whether a daily-file stem is a valid YYYY-MM-DD date.
func isDayName(name string) bool {
	_, err := time.Parse(dateLayout, name)
	return err == nil
}

// dailyDates returns the first/last date covered by the diary files.
func dailyDates(daily []topicJSON) (string, string) {
	first, last := "", ""
	for _, d := range daily {
		if !isDayName(d.Name) {
			continue
		}
		first = minDay(first, d.Name)
		last = maxDay(last, d.Name)
	}
	return first, last
}

// minDay/maxDay fold ISO dates; "" (no evidence) always loses.
func minDay(a, b string) string {
	if a == "" || (b != "" && b < a) {
		return b
	}
	return a
}

func maxDay(a, b string) string {
	if a == "" || b > a {
		return b
	}
	return a
}

// stripTags removes the memory system's own markers ([from:…] promotions, the
// [ext] provenance flag) before term extraction — markers are not content.
func stripTags(s string) string {
	s = strings.ReplaceAll(s, memory.PromotionSourceTag, " ")
	s = strings.ReplaceAll(s, memory.ExternalMarker, " ")
	return s
}

// sortedIDs returns the map's keys in sorted order — deterministic iteration
// for anything that feeds the response.
func sortedIDs(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// edgeHit is one weighted edge candidate before it enters the dedup map.
type edgeHit struct {
	from, to string
	weight   int
}

// sortEdgeHits orders edge candidates by evidence desc, then endpoints — the
// cap keeps the strongest links and the order never depends on map iteration.
func sortEdgeHits(hits []edgeHit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].weight != hits[j].weight {
			return hits[i].weight > hits[j].weight
		}
		if hits[i].from != hits[j].from {
			return hits[i].from < hits[j].from
		}
		return hits[i].to < hits[j].to
	})
}

// mentionable reports whether a name is worth scanning for — single/double
// character names mention-match too aggressively to be honest edges.
func mentionable(name string) bool {
	return len([]rune(name)) >= 3
}

// mentionsName reports whether content names candidate (see countMentions).
func mentionsName(content, name string) bool {
	return countMentions(content, name, 1) > 0
}

// countMentions counts boundary-respecting mentions of name in content, up to
// cap: a case-insensitive substring match that for ASCII names also requires
// non-alphanumeric boundaries, so "go" inside "golang" never links. CJK names
// match plainly (Han script has no word-boundary convention).
func countMentions(content, name string, cap int) int {
	idx, n := 0, 0
	for n < cap {
		i := strings.Index(content[idx:], name)
		if i < 0 {
			return n
		}
		i += idx
		beforeOK := i == 0 || !isASCIILetter(rune(content[i-1]))
		end := i + len(name)
		afterOK := end >= len(content) || !isASCIILetter(rune(content[end]))
		if beforeOK && afterOK {
			n++
		}
		idx = i + 1
	}
	return n
}

func isASCIILetter(r rune) bool {
	return unicode.IsLetter(r) && r < utf8RuneSelf || unicode.IsDigit(r)
}

// utf8RuneSelf is the ASCII cutoff — bytes below it are single-byte runes.
const utf8RuneSelf = 0x80
