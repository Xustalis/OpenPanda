package main

// `panda mcp` runs the node itself as an MCP stdio server: the agent tier's
// self-management surface (§7 companion to the tools policy). Under
// routing.tools_policy=extended the commander materializes a work-dir
// .mcp.json pointing at this command, so MCP-capable agents get structured
// OpenPanda tools — skill install, task submission, queue/mesh status —
// instead of guessing raw CLI flags. Non-MCP agents get the same capability
// through the `panda` binary already on PATH.
//
// The process is spawned per agent run by the agent's own MCP plumbing, so
// startup work stays read-mostly: the DB opens directly, skills read the
// store root, and task submission shells out to `panda task add` rather than
// duplicating the enqueue semantics.
import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/mcpserve"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

// maxSelfTaskSubmits bounds task submissions per MCP server lifetime. One
// server lives for a single agent run; an agent that submits unboundedly
// would fork-bomb its own queue, so the tool degrades to a refusal past the
// cap rather than trusting the model's judgment.
const maxSelfTaskSubmits = 8

// maxSelfToolOut caps text returned to the agent so a huge queue dump cannot
// flood its context window.
const maxSelfToolOut = 8192

func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml (default: discovered)")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, err := openStore(cfg)
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	taskStore := core.NewTaskStore(db, logger)
	skillStore := skills.NewStore(cfg.Storage.SkillsPath)

	self := selfToolsDeps{
		cfg:        cfg,
		configPath: *configPath,
		db:         db,
		tasks:      taskStore,
		skills:     skillStore,
	}
	if exe, err := os.Executable(); err == nil {
		self.exe = exe
	}
	srv := mcpserve.New("openpanda", version, selfTools(&self))
	// Stdout is the JSON-RPC channel: anything that writes to it corrupts the
	// stream, so this command never logs to stdout and fatal() stays on
	// stderr.
	_ = srv.Serve(context.Background(), os.Stdin, os.Stdout)
}

// selfToolsDeps carries the read/submit handles the self-management tools
// close over.
type selfToolsDeps struct {
	cfg        *config.Config
	configPath string
	db         *sql.DB
	tasks      *core.TaskStore
	skills     *skills.Store
	exe        string
	submits    atomic.Int64
}

// selfTools builds the advertised tool set. The set is deliberately
// read-mostly plus two writes that are themselves permission-gated
// downstream: skill installation (writes only inside the skills root, which
// the agent could edit anyway under an unrestricted shell) and task
// submission (which rides the normal queue → route → tier pipeline, so a
// tier-2 child still asks for authorization).
func selfTools(d *selfToolsDeps) []mcpserve.Tool {
	return []mcpserve.Tool{
		{
			Name:        "panda_status",
			Description: "OpenPanda node status: version, node identity, and task counts by state.",
			InputSchema: mcpserve.SchemaObject(nil),
			Handle:      d.toolStatus,
		},
		{
			Name:        "panda_nodes",
			Description: "List the mesh capability directory: every node's id, kind, online status, chip and advertised abilities.",
			InputSchema: mcpserve.SchemaObject(nil),
			Handle:      d.toolNodes,
		},
		{
			Name:        "panda_queue",
			Description: "Task queue snapshot: counts by state plus the most recent non-terminal tasks (id, title, state, owner).",
			InputSchema: mcpserve.SchemaObject(nil),
			Handle:      d.toolQueue,
		},
		{
			Name:        "panda_task_status",
			Description: "Look up one task by id: state, owner node, attempt, and a truncated result/result_reason.",
			InputSchema: mcpserve.SchemaObject(map[string]any{
				"task_id": mcpserve.StringProp("task id returned by panda_task_submit or the queue"),
			}, "task_id"),
			Handle: d.toolTaskStatus,
		},
		{
			Name:        "panda_task_submit",
			Description: "Submit a new task to this node's scheduler queue (equivalent to `panda task add`). Requires is a list of ability ids the task needs (e.g. \"coding\", \"sys:info\", \"hardware:gpio_servo\"); routing picks the node that can run it. Returns the queued task id. Capped per session.",
			InputSchema: mcpserve.SchemaObject(map[string]any{
				"title":    mcpserve.StringProp("short task title (required)"),
				"prompt":   mcpserve.StringProp("full instruction; defaults to title"),
				"requires": mcpserve.StringArrayProp("ability ids the task needs; default [\"coding\"]"),
				"project":  mcpserve.StringProp("project to attach the task to"),
				"priority": mcpserve.StringProp("low | normal | high | urgent (default normal)"),
			}, "title"),
			Handle: d.toolTaskSubmit,
		},
		{
			Name:        "panda_skill_list",
			Description: "List installed procedural skills (name, scope, status, builtin flag).",
			InputSchema: mcpserve.SchemaObject(nil),
			Handle:      d.toolSkillList,
		},
		{
			Name:        "panda_skill_install",
			Description: "Install a skill by hub name (resolved via the skills hub catalog) or directly from an http(s) URL — GitHub blob URLs are rewritten to raw automatically, zip/tar.gz archives are unpacked.",
			InputSchema: mcpserve.SchemaObject(map[string]any{
				"source": mcpserve.StringProp("hub skill name/alias, or an http(s) URL to a SKILL.md or archive"),
				"scope":  mcpserve.StringProp("global | project | device (default: frontmatter or global)"),
			}, "source"),
			Handle: d.toolSkillInstall,
		},
		{
			Name:        "panda_skill_import",
			Description: "Import a skill from raw markdown (YAML frontmatter + body). Use this to persist a workflow you just developed so future tasks can reuse it.",
			InputSchema: mcpserve.SchemaObject(map[string]any{
				"content": mcpserve.StringProp("full SKILL.md content with YAML frontmatter (required)"),
				"name":    mcpserve.StringProp("override the frontmatter name"),
			}, "content"),
			Handle: d.toolSkillImport,
		},
		{
			Name:        "panda_card",
			Description: "Show this node's capability card summary: device, resource class, native commands, agents, actuators and their tiers.",
			InputSchema: mcpserve.SchemaObject(nil),
			Handle:      d.toolCard,
		},
	}
}

