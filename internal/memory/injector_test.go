package memory

import (
	"strings"
	"testing"
)

func TestConversationInjectsHermesOnly(t *testing.T) {
	root := t.TempDir()
	h := NewHermes(root)
	p := NewProjects(root)

	if err := h.SaveUser(MemFile{Entries: []string{"user prefers dark mode"}}); err != nil {
		t.Fatalf("save user: %v", err)
	}
	if err := h.SaveMemory(MemFile{Entries: []string{"core is Go"}}); err != nil {
		t.Fatalf("save memory: %v", err)
	}
	if err := p.Save("panda", MemFile{Entries: []string{"project uses TypeScript"}}); err != nil {
		t.Fatalf("save project: %v", err)
	}

	inj := NewInjector(h, p)
	got, err := inj.Conversation("")
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	for _, want := range []string{"dark mode", "core is Go"} {
		if !strings.Contains(got, want) {
			t.Errorf("conversation should contain %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "TypeScript") {
		t.Errorf("conversation must not leak project memory, got %q", got)
	}
}

func TestConversationNilHermes(t *testing.T) {
	inj := NewInjector(nil, nil)
	got, err := inj.Conversation("")
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if got != "" {
		t.Errorf("nil hermes should inject empty, got %q", got)
	}
}

func TestContextPackProjectOnly(t *testing.T) {
	root := t.TempDir()
	h := NewHermes(root)
	p := NewProjects(root)

	if err := h.SaveMemory(MemFile{Entries: []string{"HERMES-SECRET: user likes dark theme"}}); err != nil {
		t.Fatalf("save hermes: %v", err)
	}
	if err := p.Save("panda", MemFile{Entries: []string{"Go core + Python glue"}}); err != nil {
		t.Fatalf("save project: %v", err)
	}

	inj := NewInjector(h, p)
	got, err := inj.ContextPack("panda")
	if err != nil {
		t.Fatalf("context pack: %v", err)
	}
	if !strings.Contains(got, "Go core") {
		t.Errorf("context pack should contain project memory, got %q", got)
	}
	// The isolation wall: Hermes content must never appear in a project pack.
	if strings.Contains(got, "HERMES-SECRET") {
		t.Errorf("context pack leaked Hermes memory: %q", got)
	}
}

func TestContextPackNilProjects(t *testing.T) {
	inj := NewInjector(nil, nil)
	got, err := inj.ContextPack("panda")
	if err != nil {
		t.Fatalf("context pack: %v", err)
	}
	if got != "" {
		t.Errorf("nil projects should pack empty, got %q", got)
	}
}
