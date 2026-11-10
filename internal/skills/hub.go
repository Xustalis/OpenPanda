// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// HubSkill is one entry in the Skills Hub catalog.
type HubSkill struct {
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description" yaml:"description"`
	Scope       Scope    `json:"scope,omitempty" yaml:"scope,omitempty"`
	Author      string   `json:"author,omitempty" yaml:"author,omitempty"`
	Version     string   `json:"version,omitempty" yaml:"version,omitempty"`
	Tags        []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	URL         string   `json:"url,omitempty" yaml:"url,omitempty"`
	Content     string   `json:"content,omitempty" yaml:"content,omitempty"`
	DocURL      string   `json:"doc_url,omitempty" yaml:"doc_url,omitempty"`
	Recommended bool     `json:"recommended,omitempty" yaml:"recommended,omitempty"`
	Alias       string   `json:"alias,omitempty" yaml:"alias,omitempty"`
	// Triggers are the curated bilingual retrieval keywords for this skill.
	// They live here, not inside Content, so the on-disk SKILL.md body stays a
	// plain procedure and the built-in trigger table has exactly one source.
	// Index() merges them into built-in entries that predate this field (or
	// that the user has edited), so no reseed is needed to pick them up.
	Triggers []string `json:"triggers,omitempty" yaml:"triggers,omitempty"`
	Always   bool     `json:"always,omitempty" yaml:"always,omitempty"`
}

