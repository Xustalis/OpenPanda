// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"errors"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

// A space that never enabled automation is a local switch, not a revoked
// grant. Reporting a changed authorization sends the reader looking for the
// wrong remedy, so the two refusals must stay distinguishable.
func TestAutomationRefusalNamesTheLocalPolicy(t *testing.T) {
	s, _, c, p, wire := automationTest(t, false)
	snapshot, err := wire.Snapshot(1, "Space")
	if err != nil {
		t.Fatal(err)
	}
	withoutExecute := p
	withoutExecute.Execute = false
	if err = eligible(c, withoutExecute, snapshot, s.now()); !errors.Is(err, taskproviders.ErrAutomationNotAuthorized) {
		t.Fatalf("execution without a local policy: %v", err)
	}
	withoutResearch := p
	withoutResearch.Research = false
	if err = researchEligible(c, withoutResearch, snapshot); !errors.Is(err, taskproviders.ErrAutomationNotAuthorized) {
		t.Fatalf("research without a local policy: %v", err)
	}
	if err = researchEligible(c, p, snapshot); err != nil {
		t.Fatalf("research with a local policy: %v", err)
	}
}

// The task's own permissions are a separate gate: a task that cannot be
// executed is not a policy problem and must not be reported as one.
func TestAutomationRefusalKeepsTaskPermissionsDistinct(t *testing.T) {
	s, _, c, p, wire := automationTest(t, false)
	wire.Permissions.Claim = false
	snapshot, err := wire.Snapshot(1, "Space")
	if err != nil {
		t.Fatal(err)
	}
	if err = eligible(c, p, snapshot, s.now()); errors.Is(err, taskproviders.ErrAutomationNotAuthorized) {
		t.Fatalf("a task without execution permission blamed the policy: %v", err)
	}
}
