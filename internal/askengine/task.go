package askengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/xenith/openpanda/internal/config"
	"github.com/xenith/openpanda/internal/core"
	"github.com/xenith/openpanda/internal/entry"
	"github.com/xenith/openpanda/internal/ledger"
)

// schedulerTier maps a resource class to the DCPS-style scheduler tier used
// in priority scoring. Root scheduler = 10, sub-scheduler = 5, worker = 1.
func schedulerTier(resourceClass string) int {
	switch resourceClass {
	case "Full":
		return 10
	case "Standard":
		return 5
	default:
		return 1
	}
}

// hostStatePaths returns the node's own bookkeeping paths — its SQLite/memory
// trees and the agent CLI's own config dir — so scope-drift detection ignores
// the host's side-effect writes rather than flagging them as agent drift.
func hostStatePaths(cfg *config.Config) []string {
	return []string{
		filepath.Dir(cfg.Storage.DBPath), // data/: openpanda.db + -wal/-shm + context/
		cfg.Storage.MemoryPath,
		cfg.Storage.ProjectsPath,
		cfg.Storage.SkillsPath,
		filepath.Join(cfg.Storage.WorkPath, ".claude"), // the agent CLI's own project config
	}
}

// waitForPeers polls until at least one peer has registered online (via its
// hello capability card) or the deadline passes. It lets a slow dial over a
// real network settle before the routing decision runs.
func waitForPeers(ctx context.Context, db *sql.DB, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if nodes, err := ledger.Query(db, "online", ""); err == nil && len(nodes) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// toTaskInput translates the entry model's TaskSpec into the core's local
// TaskInput. Intent is composed from the spec fields so the agent adapter
// receives one actionable natural-language instruction (design doc §7.3: the
// refined intent is the spec from the same call — no separate refinement step
// in MVP).
func toTaskInput(spec *entry.TaskSpec) core.TaskInput {
	var constraints strings.Builder
	for i, c := range spec.Spec.Constraints {
		if i > 0 {
			constraints.WriteString("；")
		}
		constraints.WriteString(c)
	}

	var intent strings.Builder
	intent.WriteString(spec.Title)
	if spec.Spec.Target != "" {
		fmt.Fprintf(&intent, "\n目标：%s", spec.Spec.Target)
	}
	if spec.Spec.Scope != "" {
		fmt.Fprintf(&intent, "\n范围：%s", spec.Spec.Scope)
	}
	if constraints.Len() > 0 {
		fmt.Fprintf(&intent, "\n约束：%s", constraints.String())
	}
	if spec.Spec.SuccessDefinition != "" {
		fmt.Fprintf(&intent, "\n成功标准：%s", spec.Spec.SuccessDefinition)
	}

	specJSON, _ := json.Marshal(spec.Spec)
	resourceJSON, _ := json.Marshal(spec.Resources)

	return core.TaskInput{
		Title:         spec.Title,
		Project:       spec.Project,
		ContextType:   spec.ContextType,
		Intent:        intent.String(),
		SpecJSON:      string(specJSON),
		Requires:      spec.Requires.Abilities,
		PreferredNode: spec.Spec.Node,
		Complexity:    spec.Complexity,
		Risk:          spec.Risk,
		ResourceJSON:  string(resourceJSON),
	}
}
