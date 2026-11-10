// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

// CC Switch is its own entry point, not a source inside the discover sheet:
// the store holds four independent app rosters (Claude Code, Claude Desktop,
// Codex, Gemini), and mixing twenty rows from all of them into one list buries
// the two the person actually wants. The console asks for the groups and shows
// one at a time.
//
// The store is somebody else's database, so the reader is deliberately narrow:
// read-only, one candidate per endpoint rather than one per CC Switch entry
// (the same relay is often saved several times under different names), and a
// credential stored as a reference ("ccswitch:<app>:<id>") so CC Switch keeps
// owning the secret.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/ccswitch"
)

// ccSwitchAppLabels name the groups the console shows as tabs. They match the
// client each roster belongs to, not the store's raw app_type values.
var ccSwitchAppLabels = map[string]string{
	ccswitch.AppClaude:        "Claude Code",
	ccswitch.AppClaudeDesktop: "Claude Desktop",
	ccswitch.AppCodex:         "Codex",
	ccswitch.AppGemini:        "Gemini",
}

// ccSwitchAppOrder fixes the tab order: the two Claude rosters first (they are
// what the store is usually for), then Codex, then Gemini.
var ccSwitchAppOrder = []string{
	ccswitch.AppClaude,
	ccswitch.AppClaudeDesktop,
	ccswitch.AppCodex,
	ccswitch.AppGemini,
}

// CCSwitchGroups reads the store and returns one Source per app type. A group
// with nothing importable comes back as an empty source rather than being
// dropped: the console shows the tab greyed out, which answers "why is Codex
// not here" without a second request.
func CCSwitchGroups(ctx context.Context, opt Options) []Source {
	opt = opt.withDefaults()
	path := ccswitch.Path(opt.Home)
	if v := opt.env("CC_SWITCH_DB"); v != "" {
		path = v
	}
	if path == "" {
		return []Source{{ID: SourceCCSwitch, Status: StatusMissing, Detail: "no home directory to look for cc-switch.db in"}}
	}
	providers, err := ccswitch.Read(path)
	if err != nil {
		return []Source{{ID: SourceCCSwitch, Status: StatusError, Detail: "cc-switch.db: " + firstLine(err.Error())}}
	}
	if providers == nil {
		return []Source{{ID: SourceCCSwitch, Status: StatusMissing, Detail: path + " not found"}}
	}
	byApp := make(map[string][]ccswitch.Provider, len(ccSwitchAppOrder))
	for _, p := range providers {
		byApp[p.AppType] = append(byApp[p.AppType], p)
	}
	out := make([]Source, 0, len(ccSwitchAppOrder))
	for _, app := range ccSwitchAppOrder {
		out = append(out, ccSwitchGroup(app, byApp[app]))
	}
	return out
}

// ccSwitchGroup turns one app roster into a Source.
func ccSwitchGroup(app string, providers []ccswitch.Provider) Source {
	label := ccSwitchAppLabels[app]
	if label == "" {
		label = app
	}
	src := Source{ID: SourceCCSwitch + ":" + app, Label: label, Status: StatusEmpty}
	if len(providers) == 0 {
		src.Detail = "not configured in CC Switch"
		return src
	}
	candidates, skipped := ccSwitchCandidates(providers)
	src.Candidates = candidates
	src.Detail = ccswitch.Describe(providers)
	if len(candidates) > 0 {
		src.Status = StatusOK
	}
	if skipped > 0 {
		src.Detail += fmt.Sprintf(" · %d skipped (no model or token)", skipped)
	}
	return src
}

// ccSwitchCandidates merges the rows of one app roster into candidates: one
// group per endpoint, the union of its models, and the live entry as the
// group's representative (its token is the one that works right now).
func ccSwitchCandidates(providers []ccswitch.Provider) ([]Candidate, int) {
	type group struct {
		rep     ccswitch.Provider
		models  []string
		seen    map[string]bool
		count   int
		current bool
	}
	order := make([]string, 0, len(providers))
	groups := make(map[string]*group, len(providers))
	skipped := 0
	for _, p := range providers {
		if p.BaseURL == "" || len(p.Models) == 0 {
			// A Claude Desktop row usually pins no model, and an official row
			// leaves the endpoint to the client: neither can become a registry
			// row on its own.
			skipped++
			continue
		}
		if !p.HasToken {
			// No stored credential means the entry relies on a client login
			// (the Codex-on-ChatGPT rows). OpenPanda cannot reuse that login,
			// so a row would import fine and fail on the first turn.
			skipped++
			continue
		}
		key := strings.ToLower(p.BaseURL)
		g, ok := groups[key]
		if !ok {
			g = &group{rep: p, seen: make(map[string]bool, len(p.Models))}
			groups[key] = g
			order = append(order, key)
		}
		g.count++
		if p.Current && !g.rep.Current {
			g.rep = p
		}
		if g.rep.Current {
			g.current = true
		}
		for _, m := range p.Models {
			if g.seen[m] {
				continue
			}
			g.seen[m] = true
			g.models = append(g.models, m)
		}
	}

	sort.Strings(order)
	out := make([]Candidate, 0, len(order)*2)
	for _, key := range order {
		g := groups[key]
		providerID := "custom"
		if id, ok := providerForBase(g.rep.BaseURL); ok {
			providerID = id
		}
		label := g.rep.Name
		if label == "" {
			// CC Switch lets a provider go unnamed; the host is the next most
			// recognisable thing, and "claude" alone reads like a placeholder.
			if u, err := url.Parse(g.rep.BaseURL); err == nil && u.Host != "" {
				label = u.Host
			} else {
				label = g.rep.AppType
			}
		}
		if g.count > 1 {
			label = fmt.Sprintf("%s +%d", label, g.count-1)
		}
		keyRef := ccswitch.MakeRef(g.rep.AppType, g.rep.ID)
		apiType := g.rep.APIType
		if apiType == "" {
			apiType = "anthropic"
		}
		for _, model := range g.models {
			out = append(out, Candidate{
				Source:     SourceCCSwitch,
				Provider:   providerID,
				Model:      model,
				Label:      model,
				BaseURL:    g.rep.BaseURL,
				APIType:    apiType,
				KeyRef:     keyRef,
				Bearer:     g.rep.Bearer,
				Suggested:  g.current,
				Importable: true,
				Note:       "cc-switch: " + label,
			})
		}
	}
	return out, skipped
}
