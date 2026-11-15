// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/mcpserve"
	"github.com/Xustalis/OpenPanda/internal/taskflow"
	"github.com/Xustalis/OpenPanda/internal/taskhub"
	"github.com/google/uuid"
)

func workTools(d *selfToolsDeps) []mcpserve.Tool {
	scope := func(extra map[string]any, required ...string) json.RawMessage {
		props := map[string]any{"connection": mcpserve.StringProp("Connection ID; local for local business tasks.")}
		for k, v := range extra {
			props[k] = v
		}
		return mcpserve.SchemaObject(props, required...)
	}
	key := mcpserve.StringProp("Full business key: instance/workspace/space/task. Kernel IDs are execution records.")
	return []mcpserve.Tool{
		{Name: "panda_work_list", Description: "List authorized business tasks visible to the ambient task's local project. Does not synchronize or execute work.", InputSchema: scope(map[string]any{"offset": mcpserve.IntProp("Task offset from nextOffset, default zero.")}), Handle: d.toolWorkList},
		{Name: "panda_work_show", Description: "Read one authorized business task in the current project scope.", InputSchema: scope(map[string]any{"key": key}, "connection", "key"), Handle: d.toolWorkShow},
		{Name: "panda_work_history", Description: "Read business execution history. Kernel done alone does not establish business completion.", InputSchema: scope(map[string]any{"key": key}, "connection", "key"), Handle: d.toolWorkHistory},
		{Name: "panda_work_request", Description: "Request execution under existing local authorization. Remote requests use the current TaskSync generation; this tool cannot grant permissions, approve actions, accept deliveries or create cloud reruns. Reuse request_id after an uncertain response.", InputSchema: scope(map[string]any{"key": key, "request_id": mcpserve.StringProp("Stable UUID for this request; reuse on retry.")}, "connection", "key", "request_id"), Handle: d.toolWorkRequest},
		{Name: "panda_work_result", Description: "Read persisted results, verification and remaining work. Returns a bounded text page; use nextOffset and sha256 for continuation. No consent proofs or credentials are exposed.", InputSchema: mcpserve.SchemaObject(map[string]any{"request_id": mcpserve.StringProp("Business execution request ID."), "offset": mcpserve.IntProp("UTF-8 byte offset from nextOffset, default zero."), "sha256": mcpserve.StringProp("Hash returned by the prior page; rejects changed results.")}, "request_id"), Handle: d.toolWorkResult},
	}
}

func workArguments(args map[string]any, names ...string) error {
	for k := range args {
		found := false
		for _, name := range names {
			if k == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unsupported business argument %q", k)
		}
	}
	return nil
}

func (d *selfToolsDeps) businessService() *taskflow.Service {
	path := ""
	if d.cfg != nil {
		path = d.cfg.Storage.DBPath
	}
	return taskflow.New(d.db, path)
}

// The caller cannot enlarge scope through a project argument or a guessed ID.
// An unresolved ambient task fails closed, rather than becoming personal scope.
func (d *selfToolsDeps) workProject(ctx context.Context) (string, error) {
	id := strings.TrimSpace(os.Getenv("PANDA_TASK_ID"))
	if id == "" {
		return "", nil
	}
	if d.tasks == nil {
		return "", errors.New("ambient task scope unavailable")
	}
	task, err := d.tasks.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("resolve ambient business scope: %w", err)
	}
	return task.Project, nil
}
func (d *selfToolsDeps) workVisible(ctx context.Context, project string, t taskhub.Task) (bool, error) {
	if project == "" {
		return true, nil
	}
	if t.ConnectionID == "local" {
		return t.Local != nil && t.Local.Project == project, nil
	}
	bindings, err := d.businessService().Bindings(ctx, t.ConnectionID, t.Snapshot.Key.SpaceID)
	if err != nil {
		return false, err
	}
	fallback := ""
	for _, b := range bindings {
		if b.RemoteProject == t.Snapshot.ProjectID {
			return b.LocalProject == project, nil
		}
		if b.RemoteProject == "" {
			fallback = b.LocalProject
		}
	}
	return fallback == project, nil
}
func (d *selfToolsDeps) scopedWork(ctx context.Context, connection, key string) (taskhub.Task, error) {
	project, err := d.workProject(ctx)
	if err != nil {
		return taskhub.Task{}, err
	}
	t, err := taskhub.New(d.db).Get(ctx, connection, key)
	if err != nil {
		return taskhub.Task{}, err
	}
	visible, err := d.workVisible(ctx, project, t)
	if err != nil {
		return taskhub.Task{}, err
	}
	if !visible {
		return taskhub.Task{}, errors.New("business task unavailable in this project")
	}
	return t, nil
}

func businessJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if len(b) <= maxSelfToolOut {
		return string(b), nil
	}
	end := maxSelfToolOut / 2
	for end > 0 && !utf8.RuneStart(b[end]) {
		end--
	}
	clipped, err := json.Marshal(map[string]any{"truncated": true, "content": string(b[:end])})
	return string(clipped), err
}

func (d *selfToolsDeps) toolWorkList(ctx context.Context, args map[string]any) (string, error) {
	if err := workArguments(args, "connection", "offset"); err != nil {
		return "", err
	}
	project, err := d.workProject(ctx)
	if err != nil {
		return "", err
	}
	tasks, err := taskhub.New(d.db).List(ctx, mcpserve.Arg(args, "connection"))
	if err != nil {
		return "", err
	}
	offset := 0
	if raw, ok := args["offset"]; ok {
		offset, err = strconv.Atoi(fmt.Sprint(raw))
		if err != nil || offset < 0 {
			return "", errors.New("invalid task offset")
		}
	}
	out := []map[string]any{}
	total := 0
	for _, t := range tasks {
		visible, err := d.workVisible(ctx, project, t)
		if err != nil {
			return "", err
		}
		if !visible {
			continue
		}
		total++
		if total <= offset || len(out) == 20 {
			continue
		}
		out = append(out, map[string]any{"connection": t.ConnectionID, "key": t.Snapshot.Key.String(), "title": t.Snapshot.Title, "status": t.Snapshot.Status, "taskRevision": t.Snapshot.TaskRevision, "priority": t.Snapshot.Priority, "projectId": t.Snapshot.ProjectID, "dueAt": t.Snapshot.DueAt})
	}
	next := 0
	if offset+len(out) < total {
		next = offset + len(out)
	}
	return businessJSON(map[string]any{"tasks": out, "total": total, "nextOffset": next})
}
func (d *selfToolsDeps) toolWorkShow(ctx context.Context, args map[string]any) (string, error) {
	if err := workArguments(args, "connection", "key"); err != nil {
		return "", err
	}
	t, err := d.scopedWork(ctx, mcpserve.Arg(args, "connection"), mcpserve.Arg(args, "key"))
	if err != nil {
		return "", err
	}
	// ProviderData is a wire cache, not an agent-facing protocol or authority.
	t.Snapshot.ProviderData = nil
	return businessJSON(t)
}
func (d *selfToolsDeps) toolWorkHistory(ctx context.Context, args map[string]any) (string, error) {
	if err := workArguments(args, "connection", "key"); err != nil {
		return "", err
	}
	t, err := d.scopedWork(ctx, mcpserve.Arg(args, "connection"), mcpserve.Arg(args, "key"))
	if err != nil {
		return "", err
	}
	rows, err := taskhub.New(d.db).History(ctx, t.ConnectionID, t.Snapshot.Key.String())
	if err != nil {
		return "", err
	}
	return businessJSON(rows)
}
func (d *selfToolsDeps) toolWorkRequest(ctx context.Context, args map[string]any) (string, error) {
	if err := workArguments(args, "connection", "key", "request_id"); err != nil {
		return "", err
	}
	t, err := d.scopedWork(ctx, mcpserve.Arg(args, "connection"), mcpserve.Arg(args, "key"))
	if err != nil {
		return "", err
	}
	id := mcpserve.Arg(args, "request_id")
	if _, err := uuid.Parse(id); err != nil {
		return "", errors.New("request_id must be a stable UUID")
	}
	if n := d.submits.Add(1); n > maxSelfTaskSubmits {
		return "", errors.New("per-session execution request limit reached")
	}
	if t.ConnectionID != "local" {
		result, err := d.businessService().RequestExecution(ctx, t.ConnectionID, t.Snapshot.Key, id)
		if err != nil {
			return "", err
		}
		return businessJSON(result)
	}
	if d.exe == "" {
		return "", errors.New("self executable path unavailable")
	}
	argv := []string{"--json", "work", "run", t.Snapshot.Key.String(), "--request", id}
	if d.configPath != "" {
		argv = append(argv, "--config", d.configPath)
	}
	cmd := exec.CommandContext(ctx, d.exe, argv...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("work request failed: %s", clipSelfOut(output.String()))
	}
	return clipSelfOut(output.String()), nil
}

