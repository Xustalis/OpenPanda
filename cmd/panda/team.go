// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// The conversation roster — "who works on this conversation" — shared by the
// web console, the classic REPL and the Bubble Tea TUI.
//
// The console keeps a thread's roster on the session row (sessions.Session.Team)
// and copies it into every ask (askengine.AskScope.Agents), so the choice
// bounds the dispatch budget and is enforced at routing time on whichever node
// executes the task. The terminal had no equivalent: `/team` persisted to the
// bound session when there is one, and to the bare conversation's own file
// otherwise (bare chats are sharded by project, exactly like convoPath).
//
// A roster name is any clean agent key (registry name or a card-defined agent).
// Unknown names are accepted — a peer node may carry the agent even when this
// one does not — but the command says so, because a typo otherwise surfaces
// much later as a refused task.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// ---- Bare-conversation roster file ----
//
// Mirrors conversations/<project>.json: conversations/<project>.team.json.
// The roster belongs to the conversation, so /new and project deletion clear
// it alongside the turns.

// teamFileName is the suffix the bare roster shares with its conversation.
const teamFileName = ".team.json"

// bareTeamPath returns the roster file for the project active in the CLI's
// global config, or "" when the node has no state directory. One-shot commands
// use it; a live repl resolves through its own project state instead (see
// bareTeamPathNow) so it never opens a store named by a different config.
func bareTeamPath() string {
	d := cliStateDir()
	if d == "" {
		return ""
	}
	convDir := filepath.Join(d, "conversations")
	cfg, _ := config.Load(cliConfigPath)
	var proj string
	if cfg != nil {
		proj, _ = activeProject(cfg)
	}
	if strings.TrimSpace(proj) == "" {
		return filepath.Join(convDir, "default"+teamFileName)
	}
	return filepath.Join(convDir, sanitizeProjectName(strings.TrimSpace(proj))+teamFileName)
}

// bareTeamFileFor is the path of one project's roster file, used by the
// rename/delete paths that already know the project name.
func bareTeamFileFor(project string) string {
	d := cliStateDir()
	if d == "" {
		return ""
	}
	name := "default"
	if strings.TrimSpace(project) != "" {
		name = sanitizeProjectName(strings.TrimSpace(project))
	}
	return filepath.Join(d, "conversations", name+teamFileName)
}

// loadBareTeam reads the roster for the project active in the CLI's global
// config. One-shot commands use it; a live repl resolves through its own
// project state instead (see bareTeamPathNow).
func loadBareTeam() []string { return readTeamFile(bareTeamPath()) }

// readTeamFile reads one roster file. A missing or corrupt file means "no
// restriction" — the same degradation an unset session roster has. Entries are
// re-validated so a hand-edited file cannot inject a name that would make
// every task in the conversation unroutable.
func readTeamFile(p string) []string {
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var f struct {
		Team []string `json:"team"`
	}
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	team, err := sessions.CleanTeam(f.Team)
	if err != nil {
		return nil
	}
	return team
}

// saveBareTeam persists the CLI-global project's roster. One-shot writers and
// tests use it; a live repl writes through its own method below.
func saveBareTeam(team []string) error { return writeTeamFile(bareTeamPath(), team) }

// writeTeamFile persists one roster. An empty roster removes the file instead
// of writing an empty one: "no restriction" is the absence of a pick, not a
// pick of nobody.
func writeTeamFile(p string, team []string) error {
	if p == "" {
		return errors.New("no state directory")
	}
	if len(team) == 0 {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Team []string `json:"team"`
	}{team})
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

// renameBareTeam moves a project's roster file with its conversation.
func renameBareTeam(oldName, newName string) {
	oldPath, newPath := bareTeamFileFor(oldName), bareTeamFileFor(newName)
	if oldPath == "" || newPath == "" || oldPath == newPath {
		return
	}
	if _, err := os.Stat(oldPath); err == nil {
		_ = os.Rename(oldPath, newPath)
	}
}

