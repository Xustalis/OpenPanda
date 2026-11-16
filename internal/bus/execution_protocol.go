// SPDX-License-Identifier: AGPL-3.0-or-later
package bus

import "encoding/json"

const ExecutionProtocolVersion = 1
const CapObjectives = "objective-policy-v1"
const CapActivities = "activity-cursor-v1"
const CapCoordination = "child-coordination-v1"

// RequiredExecutionProtocol reads explicit structured requirements only.
func RequiredExecutionProtocol(spec string) int {
	var p struct {
		Objectives   json.RawMessage `json:"objectives"`
		ChildGrant   json.RawMessage `json:"child_grant"`
		Coordination string          `json:"coordination"`
	}
	if json.Unmarshal([]byte(spec), &p) != nil {
		return 0
	}
	present := func(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" }
	if present(p.Objectives) || present(p.ChildGrant) || p.Coordination == "IMMEDIATE" {
		return ExecutionProtocolVersion
	}
	return 0
}
