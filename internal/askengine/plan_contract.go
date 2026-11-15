// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"encoding/json"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/contract"
)

// User delivery checks keep their identity. A model may independently reuse
// an ID; retain both requirements with a new stage-check ID, or deduplicate
// an identical check, before any real tasks are created.
func planDeliveryChecks(stage, delivery []contract.Check) ([]contract.Check, error) {
	checks, err := contract.Validate(delivery)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]string, len(checks)+len(stage))
	for _, check := range checks {
		encoded, _ := json.Marshal(check)
		seen[check.ID] = string(encoded)
	}
	for _, check := range stage {
		encoded, _ := json.Marshal(check)
		if old, exists := seen[check.ID]; exists && old == string(encoded) {
			continue
		}
		for n := 1; ; n++ {
			if _, exists := seen[check.ID]; !exists {
				break
			}
			check.ID = fmt.Sprintf("stage-check-%d", n)
		}
		encoded, _ = json.Marshal(check)
		seen[check.ID] = string(encoded)
		checks = append(checks, check)
	}
	return contract.Validate(checks)
}