// CuratedSkills contains built-in, production-grade procedural workflows available offline or by default.
var CuratedSkills = []HubSkill{
	{
		Name:        "git-workflow",
		Alias:       "git",
		Description: "Standard Git branching, commit conventions, rebase workflow and pull request verification",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.1.0",
		Tags:        []string{"git", "workflow", "vcs", "dev"},
		Triggers:    []string{"git", "commit", "branch", "rebase", "merge", "pull request", "提交", "分支", "合并", "变基", "代码提交", "拉取请求"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: git-workflow",
			"description: Standard Git branching, commit conventions, rebase workflow and pull request verification",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Standardized procedural workflow for code changes, commits, rebasing, and branch hygiene.",
			"",
			"## Steps",
			"1. Create a feature branch with descriptive name:",
			"   `git checkout -b feat/<topic>` or `git checkout -b fix/<topic>`",
			"2. Keep commits atomic and write conventional commit messages:",
			"   - `feat: add feature X`",
			"   - `fix: resolve bug Y`",
			"   - `docs: update documentation for Z`",
			"3. Before submitting or merging, rebase onto origin/main:",
			"   `git fetch origin && git rebase origin/main`",
			"4. Run full test gate before pushing:",
			"   `make gate` or `go test ./...`",
			"5. Push branch and track upstream:",
			"   `git push -u origin HEAD`",
			"",
		}, "\n"),
	},
	{
		Name:        "code-review",
		Alias:       "review",
		Description: "Comprehensive code quality, security check, error handling, and test coverage audit",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"review", "audit", "security", "qa"},
		Triggers:    []string{"code review", "review", "audit", "refactor", "代码审查", "评审", "复查", "检查代码", "代码质量"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: code-review",
			"description: Comprehensive code quality, security check, error handling, and test coverage audit",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Systematic code review checklist to detect anti-patterns, security flaws, and race conditions.",
			"",
			"## Review Checklist",
			"1. **Safety & Security**:",
			"   - Validate user inputs against path traversal ('..', absolute paths, slashes).",
			"   - Ensure secrets, tokens, and keys are not committed or logged in plaintext.",
			"   - Verify proper timeout and context cancellation on network calls and subprocesses.",
			"2. **Concurrency & Resource Management**:",
			"   - Check mutex locking/unlocking ordering and defer statements.",
			"   - Ensure goroutines terminate cleanly and channels do not deadlock.",
			"   - Run tests with `-race` enabled.",
			"3. **Error Handling & Logging**:",
			"   - Check that errors are wrapped with meaningful context (`fmt.Errorf(\"...: %w\", err)`).",
			"   - Ensure nil pointers and missing map keys are checked before dereferencing.",
			"4. **Testing & Verification**:",
			"   - Unit tests cover happy path and error/edge cases.",
			"   - Assertions provide clear failure diffs.",
			"",
		}, "\n"),
	},
	{
		Name:        "docker-compose",
		Alias:       "docker",
		Description: "Manage multi-container application stacks, health checks, environment configs and volume persistence",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"docker", "containers", "devops", "deploy"},
		Triggers:    []string{"docker", "compose", "container", "容器", "编排", "镜像", "本地环境"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: docker-compose",
			"description: Manage multi-container application stacks, health checks, environment configs and volume persistence",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Workflow for building, starting, validating, and tearing down containerized local environments.",
			"",
			"## Steps",
			"1. Verify Docker daemon is running:",
			"   `docker info > /dev/null 2>&1`",
			"2. Build or pull images with latest changes:",
			"   `docker compose build --pull`",
			"3. Start stack in detached mode:",
			"   `docker compose up -d`",
			"4. Check service status and healthchecks:",
			"   `docker compose ps`",
			"5. Inspect service logs on failure:",
			"   `docker compose logs -f --tail=100 <service>`",
			"6. Clean up resources when done:",
			"   `docker compose down -v`",
			"",
		}, "\n"),
	},
	{
		Name:        "unit-testing",
		Alias:       "test",
		Description: "Guidelines for writing robust table-driven unit tests, race detector verification, and benchmarks in Go",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"testing", "golang", "quality", "bench"},
		Triggers:    []string{"unit test", "test", "go test", "table-driven", "race", "单元测试", "测试用例", "覆盖率", "补测试", "跑测试"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: unit-testing",
			"description: Guidelines for writing robust table-driven unit tests, race detector verification, and benchmarks in Go",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Best practices for writing self-contained, repeatable, and concurrency-safe unit tests in Go.",
			"",
			"## Steps",
			"1. Structure test cases as table-driven structs with `name`, input, and expected output.",
			"2. Use `t.Run(tc.name, func(t *testing.T) { ... })` for clear isolation.",
			"3. Clean up temporary files with `t.TempDir()` rather than manual removals.",
			"4. Execute tests with race detection:",
			"   `go test -race -v ./...`",
			"5. Check test coverage:",
			"   `go test -cover ./...`",
			"",
		}, "\n"),
	},
	{
		Name:        "python-env",
		Alias:       "python",
		Description: "Python virtual environments, dependencies (pip/uv), linting (ruff), and pytest execution",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"python", "venv", "pip", "pytest"},
		Triggers:    []string{"python", "venv", "pip", "uv", "pytest", "ruff", "虚拟环境", "依赖安装", "包管理"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: python-env",
			"description: Python virtual environments, dependencies (pip/uv), linting (ruff), and pytest execution",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Procedures for creating isolated Python environments, installing dependencies, and running tests.",
			"",
			"## Steps",
			"1. Create and activate a virtual environment:",
			"   `python3 -m venv .venv && source .venv/bin/activate`",
			"2. Upgrade installer and install dependencies:",
			"   `pip install -U pip && pip install -r requirements.txt`",
			"3. Check code formatting and lint rules:",
			"   `ruff check .` or `flake8`",
			"4. Execute the test suite:",
			"   `pytest -v`",
			"",
		}, "\n"),
	},
	{
		Name:        "node-workflow",
		Alias:       "node",
		Description: "Node.js & npm/pnpm project setup, package management, build pipeline, and testing",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"node", "npm", "pnpm", "frontend"},
		Triggers:    []string{"node", "npm", "pnpm", "yarn", "typescript", "前端", "构建前端", "打包", "依赖安装"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: node-workflow",
			"description: Node.js & npm/pnpm project setup, package management, build pipeline, and testing",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Standard workflow for managing JavaScript/TypeScript packages and running build scripts.",
			"",
			"## Steps",
			"1. Install project dependencies:",
			"   `pnpm install` or `npm install`",
			"2. Run typechecking and static lint:",
			"   `npm run typecheck && npm run lint`",
			"3. Build production bundle:",
			"   `npm run build`",
			"4. Execute automated test suite:",
			"   `npm test`",
			"",
		}, "\n"),
	},
	{
		Name:        "api-debug",
		Alias:       "api",
		Description: "RESTful API testing, curl request inspection, headers and JSON payload verification",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"api", "curl", "http", "debug"},
		Triggers:    []string{"api", "endpoint", "curl", "http", "rest", "接口", "请求", "联调", "端点"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: api-debug",
			"description: RESTful API testing, curl request inspection, headers and JSON payload verification",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Procedures for rapidly querying, diagnosing, and inspecting HTTP API endpoints.",
			"",
			"## Steps",
			"1. Send test request with verbose headers:",
			"   `curl -iv -X GET <url>`",
			"2. Send JSON payload with Bearer authentication:",
			"   `curl -s -X POST -H 'Content-Type: application/json' -H 'Authorization: Bearer <token>' -d '{\"key\":\"val\"}' <url> | jq .`",
			"3. Verify HTTP response code and latency:",
			"   `curl -o /dev/null -s -w 'code: %{http_code} time: %{time_total}s\\n' <url>`",
			"",
		}, "\n"),
	},
	{
		Name:        "system-diagnostics",
		Alias:       "sys",
		Description: "Inspect host CPU, memory, disk usage, network ports, and running process health",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"system", "ops", "diagnostic", "monitor"},
		Triggers:    []string{"cpu", "memory", "disk", "port", "process", "内存", "磁盘", "端口", "进程", "性能", "诊断", "卡顿"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: system-diagnostics",
			"description: Inspect host CPU, memory, disk usage, network ports, and running process health",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Routine diagnostics to analyze system performance, port occupancy, and process health.",
			"",
			"## Steps",
			"1. Check disk utilization and inodes:",
			"   `df -h`",
			"2. Check memory consumption:",
			"   `vm_stat` (macOS) or `free -m` (Linux)",
			"3. List top memory and CPU consuming processes:",
			"   `ps aux --sort=-%mem | head -n 10`",
			"4. Check open listening ports:",
			"   `lsof -iTCP -sTCP:LISTEN -n -P`",
			"",
		}, "\n"),
	},
	{
		Name:        "root-cause-debugging",
		Alias:       "debug",
		Description: "Systematic root-cause analysis and regression isolation for failing code, builds and runtime behavior",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"debug", "diagnosis", "regression", "method"},
		Triggers:    []string{"debug", "troubleshoot", "root cause", "regression", "排查", "定位", "复现", "报错", "失败原因", "根因", "不对劲"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: root-cause-debugging",
			"description: Systematic root-cause analysis and regression isolation for failing code, builds and runtime behavior",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Find the cause of a failure before changing anything, so a fix addresses the mechanism rather than the symptom.",
			"",
			"## Steps",
			"1. Reproduce deterministically. Record the exact command, input, environment and observed output; an unreproduced failure is not yet diagnosable.",
			"2. Confirm the diagnostic surface and the execution surface are the same object: verify which binary, config file, working tree and endpoint the failing command actually used. A report that says a key is configured while injection is refused is a symptom, not a diagnosis.",
			"3. Minimize: remove inputs and steps until the smallest case still fails. Note the last change that made it fail (diff, dependency, config, platform).",
			"4. State 2-3 falsifiable hypotheses, then test them one at a time with the cheapest discriminating check. Do not apply fixes while still testing hypotheses.",
			"5. Fix the confirmed cause, then re-run the original reproducer and the project's verification gate. Add a regression test that fails on the old behavior.",
			"6. Reverse-verify: temporarily disable the fix (or set the guard to false) and confirm the test goes red. A test that passes either way proves nothing.",
			"",
			"## Avoid",
			"- Do not treat an environment-specific failure as a code regression; establish the baseline first by re-running and by checking another platform.",
			"- Do not refactor unrelated code while diagnosing.",
			"",
		}, "\n"),
	},
	{
		Name:        "ci-failure-triage",
		Alias:       "ci",
		Description: "Classify CI failures, separate flaky and environment-only reds from real regressions, and verify the right workflow",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"ci", "pipeline", "flake", "verification"},
		Triggers:    []string{"ci", "pipeline", "github actions", "flaky", "build failed", "持续集成", "流水线", "红灯", "构建失败", "门禁", "检查项"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: ci-failure-triage",
			"description: Classify CI failures, separate flaky and environment-only reds from real regressions, and verify the right workflow",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Decide whether a red check is a real regression before spending turns on a fix.",
			"",
			"## Steps",
			"1. Establish which checks actually ran on that commit: a green-looking check may be a skipped job, and workflows often have different triggers (pull request, push to main, tag, schedule, manual).",
			"2. Read the failing step's full log, not just the summary. Identify the first error, not the cascade after it.",
			"3. Classify into one of: real regression, known flake, environment-only failure (sandbox, port collision, missing platform capability), or infrastructure/network error.",
			"4. For a suspected flake, re-run the single job unchanged. A pass on the same SHA is evidence of a flake; record it rather than silently ignoring it.",
			"5. For a platform-specific failure, reproduce on that platform. Cross-platform behavior (certificate revocation, proxy resolution, path separators, file encoding) cannot be faked on another OS.",
			"6. Only then change code, and re-run the full gate. Never narrow a check to make it pass; fix the cause or document the exemption.",
			"",
		}, "\n"),
	},
	{
		Name:        "release-checklist",
		Alias:       "release",
		Description: "Version bump, gate verification, tag ordering, artifact checksums, changelog and rollback for a release",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"release", "version", "tag", "delivery"},
		Triggers:    []string{"release", "tag", "version", "changelog", "发布", "发版", "打标签", "版本号", "更新日志", "上线"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: release-checklist",
			"description: Version bump, gate verification, tag ordering, artifact checksums, changelog and rollback for a release",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Ship a reproducible release without a version literal drifting between documents, packages and download links.",
			"",
			"## Steps",
			"1. Enumerate every version literal (docs, download page, package manifests, sample configs) and update them together with the changelog section.",
			"2. Push the release commit to the mainline and wait for its full gate on that exact SHA to go green. Do not tag from a branch whose CI did not run.",
			"3. Only after the gate is green, create the tag. Build and cross-compile all targets, record checksums and verify signatures.",
			"4. Publish artifacts, then verify a fresh install path end-to-end: download, checksum verify, run the version command, and confirm the reported revision matches the tag.",
			"5. Expect CDN warming: the first few artifact requests may fail transiently. Retry before declaring the release broken.",
			"6. Prepare a rollback: name the previous tag, the revert path for version literals, and who can take the download links down.",
			"",
		}, "\n"),
	},
	{
		Name:        "dependency-upgrade",
		Alias:       "deps",
		Description: "Safe dependency and toolchain upgrades, lockfile semantics, breaking changes and CI parity checks",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"dependency", "lockfile", "toolchain", "upgrade"},
		Triggers:    []string{"dependency", "dependabot", "upgrade", "lockfile", "go.mod", "依赖", "升级", "锁文件", "工具链", "安全补丁"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: dependency-upgrade",
			"description: Safe dependency and toolchain upgrades, lockfile semantics, breaking changes and CI parity checks",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Upgrade a dependency without silently changing what CI runs or leaving the lockfile out of sync.",
			"",
			"## Steps",
			"1. Read the upstream changelog and classify the change (patch / minor / major). For a major, read its action or API definition rather than assuming the old contract.",
			"2. Check the toolchain pin: the language version declared in the manifest is often also the CI toolchain version. A stdlib security advisory may require raising the pin, not patching code.",
			"3. Update the manifest and the lockfile together, then run the dependency tidy/verify command and confirm the diff is limited to intended packages.",
			"4. Verify against the workflow that will actually run the new version. A step that only runs on push or tag cannot be validated by a pull request check; trigger it manually if needed.",
			"5. Run the full local gate, including formatting and cross-target builds, before pushing.",
			"6. Record the rollback point (previous manifest, lockfile and toolchain pin) in the change description.",
			"",
		}, "\n"),
	},
	{
		Name:        "schema-migration",
		Alias:       "migration",
		Description: "Backward-compatible schema and config migrations with backup, version checks and a rehearsed rollback",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"schema", "database", "migration", "compatibility"},
		Triggers:    []string{"migration", "schema", "database", "sqlite", "backward compatible", "迁移", "数据库", "表结构", "兼容性", "回滚", "版本号"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: schema-migration",
			"description: Backward-compatible schema and config migrations with backup, version checks and a rehearsed rollback",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Change persisted structure without making an older reader unable to open the data.",
			"",
			"## Steps",
			"1. Back up the live store before any write and confirm the backup is readable.",
			"2. Make each migration idempotent and ordered by an explicit version. Never let a development branch raise the on-disk version above the highest version the released binary understands.",
			"3. Keep a compatibility window: old readers must tolerate the new shape, or the migration must be gated behind a coordinated rollout.",
			"4. Verify the version and a representative read/write after migrating, using the same command a user would run.",
			"5. Rehearse rollback on a copy: restore the backup, confirm the old binary reads it, and note that re-running the newer binary will push the version forward again.",
			"6. Add a test that pins the highest supported version and fails when a migration is added without raising it.",
			"",
		}, "\n"),
	},
	{
		Name:        "frontend-verification",
		Alias:       "browser",
		Description: "Verify a rendered web page in a headless browser with console, network, layout, screenshot and localization checks",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"frontend", "browser", "rendering", "qa"},
		Triggers:    []string{"frontend", "browser", "web page", "render", "screenshot", "页面", "前端", "渲染", "无头浏览器", "截图", "网页"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: frontend-verification",
			"description: Verify a rendered web page in a headless browser with console, network, layout, screenshot and localization checks",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"A passing build is not a working page. Verify the artifact the user will actually see.",
			"",
			"## Steps",
			"1. Build the production bundle, serve it, and open the page in the node's headless browser.",
			"2. Inspect the browser console and the network panel: zero uncaught errors and zero failed requests are part of done, not a bonus.",
			"3. Take a screenshot and check layout at the intended viewport; check at least one narrow viewport if the page is responsive.",
			"4. Exercise the primary interaction (submit, navigate, toggle) and re-check console and network afterwards.",
			"5. For localized pages, switch every supported locale and look for raw translation keys, untranslated strings and CJK characters leaking into non-CJK files.",
			"6. Report the commands run, the console/network result, and attach or reference the screenshot alongside the build result.",
			"",
		}, "\n"),
	},
}

