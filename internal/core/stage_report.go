// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// A distinct file per stage follows the existing causal artifact transport,
// including remote execution. Even a native stdout-only branch has a report
// that the synthesis agent can inspect; no model transcript is replayed.
func writeStageReport(t Task, workDir, node string, result commander.Result, verdict entry.SuperviseVerdict) error {
	var detail entry.TaskSpecDetail
	if t.PlanID == "" || json.Unmarshal([]byte(t.SpecJSON), &detail) != nil || !detail.StageReports {
		return nil
	}
	if !plan.ValidID(t.StageID) {
		return fmt.Errorf("invalid report stage id")
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return err
	}
	defer root.Close()
	const dir = ".panda-stage-reports"
	if err := root.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := root.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("stage reports path is not a directory")
	}
	clip := func(s string, limit int) string {
		s = security.Redact(s)
		if len(s) > limit {
			return strings.ToValidUTF8(s[:limit], "") + "\n[truncated; inspect task evidence for the full result]"
		}
		return s
	}
	body, err := json.MarshalIndent(map[string]any{
		"task_id": t.TaskID, "stage": t.StageID, "node": node, "agent": result.Agent,
		"candidate_ok": result.OK, "exit_code": result.ExitCode,
		"supervision_verdict": verdict.Status, "stdout": clip(result.Stdout, 8000), "stderr": clip(result.Stderr, 4000),
		"objectives": detail.Objectives, "objective_assessments": verdict.ObjectiveAssessments,
		"evidence_scope": "branch output before final acceptance; dependency completion is checked by the orchestrator",
	}, "", "  ")
	if err != nil {
		return err
	}
	id, err := util.UUIDv7()
	if err != nil {
		return err
	}
	tmp := path.Join(dir, "."+id+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err = f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, path.Join(dir, t.StageID+".json"))
}
