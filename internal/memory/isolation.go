package memory

// Isolation wall (design §17.2 and §17.5): Hermes memory must never enter a
// project agent context, and project memory must never enter the conversation
// prompt. The wall is structural rather than a runtime check — Conversation
// (injector.go) reads only Hermes, and ContextPack below reads only the named
// project. No code path reads both into one prompt, so the two memory stores
// cannot leak into each other by construction.

// ContextPack builds the agent execution context for a project: the project's
// own MEMORY.md only. Hermes memory is never packed here.
//
// The pack is fenced as data (P1-23), same as Conversation: project memory is
// consolidated from task history and must carry an explicit
// data-not-instructions boundary into the agent prompt.
func (i *Injector) ContextPack(project string) (string, error) {
	if i.projects == nil {
		return "", nil
	}
	m, err := i.projects.Load(project)
	if err != nil {
		return "", err
	}
	return fenceMemoryData(string(m.Bytes())), nil
}