// HubIndex is the top-level repository index for a Skills Hub.
type HubIndex struct {
	Version string     `json:"version" yaml:"version"`
	Name    string     `json:"name,omitempty" yaml:"name,omitempty"`
	Skills  []HubSkill `json:"skills" yaml:"skills"`
}

// RecommendedSkills returns the subset of skills designated as recommended presets.
func RecommendedSkills() []HubSkill {
	var list []HubSkill
	for _, s := range CuratedSkills {
		if s.Recommended {
			list = append(list, s)
		}
	}
	return list
}

// ResolveSkillAlias maps common shorthand aliases (e.g. "git", "docker", "test", "python")
// or 1-based numeric indices ("1", "2") to canonical skill names.
func ResolveSkillAlias(name string) string {
	raw := strings.TrimSpace(name)
	lower := strings.ToLower(raw)

	// Check 1-based index (e.g. "1" -> first recommended skill)
	rec := RecommendedSkills()
	if num, err := strconv.Atoi(lower); err == nil && num >= 1 && num <= len(rec) {
		return rec[num-1].Name
	}

	for _, s := range CuratedSkills {
		if strings.EqualFold(s.Name, raw) || (s.Alias != "" && strings.EqualFold(s.Alias, raw)) {
			return s.Name
		}
	}

	switch lower {
	case "git":
		return "git-workflow"
	case "review":
		return "code-review"
	case "docker":
		return "docker-compose"
	case "test", "tests":
		return "unit-testing"
	case "python", "py":
		return "python-env"
	case "node", "npm", "pnpm":
		return "node-workflow"
	case "api", "curl":
		return "api-debug"
	case "system", "sys", "diag":
		return "system-diagnostics"
	}

	return raw
}

