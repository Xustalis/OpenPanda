package core

// The project plane: what has to travel with a task so that a project means the
// same thing on the machine that executes it as on the machine that asked.
//
// A delegated task used to carry the project's *name* and nothing else. The
// executor looked that name up in its own projects directory, found no memory and
// no tree, and ran an agent that could not tell what it was working on — the
// "另一个设备不知道做什么" failure. Two things close that gap, and both reuse
// machinery that already existed for plan stages:
//
//   - the project's memory directory, packed inline into the delegation (small by
//     design: memory is character-capped and skills are Markdown);
//   - the project's work tree, as an artifact reference the executor pulls
//     through the same chunked artifact_fetch a stage input uses.
//
// Nothing is replicated in the background and no node holds a copy it was not
// sent. Push-on-delegation means there is no half-synchronised state to recover
// after a disconnect: the payload either arrived with the task or it did not.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
)

// projectArtifactStage labels the project tree inside a task's Inputs. Inputs is
// shared with the plan plane, where the field names the producing stage; a
// reserved label keeps a project tree tellable apart from a stage output when
// both appear (a plan stage that also belongs to a project).
const projectArtifactStage = "__project__"

// attachProject fills in the project half of a delegation payload: the memory
// pack inline, and the work tree as an artifact reference. Called from every
// place that builds a payload, so a task cannot be delegated project-aware on one
// path and blind on another.
//
// Every failure here is a warning, never an error. A task that arrives without
// its project context still runs — with less to go on — and that is strictly
// better than a delegation refused because a directory could not be read.
func (c *Core) attachProject(ctx context.Context, p *bus.TaskDelegatePayload, project string) {
	if project == "" {
		return
	}
	if pack, err := c.packProjectMemory(project); err != nil {
		c.logger.Warn("pack project memory", "project", project, "err", err)
	} else if len(pack) > 0 {
		p.ProjectPack = pack
	}
	dir := c.projectDir(project)
	if dir == "" {
		return
	}
	p.ProjectDir = dir
	ref, err := c.packProjectTree(ctx, p.TaskID, dir)
	if err != nil {
		c.logger.Warn("pack project tree", "project", project, "dir", dir, "err", err)
		return
	}
	if ref.Hash != "" {
		p.Inputs = append(p.Inputs, ref)
	}
}

// projectDir is the project's work tree on this node, or "" when the project has
// none (or this node has no project table).
func (c *Core) projectDir(project string) string {
	if c.projects == nil || project == "" {
		return ""
	}
	pr, err := c.projects.Get(project)
	if err != nil {
		return ""
	}
	return pr.WorkDir
}

// packProjectMemory packs the project's memory directory for inline carriage.
// Returns nil when there is nothing to send or the pack exceeds the wire cap.
func (c *Core) packProjectMemory(project string) ([]byte, error) {
	if c.projectsRoot == "" {
		return nil, nil
	}
	dir, err := c.projectMemoryDir(c.projectsRoot, project)
	if err != nil {
		return nil, err
	}
	if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
		return nil, nil // no memory directory yet: nothing to carry
	}
	var buf bytes.Buffer
	if _, err := artifact.Pack(dir, &buf); err != nil {
		return nil, err
	}
	if buf.Len() > bus.MaxProjectPackBytes {
		return nil, fmt.Errorf("project pack is %d bytes, over the %d cap",
			buf.Len(), bus.MaxProjectPackBytes)
	}
	return buf.Bytes(), nil
}

// packProjectTree packs the project's work tree into the local pool and returns
// the reference the executor pulls it with. The bytes stay here: the reference
// names this node as the holder, and a peer that already has the hash skips the
// transfer entirely.
func (c *Core) packProjectTree(ctx context.Context, taskID, dir string) (bus.ArtifactRef, error) {
	if c.artifacts == nil {
		return bus.ArtifactRef{}, nil
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return bus.ArtifactRef{}, nil // the tree does not exist yet; nothing to send
	}
	m, err := c.artifacts.PackDir(dir)
	if err != nil {
		return bus.ArtifactRef{}, err
	}
	if manifestJSON, jerr := json.Marshal(m); jerr == nil {
		if rerr := c.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, string(manifestJSON)); rerr != nil {
			c.logger.Warn("index project artifact", "task", taskID, "hash", m.Hash, "err", rerr)
		}
	}
	return bus.ArtifactRef{Stage: projectArtifactStage, Hash: m.Hash, Source: c.nodeID}, nil
}

