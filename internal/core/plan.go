package core

// The plan plane. A plan is the flagship scenario written down: develop the
// training code where a coding agent lives, train where the GPU lives, summarize
// where the user is — three stages, on three machines, with the output of each
// becoming the input of the next and nobody switching devices in between.
//
// The design choice that makes this small is that a stage of a plan *is* an
// ordinary task. It inherits the CAS state machine, the task_events audit chain,
// the lease and its timeout, retry, supervision and the approval parking that
// tasks already have. What a stage needs beyond a task is only four things:
// which plan it belongs to, which stage of it, which stages it waits for, and
// the artifacts it consumes and produces. Those are columns on tasks (migration
// v12), not a second scheduler.
//
// A blocked stage needs no new state either. It sits in submitted with
// scheduled=1: visible on the board as part of the plan, invisible to the queue
// scheduler, which only ever looks at queued AND scheduled=1. AdvancePlan
// releasing a stage is exactly the submitted -> queued transition, and that
// transition is a CAS — so two stages finishing at the same instant race
// harmlessly, one of them losing with ErrConflict, and no per-plan lock is
// needed anywhere.
//
// This file is separate from handlers.go because run() there has a local
// variable named plan (the execution plan from the commander), which would
// shadow this package's import.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

// SetStage stamps a task's place in a plan: the plan it belongs to, its stage id
// within that plan, and the stage ids it waits for. Called once at StartPlan,
// before the stage is released.
func (s *TaskStore) SetStage(ctx context.Context, taskID, planID, stageID string, needs []string) error {
	needsJSON, err := json.Marshal(needs)
	if err != nil {
		return fmt.Errorf("marshal needs: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET plan_id=?, stage_id=?, needs_json=?, updated_at=?
		WHERE task_id=?`,
		planID, stageID, string(needsJSON), s.now(), taskID); err != nil {
		return fmt.Errorf("set stage: %w", err)
	}
	return nil
}

// SetStageInputs records the artifacts a stage starts from. Written by the plan
// node the moment the last predecessor finishes — that is the first point at
// which every input hash, and a node known to hold it, are both known.
func (s *TaskStore) SetStageInputs(ctx context.Context, taskID string, inputs []bus.ArtifactRef) error {
	inputsJSON, err := json.Marshal(inputs)
	if err != nil {
		return fmt.Errorf("marshal stage inputs: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET input_artifacts_json=?, updated_at=? WHERE task_id=?`,
		string(inputsJSON), s.now(), taskID); err != nil {
		return fmt.Errorf("set stage inputs: %w", err)
	}
	return nil
}

// SetOutputArtifact records the hash of the tree a stage produced. On the
// executor it is written when the work-dir is packed; on the plan node when the
// result arrives — both ends keep the same fact so either can serve the pull.
func (s *TaskStore) SetOutputArtifact(ctx context.Context, taskID, hash string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET output_artifact=?, updated_at=? WHERE task_id=?`,
		hash, s.now(), taskID); err != nil {
		return fmt.Errorf("set output artifact: %w", err)
	}
	return nil
}

// PlanStages returns every stage of one plan, ordered by stage id so the listing
// is stable across calls. This is the orchestrator's hot query — asked each time
// a stage finishes to decide what became ready — and idx_tasks_plan serves it.
func (s *TaskStore) PlanStages(ctx context.Context, planID string) ([]Task, error) {
	if planID == "" {
		return nil, errors.New("plan stages: empty plan id")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE plan_id = ? ORDER BY stage_id ASC`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// RecordArtifact indexes an artifact this node holds. The row is the pool's
// catalogue, not its content: the archive itself lives in the artifact store
// under its hash, because a trained model is measured in gigabytes and has no
// business inside SQLite. A re-recorded hash is an update, not a conflict —
// content addressing means the bytes cannot have changed.
func (s *TaskStore) RecordArtifact(ctx context.Context, hash string, size int64, taskID, manifestJSON string) error {
	if hash == "" {
		return errors.New("record artifact: empty hash")
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO artifacts (hash, size, task_id, created_at, manifest_json)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO UPDATE SET size=excluded.size, task_id=excluded.task_id,
			manifest_json=excluded.manifest_json`,
		hash, size, taskID, s.now(), manifestJSON); err != nil {
		return fmt.Errorf("record artifact: %w", err)
	}
	return nil
}

// StartPlan validates a plan, creates one task per stage, and releases the
// stages that have no dependencies. It returns the plan id, which is how the
// caller follows the whole run (PlanStages) rather than one task at a time.
//
// Every stage is created as a queued-scheduler task with no work dir: a path
// from the machine that wrote the plan is meaningless on the machine that runs
// the stage, so each executor derives its own (stageWorkDir). No stage carries
// tier-2 consent either — an irreversible stage parks in review for the user,
// which is exactly the pending-approval behaviour a plan should have.
func (c *Core) StartPlan(ctx context.Context, p plan.Plan, q QueueSpec) (string, error) {
	if err := plan.Validate(p); err != nil {
		return "", fmt.Errorf("invalid plan: %w", err)
	}
	if q.Priority < PriorityHigh || q.Priority > PriorityLow {
		return "", fmt.Errorf("queue priority %d out of range", q.Priority)
	}
	planID, err := newUUID()
	if err != nil {
		return "", fmt.Errorf("mint plan id: %w", err)
	}
	for _, st := range p.Stages {
		resourceJSON, err := json.Marshal(st.Resources)
		if err != nil {
			return "", fmt.Errorf("marshal stage %s resources: %w", st.ID, err)
		}
		title := st.Title
		if title == "" {
			title = st.ID
		}
		t, _, _, err := c.createTask(ctx, TaskInput{
			Title:        title,
			Intent:       st.Intent,
			Requires:     st.Requires,
			ResourceJSON: string(resourceJSON),
		})
		if err != nil {
			return "", fmt.Errorf("create stage %s: %w", st.ID, err)
		}
		// No work dir: the executor derives one per stage. Resource keys are the
		// plan's when the caller named any, so two plans that touch the same
		// resource still serialize; otherwise each stage gets a key of its own.
		//
		// That default matters: with none, every stage falls back to the shared
		// agent lock (queue.DefaultResourceKey), which exists because two
		// anonymous tasks would trample one working tree. Stages cannot — each
		// derives its own directory (stageWorkDir) — so the lock would only make
		// independent stages queue behind each other, which is the opposite of
		// what a plan's fan-out means. Concurrency stays bounded by
		// MaxConcurrent, and ordering by the dependency graph.
		keys := q.ResourceKeys
		if len(keys) == 0 {
			keys = []string{"plan:" + planID + ":" + st.ID}
		}
		if err := c.store.SetQueueMeta(ctx, t.TaskID, q.Priority, q.SessionID, "", keys); err != nil {
			return "", fmt.Errorf("queue meta for stage %s: %w", st.ID, err)
		}
		if err := c.store.SetStage(ctx, t.TaskID, planID, st.ID, st.Needs); err != nil {
			return "", fmt.Errorf("stamp stage %s: %w", st.ID, err)
		}
	}
	c.logger.Info("plan started", "plan", planID, "stages", len(p.Stages), "goal", p.Goal)
	if err := c.AdvancePlan(ctx, planID); err != nil {
		return planID, fmt.Errorf("release first stages: %w", err)
	}
	return planID, nil
}

// AdvancePlan releases every stage whose dependencies are now satisfied. It is
// idempotent and safe to call from any completion path: a stage already released
// is no longer in submitted, and a concurrent release loses the CAS.
//
// Readiness is computed by rebuilding the plan from the task rows and asking
// plan.Ready, rather than re-deriving the rule here: the rows are the plan, and
// one implementation of "ready" is easier to trust than two.
func (c *Core) AdvancePlan(ctx context.Context, planID string) error {
	stages, err := c.store.PlanStages(ctx, planID)
	if err != nil {
		return fmt.Errorf("load plan %s: %w", planID, err)
	}
	if len(stages) == 0 {
		return fmt.Errorf("plan %s has no stages", planID)
	}
	// A node executing a delegated stage holds a row with the same plan_id but
	// only that one row. It must never release anything: it cannot see the graph,
	// and its Queue would race the delegation already under way against its own
	// queue scheduler. Only the node that created the stages advances the plan.
	if !c.orchestratesAny(stages) {
		return nil
	}
	byStage := make(map[string]Task, len(stages))
	done := make(map[string]bool, len(stages))
	for _, t := range stages {
		byStage[t.StageID] = t
		if t.State == StateDone {
			done[t.StageID] = true
		}
	}
	p := planFromStages(stages)
	released := 0
	var firstErr error
	for _, st := range plan.Ready(p, done) {
		t := byStage[st.ID]
		if t.State != StateSubmitted {
			continue // already released, running, or terminal
		}
		inputs, err := c.planStageInputs(ctx, byStage, plan.Inputs(p, st.ID))
		if err != nil {
			// A missing input is not something waiting longer can fix: the
			// predecessor is done and produced nothing fetchable, so the stage
			// would run on absent data. Fail it and let the plan surface that.
			c.logger.Warn("plan: stage inputs unresolved", "plan", planID, "stage", st.ID, "err", err)
			if ferr := c.store.Cancel(ctx, t.TaskID); ferr != nil {
				c.logger.Warn("plan: cancel unresolvable stage", "task", t.TaskID, "err", ferr)
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(inputs) > 0 {
			if err := c.store.SetStageInputs(ctx, t.TaskID, inputs); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		if err := c.store.Queue(ctx, t.TaskID, t.OwnerNode); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
				// Another completion released this stage first — the expected
				// outcome of two predecessors finishing at once, not an error.
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("release stage %s: %w", st.ID, err)
			}
			continue
		}
		released++
		c.logger.Info("plan: stage released", "plan", planID, "stage", st.ID,
			"task", t.TaskID, "inputs", len(inputs))
	}
	if released > 0 {
		c.queueWake()
	}
	if firstErr == nil && released == 0 && plan.Complete(p, done) {
		c.logger.Info("plan complete", "plan", planID, "stages", len(stages))
	}
	return firstErr
}

// planFromStages rebuilds the plan from its task rows. Only the fields readiness
// and input ordering depend on are needed — the rows carry the rest.
func planFromStages(stages []Task) plan.Plan {
	p := plan.Plan{Goal: "reconstructed", Stages: make([]plan.Stage, 0, len(stages))}
	for _, t := range stages {
		p.Stages = append(p.Stages, plan.Stage{
			ID:       t.StageID,
			Title:    t.Title,
			Requires: t.Requires,
			Needs:    t.Needs,
			Intent:   t.Intent,
		})
	}
	return p
}

// planStageInputs turns a stage's dependencies into artifact references: what to
// fetch, and from whom. The hash comes from the predecessor's recorded output.
// The holder is *this* node whenever it holds the bytes, and only otherwise the
// node that executed the predecessor — see adoptStageOutput for why the plan node
// is the hub. needs arrives in plan.Inputs order, which is sorted, so a stage
// with two inputs extracts them in the same order on every node.
func (c *Core) planStageInputs(ctx context.Context, byStage map[string]Task, needs []string) ([]bus.ArtifactRef, error) {
	out := make([]bus.ArtifactRef, 0, len(needs))
	for _, need := range needs {
		dep, ok := byStage[need]
		if !ok {
			return nil, fmt.Errorf("stage %s is not part of this plan", need)
		}
		if dep.OutputArtifact == "" {
			// The predecessor finished without producing a tree (no artifact
			// pool on that node, or a pack that failed). Nothing to hand on.
			continue
		}
		source := ""
		if c.artifacts != nil {
			if _, held := c.artifacts.Has(dep.OutputArtifact); held {
				source = c.nodeID
			}
		}
		if source == "" {
			// The adoption pull failed or this node has no pool. Naming the
			// executor is the only remaining chance: it works when the consumer
			// is a node that predecessor can authenticate, and fails loudly with
			// the hash in the message when it is not.
			if target, err := c.store.DispatchTarget(ctx, dep.TaskID); err == nil && target != "" {
				source = target
			} else {
				source = dep.OwnerNode
			}
		}
		if source == "" {
			return nil, fmt.Errorf("no node known to hold artifact %s of stage %s", dep.OutputArtifact, need)
		}
		out = append(out, bus.ArtifactRef{Stage: need, Hash: dep.OutputArtifact, Source: source})
	}
	return out, nil
}

// orchestratesAny reports whether this node created any of these stage rows, and
// is therefore the node that owns the plan's graph. createTask stamps the chain
// with this node as its origin, while a delegated copy of a stage arrives with
// the plan node already at the head of its chain.
func (c *Core) orchestratesAny(stages []Task) bool {
	for _, t := range stages {
		if len(t.Chain) > 0 && t.Chain[0] == c.nodeID {
			return true
		}
	}
	return false
}

// adoptStageOutput brings a remote stage's output into this node's pool, records
// it on the local row, and then releases whatever it unblocked.
//
// The plan node is deliberately the hub for a plan's artifacts. The node that
// will run the next stage is one the *producer* has never heard of: it is absent
// from the producing task's delegation chain and is not its dispatch target, so
// artifactPeerAuthorized on the producer would — correctly — refuse it. The plan
// node, by contrast, is in every stage's chain and is the dispatch target of
// record for every stage it hands out, so relaying through it needs no new trust
// machinery. Content addressing keeps the second hop free whenever the next stage
// lands on a node that already holds the bytes.
func (c *Core) adoptStageOutput(ctx context.Context, t Task, from, hash string) {
	if hash == "" {
		c.advanceStagePlan(ctx, t)
		return
	}
	// The pull is a multi-chunk round trip, so it must not block the message
	// handler, and it must outlive the envelope's context. Advancing happens
	// after it: a successor released first would fetch from a hub that does not
	// hold the bytes yet.
	ctx = context.WithoutCancel(ctx)
	go func() {
		if c.artifacts != nil && from != "" && from != c.nodeID {
			if _, held := c.artifacts.Has(hash); !held {
				if _, err := c.FetchArtifact(ctx, from, t.TaskID, hash); err != nil {
					c.logger.Warn("plan: adopt stage artifact", "task", t.TaskID,
						"stage", t.StageID, "hash", hash, "from", from, "err", err)
				}
			}
		}
		if err := c.store.SetOutputArtifact(ctx, t.TaskID, hash); err != nil {
			c.logger.Warn("record stage output", "task", t.TaskID, "err", err)
		}
		t.OutputArtifact = hash
		c.advanceStagePlan(ctx, t)
	}()
}

// stageWorkDir is where a stage executes on *this* node. It is derived locally
// and never carried on the wire: a path from another machine means nothing here,
// and two stages running on one node must not share a tree — the second would
// pack the first's files as its own output.
func (c *Core) stageWorkDir(planID, stageID string) string {
	root := c.workDir
	if root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "plans", planID, stageID)
}

// fetchStageInputs pulls and extracts every input a stage declares. All inputs
// unpack into the work-dir root in ref order, so the linear chain accumulates
// like a workspace (the training stage sees the script the coding stage wrote)
// and a fan-in is deterministic (a later input wins a collision, identically on
// every node).
func (c *Core) fetchStageInputs(ctx context.Context, t Task, workDir string) error {
	if len(t.Inputs) == 0 {
		return nil
	}
	if c.artifacts == nil {
		return errors.New("stage has artifact inputs but this node has no artifact pool")
	}
	for _, in := range t.Inputs {
		if in.Hash == "" {
			continue
		}
		if _, held := c.artifacts.Has(in.Hash); !held {
			if in.Source == "" || in.Source == c.nodeID {
				// Named this node as the holder and it is not held: asking
				// ourselves over the bus would only time out.
				return fmt.Errorf("artifact %s of stage %s is not in the local pool", in.Hash, in.Stage)
			}
			if _, err := c.FetchArtifact(ctx, in.Source, t.TaskID, in.Hash); err != nil {
				return fmt.Errorf("fetch input %s from %s: %w", in.Hash, in.Source, err)
			}
		}
		if _, err := c.artifacts.Extract(in.Hash, workDir); err != nil {
			return fmt.Errorf("extract input %s: %w", in.Hash, err)
		}
		c.logger.Info("stage input ready", "task", t.TaskID, "stage", t.StageID,
			"from_stage", in.Stage, "hash", in.Hash)
	}
	return nil
}

// packStageOutput packs the stage's work-dir into the local pool and records the
// hash on the task row, returning it for the result payload. The bytes stay
// here: the successor's node pulls them when it needs them, which is what keeps
// a large artifact off the wire when the next stage lands on this node anyway.
func (c *Core) packStageOutput(ctx context.Context, t Task, workDir string) (string, error) {
	if c.artifacts == nil {
		return "", nil // no data plane on this node: nothing to hand on
	}
	m, err := c.artifacts.PackDir(workDir)
	if err != nil {
		return "", fmt.Errorf("pack stage output: %w", err)
	}
	manifestJSON, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	if err := c.store.RecordArtifact(ctx, m.Hash, m.Size, t.TaskID, string(manifestJSON)); err != nil {
		c.logger.Warn("index stage artifact", "task", t.TaskID, "hash", m.Hash, "err", err)
	}
	if err := c.store.SetOutputArtifact(ctx, t.TaskID, m.Hash); err != nil {
		return "", fmt.Errorf("record stage output: %w", err)
	}
	c.logger.Info("stage output packed", "task", t.TaskID, "stage", t.StageID,
		"hash", m.Hash, "bytes", m.Size, "files", len(m.Entries))
	return m.Hash, nil
}

// advanceStagePlan is the completion hook: whenever a stage reaches a terminal
// state on the node orchestrating the plan, the successors are reconsidered. A
// task that is not a stage is a no-op, so callers need no branch of their own.
func (c *Core) advanceStagePlan(ctx context.Context, t Task) {
	if t.PlanID == "" {
		return
	}
	if err := c.AdvancePlan(ctx, t.PlanID); err != nil {
		c.logger.Warn("advance plan", "plan", t.PlanID, "after", t.StageID, "err", err)
	}
}

// PendingPlans returns the plans that still hold an unreleased stage. Cheap
// enough to ask on every monitor tick: idx_tasks_plan covers it, and a node with
// no plans answers from the index without touching a row.
func (s *TaskStore) PendingPlans(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT plan_id FROM tasks WHERE plan_id IS NOT NULL AND plan_id <> '' AND state = ?`,
		StateSubmitted)
	if err != nil {
		return nil, fmt.Errorf("pending plans: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// sweepPlans re-examines every plan with an unreleased stage. The completion
// hooks cover the common paths, but a stage can also reach done outside this
// process — a human approving a parked review from the CLI or the web console
// writes the row directly — and nothing would then release its successors. The
// sweep is what makes a plan converge regardless of who moved the stage.
func (c *Core) sweepPlans(ctx context.Context) {
	plans, err := c.store.PendingPlans(ctx)
	if err != nil {
		c.logger.Warn("sweep plans", "err", err)
		return
	}
	for _, id := range plans {
		if err := c.AdvancePlan(ctx, id); err != nil {
			c.logger.Warn("sweep plan", "plan", id, "err", err)
		}
	}
}