func (d *selfToolsDeps) toolWorkResult(ctx context.Context, args map[string]any) (string, error) {
	if err := workArguments(args, "request_id", "offset", "sha256"); err != nil {
		return "", err
	}
	id := mcpserve.Arg(args, "request_id")
	var connection, key string
	err := d.db.QueryRowContext(ctx, "SELECT connection_id,task_key FROM execution_requests WHERE request_id=?", id).Scan(&connection, &key)
	if err != nil {
		// A remote request may not yet have acquired its lease and kernel link.
		err = d.db.QueryRowContext(ctx, "SELECT connection_id,task_key FROM task_flow_runs WHERE request_id=?", id).Scan(&connection, &key)
	}
	if err != nil {
		return "", err
	}
	if connection == "" {
		connection = "local"
	}
	if _, err = d.scopedWork(ctx, connection, key); err != nil {
		return "", err
	}
	project, err := d.workProject(ctx)
	if err != nil {
		return "", err
	}
	if project != "" {
		var total, matching int
		if err = d.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(t.project=?),0) FROM execution_links l LEFT JOIN tasks t ON t.task_id=l.task_id WHERE l.request_id=?`, project, id).Scan(&total, &matching); err != nil {
			return "", err
		}
		if total == 0 || matching != total {
			return "", errors.New("execution result unavailable in this project")
		}
	}
	var result any
	if connection != "local" {
		run, err := d.businessService().Work(ctx, id)
		if err != nil {
			return "", err
		}
		deliveries := []map[string]any{}
		for _, item := range run.Deliveries {
			v := item.Delivery
			deliveries = append(deliveries, map[string]any{"deliveryId": v.DeliveryID, "revision": v.Revision, "summary": v.Summary, "steps": v.Steps, "verification": v.Verification, "remaining": v.Remaining, "artifactRefs": v.ArtifactRefs, "files": item.Files, "failure": item.Failure, "syncState": item.SyncState})
		}
		result = map[string]any{"requestId": id, "taskKey": key, "state": run.State, "error": run.Error, "prepared": run.Prepared, "mode": run.Mode, "deliveries": deliveries, "sync": run.Sync}
	} else {
		history, err := taskhub.New(d.db).History(ctx, connection, key)
		if err != nil {
			return "", err
		}
		executions := []map[string]any{}
		for _, h := range history {
			if h.RequestID != id {
				continue
			}
			task, err := d.tasks.Get(ctx, h.TaskID)
			if err != nil {
				return "", err
			}
			executions = append(executions, map[string]any{"taskId": task.TaskID, "attemptId": task.AttemptID, "stageId": h.StageID, "state": task.State, "verification": task.Verification, "result": task.ResultJSON})
		}
		result = map[string]any{"requestId": id, "taskKey": key, "executions": executions}
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(b)
	digest := hex.EncodeToString(hash[:])
	if expected := mcpserve.Arg(args, "sha256"); expected != "" && expected != digest {
		return "", errors.New("business result changed; restart from offset zero")
	}
	offset := 0
	if raw, ok := args["offset"]; ok {
		parsed, err := strconv.Atoi(fmt.Sprint(raw))
		if err != nil {
			return "", errors.New("invalid result offset")
		}
		offset = parsed
	}
	if offset < 0 || offset > len(b) || offset < len(b) && !utf8.RuneStart(b[offset]) {
		return "", errors.New("invalid result offset")
	}
	if offset > 0 && mcpserve.Arg(args, "sha256") == "" {
		return "", errors.New("continued result reads require sha256")
	}
	end := min(offset+2048, len(b))
	for end < len(b) && !utf8.RuneStart(b[end]) {
		end--
	}
	next := end
	if end == len(b) {
		next = 0
	}
	return businessJSON(map[string]any{"requestId": id, "content": string(b[offset:end]), "offset": offset, "nextOffset": next, "totalBytes": len(b), "sha256": digest})
}
