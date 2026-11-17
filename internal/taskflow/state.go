// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

// runnableFlowSQL applies only to task_flow_runs. Reconciliation may recover a
// recorded result, but an ambiguous model invocation never grants another run.
const runnableFlowSQL = `(state IN ('claiming','leased','running','recovering','lease_lost') OR (state='needs_reconciliation' AND prepared_json!=''))`

func (f flowRun) canCoordinate() bool {
	switch f.State {
	case "claiming", "leased", "running", "recovering", "lease_lost":
		return true
	case "needs_reconciliation":
		return f.Prepared != ""
	default:
		return false
	}
}
