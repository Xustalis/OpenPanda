// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Tool names — the whitelist of memory tools the entry model may invoke via
// tool_call. These are protocol strings shared with the entry-layer prompt;
// Tool.Execute enforces them, so the model never touches memory files directly.
const (
	ToolRead    = "memory_read"
	ToolAdd     = "memory_add"
	ToolReplace = "memory_replace"
	ToolRemove  = "memory_remove"
)

// Tool targets — the memory layer a tool acts on. user and memory are the two
// Hermes personal layers; project targets a per-project memory file.
const (
	targetUser    = "user"
	targetMemory  = "memory"
	targetProject = "project"
)

// ErrNoStore reports a tool targeting a layer whose store was not configured.
var ErrNoStore = errors.New("memory: no store configured for target")

// Tool executes whitelisted memory tools against the Hermes and project stores.
// It is the Go-side executor the entry model's tool_call output lands in; the
// model never touches the files directly (design §7.3: Go core validates and
// executes, the model emits only the request).
type Tool struct {
	hermes   *Hermes
	projects *Projects
}

// NewTool builds a tool executor. Either store may be nil to disable its layer.
func NewTool(hermes *Hermes, projects *Projects) *Tool {
	return &Tool{hermes: hermes, projects: projects}
}

// Execute runs one tool call and returns a short, user-facing result. name must
// be a Tool* constant; args is the model-supplied argument map, validated here
// rather than trusted verbatim. Callers that know which project the turn runs
// in must use ExecuteCtx; this entry point exists for tests and for callers
// with no project context.
func (t *Tool) Execute(name string, args map[string]any) (string, error) {
	return t.ExecuteCtx(context.Background(), name, args)
}

// ExecuteCtx is Execute with the turn's project scope. A pinned project makes
// target=project default to it and refuses any other name: the model supplies
// the argument, so without the pin a project conversation could read or edit
// another project's memory by naming it.
func (t *Tool) ExecuteCtx(ctx context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case ToolRead:
		return t.read(ctx, args)
	case ToolAdd:
		return t.add(ctx, args)
	case ToolReplace:
		return t.replace(ctx, args)
	case ToolRemove:
		return t.remove(ctx, args)
	default:
		return "", fmt.Errorf("memory: unknown tool %q", name)
	}
}

// projectFor resolves the project a target=project call acts on, enforcing the
// turn's pinned scope. An unpinned caller (tests, non-conversation tools) may
// still name a project explicitly.
func (t *Tool) projectFor(ctx context.Context, args map[string]any) (string, error) {
	requested := ""
	if v, ok := args["project"]; ok {
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("memory: argument %q must be a string", "project")
		}
		requested = strings.TrimSpace(s)
	}
	if pinned, ok := ScopeProject(ctx); ok {
		if requested == "" {
			if pinned == "" {
				return "", errors.New("memory: this conversation is not scoped to a project, so project memory is unavailable")
			}
			return pinned, nil
		}
		if !strings.EqualFold(requested, pinned) {
			return "", fmt.Errorf("memory: project %q is outside this conversation's project %q", requested, pinned)
		}
		return pinned, nil
	}
	if requested == "" {
		return "", fmt.Errorf("memory: argument %q is required", "project")
	}
	return requested, nil
}

// read lists current entries so the agent can consolidate before an add that
// would exceed a limit — the Hermes merge workflow. With no target it lists
// both personal layers; target=project lists one project.
func (t *Tool) read(ctx context.Context, args map[string]any) (string, error) {
	target := ""
	if v, ok := args["target"]; ok {
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("memory: argument %q must be a string", "target")
		}
		target = s
	}
	switch target {
	case "":
		return t.readPersonal()
	case targetUser, targetMemory:
		// A named layer lists that layer alone — returning both for an
		// explicit target would make a "what's in MEMORY.md" read lie by
		// including the user profile.
		if t.hermes == nil {
			return "", ErrNoStore
		}
		m, err := t.load(target, "")
		if err != nil {
			return "", err
		}
		return formatEntries(target, m), nil
	case targetProject:
		project, err := t.projectFor(ctx, args)
		if err != nil {
			return "", err
		}
		m, err := t.load(target, project)
		if err != nil {
			return "", err
		}
		return formatEntries("project:"+project, m), nil
	default:
		return "", fmt.Errorf("memory: unknown target %q", target)
	}
}