// FetchHubIndex retrieves the skill index from hubURL. If hubURL is empty or remote fetch fails,
// it gracefully returns the curated catalog.
func FetchHubIndex(ctx context.Context, hubURL string) (*HubIndex, error) {
	hubURL = strings.TrimSpace(hubURL)
	if hubURL == "" {
		return &HubIndex{
			Version: "1.0",
			Name:    "OpenPanda Curated Skills Hub",
			Skills:  CuratedSkills,
		}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL, nil)
	if err != nil {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}
	req.Header.Set("User-Agent", "OpenPanda-Skills-Hub/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes))
	if err != nil {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}

	var idx HubIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}

	// Merge curated skills that are not already present in remote index
	existing := make(map[string]bool)
	for _, s := range idx.Skills {
		existing[s.Name] = true
	}
	for _, s := range CuratedSkills {
		if !existing[s.Name] {
			idx.Skills = append(idx.Skills, s)
		}
	}

	return &idx, nil
}

// SearchHub filters skills in index by a search query (matching name, description, tags, author, alias).
// SearchHub ranks catalog entries by relevance to query. The old behaviour
// required every whitespace token to be a substring of one concatenated
// string and returned catalog order, so a multi-word need either matched
// nothing or made DiscoverAndInstall install whichever skill happened to be
// listed first. Scoring is lexical (whole Latin words, individual Han
// ideographs) with name/alias/tag/trigger boosts; only positive scores are
// returned, so a completely unrelated query still yields no match.
func SearchHub(index *HubIndex, query string) []HubSkill {
	if index == nil {
		return nil
	}
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return index.Skills
	}
	qTokens := util.Tokenize(query)
	type scored struct {
		skill HubSkill
		score int
		order int
	}
	var ranked []scored
	for i, sk := range index.Skills {
		if s := hubSearchScore(sk, query, qTokens); s > 0 {
			ranked = append(ranked, scored{sk, s, i})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].order < ranked[j].order
	})
	out := make([]HubSkill, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.skill)
	}
	return out
}

