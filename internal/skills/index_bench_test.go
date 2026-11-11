// SPDX-License-Identifier: AGPL-3.0-or-later
package skills

import (
	"fmt"
	"testing"
)

func BenchmarkIndex100Skills(b *testing.B) {
	s := NewStore(b.TempDir())
	for i := 0; i < 100; i++ {
		if err := s.Save(&Skill{Name: fmt.Sprintf("skill-%03d", i), Description: "design and verification workflow", Scope: ScopeGlobal, Status: StatusActive, Body: "Read relevant inputs and validate actual outputs. "}); err != nil {
			b.Fatal(err)
		}
	}
	for b.Loop() {
		if _, err := s.Index(); err != nil {
			b.Fatal(err)
		}
	}
}
