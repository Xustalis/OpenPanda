// SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
)

func TestSubtaskSpawnCarriesBoundedVersionedAllocation(t *testing.T) {
	var received core.ChildRPCRequest
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/children" || r.Header.Get("Authorization") != "Bearer capability" {
			t.Error("missing attempt capability")
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer endpoint.Close()
	t.Setenv("PANDA_CHILD_RPC_URL", endpoint.URL)
	t.Setenv("PANDA_CHILD_RPC_TOKEN", "capability")
	t.Setenv("PANDA_ATTEMPT_ID", "attempt-one")
	d := &selfToolsDeps{}
	_, err := d.toolSubtaskSpawn(context.Background(), map[string]any{"title": "repair", "request_id": "repair-one", "token_budget": float64(1200), "delegation_budget": float64(4), "input_version": "snapshot-one"})
	if err != nil {
		t.Fatal(err)
	}
	if received.InputVersion != "snapshot-one" || received.TokenBudget != 1200 || received.DelegationBudget != 4 || received.AttemptID != "attempt-one" || received.RequestID != "repair-one" {
		t.Fatalf("lost coordination bounds: %+v", received)
	}
}

func TestChildIntegerArgRejectsInvalidAllocations(t *testing.T) {
	for _, value := range []any{-1, 0.5, "1200", nil, math.NaN(), math.Inf(1), float64(9007199254740992)} {
		if _, err := childIntegerArg(map[string]any{"budget": value}, "budget", 0, 9007199254740991); err == nil {
			t.Fatalf("accepted invalid value %v", value)
		}
	}
	if _, err := (&selfToolsDeps{}).toolSubtaskAwait(context.Background(), map[string]any{"task_id": "x", "timeout_s": 301}); err == nil {
		t.Fatal("unbounded await accepted")
	}
}
