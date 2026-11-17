// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import "testing"

func TestManualRetryOnlyClearsSelectedAgentCooldown(t *testing.T) {
	r := newTwoAgentRouter()
	r.markAgentDispatchFailure("alpha", "api_retry error 429: rate_limited")
	r.markAgentDispatchFailure("beta", "api_retry error 503: unavailable")
	r.RetryAgent("alpha")
	if _, _, blocked := r.agentCooldown("alpha"); blocked {
		t.Fatal("operator retry did not let alpha probe again")
	}
	if _, _, blocked := r.agentCooldown("beta"); !blocked {
		t.Fatal("alpha retry cleared beta's unrelated cooldown")
	}
}
