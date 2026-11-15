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

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// GET /api/memory/graph projects admitted, retained long-term facts. Daily
// logs, task summaries, skill descriptions and dream boilerplate never create
// knowledge nodes. Stable attribute IDs survive value changes, so a corrected
// name/preference updates its node instead of leaving a contradictory one.

// graphNodeJSON / graphEdgeJSON are the wire shapes of the graph response.
type graphNodeJSON struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // core | topic | project | entity
	// Layer separates provenance from knowledge: "file" nodes are the memory
	// surfaces themselves, "entity" nodes are retained durable assertions.
	Layer    string `json:"layer"`
	Label    string `json:"label"`
	Category string `json:"category,omitempty"`
	Key      string `json:"key,omitempty"`
	// Chars is the surface size — the view scales the node with it.
	Chars int `json:"chars,omitempty"`
	// Entries is the §-entry count on entry-shaped surfaces (user/memory,
	// topics, projects). DREAMS.md is prose and leaves it unset.
	Entries int `json:"entries,omitempty"`
	// Files is the daily-log count on the aggregated diary node.
	Files int `json:"files,omitempty"`
	// Status carries skill approval state (pending skills render dimmed).
	Status string `json:"status,omitempty"`
	// Weight is the number of retained facts or receipts on a surface.
	Weight int `json:"weight,omitempty"`
	// FirstSeen/LastSeen are YYYY-MM-DD dates the data honestly carries
	// (diary filenames, DREAMS.md sections, file mtimes, per-day entity
	// sightings). Empty when the surface has no dated evidence; the timeline
	// then treats the node as always visible.
	FirstSeen string `json:"first_seen,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
	// Evidence carries the literal assertion shown in the detail card.
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

// Knowledge-layer caps keep the initial fact view readable and bound payloads.
const (
	entityCapDefault = 20 // default entity nodes — a readable first view
	entityCap        = 60 // hard ceiling, reachable with ?entities=60
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
	projectsRoot := ""
	if h.projects != nil {
		projectsRoot = h.projects.Root()
	}
	entityLimit := entityCapDefault
	if n, err := strconv.Atoi(r.URL.Query().Get("entities")); err == nil && n > 0 {
		entityLimit = min(n, entityCap)
	}
	// Query options remain in the fingerprint for older graph clients.
	etag := memoryGraphETag(entityLimit, r.URL.Query().Get("co") == "1", root, projectsRoot)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	nodes := []graphNodeJSON{}
	edges := []graphEdgeJSON{}
	entities := 0
	var retained []string
	var personalIDs []string
	retainedBySurface := map[string][]string{}
	contents := map[string]string{}
	var sourceErr error
	readContent := func(path string) string {
		if content, ok := contents[path]; ok {
			return content
		}
		content, _, err := readEditableMemory(path)
		if err != nil && sourceErr == nil {
			sourceErr = err
		}
		contents[path] = content
		return content
	}
	seenPersonal := map[string]bool{}
	preferred := map[string]string{}
	for _, source := range []struct{ id, path, target string }{{"core:user", memory.UserPath(root), "user"}, {"core:memory", memory.MemoryPath(root), "memory"}} {
		for _, fact := range memory.Facts(memory.ParseMem([]byte(readContent(source.path))), source.target) {
			if fact.Target() == source.target {
				preferred[fact.ID()] = source.id
			}
		}
	}
	addSurface := func(id, kind, label, path, target string) {
		if r.Context().Err() != nil {
			return
		}
		content := readContent(path)
		facts := memory.Facts(memory.ParseMem([]byte(content)), target)
		if target != "project" {
			unique := facts[:0]
			for _, fact := range facts {
				if !seenPersonal[fact.ID()] && (preferred[fact.ID()] == "" || preferred[fact.ID()] == id) {
					seenPersonal[fact.ID()] = true
					unique = append(unique, fact)
				}
			}
			facts = unique
		}
		if len(facts) == 0 {
			return
		}
		day := fileDate(path)
		chars := 0
		for _, f := range facts {
			chars += len([]rune(f.Text))
		}
		nodes = append(nodes, graphNodeJSON{ID: id, Kind: kind, Layer: "file", Label: label, Chars: chars, Entries: len(facts), Weight: len(facts), LastSeen: day})
		if kind != "project" {
			personalIDs = append(personalIDs, id)
		}
		for _, fact := range facts {
			if kind != "project" {
				variants := []string{fact.Entry(), fact.Text, fact.Entry() + " " + memory.PromotionSourceTag, fact.Text + " " + memory.PromotionSourceTag}
				retained = append(retained, variants...)
				retainedBySurface[id] = append(retainedBySurface[id], variants...)
			}
			if entities >= entityLimit {
				continue
			}
			factID := "entity:" + id + ":" + fact.ID()
			nodes = append(nodes, graphNodeJSON{ID: factID, Kind: "entity", Layer: "entity", Label: fact.Text, Category: fact.Category, Key: fact.Key, Weight: 1, LastSeen: day, Evidence: []string{fact.Text}})
			edges = append(edges, graphEdgeJSON{From: id, To: factID, Kind: "mention", Weight: 1})
			entities++
		}
	}
	addSurface("core:user", "core", "USER.md", memory.UserPath(root), "user")
	addSurface("core:memory", "core", "MEMORY.md", memory.MemoryPath(root), "memory")
	hermes := h.memoryHermes()
	names, err := hermes.ListTopics()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, name := range names {
		path, _ := hermes.TopicPath(name)
		addSurface("topic:"+name, "topic", name, path, "memory")
	}
	if h.projects != nil {
		names, err := h.projects.List()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		for _, name := range names {
			path, err := h.projects.Path(name)
			if err != nil {
				continue
			}
			addSurface("project:"+name, "project", name, path, "project")
		}
	}
	if sourceErr != nil {
		writeErr(w, http.StatusInternalServerError, sourceErr)
		return
	}
	// A diary is a provenance node only when a receipt still points to an
	// admitted fact. No daily/skill/report content participates in the graph.
	if diary, err := os.Open(filepath.Join(root, "DREAMS.md")); err == nil {
		count, first, last, scanErr := dreamPromotionEvidence(diary, string((memory.MemFile{Entries: retained}).Bytes()))
		_ = diary.Close()
		if scanErr == nil && count > 0 {
			nodes = append(nodes, graphNodeJSON{ID: "core:dreams", Kind: "core", Layer: "file", Label: "DREAMS.md", Weight: count, FirstSeen: first, LastSeen: last})
			for _, id := range personalIDs {
				entries := retainedBySurface[id]
				if receipt, err := os.Open(filepath.Join(root, "DREAMS.md")); err == nil {
					n, _, _, e := dreamPromotionEvidence(receipt, string((memory.MemFile{Entries: entries}).Bytes()))
					_ = receipt.Close()
					if e == nil && n > 0 {
						edges = append(edges, graphEdgeJSON{From: "core:dreams", To: id, Kind: "promotion", Weight: min(n, edgeWeightCap)})
					}
				}
			}
		}
	}
	if r.Context().Err() != nil {
		return
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
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
//
// Two exclusions keep it stable without letting it go stale. Directories are
// left out entirely: a directory's mtime moves when one of its children is
// added or removed, and the platforms disagree about when that lands — NTFS
// applies it synchronously, APFS may defer it — so the same tree hashed two
// ways on Windows and one way on macOS. That is how
// TestMemoryGraphDefaultViewAndETag failed on the Windows runner
// ("conditional request status = 200, want 304") while passing locally. Entries
// that are not .md keep their path and type but no timestamp: the graph never
// reads them, yet a file planted where a reader expects a directory is a read
// error, and a 304 minted from before that break would certify a body the node
// can no longer produce. An added or removed .md still moves the hash because
// its own full line appears or disappears.
func memoryGraphETag(entityLimit int, includeCo bool, roots ...string) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "facts-v1;entities=%d;co=%t;", entityLimit, includeCo)
	for rootIndex, root := range roots {
		if root == "" {
			continue
		}
		if info, err := os.Lstat(root); err != nil {
			fmt.Fprintf(h, "root:%s|%v;", root, err)
		} else {
			fmt.Fprintf(h, "root:%s|%s;", root, info.Mode().Type())
		}
		if rootIndex == 0 {
			for _, name := range []string{"USER.md", "MEMORY.md", "DREAMS.md", "topics"} {
				path := filepath.Join(root, name)
				info, err := os.Lstat(path)
				if err != nil {
					fmt.Fprintf(h, "surface:%s|%v;", path, err)
					continue
				}
				fmt.Fprintf(h, "surface:%s|%s;", path, info.Mode().Type())
			}
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				fmt.Fprintf(h, "error:%s|%v;", path, err)
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if d.IsDir() {
				if rel == "." {
					return nil
				}
				if rootIndex == 0 && rel == "topics" {
					return nil
				}
				if rootIndex > 0 && len(parts) == 1 {
					return nil
				}
				return filepath.SkipDir
			}
			relevant := (rootIndex == 0 && (rel == "USER.md" || rel == "MEMORY.md" || rel == "DREAMS.md" || (len(parts) == 2 && parts[0] == "topics" && strings.HasSuffix(parts[1], ".md")))) || (rootIndex > 0 && len(parts) == 2 && parts[1] == "MEMORY.md")
			if !relevant {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".md") {
				// The graph does not read this entry, but its presence still
				// describes the response: a plain file planted where a reader
				// expects a directory is a read error, and a 304 minted from
				// before that break would certify a body the node can no longer
				// produce. Name and type only — no timestamp, which is the part
				// that moves for reasons neither reader nor writer caused.
				fmt.Fprintf(h, "%s|%s;", path, d.Type())
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
