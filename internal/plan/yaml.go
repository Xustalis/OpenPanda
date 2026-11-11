// SPDX-License-Identifier: AGPL-3.0-or-later

package plan

// YAML is the plan plane's hand-written form. It exists because the plan layer
// had no entry point at all: StartPlan was reachable only from a test, so the
// three-machine pipeline this project is built for could not be started by a
// person. A file is the smallest thing that fixes that, and it is also the
// artifact worth having permanently — a pipeline you run every week should be
// something you can read, diff and review, not something a model re-derives from
// a sentence each time.
//
// Decoding stays in this package, and stays pure: bytes in, a validated Plan
// out, no file access and no clock. The caller reads the file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// yamlResources is the per-stage hardware request. The keys match the
// resource_profile the entry model emits and the scheduler routes by, so the
// same numbers mean the same thing whether a plan was written or generated.
type yamlResources struct {
	CPU          int     `yaml:"cpu"`
	RAMGB        float64 `yaml:"ram_gb"`
	GPUVRAMGB    float64 `yaml:"gpu_vram_gb"`
	DurationHint string  `yaml:"duration_hint"`
}

type yamlCheck struct {
	ID     string   `yaml:"id"`
	Kind   string   `yaml:"kind"`
	Run    string   `yaml:"run"`
	Exists []string `yaml:"exists"`
	Expect struct {
		Exit           *int   `yaml:"exit"`
		StdoutContains string `yaml:"stdout_contains"`
	} `yaml:"expect"`
}

type yamlStage struct {
	Acceptance        []yamlCheck `yaml:"acceptance"`
	SuccessDefinition string      `yaml:"success_definition"`
	ToolsPolicy       string      `yaml:"tools_policy"`
	MaxTurns          int         `yaml:"max_turns"`

	ID        string        `yaml:"id"`
	Title     string        `yaml:"title"`
	Requires  []string      `yaml:"requires"`
	Needs     []string      `yaml:"needs"`
	Node      string        `yaml:"node"`
	Intent    string        `yaml:"intent"`
	Resources yamlResources `yaml:"resources"`
}

type yamlPlan struct {
	Goal   string      `yaml:"goal"`
	Stages []yamlStage `yaml:"stages"`
}

