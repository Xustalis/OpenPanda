package main

// Argument-position Tab completion. Completing the command name is only half
// the job: the commands that actually cost typing are the ones that take an
// id (/task /logs /cancel /approve /reject /resume), and an id is a UUID
// nobody retypes out of scrollback. The editor asks the REPL for candidates
// through argCandidates, which reads live state — task ids from the store,
// session ids from the session store, project names from memory — behind a
// short cache: the completion menu is recomputed on every keystroke, and a
// SQLite round trip per keypress is not something a line editor can afford.

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

// argResolver returns the candidates for an argument position: cmd is the
// slash command without its "/", args the arguments typed so far with the last
// element the partial token under the cursor (possibly "").
type argResolver func(cmd string, args []string) []string

// argCandidatesFor splits a command line at the cursor's argument and returns
// that partial token plus the candidates matching it. The token is always a
// suffix of line, so callers can rebuild the line by prefix concatenation.
// Empty candidates means "this is not an argument position, or nothing fits".
func argCandidatesFor(line string, resolve argResolver) (string, []string) {
	if resolve == nil || strings.ContainsRune(line, '\n') || !strings.HasPrefix(line, "/") {
		return "", nil
	}
	sp := strings.IndexAny(line, " \t")
	if sp < 0 {
		return "", nil // still on the command name itself
	}
	cmd := strings.TrimPrefix(line[:sp], "/")
	args := strings.Fields(line[sp+1:])
	// A line ending in whitespace opens a fresh, empty argument; without this
	// "/config set " would complete against "set" instead of the next slot.
	if last := line[len(line)-1]; last == ' ' || last == '\t' {
		args = append(args, "")
	}
	if len(args) == 0 {
		args = []string{""}
	}
	token := args[len(args)-1]
	var matches []string
	for _, c := range resolve(cmd, args) {
		if strings.HasPrefix(strings.ToLower(c), strings.ToLower(token)) {
			matches = append(matches, c)
		}
	}
	sort.Strings(matches)
	if len(matches) == 1 && matches[0] == token {
		return token, nil // already complete: no menu, no rewrite
	}
	return token, matches
}

// taskStates are the /tasks state filters in lifecycle order — the same
// strings the store transitions between (internal/core/state.go).
var taskStates = []string{
	core.StateSubmitted, core.StateQueued, core.StateDispatched,
	core.StateWaitingCtx, core.StateRunning, core.StateReview,
	core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired,
}

// replConfigSections are the sections `/config set` accepts — a subset of the
// `panda config` ones (configcmd.go): the REPL edits policy, not the model
// endpoint, which /model owns.
var replConfigSections = []string{"injection", "approval", "limits", "routing", "mcp"}

// argCandidates resolves the candidate list for the argument under the cursor.
// cmd is the slash command without its "/", args are the arguments typed so
// far with the last element the partial token being completed (possibly "").
// A nil return means "nothing to suggest here", which is the honest answer for
// free-text arguments like /ask or /reject's reason.
func (r *repl) argCandidates(cmd string, args []string) []string {
	section := ""
	if len(args) > 1 {
		section = args[0]
	}
	switch cmd {
	case "task", "logs", "cancel":
		if len(args) == 1 {
			return r.taskIDs("")
		}
	case "approve", "reject":
		// Only a reviewed task can be approved or rejected; offering any
		// other id would just produce a state-transition error.
		if len(args) == 1 {
			return r.taskIDs(core.StateReview)
		}
	case "tasks":
		if len(args) == 1 {
			return append(append([]string{}, taskStates...), "watch")
		}
	case "resume":
		if len(args) == 1 {
			return append(r.sessionIDs(), "-")
		}
	case "lang":
		if len(args) == 1 {
			return localeCodeList()
		}
	case "help":
		if len(args) == 1 {
			return commandNames()
		}
	case "project":
		if len(args) == 1 {
			return r.projectNames()
		}
	case "memory":
		switch len(args) {
		case 1:
			return []string{"get"}
		case 2:
			if section == "get" {
				return r.memoryTargets()
			}
		}
	case "config":
		switch len(args) {
		case 1:
			return []string{"set"}
		case 2:
			if section == "set" {
				return replConfigSections
			}
		case 3:
			if args[0] == "set" {
				return configValues(args[1])
			}
		}
	case "model":
		// First position: the verbs plus every registered alias and the active
		// model (deduplicated). `/model add` completes its next position with
		// provider ids, while remove/test/fetch complete aliases/providers.
		if len(args) == 1 {
			out := []string{"list", "add", "remove", "fetch", "test"}
			seen := make(map[string]bool, len(out)+8)
			for _, v := range out {
				seen[v] = true
			}
			if r.cfg != nil {
				for _, m := range r.cfg.Models {
					alias := m.Alias()
					if alias != "" && !seen[alias] {
						seen[alias] = true
						out = append(out, alias)
					}
				}
				active := r.cfg.Model.Alias()
				if active != "" && !seen[active] {
					seen[active] = true
					out = append(out, active)
				}
			}
			return out
		}
		if len(args) == 2 {
			switch args[0] {
			case "add":
				ids := make([]string, 0, len(providers.All()))
				for _, p := range providers.All() {
					ids = append(ids, p.ID)
				}
				return ids
			case "remove", "rm", "del", "test":
				var aliases []string
				seen := make(map[string]bool)
				if r.cfg != nil {
					for _, m := range r.cfg.Models {
						alias := m.Alias()
						if alias != "" && !seen[alias] {
							seen[alias] = true
							aliases = append(aliases, alias)
						}
					}
				}
				return aliases
			case "fetch", "models":
				var candidates []string
				seen := make(map[string]bool)
				if r.cfg != nil {
					for _, m := range r.cfg.Models {
						alias := m.Alias()
						if alias != "" && !seen[alias] {
							seen[alias] = true
							candidates = append(candidates, alias)
						}
					}
				}
				for _, p := range providers.All() {
					if !seen[p.ID] {
						seen[p.ID] = true
						candidates = append(candidates, p.ID)
					}
				}
				return candidates
			}
		}
		if len(args) == 3 && args[0] == "add" {
			providerID := args[1]
			if p, ok := providers.Lookup(providerID); ok && p.DefaultModel != "" {
				return []string{p.DefaultModel}
			}
		}
	}
	return nil
}

