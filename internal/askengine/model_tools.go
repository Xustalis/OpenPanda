// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// model_list reads a projection of configuration, never credentials, URLs,
// headers or arbitrary provider parameters. It needs neither an agent nor a
// scheduler/worktree lock, and makes no provider request.
func registerModelTools(reg *entry.Registry, e *Engine) {
	reg.Register(entry.Tool{
		Name:        "model_list",
		Description: "List this node's configured models and the active model. Read-only local settings; no provider request or agent task. / 列出本节点已配置模型及当前模型，直接读取本地设置，不启动 Agent。",
		Tier:        defense.TierReversible,
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(context.Context, map[string]any) (string, error) {
			type modelView struct {
				Name     string `json:"name"`
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}
			view := func(m config.ModelConfig) modelView { return modelView{m.Name, m.Provider, m.Model} }
			out := struct {
				Active modelView   `json:"active"`
				Models []modelView `json:"models"`
			}{Models: []modelView{}}
			e.cfgMu.RLock()
			if e.cfg != nil {
				out.Active = view(e.cfg.Model)
				for _, m := range e.cfg.Models {
					out.Models = append(out.Models, view(m))
				}
			}
			e.cfgMu.RUnlock()
			b, err := json.Marshal(out)
			return string(b), err
		},
	})
}
