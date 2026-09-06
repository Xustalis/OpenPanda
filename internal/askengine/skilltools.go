package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

// registerSkillTools equips the entry model with autonomous skill acquisition
// and management tools: searching the hub, automatically discovering and installing
// skills to handle unfamiliar tasks, and inspecting procedural memory.
func registerSkillTools(reg *entry.Registry, e *Engine) {
	reg.Register(entry.Tool{
		Name:        "skill_search",
		Description: "在 Skills Hub（技能集市）与本地技能库中搜索技能。当用户询问是否有某项技能、需要特定开发/运维规范指导，或你要自主寻找新技能执行任务时调用。query 填关键词，如 'k8s', 'python', 'docker', 'test', 'review' 等。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "技能搜索关键词或技术领域"},
			},
			"required": []string{"query"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			q, _ := args["query"].(string)
			return e.toolSkillSearch(ctx, strings.TrimSpace(q))
		},
	})

	reg.Register(entry.Tool{
		Name:        "skill_discover",
		Description: "智能自主寻找并安装技能：根据需求描述或技术关键词，自动在技能集市中搜索最匹配的技能，并一键完成安装与激活。适用于‘帮我找找并安装关于 X 的技能’，或者在处理缺乏先验工作流的任务时自主装备技能。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "需要寻找的技能需求描述或技术关键词（如 'redis'、'k8s deploy'、'爬虫'）"},
			},
			"required": []string{"query"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			q, _ := args["query"].(string)
			return e.toolSkillDiscover(ctx, strings.TrimSpace(q))
		},
	})

	reg.Register(entry.Tool{
		Name:        "skill_install",
		Description: "自主安装指定技能到本地。target 支持填写 Skills Hub 技能名称（如 'docker-compose'），或者填入 HTTP/HTTPS 网络直链（如 GitHub 上的 SKILL.md）及本地路径。安装后即可在后续任务中直接遵循该技能执行。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{"type": "string", "description": "技能名称、网络 URL 或本地文件路径"},
				"force":  map[string]any{"type": "boolean", "description": "是否覆盖已存在的同名技能（默认 false）"},
			},
			"required": []string{"target"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			target, _ := args["target"].(string)
			force, _ := args["force"].(bool)
			return e.toolSkillInstall(ctx, strings.TrimSpace(target), force)
		},
	})

	reg.Register(entry.Tool{
		Name:        "skill_list",
		Description: "列出当前已安装且生效的所有技能，了解已掌握的工作流、代码规范与诊断规程。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return e.toolSkillList(ctx)
		},
	})

	reg.Register(entry.Tool{
		Name:        "skill_show",
		Description: "查看已安装技能的完整操作规程与步骤内容（SKILL.md 正文）。name 填技能名称。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "技能名称"},
			},
			"required": []string{"name"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			return e.toolSkillShow(ctx, strings.TrimSpace(name))
		},
	})
}

func (e *Engine) getSkillsStore() *skills.Store {
	if e.skills != nil {
		return e.skills
	}
	if e.cfg != nil && e.cfg.Storage.SkillsPath != "" {
		st := skills.NewStore(e.cfg.Storage.SkillsPath)
		_ = st.EnsureBuiltins()
		e.skills = st
		return st
	}
	return nil
}

func (e *Engine) getHubURL() string {
	if e.cfg != nil {
		return e.cfg.Skills.HubURL
	}
	return ""
}

func (e *Engine) toolSkillSearch(ctx context.Context, query string) (string, error) {
	if query == "" {
		return "", fmt.Errorf("query 不能为空")
	}
	st := e.getSkillsStore()
	idx, err := skills.FetchHubIndex(ctx, e.getHubURL())
	if err != nil {
		return "", fmt.Errorf("获取 Skills Hub 失败: %w", err)
	}
	results := skills.SearchHub(idx, query)
	if len(results) == 0 {
		return fmt.Sprintf("Skills Hub 中未找到与 %q 匹配的技能。", query), nil
	}

	type matchItem struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Version     string   `json:"version,omitempty"`
		Author      string   `json:"author,omitempty"`
		Tags        []string `json:"tags,omitempty"`
		Installed   bool     `json:"installed"`
		Builtin     bool     `json:"builtin"`
	}
	items := make([]matchItem, 0, len(results))
	for _, r := range results {
		installed := false
		if st != nil {
			sk, _ := st.Load(skills.ScopeGlobal, "", r.Name)
			installed = sk != nil
		}
		items = append(items, matchItem{
			Name:        r.Name,
			Description: r.Description,
			Version:     r.Version,
			Author:      r.Author,
			Tags:        r.Tags,
			Installed:   installed,
			Builtin:     skills.IsBuiltinSkill(r.Name),
		})
	}
	data, _ := json.MarshalIndent(items, "", "  ")
	return string(data), nil
}