// landProjectPack extracts a delegated project's memory into this node's own
// projects directory, so the executing agent reads the project's memory from the
// same path a local task would. Best-effort: a task without its memory still runs.
func (c *Core) landProjectPack(project string, pack []byte) {
	if len(pack) == 0 || project == "" || c.projectsRoot == "" {
		return
	}
	dir, err := c.projectMemoryDir(c.projectsRoot, project)
	if err != nil {
		c.logger.Warn("land project pack: unsafe project name", "project", project, "err", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.logger.Warn("land project pack: create dir", "project", project, "err", err)
		return
	}
	if _, err := artifact.Unpack(bytes.NewReader(pack), dir); err != nil {
		c.logger.Warn("land project pack: unpack", "project", project, "err", err)
		return
	}
	// Adopt the project locally too, so `panda project list` on the executor shows
	// the work it is doing rather than a task belonging to nothing.
	if c.projects != nil {
		if _, err := c.projects.EnsureFromName(project); err != nil {
			c.logger.Warn("adopt delegated project", "project", project, "err", err)
		}
	}
	c.logger.Info("project context landed", "project", project, "dir", dir, "bytes", len(pack))
}

// projectWorkDir is where a project's tasks execute on *this* node. The origin's
// path is meaningless here (and trusting it would let a peer aim execution at any
// directory), so it is derived locally under the node's work dir, the same way a
// plan stage's directory is.
func (c *Core) projectWorkDir(project string) (string, error) {
	// A project the user configured locally wins: if this node has the tree, work
	// in it rather than in a copy.
	if dir := c.projectDir(project); dir != "" {
		return dir, nil
	}
	root := c.workDir
	if root == "" {
		root = os.TempDir()
	}
	name, err := safeProjectSegment(project)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "projects", name)
	if !strings.HasPrefix(dir, filepath.Clean(root)+string(os.PathSeparator)) {
		return "", fmt.Errorf("project work dir escapes root: %q", dir)
	}
	return dir, nil
}

// projectMemoryDir joins a project name onto a root after checking it is a single
// safe path segment.
func (c *Core) projectMemoryDir(root, project string) (string, error) {
	name, err := safeProjectSegment(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// safeProjectSegment rejects a project name that could escape a directory it is
// joined onto. The name arrives over the bus, so it is checked here rather than
// trusted: a peer must never be able to aim a write at an arbitrary path (the
// same reasoning that whitelists plan and stage ids at the wire boundary).
func safeProjectSegment(project string) (string, error) {
	name := strings.TrimSpace(project)
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("unsafe project name %q", project)
	}
	return name, nil
}

// projectInputs reports whether a task carries a project tree to pull. A plan
// stage's inputs are the plan's business (fetchStageInputs already handles them);
// this is the standalone-task case.
func projectInputs(t Task) bool {
	return t.PlanID == "" && t.Project != "" && len(t.Inputs) > 0
}

// adoptProjectOutput pulls the tree an executor produced for a project task back
// into this node's copy of the project, so work done on another machine is
// visible where the user asked for it. It is the return leg of the push: without
// it a delegated task would edit files on a machine the user never looks at.
//
// The extraction is additive — the artifact's files land over the local tree —
// and the count of what changed is recorded as a task event rather than merged.
// Automatic merging is deliberately absent: two machines editing one project is a
// real conflict, and silently resolving it in favour of whichever result arrived
// last would lose work without telling anyone. `panda task <id>` shows what came
// back; the user decides.
func (c *Core) adoptProjectOutput(ctx context.Context, t Task, from, hash string) {
	if hash == "" || c.artifacts == nil {
		return
	}
	dir := c.projectDir(t.Project)
	if dir == "" {
		// No local tree for this project: keep the artifact in the pool (it is
		// already recorded on the row) rather than inventing a directory the user
		// never asked for.
		if err := c.store.SetOutputArtifact(ctx, t.TaskID, hash); err != nil {
			c.logger.Warn("record project output", "task", t.TaskID, "err", err)
		}
		return
	}
	// The pull is a multi-chunk round trip, so it must not block the message
	// handler, and it must outlive the envelope's context.
	ctx = context.WithoutCancel(ctx)
	go func() {
		if from != "" && from != c.nodeID {
			if _, held := c.artifacts.Has(hash); !held {
				if _, err := c.FetchArtifact(ctx, from, t.TaskID, hash); err != nil {
					c.logger.Warn("adopt project artifact", "task", t.TaskID,
						"project", t.Project, "hash", hash, "from", from, "err", err)
					return
				}
			}
		}
		m, err := c.artifacts.Extract(hash, dir)
		if err != nil {
			c.logger.Warn("extract project artifact", "task", t.TaskID, "hash", hash, "err", err)
			return
		}
		if err := c.store.SetOutputArtifact(ctx, t.TaskID, hash); err != nil {
			c.logger.Warn("record project output", "task", t.TaskID, "err", err)
		}
		c.EvTrace(ctx, t.TaskID, EvProjectSync, map[string]any{
			"project": t.Project,
			"dir":     dir,
			"from":    from,
			"hash":    hash,
			"files":   len(m.Entries),
			"bytes":   m.Size,
		})
		c.logger.Info("project tree adopted", "task", t.TaskID, "project", t.Project,
			"dir", dir, "files", len(m.Entries))
	}()
}