// hubSearchScore weights a catalog entry's fields against one query. Explicit
// triggers and tags outrank free-text description overlap, and an exact
// substring hit on the whole query (common with Chinese phrases) is a strong
// signal. Recommended entries get a single tie-break point, not a ranking
// override.
func hubSearchScore(sk HubSkill, query string, qTokens map[string]struct{}) int {
	name := strings.ToLower(sk.Name)
	alias := strings.ToLower(sk.Alias)
	desc := strings.ToLower(sk.Description)
	tags := strings.ToLower(strings.Join(sk.Tags, " "))
	triggers := strings.ToLower(strings.Join(sk.Triggers, " "))
	author := strings.ToLower(sk.Author)
	all := strings.ToLower(strings.Join([]string{name, alias, desc, tags, triggers, author}, " "))
	allTokens := util.Tokenize(all)

	score := 0
	if containsHan(query) && strings.Contains(all, query) {
		score += 10
	}
	for t := range qTokens {
		if util.IsStopword(t) {
			continue
		}
		_, whole := allTokens[t]
		if !whole && !(containsHan(t) && strings.Contains(all, t)) {
			continue
		}
		score++
		if strings.Contains(name, t) {
			score += 5
		}
		if alias != "" && strings.Contains(alias, t) {
			score += 4
		}
		if strings.Contains(tags, t) {
			score += 4
		}
		if strings.Contains(triggers, t) {
			score += 5
		}
	}
	if score > 0 && sk.Recommended {
		score++
	}
	return score
}