func (e *Engine) toolSkillDiscover(ctx context.Context, query string) (string, error) {
	st := e.getSkillsStore()
	if st == nil {
		return "", fmt.Errorf("技能存储未配置")
	}
	sk, isNew, err := st.DiscoverAndInstall(ctx, e.getHubURL(), query)
	if err != nil {
		return "", fmt.Errorf("自动寻找技能失败: %w", err)
	}
	if isNew {
		return fmt.Sprintf("已自动从技能集市中找到并安装激活新技能：%s\n描述：%s\n状态：%s\n包含工作流规范已就绪，可直接指导任务执行。",
			sk.Name, sk.Description, sk.Status), nil
	}
	return fmt.Sprintf("已找到最匹配技能 %s，该技能已处于激活状态，无需重复安装。\n描述：%s\n可直接调用。",
		sk.Name, sk.Description), nil
}

func (e *Engine) toolSkillInstall(ctx context.Context, target string, force bool) (string, error) {
	if target == "" {
		return "", fmt.Errorf("target 不能为空")
	}
	st := e.getSkillsStore()
	if st == nil {
		return "", fmt.Errorf("技能存储未配置")
	}

	opts := skills.ImportOptions{
		Scope:  skills.ScopeGlobal,
		Status: skills.StatusActive,
		Force:  force,
	}

	// 1. URL or file path
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.Contains(target, "/") || strings.Contains(target, "\\") {
		imported, err := st.ImportSource(ctx, target, opts)
		if err != nil {
			return "", fmt.Errorf("导入技能失败: %w", err)
		}
		var names []string
		for _, s := range imported {
			names = append(names, s.Name)
		}
		return fmt.Sprintf("成功导入 %d 个技能: %s", len(imported), strings.Join(names, ", ")), nil
	}

	// 2. Hub install by name
	sk, err := skills.InstallFromHub(ctx, st, e.getHubURL(), target, opts)
	if err != nil {
		return "", fmt.Errorf("从 Hub 安装技能失败: %w", err)
	}
	return fmt.Sprintf("成功从 Skills Hub 安装技能 %s (%s)，已激活生效。", sk.Name, sk.Description), nil
}

func (e *Engine) toolSkillList(ctx context.Context) (string, error) {
	st := e.getSkillsStore()
	if st == nil {
		return "", fmt.Errorf("技能存储未配置")
	}
	index, err := st.Index()
	if err != nil {
		return "", fmt.Errorf("读取技能列表失败: %w", err)
	}
	if len(index) == 0 {
		return "当前没有已安装的技能。", nil
	}
	type skillSummary struct {
		Name        string `json:"name"`
		Scope       string `json:"scope"`
		Status      string `json:"status"`
		Builtin     bool   `json:"builtin"`
		Description string `json:"description"`
	}
	summaries := make([]skillSummary, 0, len(index))
	for _, entry := range index {
		summaries = append(summaries, skillSummary{
			Name:        entry.Name,
			Scope:       string(entry.Scope),
			Status:      string(entry.Status),
			Builtin:     entry.Builtin,
			Description: entry.Description,
		})
	}
	data, _ := json.MarshalIndent(summaries, "", "  ")
	return string(data), nil
}

func (e *Engine) toolSkillShow(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("技能名称不能为空")
	}
	st := e.getSkillsStore()
	if st == nil {
		return "", fmt.Errorf("技能存储未配置")
	}
	sk, err := st.Load(skills.ScopeGlobal, "", name)
	if err != nil {
		return "", fmt.Errorf("加载技能 %s 失败: %w", name, err)
	}
	if sk == nil {
		index, _ := st.Index()
		for _, e := range index {
			if strings.EqualFold(e.Name, name) {
				sk, _ = st.Load(e.Scope, e.Key, e.Name)
				break
			}
		}
	}
	if sk == nil {
		return fmt.Sprintf("未找到名为 %q 的技能。", name), nil
	}
	bytes, err := sk.Bytes()
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