// deleteBareTeam removes a project's roster file.
func deleteBareTeam(name string) {
	if p := bareTeamFileFor(name); p != "" {
		_ = os.Remove(p)
	}
}

// ---- repl team state ----

// invalidateTeam drops the cached display roster. Called whenever the active
// session or project changes: the cache is a frame-render convenience, never
// the source of truth for an ask (teamForAsk always re-reads).
func (r *repl) invalidateTeam() {
	if r == nil {
		return
	}
	r.sessMu.Lock()
	r.teamCacheSet = false
	r.teamCache = nil
	r.sessMu.Unlock()
}

// teamForAsk is the fresh read an ask performs: the bound session's roster, or
// the bare conversation's persisted roster. It never returns a stale cache, so
// a team changed in the web console while this terminal is open still binds
// the next turn.
func (r *repl) teamForAsk() []string {
	if r == nil {
		return nil
	}
	if sid := r.sessID(); sid != "" && r.sessionsSt != nil {
		if sess, err := r.sessionsSt.Get(sid); err == nil {
			return append([]string(nil), sess.Team...)
		}
		// A stale session id degrades to the bare scope rather than failing
		// the ask; askContext takes the same path for the transcript.
	}
	return r.loadBareTeam()
}

// bareTeamPathNow resolves the roster file from the project this repl is bound
// to. It deliberately avoids bareTeamPath: that path re-reads the CLI's global
// config and calls activeProject, which opens — and on a fresh machine creates
// — the store named there. teamNow runs on every frame, so a repl started
// against another config would otherwise touch the wrong node's database.
func (r *repl) bareTeamPathNow() string {
	if r == nil {
		return ""
	}
	project := r.currentProject()
	if strings.TrimSpace(project) == "" && r.projStore != nil {
		project, _ = r.projStore.Active()
	}
	return bareTeamFileFor(project)
}

// loadBareTeam reads the roster for this repl's project.
func (r *repl) loadBareTeam() []string { return readTeamFile(r.bareTeamPathNow()) }

// saveBareTeam persists the roster for this repl's project.
func (r *repl) saveBareTeam(team []string) error {
	return writeTeamFile(r.bareTeamPathNow(), team)
}

// teamNow is the display read: cached because contextLine runs on every frame,
// including every cursor blink.
func (r *repl) teamNow() []string {
	if r == nil {
		return nil
	}
	r.sessMu.Lock()
	if r.teamCacheSet {
		out := append([]string(nil), r.teamCache...)
		r.sessMu.Unlock()
		return out
	}
	sid := r.activeSess
	r.sessMu.Unlock()

	var team []string
	if sid != "" && r.sessionsSt != nil {
		if sess, err := r.sessionsSt.Get(sid); err == nil {
			team = append([]string(nil), sess.Team...)
		} else {
			// Stale binding: the bare scope is what the next ask will use.
			team = r.loadBareTeam()
		}
	} else {
		team = r.loadBareTeam()
	}

	r.sessMu.Lock()
	r.teamCache = append([]string(nil), team...)
	r.teamCacheSet = true
	r.sessMu.Unlock()
	return team
}

// setTeam replaces the active scope's roster: the bound session when there is
// one, else the bare conversation's file. The cache follows the write, so the
// status row updates on the next frame without another disk read.
func (r *repl) setTeam(team []string) error {
	if r == nil {
		return errors.New("no conversation")
	}
	clean, err := sessions.CleanTeam(team)
	if err != nil {
		return err
	}
	if sid := r.sessID(); sid != "" && r.sessionsSt != nil {
		if err := r.sessionsSt.SetTeam(sid, clean); err != nil {
			return err
		}
	} else if err := r.saveBareTeam(clean); err != nil {
		return err
	}
	r.sessMu.Lock()
	r.teamCache = append([]string(nil), clean...)
	r.teamCacheSet = true
	r.sessMu.Unlock()
	return nil
}

// teamOp is what one /team argument asks for.
type teamOp int

