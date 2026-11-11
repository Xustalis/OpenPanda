// SPDX-License-Identifier: AGPL-3.0-or-later
package skills

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestIndexCacheObservesChangesAndDoesNotAlias(t *testing.T) {
	s := NewStore(t.TempDir())
	sk := &Skill{Name: "x", Description: "original", Scope: ScopeGlobal, Status: StatusActive, Triggers: []string{"x"}, Body: "body"}
	if err := s.Save(sk); err != nil {
		t.Fatal(err)
	}
	a, err := s.Index()
	if err != nil {
		t.Fatal(err)
	}
	a[0].Triggers[0] = "mutated"
	b, err := s.Index()
	if err != nil || b[0].Triggers[0] != "x" {
		t.Fatalf("cache alias: %v %v", b, err)
	}
	sk.Description = "external edit longer"
	if err := s.Save(sk); err != nil {
		t.Fatal(err)
	}
	b, err = s.Index()
	if err != nil || b[0].Description != sk.Description {
		t.Fatalf("stale cache: %v %v", b, err)
	}
	p, _ := s.Path(sk)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	b, err = s.Index()
	if err != nil || len(b) != 0 {
		t.Fatalf("deleted entry retained: %v %v", b, err)
	}
}
func TestConcurrentSkillUseDoesNotLoseUpdates(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Save(&Skill{Name: "x", Description: "x", Scope: ScopeGlobal, Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.RecordUse(ScopeGlobal, "", "x", true, time.Now()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	sk, err := s.Load(ScopeGlobal, "", "x")
	if err != nil || sk.UseCount != 32 || sk.SuccessCount != 32 {
		t.Fatalf("lost counters: %+v %v", sk, err)
	}
}