// configValues lists the accepted values for one `/config set <section>`
// position — the enums cmdConfig validates against, so completion and
// validation cannot drift apart silently.
func configValues(section string) []string {
	switch section {
	case "injection":
		return []string{config.InjectionModelAuto, config.InjectionModelAlways, config.InjectionModelNever}
	case "approval":
		return []string{config.ApprovalModeAlways, config.ApprovalModeOnRequest, config.ApprovalModeNever}
	case "limits":
		return []string{"user", "memory", "project"}
	case "routing":
		names := make([]string, 0, 4)
		for _, k := range agents.Registry() {
			names = append(names, k.Name)
		}
		return names
	}
	return nil
}

func localeCodeList() []string {
	out := make([]string, len(i18n.Locales))
	for i, loc := range i18n.Locales {
		out[i] = string(loc)
	}
	return out
}

// argCacheTTL is how long a completion candidate list is reused. Long enough
// that a burst of keystrokes costs one query, short enough that a task that
// just finished shows up by the time the user looks for it.
const argCacheTTL = 2 * time.Second

// argCache memoizes one candidate list keyed by what produced it.
type argCache struct {
	key    string
	at     time.Time
	values []string
}

func (c *argCache) get(key string) ([]string, bool) {
	if c.key != key || time.Since(c.at) > argCacheTTL {
		return nil, false
	}
	return c.values, true
}

func (c *argCache) put(key string, values []string) []string {
	c.key, c.at, c.values = key, time.Now(), values
	return values
}

// taskIDs lists task ids in the given state ("" = any), newest first as the
// store returns them. Completion is a convenience, so every failure here is
// an empty list rather than an error on the prompt line.
func (r *repl) taskIDs(state string) []string {
	if r.store == nil {
		return nil
	}
	if v, ok := r.taskIDCache.get(state); ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	tasks, err := r.store.ListByState(ctx, state)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.TaskID)
	}
	return r.taskIDCache.put(state, ids)
}

func (r *repl) sessionIDs() []string {
	if r.sessionsSt == nil {
		return nil
	}
	if v, ok := r.sessionCache.get("sessions"); ok {
		return v
	}
	list, err := r.sessionsSt.List()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	return r.sessionCache.put("sessions", ids)
}

func (r *repl) projectNames() []string {
	if r.projects == nil {
		return nil
	}
	if v, ok := r.projectCache.get("projects"); ok {
		return v
	}
	names, err := r.projects.List()
	if err != nil {
		return nil
	}
	return r.projectCache.put("projects", names)
}

// memoryTargets lists the names `/memory get` accepts: the two fixed layers,
// the dream diary, every topic file in the manifest, and every project — the
// same vocabulary resolveMemoryTarget parses.
func (r *repl) memoryTargets() []string {
	if v, ok := r.memoryCache.get("memory"); ok {
		return v
	}
	out := []string{"user", "memory", "dreams"}
	if r.hermes != nil {
		if files, err := r.hermes.Files(); err == nil {
			for _, f := range files {
				if strings.HasPrefix(f.Name, "topics/") {
					out = append(out, "topic:"+strings.TrimSuffix(strings.TrimPrefix(f.Name, "topics/"), ".md"))
				}
			}
		}
	}
	for _, n := range r.projectNames() {
		out = append(out, "project:"+n)
	}
	return r.memoryCache.put("memory", out)
}