const (
	teamShow teamOp = iota
	teamSet
	teamAdd
	teamRemove
	teamClear
)

// errTeamUsage is returned by the parser and localized by teamCommand, so the
// pure parser stays locale-free.
var errTeamUsage = errors.New("team usage")

// parseTeamCommand splits "/team [verb] names…" into an operation and its
// names. A bare argument with no verb is a set — "/team claude_code codex" is
// the shortest spelling of the pick, and the one people type first.
func parseTeamCommand(arg string) (teamOp, []string, error) {
	fields := strings.FieldsFunc(arg, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	if len(fields) == 0 {
		return teamShow, nil, nil
	}
	verb := strings.ToLower(fields[0])
	rest := fields[1:]
	switch verb {
	case "set", "=":
		if len(rest) == 0 {
			return teamSet, nil, errTeamUsage
		}
		return teamSet, rest, nil
	case "add", "+", "use":
		if len(rest) == 0 {
			return teamAdd, nil, errTeamUsage
		}
		return teamAdd, rest, nil
	case "rm", "remove", "del", "delete", "-":
		if len(rest) == 0 {
			return teamRemove, nil, errTeamUsage
		}
		return teamRemove, rest, nil
	case "clear", "none", "auto", "reset":
		return teamClear, nil, nil
	default:
		// No verb: the whole argument is the roster.
		return teamSet, fields, nil
	}
}

// canonicalAgent maps a typed name onto the registry's spelling when it matches
// case-insensitively. An unknown name is kept as typed.
func canonicalAgent(name string) string {
	for _, k := range agents.Registry() {
		if strings.EqualFold(k.Name, name) {
			return k.Name
		}
	}
	return name
}

// normalizeTeam canonicalises, then applies the shared validation. It is the
// one gate every terminal roster write passes through.
func normalizeTeam(names []string) ([]string, error) {
	canon := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			canon = append(canon, canonicalAgent(n))
		}
	}
	return sessions.CleanTeam(canon)
}

// unknownAgents lists roster names this node does not know about. They are
// legal — a peer may carry the agent — but the command warns, because a typo
// otherwise surfaces much later as a refused task.
func (r *repl) unknownAgents(names []string) []string {
	return unknownAgentNames(names, r.cardPathNow())
}

// unknownAgentNames lists roster entries neither the registry nor the node's
// capability card knows about. A card-defined agent is a valid roster entry
// even when the registry has never heard of it (a local adapter wrapper), so
// the card has to be consulted here too — the one-shot CLI used to warn about
// every wrapper the user had correctly picked. A missing card is fine: the
// roster can be chosen before `panda init` ran.
func unknownAgentNames(names []string, cardPath string) []string {
	known := map[string]bool{}
	for _, k := range agents.Registry() {
		known[k.Name] = true
	}
	if card, err := ledger.LoadCard(cardPath); err == nil {
		for name := range card.Agents {
			known[name] = true
		}
	}
	var out []string
	for _, n := range names {
		if !known[n] {
			out = append(out, n)
		}
	}
	return out
}

// agentRosterNames lists the registry's agent keys in registry order — the
// vocabulary a roster is written in, offered by tab completion.
func agentRosterNames() []string {
	known := agents.Registry()
	out := make([]string, 0, len(known))
	for _, k := range known {
		out = append(out, k.Name)
	}
	return out
}

// getTeam returns the active scope's roster (fresh).
func (r *repl) getTeam() []string { return r.teamForAsk() }

// splitTeamNames parses a comma/space separated roster flag.
func splitTeamNames(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
}

// resolveAskTeam is the one-shot CLI's roster resolution: an explicit --team
// wins; --continue inherits the bare conversation's persisted pick, so a team
// chosen in the terminal keeps binding the same thread from scripts.
func resolveAskTeam(flag string, inherit bool) ([]string, error) {
	var team []string
	if inherit {
		team = loadBareTeam()
	}
	return resolveAskTeamWith(flag, team)
}

