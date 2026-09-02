package memory

import (
	"fmt"
	"strings"
)

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

// ProjectManifest is the project-context counterpart to RenderManifest: a pointer
// to the project's own memory file, with its size, so the agent can read it with
// its own file tools if the task needs the background.
//
// It exists because a project task used to receive *less* context than a
// non-project one. Outside a project the agent gets the personal-memory manifest
// (A3); inside a project it got nothing at all — the isolation wall (D3) correctly
// keeps Hermes memory out, and nothing was put in its place, so the agent had no
// way to know the project even had memory. That inversion is what made a delegated
// project task arrive not knowing what it was working on.
//
// A pointer rather than the content, deliberately: injecting project memory
// wholesale is what the A1 decision removed for burning tokens on every task. The
// wall still holds — this reads only the named project, never Hermes.
//
// workDir, when non-empty, is named too. The tree is the other half of what a
// project *is*, and an agent that is told where the files are does not have to
// guess which directory it landed in.
func (i *Injector) ProjectManifest(project, workDir string) (string, error) {
	if i.projects == nil || project == "" {
		return "", nil
	}
	path, err := i.projects.Path(project)
	if err != nil {
		return "", err
	}
	m, err := i.projects.Load(project)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "当前项目：%s\n", project)
	if workDir != "" {
		fmt.Fprintf(&b, "- 工作目录：%s\n", workDir)
	}
	if len(m.Entries) > 0 {
		fmt.Fprintf(&b, "- 项目记忆：%s（%d 条，%d 字符）——如任务需要项目背景，请自行读取\n",
			path, len(m.Entries), m.Chars())
	} else {
		fmt.Fprintf(&b, "- 项目记忆：%s（暂无内容）\n", path)
	}
	return fenceMemoryData(b.String()), nil
}