// Parse decodes a YAML plan and validates it. Validation happens here rather
// than at the caller so no path can load a plan without it: an unvalidated plan
// fails after stages have already been dispatched, which is the expensive way to
// find out a dependency was misspelled.
//
// KnownFields is on: a misspelled key in a plan file is silently dropped by
// default, and "reqires: [gpu]" quietly producing a stage with no requirements
// is exactly how a training stage ends up on the Pi.
func Parse(data []byte) (Plan, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	var raw yamlPlan
	if err := dec.Decode(&raw); err != nil {
		return Plan{}, fmt.Errorf("parse plan: %w", err)
	}
	// A plan file is exactly one document. A second document (a stray "---"
	// when appending, or two plans pasted together) would be silently
	// dropped by a single Decode — and running the first of the two is the
	// wrong half of the typo to execute. A malformed tail is likewise not
	// ignored: io.EOF is the only acceptable second-Decode outcome.
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil && extra.Kind != 0:
		return Plan{}, fmt.Errorf("parse plan: multiple YAML documents (a plan file contains exactly one)")
	case err != nil && !errors.Is(err, io.EOF):
		return Plan{}, fmt.Errorf("parse plan: %w", err)
	}

	p := Plan{Goal: strings.TrimSpace(raw.Goal), Stages: make([]Stage, 0, len(raw.Stages))}
	for _, s := range raw.Stages {
		if s.ToolsPolicy != "" && s.ToolsPolicy != "minimal" && s.ToolsPolicy != "extended" {
			return Plan{}, fmt.Errorf("plan: stage %q invalid tools_policy", s.ID)
		}
		if s.MaxTurns < 0 || s.MaxTurns > 200 {
			return Plan{}, fmt.Errorf("plan: stage %q max_turns outside 0..200", s.ID)
		}
		var checks []contract.Check
		for _, check := range s.Acceptance {
			checks = append(checks, contract.Check{ID: check.ID, Kind: check.Kind, Run: check.Run, Exists: check.Exists, Expect: contract.Expect{Exit: check.Expect.Exit, StdoutContains: check.Expect.StdoutContains}})
		}
		checked, err := contract.Validate(checks)
		if err != nil {
			return Plan{}, fmt.Errorf("plan: stage %q: %w", s.ID, err)
		}
		checks = checked
		specJSON := ""
		if len(checks) > 0 || s.ToolsPolicy != "" || s.MaxTurns != 0 || s.SuccessDefinition != "" {
			data, e := json.Marshal(entry.TaskSpecDetail{Acceptance: checks, ToolsPolicy: s.ToolsPolicy, MaxTurns: s.MaxTurns, SuccessDefinition: s.SuccessDefinition})
			if e != nil {
				return Plan{}, e
			}
			specJSON = string(data)
		}
		p.Stages = append(p.Stages, Stage{
			ID:       strings.TrimSpace(s.ID),
			Title:    strings.TrimSpace(s.Title),
			Requires: trimAll(s.Requires),
			Needs:    trimAll(s.Needs),
			Node:     strings.TrimSpace(s.Node),
			Intent:   strings.TrimSpace(s.Intent),
			SpecJSON: specJSON,
			Resources: entry.ResourceProfile{
				CPU:          s.Resources.CPU,
				RAMGB:        s.Resources.RAMGB,
				GPUVRAMGB:    s.Resources.GPUVRAMGB,
				DurationHint: strings.TrimSpace(s.Resources.DurationHint),
			},
		})
	}
	if err := Validate(p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// trimAll trims each entry and drops the empty ones, so a trailing "- " in a
// hand-edited list does not become a stage id of "".
func trimAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ExampleYAML is the flagship scenario as a runnable file: develop the training
// code where a coding agent lives, train where the VRAM is, summarize where the
// user is. `panda plan example` prints it so the first plan a user runs is one
// they edited rather than one they invented.
const ExampleYAML = `# OpenPanda 计划：一个目标，多个阶段，跨设备接力。
# 每个阶段就是一个普通任务，所以它照常进队列、照常路由、照常重试、
# 不可逆操作照常进待审批 —— 计划只多做一件事：谁等谁，谁吃谁的产物。
#
# 运行：panda plan run this-file.yaml
# 跟踪：panda plan show <plan-id>

goal: 训练一个图像分类模型，并把结论总结回来

stages:
  # 第一步：在有编码 agent 的机器上写训练脚本（MacBook）。
  # node 是硬钉：指定后这一阶段只在那台机器上跑，离线就排队等它，
  # 绝不会悄悄落到另一台恰好满足 requires 的设备。填 panda nodes
  # 里看到的节点 id 或显示名；不写则按 requires 自由路由。
  - id: develop
    title: 写训练脚本
    # node: macbook-air      # 可选：钉死设备
    requires: [coding]
    resources:
      cpu: 2
      ram_gb: 4
      duration_hint: short
    intent: |
      在当前目录写一个 PyTorch 训练脚本 train.py：
      CIFAR-10、ResNet-18、10 个 epoch，把最终准确率写进 result.txt。
      只写代码，不要在这台机器上训练。

  # 第二步：在有显存的机器上真正训练（Windows 算力节点）。
  # needs 既是依赖，也是产物接线：上一阶段的工作目录会被打包搬到这里。
  - id: train
    title: 跑训练
    needs: [develop]
    requires: [compute]
    resources:
      cpu: 8
      ram_gb: 16
      gpu_vram_gb: 8
      duration_hint: long
    intent: |
      运行上一阶段产出的 train.py，等它跑完，
      确认 result.txt 里有最终准确率。

  # 第三步：回到轻量节点做总结（香橙派/入口设备）。
  - id: report
    title: 总结结果
    needs: [train]
    requires: [coding]
    resources:
      cpu: 1
      ram_gb: 1
      duration_hint: short
    intent: |
      读 result.txt，用三句话说明这次训练的结果和值得注意的地方，
      写进 summary.md。
`