// resolveAskTeamWith is resolveAskTeam for callers that already resolved the
// inherited roster — a session thread's Team, rather than the bare file.
func resolveAskTeamWith(flag string, inheritTeam []string) ([]string, error) {
	names := splitTeamNames(flag)
	if len(names) == 0 {
		names = inheritTeam
	}
	if len(names) == 0 {
		return nil, nil
	}
	return normalizeTeam(names)
}

// applyTeamDelta computes the next roster for one operation.
func applyTeamDelta(current []string, op teamOp, names []string) []string {
	switch op {
	case teamClear:
		return nil
	case teamSet:
		return names
	case teamAdd:
		out := append([]string(nil), current...)
		for _, n := range names {
			found := false
			for _, c := range out {
				if c == n {
					found = true
					break
				}
			}
			if !found {
				out = append(out, n)
			}
		}
		return out
	case teamRemove:
		drop := map[string]bool{}
		for _, n := range names {
			drop[n] = true
		}
		var out []string
		for _, c := range current {
			if !drop[c] {
				out = append(out, c)
			}
		}
		return out
	default:
		return current
	}
}

// teamCommand applies one /team argument and returns the localized result.
// Shared by the classic REPL (which prints it) and the TUI (which commits it
// as a transcript note), so the two front ends cannot drift on validation or
// wording.
func (r *repl) teamCommand(arg string) (string, error) {
	if r == nil {
		return "", errors.New("no conversation")
	}
	loc := r.locale()
	op, names, err := parseTeamCommand(arg)
	if err != nil {
		if errors.Is(err, errTeamUsage) {
			return "", errors.New(i18n.T(loc, "repl.team.usage"))
		}
		return "", err
	}
	current := r.getTeam()
	if op == teamShow {
		lines := []string{r.teamHead(loc, current)}
		if hint := i18n.T(loc, "repl.team.hint"); hint != "" {
			lines = append(lines, hint)
		}
		return strings.Join(lines, "\n"), nil
	}
	next := applyTeamDelta(current, op, names)
	if op != teamClear {
		var nerr error
		if next, nerr = normalizeTeam(next); nerr != nil {
			return "", nerr
		}
		if len(next) == 0 {
			return "", errors.New(i18n.T(loc, "repl.team.names"))
		}
	}
	if err := r.setTeam(next); err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T(loc, "repl.team.saveFail"), err)
	}
	if len(next) == 0 {
		return i18n.T(loc, "repl.team.cleared"), nil
	}
	out := i18n.Tf(loc, "repl.team.set", "names", strings.Join(next, ", "))
	if unknown := r.unknownAgents(next); len(unknown) > 0 {
		sort.Strings(unknown)
		out += "\n" + i18n.Tf(loc, "repl.team.unknown", "names", strings.Join(unknown, ", "))
	}
	return out, nil
}

// teamHead renders the one-line summary shared by /team output and the TUI's
// picker confirmation.
func (r *repl) teamHead(loc i18n.Locale, team []string) string {
	if len(team) == 0 {
		return i18n.T(loc, "repl.team.auto")
	}
	return i18n.Tf(loc, "repl.team.head", "names", strings.Join(team, ", "))
}

// cmdTeam is the classic REPL's "/team" handler.
func (r *repl) cmdTeam(arg string) {
	msg, err := r.teamCommand(arg)
	if err != nil {
		r.outln(pal().Warn(err.Error()))
		return
	}
	for _, line := range strings.Split(msg, "\n") {
		r.outln("  " + line)
	}
}

// teamNamesShort compacts a roster for the TUI's status row: two names fit,
// and beyond that the count matters more than the spelling.
func teamNamesShort(team []string) string {
	switch {
	case len(team) == 0:
		return ""
	case len(team) <= 2:
		return strings.Join(team, ", ")
	default:
		return strings.Join(team[:2], ", ") + fmt.Sprintf(" +%d", len(team)-2)
	}
}