func (d *selfToolsDeps) toolStatus(ctx context.Context, _ map[string]any) (string, error) {
	counts := map[string]int{}
	rows, err := d.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM tasks GROUP BY state`)
	if err != nil {
		return "", fmt.Errorf("count tasks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return "", err
		}
		counts[state] = n
	}
	out := map[string]any{
		"version":     version,
		"node":        d.cfg.Node.Name,
		"node_kind":   d.cfg.Node.Kind,
		"task_counts": counts,
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (d *selfToolsDeps) toolNodes(ctx context.Context, _ map[string]any) (string, error) {
	nodes, err := ledger.Query(d.db, "", "")
	if err != nil {
		return "", fmt.Errorf("query nodes: %w", err)
	}
	type view struct {
		ID        string   `json:"id"`
		Kind      string   `json:"kind"`
		Status    string   `json:"status"`
		Chip      string   `json:"chip,omitempty"`
		Abilities []string `json:"abilities,omitempty"`
		LastSeen  int64    `json:"last_seen"`
	}
	out := make([]view, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, view{ID: n.ID, Kind: n.NodeKind, Status: n.Status,
			Chip: n.Chip, Abilities: n.Abilities(), LastSeen: n.LastSeen})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (d *selfToolsDeps) toolQueue(ctx context.Context, _ map[string]any) (string, error) {
	type item struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		State string `json:"state"`
		Owner string `json:"owner"`
	}
	var items []item
	counts := map[string]int{}
	for _, state := range []string{"submitted", "queued", "dispatched", "running", "waiting_context", "review"} {
		tasks, err := d.tasks.ListByState(ctx, state)
		if err != nil {
			return "", fmt.Errorf("list %s: %w", state, err)
		}
		counts[state] = len(tasks)
		for _, t := range tasks {
			if len(items) >= 25 {
				break
			}
			items = append(items, item{ID: t.TaskID, Title: t.Title, State: t.State, Owner: t.OwnerNode})
		}
	}
	out := map[string]any{"counts": counts, "active": items}
	b, _ := json.Marshal(out)
	return clipSelfOut(string(b)), nil
}

func (d *selfToolsDeps) toolTaskStatus(ctx context.Context, args map[string]any) (string, error) {
	id := strings.TrimSpace(mcpserve.Arg(args, "task_id"))
	if id == "" {
		return "", fmt.Errorf("task_id required")
	}
	t, err := d.tasks.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("task %s: %w", id, err)
	}
	result := t.ResultJSON
	if len(result) > 2048 {
		result = result[:2048] + "…[truncated]"
	}
	out := map[string]any{
		"task_id": t.TaskID, "title": t.Title, "state": t.State,
		"owner": t.OwnerNode, "attempt": t.AttemptID, "project": t.Project,
		"result": result,
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// toolTaskSubmit shells out to `panda task add` so the submission rides the
// exact CLI semantics — capability-card routing requirements, queue metadata,
// session linkage — instead of a parallel implementation that can drift.
func (d *selfToolsDeps) toolTaskSubmit(ctx context.Context, args map[string]any) (string, error) {
	title := strings.TrimSpace(mcpserve.Arg(args, "title"))
	if title == "" {
		return "", fmt.Errorf("title required")
	}
	if d.exe == "" {
		return "", fmt.Errorf("self executable path unavailable")
	}
	if n := d.submits.Add(1); n > maxSelfTaskSubmits {
		return "", fmt.Errorf("per-session task submission cap (%d) reached", maxSelfTaskSubmits)
	}
	cmdArgs := []string{"task", "add", "--title", title}
	if p := strings.TrimSpace(mcpserve.Arg(args, "prompt")); p != "" {
		cmdArgs = append(cmdArgs, "--prompt", p)
	}
	if req := mcpserve.ArgList(args, "requires"); len(req) > 0 {
		cmdArgs = append(cmdArgs, "--requires", strings.Join(req, ","))
	}
	if p := strings.TrimSpace(mcpserve.Arg(args, "project")); p != "" {
		cmdArgs = append(cmdArgs, "--project", p)
	}
	if p := strings.TrimSpace(mcpserve.Arg(args, "priority")); p != "" {
		cmdArgs = append(cmdArgs, "--priority", p)
	}
	if d.configPath != "" {
		cmdArgs = append(cmdArgs, "--config", d.configPath)
	}
	cmd := exec.CommandContext(ctx, d.exe, cmdArgs...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("task add failed: %s", clipSelfOut(out.String()))
	}
	return clipSelfOut(out.String()), nil
}

func (d *selfToolsDeps) toolSkillList(ctx context.Context, _ map[string]any) (string, error) {
	index, err := d.skills.Index()
	if err != nil {
		return "", fmt.Errorf("skill index: %w", err)
	}
	type view struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Scope       string `json:"scope"`
		Status      string `json:"status"`
		Builtin     bool   `json:"builtin,omitempty"`
	}
	out := make([]view, 0, len(index))
	for _, e := range index {
		out = append(out, view{Name: e.Name, Description: e.Description,
			Scope: string(e.Scope), Status: string(e.Status), Builtin: e.Builtin})
	}
	b, _ := json.Marshal(out)
	return clipSelfOut(string(b)), nil
}

func (d *selfToolsDeps) toolSkillInstall(ctx context.Context, args map[string]any) (string, error) {
	source := strings.TrimSpace(mcpserve.Arg(args, "source"))
	if source == "" {
		return "", fmt.Errorf("source required")
	}
	opts := skills.ImportOptions{Scope: skills.Scope(strings.TrimSpace(mcpserve.Arg(args, "scope")))}
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		imported, err := d.skills.ImportSource(ctx, source, opts)
		if err != nil {
			return "", err
		}
		var names []string
		for _, sk := range imported {
			names = append(names, sk.Name)
		}
		return fmt.Sprintf("imported %d skill(s): %s", len(imported), strings.Join(names, ", ")), nil
	}
	sk, err := skills.InstallFromHub(ctx, d.skills, d.cfg.Skills.HubURL, source, opts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("installed skill %q (scope %s, status %s)", sk.Name, sk.Scope, sk.Status), nil
}

func (d *selfToolsDeps) toolSkillImport(ctx context.Context, args map[string]any) (string, error) {
	content := mcpserve.Arg(args, "content")
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("content required")
	}
	sk, err := d.skills.ImportBytes([]byte(content), skills.ImportOptions{
		Name: strings.TrimSpace(mcpserve.Arg(args, "name")),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("imported skill %q (scope %s)", sk.Name, sk.Scope), nil
}

func (d *selfToolsDeps) toolCard(ctx context.Context, _ map[string]any) (string, error) {
	path := defaultCardPath()
	card, err := ledger.LoadCard(path)
	if err != nil {
		return "", fmt.Errorf("load card %s: %w", path, err)
	}
	type act struct {
		ID      string `json:"id"`
		Command string `json:"command,omitempty"`
		Tier    int    `json:"tier"`
	}
	var acts []act
	for _, a := range card.Actuators {
		acts = append(acts, act{ID: a.ID, Command: a.Command, Tier: a.Tier})
	}
	var natives []string
	for _, n := range card.Native {
		natives = append(natives, n.ID)
	}
	var agents []string
	for name := range card.Agents {
		agents = append(agents, name)
	}
	out := map[string]any{
		"device":         card.Device,
		"resource_class": card.ResourceClass,
		"native":         natives,
		"agents":         agents,
		"actuators":      acts,
	}
	b, _ := json.Marshal(out)
	return clipSelfOut(string(b)), nil
}

func clipSelfOut(s string) string {
	if len(s) > maxSelfToolOut {
		return s[:maxSelfToolOut] + "…[truncated]"
	}
	return s
}