// InstallFromHub finds a skill in the hub (resolving aliases and numeric IDs) and installs it.
func InstallFromHub(ctx context.Context, store *Store, hubURL string, name string, opts ImportOptions) (*Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("skills: skill name cannot be empty")
	}

	resolvedName := ResolveSkillAlias(name)

	index, err := FetchHubIndex(ctx, hubURL)
	if err != nil {
		return nil, fmt.Errorf("skills: fetch hub index: %w", err)
	}

	var target *HubSkill
	for i := range index.Skills {
		if strings.EqualFold(index.Skills[i].Name, resolvedName) ||
			(index.Skills[i].Alias != "" && strings.EqualFold(index.Skills[i].Alias, name)) {
			target = &index.Skills[i]
			break
		}
	}

	if target == nil {
		return nil, fmt.Errorf("skills: skill %q not found in hub", name)
	}

	if opts.Scope == "" && target.Scope != "" {
		opts.Scope = target.Scope
	}

	if strings.TrimSpace(target.Content) != "" {
		return store.ImportBytes([]byte(target.Content), opts)
	}

	if strings.TrimSpace(target.URL) != "" {
		skills, err := store.ImportURL(ctx, target.URL, opts)
		if err != nil {
			return nil, err
		}
		if len(skills) == 0 {
			return nil, fmt.Errorf("skills: no skills imported from %s", target.URL)
		}
		return skills[0], nil
	}

	return nil, fmt.Errorf("skills: hub skill %q has neither content nor URL", name)
}

// InstallAllRecommended installs all recommended presets in one batch.
func InstallAllRecommended(ctx context.Context, store *Store, hubURL string, opts ImportOptions) ([]*Skill, error) {
	rec := RecommendedSkills()
	var installed []*Skill
	for _, item := range rec {
		sk, err := InstallFromHub(ctx, store, hubURL, item.Name, opts)
		if err != nil {
			// If already exists and not forced, continue
			if !opts.Force && strings.Contains(err.Error(), "already exists") {
				continue
			}
			return installed, err
		}
		installed = append(installed, sk)
	}
	return installed, nil
}

