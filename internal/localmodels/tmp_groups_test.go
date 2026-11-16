package localmodels

import (
	"context"
	"testing"
)

func TestTmpRealGroups(t *testing.T) {
	groups := CCSwitchGroups(context.Background(), Options{}.withDefaults())
	for _, g := range groups {
		t.Logf("=== %s (%s) status=%s", g.Label, g.ID, g.Status)
		t.Logf("    %s", g.Detail)
		for _, c := range g.Candidates {
			t.Logf("    %-32s %-10s %-46s keyref=%-32s note=%s", c.Model, c.APIType, c.BaseURL, c.KeyRef, c.Note)
		}
	}
}