// readPersonal lists both personal layers — the no-target view the merge
// workflow wants before deciding where an entry belongs.
func (t *Tool) readPersonal() (string, error) {
	if t.hermes == nil {
		return "", ErrNoStore
	}
	user, err := t.hermes.LoadUser()
	if err != nil {
		return "", err
	}
	mem, err := t.hermes.LoadMemory()
	if err != nil {
		return "", err
	}
	return formatEntries("user", user) + "\n" + formatEntries("memory", mem), nil
}

// add appends one entry to a layer, enforcing that layer's character cap.
func (t *Tool) add(ctx context.Context, args map[string]any) (string, error) {
	target, entry, err := targetEntry(args)
	if err != nil {
		return "", err
	}
	if err := t.mutate(ctx, target, args, func(m *MemFile) error { return m.Add(entry) }); err != nil {
		return "", err
	}
	return fmt.Sprintf("已记住（%s）：%s", target, entry), nil
}

// replace swaps an entry identified by a unique substring match (Hermes
// semantics; ambiguous matches are rejected).
func (t *Tool) replace(ctx context.Context, args map[string]any) (string, error) {
	target, err := argString(args, "target")
	if err != nil {
		return "", err
	}
	old, err := argString(args, "old")
	if err != nil {
		return "", err
	}
	new, err := argString(args, "new")
	if err != nil {
		return "", err
	}
	if err := t.mutate(ctx, target, args, func(m *MemFile) error { return m.Replace(old, new) }); err != nil {
		return "", err
	}
	return fmt.Sprintf("已更新（%s）：%s", target, new), nil
}

// remove deletes an entry identified by a unique substring match.
func (t *Tool) remove(ctx context.Context, args map[string]any) (string, error) {
	target, err := argString(args, "target")
	if err != nil {
		return "", err
	}
	old, err := argString(args, "old")
	if err != nil {
		return "", err
	}
	if err := t.mutate(ctx, target, args, func(m *MemFile) error { return m.Remove(old) }); err != nil {
		return "", err
	}
	return fmt.Sprintf("已删除（%s）：%s", target, old), nil
}

// load loads the MemFile for a target. project names the project for a
// target=project read and is ignored otherwise.
func (t *Tool) load(target, project string) (MemFile, error) {
	switch target {
	case targetUser:
		if t.hermes == nil {
			return MemFile{}, ErrNoStore
		}
		return t.hermes.LoadUser()
	case targetMemory:
		if t.hermes == nil {
			return MemFile{}, ErrNoStore
		}
		return t.hermes.LoadMemory()
	case targetProject:
		if t.projects == nil {
			return MemFile{}, ErrNoStore
		}
		return t.projects.Load(project)
	default:
		return MemFile{}, fmt.Errorf("memory: unknown target %q", target)
	}
}

// mutate runs fn against the layer's current contents with the store's
// read-modify-write lock held, then writes the result back. The whole cycle is
// one critical section: load and save as two steps let a second writer slip
// between them and vanish one of the two changes with both reporting success.
func (t *Tool) mutate(ctx context.Context, target string, args map[string]any, fn func(*MemFile) error) error {
	switch target {
	case targetUser:
		if t.hermes == nil {
			return ErrNoStore
		}
		return t.hermes.UpdateUser(fn)
	case targetMemory:
		if t.hermes == nil {
			return ErrNoStore
		}
		return t.hermes.UpdateMemory(fn)
	case targetProject:
		if t.projects == nil {
			return ErrNoStore
		}
		project, err := t.projectFor(ctx, args)
		if err != nil {
			return err
		}
		return t.projects.Update(project, fn)
	default:
		return fmt.Errorf("memory: unknown target %q", target)
	}
}

// targetEntry extracts target + entry, the arguments shared by add.
func targetEntry(args map[string]any) (target, entry string, err error) {
	target, err = argString(args, "target")
	if err != nil {
		return "", "", err
	}
	entry, err = argString(args, "entry")
	if err != nil {
		return "", "", err
	}
	return target, entry, nil
}

// argString reads a required string argument, rejecting missing or non-string
// values so a malformed tool call fails loudly rather than misbehaving.
func argString(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("memory: missing argument %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("memory: argument %q must be a string", key)
	}
	return s, nil
}

// formatEntries renders a layer's entries as a numbered list for the agent to
// read and consolidate against.
func formatEntries(label string, m MemFile) string {
	if len(m.Entries) == 0 {
		return label + "：(空)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s（%d 条）：\n", label, len(m.Entries))
	for i, e := range m.Entries {
		fmt.Fprintf(&b, "%d. %s\n", i+1, e)
	}
	return strings.TrimRight(b.String(), "\n")
}
