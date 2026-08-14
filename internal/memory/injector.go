package memory

import "strings"

// Injector assembles the memory text injected into a given context (design
// §17.2). It is the only sanctioned way to pull memory into a prompt; its two
// methods are deliberately separate so Hermes and project memory can never mix.
type Injector struct {
	hermes   *Hermes
	projects *Projects
}

// NewInjector builds an injector over a Hermes store and a project store.
// Either may be nil to disable that layer (e.g. a minimal node with no
// personal memory); the corresponding methods then return empty.
func NewInjector(hermes *Hermes, projects *Projects) *Injector {
	return &Injector{hermes: hermes, projects: projects}
}

// Conversation returns the Hermes personal memory for the entry model's system
// prompt: the user profile (USER.md) and the agent's world notes (MEMORY.md),
// each already capped by its store, so no further truncation is needed. It
// returns "" when there is no memory. The result is a frozen snapshot — a later
// write does not change it.
func (i *Injector) Conversation() (string, error) {
	if i.hermes == nil {
		return "", nil
	}
	user, err := i.hermes.LoadUser()
	if err != nil {
		return "", err
	}
	mem, err := i.hermes.LoadMemory()
	if err != nil {
		return "", err
	}
	return joinSnapshot(user, mem), nil
}

// joinSnapshot renders the two personal-memory layers as one prompt block,
// labelled so the model can tell profile facts from environment facts. Empty
// layers are omitted.
func joinSnapshot(user, mem MemFile) string {
	var b strings.Builder
	if len(user.Entries) > 0 {
		b.WriteString("用户画像\n")
		b.Write(user.Bytes())
	}
	if len(mem.Entries) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("环境笔记\n")
		b.Write(mem.Bytes())
	}
	return b.String()
}