// BuiltinTriggers returns the curated retrieval keywords and the always-on
// flag for a built-in skill (by canonical name or alias). Index() uses it to
// upgrade entries that were seeded before triggers existed, without rewriting
// the user's on-disk SKILL.md — the curated table stays the single source.
func BuiltinTriggers(name string) ([]string, bool) {
	clean := strings.ToLower(strings.TrimSpace(name))
	for _, c := range CuratedSkills {
		if strings.EqualFold(c.Name, clean) || (c.Alias != "" && strings.EqualFold(c.Alias, clean)) {
			return append([]string(nil), c.Triggers...), c.Always
		}
	}
	return nil, false
}

// IsBuiltinSkill reports whether name (or its alias) matches a standard built-in skill.
func IsBuiltinSkill(name string) bool {
	clean := strings.ToLower(strings.TrimSpace(name))
	for _, c := range CuratedSkills {
		if strings.EqualFold(c.Name, clean) || (c.Alias != "" && strings.EqualFold(c.Alias, clean)) {
			return true
		}
	}
	return false
}

// BuiltinSkills returns all standard built-in skills defined in the system.
func BuiltinSkills() []HubSkill {
	return CuratedSkills
}

// EnsureBuiltins seeds all standard built-in skills into the store with global
// scope and active status if they are not already present on disk.
// If a skill is already present, it is left untouched to respect user modifications.
func (s *Store) EnsureBuiltins() error {
	for _, cur := range CuratedSkills {
		existing, err := s.Load(ScopeGlobal, "", cur.Name)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		sk, err := ParseSkill([]byte(cur.Content))
		if err != nil {
			// A broken factory template used to be skipped silently: the
			// built-in simply never appeared in the index and the only symptom
			// was a missing skill. Fail loudly instead.
			return fmt.Errorf("skills: builtin %q template: %w", cur.Name, err)
		}
		sk.Builtin = true
		sk.Status = StatusActive
		if err := s.Save(sk); err != nil {
			return err
		}
	}
	return nil
}

// ResetBuiltin restores a built-in skill (by name or alias) to its original factory definition.
func (s *Store) ResetBuiltin(name string) (*Skill, error) {
	clean := strings.ToLower(strings.TrimSpace(name))
	for _, cur := range CuratedSkills {
		if strings.EqualFold(cur.Name, clean) || (cur.Alias != "" && strings.EqualFold(cur.Alias, clean)) {
			sk, err := ParseSkill([]byte(cur.Content))
			if err != nil {
				return nil, err
			}
			sk.Builtin = true
			sk.Status = StatusActive
			if err := s.Save(sk); err != nil {
				return nil, err
			}
			return sk, nil
		}
	}
	return nil, fmt.Errorf("skills: %q is not a built-in skill", name)
}

// DiscoverAndInstall searches the Skills Hub for skills matching query, picks the top
// match, and installs it if not already present. It returns the skill and a boolean
// indicating whether it was newly installed (true) or was already present (false).
// The caller picks the landing status through opts: user-initiated installs pass
// StatusActive (the explicit click/command is the approval), while autonomous
// installs (model tools, MCP self-tools) must pass StatusPending so the skill
// cannot steer future tasks until a human approves it in the foreground.
func (s *Store) DiscoverAndInstall(ctx context.Context, hubURL string, query string, opts ImportOptions) (*Skill, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, false, fmt.Errorf("skills: search query cannot be empty")
	}

	idx, err := FetchHubIndex(ctx, hubURL)
	if err != nil {
		return nil, false, fmt.Errorf("skills: fetch hub index: %w", err)
	}

	results := SearchHub(idx, query)
	if len(results) == 0 {
		return nil, false, fmt.Errorf("skills: no skills matching %q found in hub", query)
	}

	top := results[0]
	existing, err := s.Load(ScopeGlobal, "", top.Name)
	if err == nil && existing != nil {
		return existing, false, nil
	}

	if opts.Scope == "" {
		opts.Scope = ScopeGlobal
	}
	installed, err := InstallFromHub(ctx, s, hubURL, top.Name, opts)
	if err != nil {
		return nil, false, err
	}
	return installed, true, nil
}
