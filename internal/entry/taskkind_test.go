// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskKindValidationAndPersistence(t *testing.T) {
	for _, kind := range []TaskKind{"", TaskCodeQuestion, TaskInvestigation, TaskEdit, TaskOperations, "magic"} {
		t.Run(string(kind), func(t *testing.T) {
			spec := &TaskSpec{Title: "inspect", ContextType: "file", Requires: Requires{Abilities: []string{"agent:codex"}}, Spec: TaskSpecDetail{Kind: kind}}
			err := ValidateTaskSpec(spec)
			if (err == nil) != kind.Valid() {
				t.Fatalf("kind=%q err=%v", kind, err)
			}
			if !kind.Valid() {
				return
			}
			b, err := json.Marshal(spec.Spec)
			if err != nil {
				t.Fatal(err)
			}
			var restored TaskSpecDetail
			if err := json.Unmarshal(b, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.Kind != kind {
				t.Fatalf("lost task kind: %s", b)
			}
			if kind == "" && strings.Contains(string(b), "task_kind") {
				t.Fatalf("legacy spec changed: %s", b)
			}
		})
	}
}

func TestTaskKindEvidencePoliciesAreTaskSpecific(t *testing.T) {
	for _, kind := range []TaskKind{TaskCodeQuestion, TaskInvestigation} {
		p := kind.EvidencePolicy()
		for _, want := range []string{"actual implementation", "counterexamples", "unexamined", "does not require builds"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s missing %q", kind, want)
			}
		}
	}
	if p := TaskEdit.EvidencePolicy(); !strings.Contains(p, "verification results") || strings.Contains(p, "does not require builds") {
		t.Fatalf("edit policy: %s", p)
	}
	if !strings.Contains(TaskOperations.EvidencePolicy(), "postcondition") {
		t.Fatal("operations needs actual state")
	}
	if TaskKind("unsupported").EvidencePolicy() != "" {
		t.Fatal("unknown kind must not invent a policy")
	}
}
