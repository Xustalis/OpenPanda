// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
)

func TestPlanDevicesDistinguishTargetsFromExecution(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	for _, id := range []string{"pinned", "dispatched", "complete"} {
		task, err := store.Create(ctx, "", "", id, "origin", []string{"origin"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetStage(ctx, task.TaskID, "p", id, nil); err != nil {
			t.Fatal(err)
		}
		if id == "pinned" {
			if err := store.SetDetail(ctx, task.TaskID, core.TaskDetail{SpecJSON: `{"node":"pi","plan_final":true}`}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := store.Queue(ctx, task.TaskID, "origin"); err != nil {
			t.Fatal(err)
		}
		if err := store.Dispatch(ctx, task.TaskID, "origin", "relay"); err != nil {
			t.Fatal(err)
		}
		if id == "complete" {
			if err := store.Accept(ctx, task.TaskID, "relay"); err != nil {
				t.Fatal(err)
			}
			if err := store.Complete(ctx, task.TaskID, "relay", map[string]any{"executor": "windows"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	h := &handler{store: store}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/plans/p", nil)
	r.SetPathValue("id", "p")
	h.getPlan(w, r)
	var data struct {
		Stages []planStageJSON `json:"stages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || w.Code != 200 || len(data.Stages) != 3 {
		t.Fatalf("plan: %d %s %v", w.Code, w.Body, err)
	}
	for _, stage := range data.Stages {
		switch stage.Stage {
		case "pinned":
			if stage.Executor != "" || stage.Target != "pi" || !stage.Final {
				t.Fatalf("pin presented as execution: %+v", stage)
			}
		case "dispatched":
			if stage.Executor != "" || stage.Target != "relay" {
				t.Fatalf("relay presented as executor: %+v", stage)
			}
		case "complete":
			if stage.Executor != "windows" || stage.Target != "relay" {
				t.Fatalf("actual execution attribution lost: %+v", stage)
			}
		}
	}
}
