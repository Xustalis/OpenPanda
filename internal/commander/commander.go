// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/pyexec"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// Router matches a task's required abilities against a node's capability
// card and produces an execution plan (native command / agent adapter /
// manual notification).
type Router struct {
	card     ledger.Card
	executor *Executor
	model    config.ModelConfig
	// injectionModel is the normalized injection.model strategy
	// (auto | always | never). Zero value means auto.
	injectionModel string
	// injectionAgents pins a per-agent model for injection only
	// (injection.agents). Empty falls back to model for every agent.
	injectionAgents map[string]config.ModelConfig
	// toolsPolicy is the normalized routing.tools_policy strategy
	// (minimal | extended) the adapters run under. Zero value means minimal.
	toolsPolicy string
	// mcpCommand is the configured stdio MCP server (mcp.command, argv
	// string). Under the extended tools policy it reaches agents that
	// declare an MCP surface: flag-capable CLIs get it on the adapter
	// request's mcp_config field, project-discovery CLIs get a
	// materialized .mcp.json in the work dir for the run. Empty = none.
	mcpCommand string
	// pandaTools gates the openpanda self-management server on the same
	// passthrough (routing.panda_tools, default on). It only ever applies
	// under the extended tools policy — minimal-policy runs get neither
	// server.
	pandaTools bool
	// selfConfigPath is forwarded to `panda mcp --config` inside the
	// passthrough document so the self-tools server resolves the same
	// config the daemon loaded. Empty = the server's own default discovery.
	selfConfigPath string
	// preferred lists agent names that receive a score bonus during routing.
	preferred []string
	// agentModels lists, per agent, that agent's own model preferences in
	// order (routing.agent_models). It is passed to the adapter as an ordered
	// candidate list; a CLI that picks its own model consults it, and the
	// adapter takes the first entry it serves.
	agentModels map[string][]string
	// allowedAgents, when non-empty, is the only agent pool routing may pick
	// from — a chat thread's team roster (see WithAgents). Empty is the
	// ordinary case: every agent on the card is a candidate.
	allowedAgents []string
	// probeAgent reports whether an agent's CLI is usable on this machine.
	// Injectable for tests; production runs AgentDispatchable (static
	// viability plus the provider-endpoint reachability probe).
	probeAgent func(name string, ag ledger.Agent) bool
	// endpointProbe reports whether the resolved model endpoint answers a
	// minimal request within the probe budget. Injectable for tests;
	// production is modelProbe.
	endpointProbe func(spec ProbeSpec) ProbeVerdict
	// runAdapter is injectable for tests; production uses RunAgent.
	runAdapter func(ctx context.Context, adapter string, prompt string, cwd string) AgentResult
	// runProcess runs the adapter process with injected environment.
	runProcess func(ctx context.Context, adapter string, prompt string, cwd string, env []string) AgentResult
	// agentBlocked reports whether the node's agent circuit breaker currently
	// has this agent open (defense.CircuitBreaker.Blocked). Injected by the
	// owner of the breaker — the core — so ranking demotes an agent the node
	// already knows is failing without maintaining a second record. Nil means
	// "no breaker here" and never demotes.
	agentBlocked func(name string) bool
}

// NewRouter builds a router from this node's capability card. The model
// config is injected into agent adapter subprocesses only as the injection
// policy allows (default auto: agent-native credentials win); routing honors
// the preferred-agents bonus and falls back across scored candidates.
func NewRouter(card ledger.Card, executor *Executor, model config.ModelConfig, injection config.InjectionConfig, routing config.RoutingConfig) *Router {
	r := &Router{
		card:            card,
		executor:        executor,
		model:           model,
		injectionModel:  injection.NormalizedModel(),
		injectionAgents: injection.Agents,
		toolsPolicy:     routing.NormalizedToolsPolicy(),
		pandaTools:      routing.NormalizedPandaTools(),
		preferred:       routing.PreferredAgents,
		agentModels:     routing.AgentModels,
	}
	r.runAdapter = r.runAdapterDefault
	r.runProcess = runAdapterProcess
	r.endpointProbe = modelProbe
	// The production probe is credential- AND connectivity-aware
	// (AgentDispatchable), not just a PATH check: routing to an
	// installed-but-locked-out CLI, or one whose model endpoint is
	// unreachable, guarantees a runtime failure after a long hang.
	r.probeAgent = r.AgentDispatchable
	return r
}

// SetPolicy swaps the injection/routing policy after construction (the core
// builds its router before the full config is wired through).
func (r *Router) SetPolicy(injection config.InjectionConfig, routing config.RoutingConfig) {
	r.injectionModel = injection.NormalizedModel()
	r.injectionAgents = injection.Agents
	r.toolsPolicy = routing.NormalizedToolsPolicy()
	r.pandaTools = routing.NormalizedPandaTools()
	r.preferred = routing.PreferredAgents
	r.agentModels = routing.AgentModels
}

// agentModelPrefs resolves an agent's ordered model preference list by its
// card name first, then by adapter file name — the same lookup order
// injection.agents uses, so a renamed card entry still finds its list.
func (r *Router) agentModelPrefs(name, adapter string) []string {
	if len(r.agentModels) == 0 {
		return nil
	}
	if prefs, ok := r.agentModels[name]; ok && len(prefs) > 0 {
		return prefs
	}
	if prefs, ok := r.agentModels[adapter]; ok && len(prefs) > 0 {
		return prefs
	}
	return nil
}

// SetMCPPassthrough sets the stdio MCP server (argv string, empty disables)
// that extended-policy agent runs expose — on the adapter request for
// flag-capable CLIs, via a work-dir .mcp.json for discovery-only CLIs. The
// ask engine's own MCP client is independent of this; the passthrough only
// concerns what the delegated agent CLIs may discover.
func (r *Router) SetMCPPassthrough(command string) {
	r.mcpCommand = strings.TrimSpace(command)
}

// SetSelfConfigPath records the daemon's --config path so the passthrough
// document can hand it to `panda mcp`. Empty means the self-tools server
// falls back to the same default discovery every CLI command uses.
func (r *Router) SetSelfConfigPath(path string) {
	r.selfConfigPath = strings.TrimSpace(path)
}

// ToolsPolicy reports the normalized agent tools policy the router runs
// adapters under (config routing.tools_policy).
func (r *Router) ToolsPolicy() string { return r.toolsPolicy }

// PandaTools reports whether the openpanda self-management server is
// enabled (routing.panda_tools). Still gated on the extended tools policy
// by the caller.
func (r *Router) PandaTools() bool { return r.pandaTools }

// SetAdapterRunner overrides the agent adapter invocation. It is a test seam:
// suites that need to exercise agent execution without spawning a real LLM CLI
// (e.g. scope-drift interception in core) inject a fake here.
func (r *Router) SetAdapterRunner(fn func(ctx context.Context, adapter, prompt, cwd string) AgentResult) {
	r.runAdapter = fn
}

// SetAgentProber overrides the agent availability probe. Test seam: suites
// with a fake adapter runner normally pair it with an always-available probe.
func (r *Router) SetAgentProber(fn func(name string, ag ledger.Agent) bool) {
	r.probeAgent = fn
}

// SetAgentBlocked injects the agent circuit-breaker predicate: it reports
// whether the node's breaker currently holds this agent open. Ranking demotes
// such an agent (never excludes it). The core owns the breaker and wires this
// at construction; a router without it ranks exactly as before.
func (r *Router) SetAgentBlocked(fn func(name string) bool) {
	r.agentBlocked = fn
}

// SetEndpointProber overrides the model-endpoint reachability probe. Test
// seam: unit tests must never depend on real network state.
func (r *Router) SetEndpointProber(fn func(spec ProbeSpec) ProbeVerdict) {
	r.endpointProbe = fn
}

// Card returns the router's underlying capability card.
func (r *Router) Card() ledger.Card { return r.card }

// WithAgents returns a copy of the router whose agent pool is limited to
// names — a chat thread's roster, chosen in the composer. Native commands and
// actuators are deliberately untouched: the roster answers "which agent may
// take this work", not "may this declared command run".
//
// Names this node does not carry are ignored, and when none of them are
// present the router comes back unchanged. That is the honest degradation: a
// roster of employees who work on another machine must not strand the task
// here with "no route" when this node has hands of its own.
func (r *Router) WithAgents(names []string) *Router {
	if len(names) == 0 {
		return r
	}
	agents := make(map[string]ledger.Agent, len(names))
	present := make([]string, 0, len(names))
	for _, n := range names {
		if ag, ok := r.card.Agents[n]; ok {
			if _, dup := agents[n]; dup {
				continue
			}
			agents[n] = ag
			present = append(present, n)
		}
	}
	if len(present) == 0 {
		return r
	}
	card := r.card
	card.Agents = agents
	clone := *r
	clone.card = card
	clone.allowedAgents = present
	return &clone
}

// agentAllowed reports whether name is inside the roster (always true when the
// router carries no roster).
func (r *Router) agentAllowed(name string) bool {
	if len(r.allowedAgents) == 0 {
		return true
	}
	for _, n := range r.allowedAgents {
		if n == name {
			return true
		}
	}
	return false
}

// Agent returns the agent definition by name, or ok=false.
func (r *Router) Agent(name string) (ledger.Agent, bool) {
	ag, ok := r.card.Agents[name]
	return ag, ok
}

// Plan describes how to execute a task on this node.
type Plan struct {
	Kind    string // native | agent | manual
	Ability string
	Command string
	Args    []string
	Tier    int // native command tier (defense §16)
	Agent   string
	Adapter string
	Notify  string
	// Alternates lists the remaining matching agents in score order. When
	// the primary agent's CLI is unavailable at execution time, execAgent
	// falls back through this chain.
	Alternates []string
	// ActuatorID is set when the plan resolves to a card actuator (§7.2):
	// the command is that actuator's driver invocation, and the run path
	// substitutes its {intent}/{action}/{param:<name>} placeholders from the
	// task's action_spec before execution. Empty for ordinary plans.
	ActuatorID string
}

// Match finds the first native ability whose id matches any of required.
func (r *Router) MatchNative(required []string) (ledger.NativeAbility, bool) {
	for _, req := range required {
		for _, ab := range r.card.Native {
			if ledger.AbilityMatches(ab.ID, req) {
				return ab, true
			}
		}
	}
	return ledger.NativeAbility{}, false
}

// MatchActuator finds a card actuator whose id satisfies any of required
// (§7.1). Actuators live on the card's own list — not in card.Native — so
// they need their own matcher; the ability namespace is shared, which is
// what makes a required "hardware:gpio_servo" route here at all.
func (r *Router) MatchActuator(required []string) (ledger.ActuatorProfile, bool) {
	for _, req := range required {
		for _, act := range r.card.Actuators {
			if ledger.AbilityMatches(act.ID, req) {
				return act, true
			}
		}
	}
	return ledger.ActuatorProfile{}, false
}

// preferredBonus is the score bonus an agent listed in
// routing.preferred_agents receives during ranking.
const preferredBonus = 0.5

// agentBlockedPenalty multiplies the rank score of an agent whose circuit the
// node's breaker holds open. The cheapest healthy agent scores 1.0 and the
// most expensive 0.6, so 0.5 lets any healthy candidate win the next dispatch
// while a blocked agent still outranks no one when it is the only match.
const agentBlockedPenalty = 0.5

// allowedFloor is the capability score a roster member gets when it declares
// none of a task's required abilities. Below every real match (0.9/1.0) so a
// capable teammate still wins, above zero so the roster can always answer.
const allowedFloor = 0.5

// retryBackoff is the wait before a single retry on a transient provider
// failure (rate limit / 5xx). Deliberately short: these resolve in seconds.
const retryBackoff = 3 * time.Second

// AgentCandidate is one scored agent match produced by RankAgents.
type AgentCandidate struct {
	Name  string
	Agent ledger.Agent
	Score float64
}

// costMultiplier converts a card's cost_tier label into a routing discount:
// cheaper agents score higher. An unknown/missing tier gets the middle-of-the
// road factor so undeclared agents neither win nor lose on cost alone.
func costMultiplier(tier string) float64 {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "low":
		return 1.0
	case "low_medium":
		return 0.9
	case "medium":
		return 0.8
	case "medium_high":
		return 0.7
	case "high":
		return 0.6
	default:
		return 0.8
	}
}

// PinnedAgentName reports whether req is an "agent:<name>" requirement and, if
// so, the registry key it names on this router's card. known is false when the
// pin names no agent the card carries — an uninstalled CLI or one this node
// never registered — so a caller can tell "not a pin" from "a pin this node
// cannot honour" instead of treating both as "no opinion".
func (r *Router) PinnedAgentName(req string) (name string, pinned, known bool) {
	if _, ok := strings.CutPrefix(req, "agent:"); !ok {
		return "", false, false
	}
	name, _, known = r.resolveAgentName(req)
	return name, true, known
}

// RosterOnCard returns the entries of names this node's capability card
// actually carries. A thread's team can legitimately name agents that only a
// peer has (the pick is mesh-wide), so an executor needs to know whether the
// team it was handed can be staffed HERE before it routes: WithAgents degrades
// to the unrestricted card when none of the names resolve, which silently
// re-staffs a task the user scoped to agents this machine does not have.
func (r *Router) RosterOnCard(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := r.card.Agents[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// resolveAgentName resolves an agent requirement (e.g. "agent:claude_code", "claude",
// "claude-code", "grok") to the registered agent on the card, accounting for
// name variations and CLI binary aliases.
func (r *Router) resolveAgentName(req string) (string, ledger.Agent, bool) {
	name := req
	if stripped, ok := strings.CutPrefix(req, "agent:"); ok {
		name = stripped
	}
	if ag, exists := r.card.Agents[name]; exists {
		return name, ag, true
	}
	norm := strings.ReplaceAll(strings.ToLower(name), "-", "_")
	if ag, exists := r.card.Agents[norm]; exists {
		return norm, ag, true
	}
	if norm == "claude" {
		if ag, exists := r.card.Agents["claude_code"]; exists {
			return "claude_code", ag, true
		}
	}
	for agentName, ag := range r.card.Agents {
		if k, ok := agents.ByAdapter(ag.Adapter); ok {
			if strings.EqualFold(k.Name, name) || strings.EqualFold(k.DisplayName, name) {
				return agentName, ag, true
			}
			for _, b := range k.Binaries {
				if strings.EqualFold(b, name) {
					return agentName, ag, true
				}
			}
		}
	}
	return "", ledger.Agent{}, false
}

// RankAgents scores every agent that satisfies any of required and returns
// the candidates sorted by descending score (ties broken by name, so the
// choice stays deterministic across map iteration orders).
//
// Score = capability match (1.0 exact / 0.9 token-subset) × cost_tier
// discount + preferred-agents bonus, halved while the agent sits in a
// transient-failure cooldown (agenthealth.go). A requirement of the form
// "agent:<name>" pins that exact agent — an explicit user choice never fans
// out to substitutes.
//
// A roster (WithAgents) filters the pool before any of that: an agent outside
// the thread's team is not a candidate at all, including one named by an
// "agent:<name>" pin the model emitted. Inside the roster, an agent that
// declares none of the required abilities still scores allowedFloor — the
// team is an instruction, so the smallest roster of one employee must run the
// work rather than fail to route.
func (r *Router) RankAgents(required []string) []AgentCandidate {
	for _, req := range required {
		if name, ag, ok := r.resolveAgentName(req); ok && r.agentAllowed(name) {
			return []AgentCandidate{{Name: name, Agent: ag, Score: 1 + r.bonus(name)}}
		}
	}

	preferred := make(map[string]bool, len(r.preferred))
	for _, n := range r.preferred {
		preferred[n] = true
	}

	var out []AgentCandidate
	names := make([]string, 0, len(r.card.Agents))
	for name := range r.card.Agents {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ag := r.card.Agents[name]
		best := 0.0
		for _, req := range required {
			for _, cap := range ag.Capabilities {
				if cap == req {
					if best < 1.0 {
						best = 1.0
					}
				} else if ledger.AbilityMatches(cap, req) && best < 0.9 {
					best = 0.9
				}
			}
		}
		if best == 0 {
			// A roster member with no declared match still gets a floor
			// score; without it a one-person team could not take a task
			// whose abilities that person's card does not spell out.
			if len(r.allowedAgents) == 0 {
				continue
			}
			best = allowedFloor
		}
		score := best*costMultiplier(ag.CostTier) + r.bonus(name)
		// An agent the node's breaker holds open keeps its capability rank but
		// loses to every healthy candidate: the next dispatch should not start
		// on the agent the last three failures already ruled out. Still a
		// demotion, never an exclusion — core/run.go's spawn gate makes the
		// final call, and a lone capable agent must remain routable.
		if r.agentBlocked != nil && r.agentBlocked(name) {
			score *= agentBlockedPenalty
		}
		out = append(out, AgentCandidate{Name: name, Agent: ag, Score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// bonus returns the preferred-agents score bonus for name.
func (r *Router) bonus(name string) float64 {
	for _, p := range r.preferred {
		if p == name {
			return preferredBonus
		}
	}
	return 0
}

// MatchAgent finds the best-scoring configured agent that satisfies any of
// required — either by name ("agent:<name>", the form the device summary
// advertises) or by one of its declared capabilities. Scoring is
// capability match × cost_tier discount plus the preferred-agents bonus
// (see RankAgents); the deterministic tie-break keeps repeated runs stable.
func (r *Router) MatchAgent(required []string) (string, ledger.Agent, bool) {
	cands := r.RankAgents(required)
	if len(cands) == 0 {
		return "", ledger.Agent{}, false
	}
	return cands[0].Name, cands[0].Agent, true
}

// MatchManual finds a manual ability whose id matches any of required.
func (r *Router) MatchManual(required []string) (ledger.ManualAbility, bool) {
	for _, req := range required {
		for _, ab := range r.card.Manual {
			if ledger.AbilityMatches(ab.ID, req) {
				return ab, true
			}
		}
	}
	return ledger.ManualAbility{}, false
}

// Route picks the best way to execute required capabilities on this node.
// Priority: native > agent > manual (design doc §6.4) — except an explicit
// `agent:<name>` pin: a bare native or actuator command can never satisfy a
// demand for an agent run, so letting MatchNative win on a sibling
// capability token silently drops the pin. requires ["agent:x","git"] used
// to run `git status` and report the task done without the agent ever
// starting.
func (r *Router) Route(required []string) (Plan, error) {
	agentPinned := false
	for _, req := range required {
		if strings.HasPrefix(req, "agent:") {
			agentPinned = true
			break
		}
	}
	if ab, ok := r.MatchNative(required); ok && !agentPinned {
		// A card's declared tier may only tighten the command's own
		// classification, never loosen it. Taking the declaration outright
		// meant a card reading `tier: 1` made `rm -rf` reversible — and since
		// cards are writable through the entry model, the gate was only ever as
		// strong as the thing it guards. TierFromCommand (which fails closed on
		// destructive forms and stays Tier 1 otherwise) is the floor; a
		// declaration still wins when it is *more* cautious than the command.
		tier := defense.TierFromCommand(ab.Command, ab.Args...)
		if ab.Tier > tier {
			tier = ab.Tier
		}
		return Plan{Kind: "native", Ability: ab.ID, Command: ab.Command, Args: ab.Args, Tier: tier}, nil
	}
	// §7.1/§7.2: an actuator with a declared driver command executes through
	// the same native path — its args are ActionSpec templates the run path
	// substitutes before exec. One without a command stays a routing
	// advertisement only and falls through to the agent tier, which scripts
	// the hardware itself. The agent-pin guard above applies here too.
	if act, ok := r.MatchActuator(required); ok && act.Command != "" && !agentPinned {
		tier := defense.TierFromCommand(act.Command, act.Args...)
		if act.Tier > tier {
			tier = act.Tier
		}
		return Plan{Kind: "native", Ability: act.ID, Command: act.Command,
			Args: act.Args, Tier: tier, ActuatorID: act.ID}, nil
	}
	cands := r.RankAgents(required)
	if len(cands) == 0 && len(required) == 0 {
		cands = r.RankAgents([]string{"coding", "shell", "file_edit"})
		if len(cands) == 0 && len(r.card.Agents) > 0 {
			for name, ag := range r.card.Agents {
				cands = append(cands, AgentCandidate{Name: name, Agent: ag, Score: 1.0})
			}
			sort.Slice(cands, func(i, j int) bool { return cands[i].Name < cands[j].Name })
		}
	}
	if len(cands) > 0 {
		top := cands[0]
		// Agent plans carry a tier like native ones, and an undeclared tier now
		// defaults to 1 — delegating to an agent is auto-approved.
		//
		// It used to default to 2, on the reasoning that an LLM agent can run
		// arbitrary commands and so the absence of a declaration must fail closed.
		// The cost of that was the product: every `panda ask` that classified as
		// an agent task was refused by defense.Authorize, parked in review, and
		// waited for a human — so the one thing the network exists to do could
		// not happen unattended. An agent that should ask first declares
		// `tier: 2` on itself in capabilities.yaml; that declaration still wins.
		tier := top.Agent.Tier
		if tier == 0 {
			tier = defense.TierReversible
		}
		plan := Plan{Kind: "agent", Ability: top.Name, Agent: top.Name, Adapter: top.Agent.Adapter, Tier: tier}
		// An explicit `agent:<name>` pin never gets alternates. A pin is a
		// statement about WHO must do the work ("test that claude_code can
		// run this"): silently re-running it on a different CLI turns a
		// failed dispatch into a green pass for an agent that never started,
		// and it is exactly how every failing agent used to end up on
		// deepseek_harness. The run fails and names the reason instead.
		if !agentPinned {
			for _, c := range cands[1:] {
				plan.Alternates = append(plan.Alternates, c.Name)
			}
		}
		return plan, nil
	}
	if ab, ok := r.MatchManual(required); ok {
		return Plan{Kind: "manual", Ability: ab.ID, Notify: ab.Notify}, nil
	}
	return Plan{}, fmt.Errorf("no capability matches required: %v", required)
}

// Execute runs the plan and returns a formatted result. authorized is the
// user's explicit consent to run Tier-2 (irreversible) commands; a Tier-2
// native command without it is refused before execution.
func (r *Router) Execute(ctx context.Context, plan Plan, prompt string, cwd string, authorized bool) Result {
	switch plan.Kind {
	case "native":
		if err := defense.Authorize(plan.Tier, authorized); err != nil {
			return Result{OK: false, ExitCode: 1, Stderr: authorizationHint(err, plan)}
		}
		return r.execNative(ctx, plan, cwd)
	case "agent":
		// P1-15: the tier model covers agents too — a Tier-2 agent without
		// consent is refused before the adapter subprocess is spawned.
		if err := defense.Authorize(plan.Tier, authorized); err != nil {
			return Result{OK: false, ExitCode: 1, Stderr: authorizationHint(err, plan)}
		}
		// A remote-origin task arrives unconsented with prompt text authored
		// off-node — the prompt IS the command surface, and a tier-1 agent
		// label only ever meant "the card didn't say otherwise", never "this
		// text is safe to hand a shell". Unconsented remote work gets the
		// read-only tool face; adapters that cannot express it are skipped
		// rather than silently running full-power (fail closed).
		if RemoteTask(ctx) && !authorized {
			ctx = WithRestricted(ctx)
		}
		// Consent must reach the adapter, not just this gate: a CLI that runs
		// with its own permission prompts disabled (claude --permission-prompts
		// none) auto-denies anything its permission mode does not allow, so an
		// approval here used to re-run byte-identical argv and park the task in
		// review again with the same question (issue #165).
		if authorized {
			ctx = WithAuthorized(ctx)
		}
		return r.execAgent(ctx, plan, prompt, cwd)
	case "manual":
		return Result{OK: false, ExitCode: 0, Stdout: plan.Notify, NeedManual: true}
	default:
		return Result{OK: false, ExitCode: 1, Stderr: "unknown plan kind " + plan.Kind}
	}
}

// authorizationHint turns a tier-2 refusal into an actionable message: the
// sentinel alone ("defense: tier-2 command requires authorization") tells the
// user the task was blocked but not how to unblock it. The hint names both
// exits — one-shot consent via --authorize, or a standing tier:1 declaration
// on the agent in capabilities.yaml — and stays inside the sentinel's prefix
// so IsAuthorizationRefusal keeps matching the failure wherever it surfaces.
func authorizationHint(err error, plan Plan) string {
	if !errors.Is(err, defense.ErrNotAuthorized) {
		return err.Error()
	}
	msg := err.Error()
	switch plan.Kind {
	case "agent":
		return msg + fmt.Sprintf(" (agent %q is tier-2; re-submit with --authorize, or declare tier:1 on it in capabilities.yaml)", plan.Agent)
	default:
		return msg + " (re-submit with --authorize to consent to irreversible commands)"
	}
}

// IsAuthorizationRefusal reports whether a failure string carries the tier-2
// authorization refusal. The retry loop uses it to skip pointless re-runs: a
// policy refusal is deterministic — another attempt cannot produce consent.
func IsAuthorizationRefusal(stderr string) bool {
	return strings.Contains(stderr, defense.ErrNotAuthorized.Error())
}

func (r *Router) execNative(ctx context.Context, plan Plan, cwd string) Result {
	// A per-call executor copy so the sandboxed cwd is this task's, not a
	// shared mutable field (native commands may run concurrently).
	ex := *r.executor
	ex.dir = cwd
	nr := ex.Run(ctx, plan.Command, plan.Args...)
	return Result{
		OK:       nr.OK,
		ExitCode: nr.ExitCode,
		Stdout:   nr.Stdout,
		Stderr:   nr.Stderr,
	}
}

func (r *Router) execAgent(ctx context.Context, plan Plan, prompt string, cwd string) Result {
	// Fallback chain: run the highest-scored agent whose CLI is actually
	// available; an unavailable candidate (missing binary / failed probe) is
	// skipped in favor of the next match. All unavailable fails closed with
	// an explicit error the upper layer can turn into a manual plan.
	seenAttempts := make(map[string]bool, len(plan.Alternates)+1)
	var attempts []string
	for _, name := range append([]string{plan.Agent}, plan.Alternates...) {
		if !seenAttempts[name] && name != "" {
			seenAttempts[name] = true
			attempts = append(attempts, name)
		}
	}
	var unavailable []string
	var lastExecRes *Result
	for i, name := range attempts {
		ag, ok := r.card.Agents[name]
		if !ok {
			unavailable = append(unavailable, name+" (not on card)")
			continue
		}
		if Restricted(ctx) && !AdapterSupportsRestricted(ag.Adapter) {
			// This run is an unconsented remote task: the adapter must
			// express a read-only tool face or it does not run at all —
			// degrading to its default (shell-capable) flags would be the
			// exact hole the restricted mode exists to close. The refusal
			// carries the authorization sentinel: it is deterministic (a
			// retry cannot grow a restricted mode) and consent is the fix,
			// so the orchestration layer parks it for human review.
			unavailable = append(unavailable,
				name+" ("+defense.ErrNotAuthorized.Error()+": adapter has no restricted mode; authorize the task to run it)")
			continue
		}
		if !r.probeAgent(name, ag) {
			// Explain the skip in dispatch terms — "no model configured",
			// "endpoint unreachable", "cli not found" — not a bare
			// "unavailable", so the failure that eventually surfaces names
			// the fix (configure a key, restore egress, install the CLI).
			reason := "cli unavailable"
			if _, why := r.agentUsable(name, ag); why != "" {
				reason = why
			}
			unavailable = append(unavailable, name+" ("+reason+")")
			continue
		}
		// The run is starting on a fallback candidate: say so NOW, at the
		// switch, not only in the end-of-round fallback record — an
		// exec_agent_start tracing the plan's primary must not keep naming
		// an agent that never launched while the real runner works for
		// minutes underneath it.
		if name != plan.Agent {
			reason := "primary failed or skipped"
			if len(unavailable) > 0 {
				reason = unavailable[len(unavailable)-1]
			}
			if sink, ok := ctx.Value(progressKey{}).(ProgressFunc); ok {
				sink(fmt.Sprintf("agent fallback — %s; %s is running this round", reason, name), "agent_switch")
			}
		}
		// The tools policy rides the context (the runAdapter seam's signature
		// stays unchanged for tests); runAdapterProcess copies it into the
		// request. MCP passthrough materializes a project .mcp.json for the
		// run when the policy is extended and a server is configured. A
		// per-task override (WithToolsPolicy set by the orchestration layer on
		// ctx) wins: the global policy is applied only when the context
		// carries no task-level policy yet.
		runCtx := WithAgentName(ctx, name)
		if ctx.Value(toolsPolicyKey{}) == nil {
			runCtx = WithToolsPolicy(runCtx, r.toolsPolicy)
		}
		if ag.Command != "" {
			runCtx = WithAgentCommand(runCtx, ag.Command)
		}
		// MCP passthrough: on a CLI with an MCP config flag the servers ride
		// the request (no .mcp.json in the work dir); project-discovery CLIs
		// keep the materialize-and-remove file path.
		runCtx, cleanupMCP := r.wireMCPPassthrough(runCtx, name, ag.Adapter, cwd)
		ar := r.runAdapter(runCtx, ag.Adapter, prompt, cwd)
		// One bounded retry on provider-side turbulence (rate limit /
		// overload / 5xx): these resolve in seconds, and the narrow
		// transientAgentFailure patterns keep real task failures — and
		// their side effects — from ever being re-run. cleanupMCP is deferred
		// past this block so the retried run gets the same .mcp.json the first
		// attempt had; removing it between attempts would silently strip the
		// task's MCP tools from exactly the run that decides the outcome.
		if !ar.OK && transientAgentFailure(ar) {
			timer := time.NewTimer(retryBackoff)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
			}
			if ctx.Err() == nil {
				retry := r.runAdapter(runCtx, ag.Adapter, prompt, cwd)
				if retry.OK {
					retry.Result = "[retried once after transient provider error] " + retry.Result
					ar = retry
				}
			}
		}
		cleanupMCP()
		// On failure the adapter's diagnosis lives in ar.Result (Stdout);
		// mirroring it into Stderr keeps store.Fail and the task-result
		// payload from recording an empty reason.
		stderr := ar.Stderr
		if !ar.OK && stderr == "" {
			stderr = ar.Result
		}

		res := Result{
			OK:            ar.OK,
			ExitCode:      ar.ExitCode,
			Stdout:        ar.Result,
			Stderr:        stderr,
			Tokens:        ar.Tokens,
			Cost:          ar.Cost,
			Agent:         name,
			Adapter:       ag.Adapter,
			Usage:         ar.Usage,
			SessionID:     ar.SessionID,
			Model:         ar.Model,
			Injected:      ar.Injected,
			Structured:    ar.Structured,
			SubagentStats: ar.SubagentStats,
		}
		lastExecRes = &res

		// If execution succeeded, or if user explicitly cancelled, return immediately.
		if ar.OK || ctx.Err() != nil {
			return res
		}

		// A timeout (or stall/budget kill — all exit 124) whose run already
		// minted a session is a checkpoint, not a dead end: the orchestrator's
		// retry resumes that conversation where it stopped. Failing over to an
		// alternate here would cold-start a different CLI at minute zero and
		// overwrite the persisted handle with the alternate's fresh session,
		// discarding everything the timed-out run accumulated.
		if ar.ExitCode == 124 && ar.SessionID != "" {
			return res
		}

		// Not every failure is a dispatch failure. An agent that ran, produced
		// a result and reported a task-level failure (a failing test, a refused
		// edit, a nonzero command) has done the work, and its outcome is the
		// honest answer: re-running it on another CLI repeats the side effects
		// and then reports the work under a different agent's name. Only a run
		// that never got off the ground — the CLI is missing, the credential
		// was rejected, the provider account is empty — may fall through to
		// the next candidate.
		if !agentFallbackEligible(ar) {
			return res
		}
		unavailable = append(unavailable, fmt.Sprintf("%s (exec failed: %s)", name, describeAgentFailure(ar)))
		// If this was the last attempt, return the failure result
		if i == len(attempts)-1 {
			return res
		}
	}
	if lastExecRes != nil {
		return *lastExecRes
	}
	return Result{
		OK:       false,
		ExitCode: 1,
		Stderr:   "no usable agent (tried " + strings.Join(unavailable, ", ") + "); install an agent CLI or route manually",
	}
}

// agentDispatchFailurePatterns name the ways an agent run fails before the
// work starts: the CLI is not signed in, the provider rejects the credential
// or has no funds left, the model does not exist, the binary is missing, the
// network is down. They are deliberately about DISPATCH, not about tasks — a
// failed test, a refused edit or a nonzero command must never match, because
// those are outcomes the caller needs to see rather than reasons to run the
// same work on someone else's model.
var agentDispatchFailurePatterns = []string{
	"not logged in", "please run /login", "please log in", "login required",
	"unauthorized", "authentication failed", "authentication error",
	"invalid api key", "invalid_api_key", "incorrect api key",
	"no api key", "missing api key", "missing credentials", "no credentials",
	"insufficient", "quota exceeded", "insufficient_quota", "out of credits",
	"billing", "account funds",
	"model not found", "unknown model", "invalid model", "unrecognized_model",
	"connection refused", "no such host", "network is unreachable",
	"certificate", "tls handshake", "ssl", "x509",
	"rate limit", "rate_limit", "429", "overloaded",
	"bad gateway", "service unavailable", "upstream request failed",
	"adapter output not json",
}

// agentFallbackEligible reports whether a failed agent run may be retried on
// the next alternate. The evidence required is that no model work was billed
// (Tokens == 0) plus a dispatch-level reason, so a run that actually did the
// task — or that failed at the task — is returned to the caller instead of
// being silently re-staffed. Output length is deliberately not part of the
// test: a crashing CLI can dump a long stack trace before it ever reaches the
// model. A timeout is never eligible: the work may be half done, and the
// orchestration layer's retry/resume path owns that decision.
func agentFallbackEligible(ar AgentResult) bool {
	if ar.OK {
		return false
	}
	if ar.SessionDead {
		return true
	}
	if ar.ExitCode == 124 {
		return false
	}
	if ar.ExitCode == 126 || ar.ExitCode == 127 {
		return true
	}
	if ar.Tokens > 0 {
		return false // the model ran: this is the task's outcome, not dispatch
	}
	msg := strings.ToLower(strings.TrimSpace(ar.Result + "\n" + ar.Stderr))
	if msg == "" {
		// The adapter said nothing at all: nothing ran, nothing reported.
		return true
	}
	for _, pat := range agentDispatchFailurePatterns {
		if strings.Contains(msg, pat) {
			return true
		}
	}
	return false
}

// describeAgentFailure keeps the reason a run failed visible in the fallback
// note. An empty message ("exec failed: " with nothing after it) is what made
// silent substitutions unreadable; name the exit code instead.
func describeAgentFailure(ar AgentResult) string {
	msg := strings.TrimSpace(ar.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(ar.Result)
	}
	if msg == "" {
		return fmt.Sprintf("no output (exit %d)", ar.ExitCode)
	}
	return msg
}

// Result is the normalized execution result (design doc P0-33).
type Result struct {
	OK         bool
	ExitCode   int
	Stdout     string
	Stderr     string
	Tokens     int
	Cost       float64
	NeedManual bool
	// Agent is the agent that actually executed (may differ from the plan's
	// primary when the fallback chain kicked in). Empty for non-agent plans
	// and when no candidate was usable at all.
	Agent string
	// Adapter is the adapter file that ran ("opencode.py") — the owner of
	// SessionID's wire shape. Persisted alongside the session handle so a
	// later round offers resume only to the adapter that minted it.
	Adapter string
	// Usage is the structured token breakdown the adapter reported (nil when
	// the adapter is a plain-text one); SessionID is the agent session a
	// follow-up round can resume (empty when the adapter has none).
	Usage     *UsageDetail
	SessionID string
	Model     string `json:"model,omitempty"`
	Injected  bool   `json:"injected,omitempty"`
	// Structured carries the harness's validated structured output when the
	// run used a result schema — the parsed protocol fields the run loop
	// prefers over text markers. SubagentStats is the harness's own
	// delegation fan-out summary (claude's result.subagent_stats).
	Structured    json.RawMessage `json:"structured,omitempty"`
	SubagentStats json.RawMessage `json:"subagent_stats,omitempty"`
}

// AgentResult is what an adapter returns. Every field carries its wire name
// explicitly: Go's untagged fallback matches keys case-insensitively but NOT
// underscore-insensitively, so without the tags "exit_code" would silently
// drop — adapter-reported 124/127/2 would all decode as 0.
type AgentResult struct {
	OK        bool         `json:"ok"`
	Result    string       `json:"result"`
	Stderr    string       `json:"stderr"`
	ExitCode  int          `json:"exit_code"`
	Tokens    int          `json:"tokens"`
	Cost      float64      `json:"cost"`
	Usage     *UsageDetail `json:"usage"`
	SessionID string       `json:"session_id"`
	Model     string       `json:"model,omitempty"`
	Injected  bool         `json:"injected,omitempty"`
	// Structured is the harness's validated structured output (claude
	// --json-schema → structured_output), passed through verbatim. When the
	// schema carries the panda protocol fields (status/question/
	// delegate_requests) the run loop reads them here instead of parsing
	// markers out of prose.
	Structured json.RawMessage `json:"structured,omitempty"`
	// SubagentStats is the harness's own delegation fan-out summary
	// (claude's result.subagent_stats) — how many sub-agents the run
	// spawned, by type. Passthrough for observability.
	SubagentStats json.RawMessage `json:"subagent_stats,omitempty"`
	// SessionDead marks a session-mode result whose adapter process is
	// gone. The session teardown marker (cmd.Wait) races the envelope
	// when the adapter kills the CLI on its way out, so a failed turn
	// needs the in-band verdict to drive the one-shot fallback.
	SessionDead bool `json:"session_dead,omitempty"`
}

// runAdapterDefault shells out to a Python adapter in adapters/, injecting
// the model config only when the injection policy says so (default auto:
// agent-native credentials win).
func (r *Router) runAdapterDefault(ctx context.Context, adapter string, prompt string, cwd string) AgentResult {
	// The card's agent name (WithAgentName, set by execAgent) resolves the
	// registry record alongside the adapter script: two unrelated CLIs can
	// share generic.py, so the script alone must not inherit an entry's
	// credential manifest or model-env mapping.
	name := AgentName(ctx)
	dec := r.InjectionDecision(name, adapter)
	// The model actually injected into this agent: a per-agent pin
	// (injection.agents) overrides the node model, so the endpoint guard,
	// the env, and the failure-time retry below all have to read the same
	// resolved config rather than r.model.
	injModel := r.injectionModelFor(name, adapter)
	if ModelCredsWithheld(ctx) && dec.Inject {
		// Trust-zone cred withholding: the task's admission tier is
		// sub-mesh, and this node's model credentials do not serve its
		// work. The decision is overridden here — at the env-build point,
		// not inside InjectionDecision — so non-task callers (probes,
		// the run-level notice) keep their own policy answer.
		dec = InjectionDecision{
			Inject: false,
			Reason: "trust zone: model credentials withheld from sub-mesh origin",
		}
	}
	var env []string
	// The agent's own model preference list (routing.agent_models) is
	// advisory: the adapter picks the first entry its install serves. It
	// rides every run, injected or not, because it describes the CLI's own
	// model choice; an injected run ignores it.
	if prefs := r.agentModelPrefs(name, adapter); len(prefs) > 0 {
		if raw, err := json.Marshal(prefs); err == nil {
			env = append(env, "OPENPANDA_AGENT_MODELS="+string(raw))
		}
	}
	if dec.Inject {
		// The model endpoint must be HTTPS so the API key never travels
		// cleartext, and pinned to the configured host so the allowlist is
		// never empty (D7). Only enforced when we actually inject the env —
		// and against the endpoint the run will really dial, which is the
		// catalogue's when base_url is empty.
		if ep := effectiveBaseURLFor(name, adapter, injModel); ep != "" {
			if err := security.NewNetworkGuard(security.EndpointHost(ep)).CheckURL(ep); err != nil {
				return AgentResult{OK: false, Result: security.Redact(err.Error()), ExitCode: 1}
			}
		}
		// Merged rather than assigned: the preference list rides every run
		// (the adapter ignores it on an injected one), and the agent's own
		// credential vars must survive alongside.
		env = mergeAdapterEnv(env, modelEnvForAdapter(injModel, name, adapter))
	}
	// Native Agent credentials must survive the minimal sandbox even when PANDA
	// does not inject its own model. Only adapter-specific keys are forwarded —
	// and a withheld context forwards none at all: a credential env var read
	// out of this daemon's environment is exactly what the withhold exists to
	// keep out of a semi-trusted task's reach.
	var credEnv []string
	if !ModelCredsWithheld(ctx) {
		credEnv = adapterCredentialEnv(name, adapter)
	}
	env = mergeAdapterEnv(credEnv, env)
	res := r.runProcess(ctx, adapter, prompt, cwd, env)
	if dec.Inject {
		res.Injected = true
		res.Model = dec.Model
	}
	// Dynamic injection fallback (Requirement 5):
	// If the agent failed with authentication, quota, provider errors, or timeouts
	// and PANDA had not injected its model key (because the agent declared its own credentials which failed),
	// automatically inject PANDA's configured model API key to adapt the harness instead of discarding the task.
	// The retried result REPLACES the original only when it succeeded: a
	// different failure under injection is not more informative than the
	// original failure — swapping it in would mask the real reason the task
	// failed (e.g. report exit-code noise instead of "quota exhausted").
	//
	// injection.model=never opts out of this too: "never" is documented as
	// "agents always use their own models", and a retry that hands the task
	// panda's key is exactly the override the setting promises not to do.
	//
	// A kill the commander itself performed is not a provider failure even
	// though its verdict says "timeout" — see commanderKill.
	if !res.OK && replayableFailure(res) && !dec.Inject && r.injectionModel != config.InjectionModelNever && !ModelCredsWithheld(ctx) && modelConfigured(injModel) && supportsModelInjection(name, adapter, injModel) && injModel.APIKey != "" && !commanderKill(res) && isProviderFailureOrAuth(res.Result+" "+res.Stderr) {
		if ep := effectiveBaseURLFor(name, adapter, injModel); ep == "" || security.NewNetworkGuard(security.EndpointHost(ep)).CheckURL(ep) == nil {
			// Merged with the first attempt's environment, not replacing it:
			// the retry is the same run with a different model endpoint, so the
			// agent's own credential vars, preference list and task id still
			// belong in its environment.
			injectedEnv := mergeAdapterEnv(env, modelEnvForAdapter(injModel, name, adapter))
			if len(injectedEnv) > 0 {
				retryRes := r.runProcess(ctx, adapter, prompt, cwd, injectedEnv)
				if retryRes.OK {
					retryRes.Injected = true
					retryRes.Model = effectiveModelNameFor(name, adapter, injModel)
					return retryRes
				}
			}
		}
	}
	return res
}

// SetAdapterProcessRunner swaps the process runner (test seam).
func (r *Router) SetAdapterProcessRunner(fn func(ctx context.Context, adapter, prompt, cwd string, env []string) AgentResult) {
	r.runProcess = fn
}

// providerStatusRE matches a bare HTTP status token relevant to the injection
// fallback. Word boundaries keep it from matching digits glued into a larger
// token — a task that fails mentioning "port 5030" or "errno 4291" is not a
// provider error, and falling back to injection on it would re-run the task
// with different credentials for no reason (same reasoning as
// transientStatusRE in adapter.go).
var providerStatusRE = regexp.MustCompile(`\b(?:401|402|403|429|500|502|503|504)\b`)

// providerTimeoutRE matches timeout phrasing as a word, so a real task failure
// mentioning e.g. "session timeout file lock" still qualifies, but substring
// accidents ("timeouts5400", a hex blob) do not.
var providerTimeoutRE = regexp.MustCompile(`\b(?:timed out|timeout)\b`)

func isProviderFailureOrAuth(text string) bool {
	low := strings.ToLower(text)
	// Phrase patterns are substrings by design — they are long enough to be
	// unambiguous ("invalid api key" cannot appear by accident).
	patterns := []string{
		"unauthorized", "forbidden", "invalid token",
		"payment required", "payment_required", "budget pool", "exhausted",
		"invalid api key", "invalid_api_key", "额度不足", "quota",
		"insufficient_quota", "credit balance", "out of credit",
		"not logged in", "failed to authenticate", "auth error",
		"authentication failed", "no api key", "api key missing",
		"server_error", "internal server error",
		"service unavailable", "bad gateway", "overloaded", "rate limit",
		"rate_limit", "connection refused", "connection reset",
		"connect error", "failed to connect",
		"unrecognized_model", "model_not_found", "model not found",
		"api_retry", "api_error", "provider failure",
	}
	for _, p := range patterns {
		if strings.Contains(low, p) {
			return true
		}
	}
	// Bare numeric statuses and timeout wording need word boundaries: "403"
	// inside "port 4030" or "error-14031" is not a provider failure.
	return providerStatusRE.MatchString(low) || providerTimeoutRE.MatchString(low)
}

// commanderKill reports a run this package ended itself — the silence
// watchdog, the tool-call fuse, or the hard deadline. All three carry exit
// 124 and a verdict phrased as a timeout, so isProviderFailureOrAuth would
// read the commander's own kill as provider trouble and answer it with a
// full injected-model re-run: the silent CLI stalls again on the same bound
// four minutes later, and whatever side effects the first attempt already
// performed happen twice. Exit 124 alone is not the test — the adapter
// process may exit 124 on its own for a genuine provider timeout, and that
// run still deserves the rescue.
func commanderKill(ar AgentResult) bool {
	if ar.ExitCode != 124 {
		return false
	}
	return strings.HasPrefix(ar.Result, "adapter stalled: ") ||
		ar.Result == "adapter timed out (hard limit)" ||
		strings.HasPrefix(ar.Result, "agent tool-call budget exhausted")
}

// agentBinary derives the CLI binary for an agent: the card's install_check
// ("which claude") wins, then — for the generic adapter — the command
// template's own first token (the registry cannot name a custom CLI's
// binary), then the canonical probe binary from the agent registry
// (internal/agents) resolved by name+adapter. "" means the probe cannot
// decide and the agent is treated as available (the adapter itself will fail
// loudly if its CLI is really missing).
func agentBinary(name string, ag ledger.Agent) string {
	if fields := strings.Fields(ag.InstallCheck); len(fields) == 2 &&
		(fields[0] == "which" || fields[0] == "command" || fields[0] == "where") {
		return fields[1]
	}
	if ag.Adapter == agents.GenericAdapter || ag.Adapter == agents.GenericNativeAdapter {
		// The template's first token IS the binary to probe — the registry
		// record (whichever agent happens to share generic.py) cannot name a
		// custom CLI's binary. With no template, fall through to Lookup so a
		// name-matched entry (e.g. "zcode") still resolves its binaries.
		// splitArgv, not Fields: a quoted path ("C:\Program Files\tool.exe")
		// must probe as one element, matching what the adapter will exec.
		if fields := splitArgv(ag.Command); len(fields) > 0 {
			return fields[0]
		}
	}
	if k, ok := agents.Lookup(name, ag.Adapter); ok {
		return k.PrimaryBinary()
	}
	return ""
}

// defaultAgentProbe reports whether an agent's CLI resolves on PATH. It is
// the production availability probe behind the fallback chain.
func defaultAgentProbe(name string, ag ledger.Agent) bool {
	bin := agentBinary(name, ag)
	if bin == "" {
		return true // unknown CLI: let the adapter try
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// AgentViable reports whether an agent can actually execute on this machine:
// the adapter runtime exists, its CLI resolves on PATH, AND it can reach a
// model — either its own credentials (registry manifest) or panda's configured
// model via injection. An installed-but-locked-out CLI (e.g. claude.exe without
// login state on a node with no model key) fails only after minutes of runtime
// hang; probing viability up front keeps both the local fallback chain and the
// capability summary peers route on from ever selecting such an agent.
func (r *Router) AgentViable(name string, ag ledger.Agent) bool {
	// Every script adapter runs through Python, so a host without an
	// interpreter can run none of them however well the CLI itself is
	// installed. The built-in "generic" adapter is the exception — it is
	// implemented in-process and needs no interpreter, which is exactly what
	// makes it the fallback for bare-metal nodes. Without this check such a
	// node advertises coding abilities, wins the routing score, accepts the
	// delegated stage and fails at exec time.
	if ag.Adapter != agents.GenericNativeAdapter && !pyexec.Available() {
		return false
	}
	bin := agentBinary(name, ag)
	if bin != "" {
		if _, err := exec.LookPath(bin); err != nil {
			return false
		}
	}
	// Unknown adapters keep the legacy "let the adapter try" behavior: their
	// credential contract is not in the registry, so viability cannot be
	// judged here. A generic.py entry whose card name matches no registry
	// agent resolves to nothing here the same way.
	k, known := agents.Lookup(name, ag.Adapter)
	if !known {
		return true
	}
	if k.SelfContainedModel {
		return true
	}
	if own, _ := probeAgentCredentials(name, ag.Adapter); own {
		return true
	}
	// No credentials of its own: the agent runs only when panda's model can
	// be injected into it — strategy allows it, a key is configured, and the
	// registry declares a safe env mapping for the adapter.
	if r.injectionModel == config.InjectionModelNever {
		return false
	}
	m := r.injectionModelFor(name, ag.Adapter)
	return modelConfigured(m) && m.APIKey != "" && supportsModelInjection(name, ag.Adapter, m)
}

// AgentDispatchable reports whether an agent is ready to receive a task right
// now: statically viable (AgentViable) AND able to reach the model endpoint
// its run would actually use. The second check is what catches the
// "configured but cannot connect" case — a provider outage, a broken egress
// route, a dead relay host — before the task blocks inside the adapter.
func (r *Router) AgentDispatchable(name string, ag ledger.Agent) bool {
	ok, _ := r.agentUsable(name, ag)
	return ok
}

// AgentUsableReason is AgentDispatchable with the first failure's reason
// attached — the diagnostic form (doctor, debug logs). An empty reason means
// usable.
func (r *Router) AgentUsableReason(name string, ag ledger.Agent) (bool, string) {
	return r.agentUsable(name, ag)
}

// agentUsable is the canonical dispatchability check, carrying the first
// failure's reason so callers can explain a skipped candidate. The reason
// list intentionally mirrors AgentViable's order — runtime/runtime, binary,
// credentials/injection — then appends the live endpoint probe.
func (r *Router) agentUsable(name string, ag ledger.Agent) (usable bool, reason string) {
	// Same interpreter exemption as AgentViable: adapter "generic" (no .py)
	// is implemented natively.
	if ag.Adapter != agents.GenericNativeAdapter && !pyexec.Available() {
		return false, "no Python 3 interpreter"
	}
	if bin := agentBinary(name, ag); bin != "" {
		if _, err := exec.LookPath(bin); err != nil {
			return false, "cli " + bin + " not found on PATH"
		}
	}
	k, known := agents.Lookup(name, ag.Adapter)
	if !known {
		// Unknown adapters keep the legacy "let the adapter try" behavior:
		// their credential and endpoint contract is not in the registry.
		return true, ""
	}
	if !k.SelfContainedModel {
		if own, _ := probeAgentCredentials(name, ag.Adapter); !own {
			m := r.injectionModelFor(name, ag.Adapter)
			switch {
			case r.injectionModel == config.InjectionModelNever:
				return false, "no model configured (no own credentials; injection.model=never)"
			case !modelConfigured(m):
				return false, "no model configured (no own credentials; no panda model endpoint)"
			case m.APIKey == "":
				return false, "no model configured (no own credentials; no panda model key)"
			case !supportsModelInjection(name, ag.Adapter, m):
				return false, "no model configured (no own credentials; injection unsupported for " + ag.Adapter + ")"
			}
		}
	}
	if spec := r.agentTarget(name, ag); spec.Endpoint != "" {
		// A probe that drew no answer inside its deadline falls through to the
		// pass below. This gate exists to stop an agent that is configured but
		// broken from winning a plan and dying at exec — a refused connect or a
		// dead host proves exactly that, and still disqualifies. A timeout proves
		// nothing: it is what a healthy relay behind its own account-pool queue
		// looks like from here, and blocking on it hid a working Claude Code
		// while the very next request answered 200. The run keeps its own
		// deadline to fail on.
		if v := r.endpointProbe(spec); !v.OK && !v.Unknown {
			if v.Rejected {
				return false, "model credentials/quota rejected by " + spec.Endpoint + " (" + v.Detail + ")"
			}
			reason := "model endpoint unreachable: " + spec.Endpoint
			if v.Detail != "" {
				reason += " (" + v.Detail + ")"
			}
			return false, reason
		}
	}
	return true, ""
}

// agentEndpoint resolves the provider base URL the agent's run would
// actually hit — kept for callers that only need the URL.
func (r *Router) agentEndpoint(name string, ag ledger.Agent) string {
	return r.agentTarget(name, ag).Endpoint
}

// agentTarget resolves the model call the agent's run would actually make:
// the injected endpoint+key when PANDA's model wins, the agent's own env
// override when it set one (e.g. ANTHROPIC_BASE_URL), the base URL inside the
// harness's own config file when it points at a relay or custom provider
// (codex's config.toml, claude's settings.json, opencode's providers), else
// the registry's provider default. An empty Endpoint means the endpoint
// cannot be determined and the probe is skipped — never block an unknown
// adapter on a guess.
func (r *Router) agentTarget(name string, ag ledger.Agent) ProbeSpec {
	k, ok := agents.Lookup(name, ag.Adapter)
	if !ok {
		return ProbeSpec{}
	}
	apiType := config.APITypeOpenAI
	injModel := r.injectionModelFor(name, ag.Adapter)
	if k.ModelEnv != nil {
		apiType = k.ModelEnv.APIType
		if apiType == "" {
			// Polyglot agent (pi): the probe speaks the dialect the run
			// actually uses — the resolved surface, which for an aggregator
			// is the route serving this model, not the row's default. A
			// dialect only panda's client speaks (Responses/Google) is not an
			// agent protocol, so the probe falls back to Chat Completions.
			switch mt := injectedDialect(name, ag.Adapter, injModel); mt {
			case config.APITypeAnthropic, config.APITypeOpenAI:
				apiType = mt
			default:
				apiType = config.APITypeOpenAI
			}
		}
	}
	if dec := r.InjectionDecision(name, ag.Adapter); dec.Inject {
		ep := dec.BaseURL
		if ep == "" {
			ep = effectiveBaseURL(injModel)
		}
		return ProbeSpec{
			Endpoint: ep, APIType: apiType,
			APIKey: injModel.APIKey, Model: dec.Model,
			KeyDefinitive: true, // the injected key is exactly what the run sends
		}
	}
	// The run would authenticate with whatever the env exposes — matching it
	// makes the probe's 2xx mean what it says, and a definitive rejection
	// means the run would fail identically.
	var key, model string
	keyDef := false
	if k.ModelEnv != nil {
		key = firstEnvValue(append([]string{k.ModelEnv.APIKey}, k.CredentialEnvVars...)...)
		model = firstEnvValue(k.ModelEnv.Model)
		keyDef = key != "" // env keys are the literal credential
	}
	// Env override — the same variable the run inherits.
	if k.ModelEnv != nil && k.ModelEnv.BaseURL != "" {
		if v := strings.TrimSpace(os.Getenv(k.ModelEnv.BaseURL)); v != "" {
			return ProbeSpec{Endpoint: v, APIType: apiType, APIKey: key, Model: model, KeyDefinitive: keyDef}
		}
	}
	// The harness's own config file: relay stations and custom providers are
	// configured there, and the registry default would probe the wrong host.
	// The same files often carry the credential too (codex's env_key, claude's
	// settings env, auth.json tokens) — an authenticated probe answers 2xx and
	// proves the model serves, where an anonymous 401 only proves the host.
	cfg := agentConfiguredTarget(ag.Adapter)
	if key == "" {
		key, keyDef = cfg.APIKey, cfg.KeyDef
	}
	if cfg.APIType != "" {
		apiType = cfg.APIType
	}
	if model == "" {
		model = cfg.Model
	}
	if cfg.Endpoint != "" {
		return ProbeSpec{Endpoint: cfg.Endpoint, APIType: apiType, APIKey: key, Model: model, KeyDefinitive: keyDef, RejectionHint: cfg.Hint}
	}
	return ProbeSpec{Endpoint: k.Endpoint, APIType: apiType, APIKey: key, Model: model, KeyDefinitive: keyDef}
}

// firstEnvValue returns the first non-empty env var among names.
func firstEnvValue(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// PlanUsable reports whether the plan can be dispatched for execution on this
// node right now. Native/manual plans have no agent dependency and always
// pass; an agent plan needs at least one candidate — primary or alternate —
// that is dispatchable (viable + endpoint reachable). localMatch and the run
// path gate on this so a node whose harnesses are all installed-but-dead
// declines the task fast and the scheduler fails over to a healthy peer.
func (r *Router) PlanUsable(p Plan) bool {
	if p.Kind != "agent" {
		return true
	}
	for _, name := range append([]string{p.Agent}, p.Alternates...) {
		if name == "" {
			continue
		}
		if ag, ok := r.card.Agents[name]; ok && r.probeAgent(name, ag) {
			return true
		}
	}
	return false
}

// PlanUnusableReason explains why PlanUsable said no: one "name: reason" pair
// per rejected candidate. The gate used to fail with a generic "cli/
// credentials/endpoint check failed", which is useless to the operator when
// the real cause is a 429 from the provider behind the agent — the reason
// names the failing check (missing cli, no model, endpoint unreachable,
// quota/auth rejected) so the fix is obvious. "" means the plan is usable.
func (r *Router) PlanUnusableReason(p Plan) string {
	if p.Kind != "agent" {
		return ""
	}
	var parts []string
	for _, name := range append([]string{p.Agent}, p.Alternates...) {
		if name == "" {
			continue
		}
		ag, ok := r.card.Agents[name]
		if !ok {
			parts = append(parts, name+": not in the capability card")
			continue
		}
		if r.probeAgent(name, ag) {
			return ""
		}
		_, reason := r.AgentUsableReason(name, ag)
		if reason == "" {
			reason = "not dispatchable"
		}
		parts = append(parts, name+": "+reason)
	}
	return strings.Join(parts, "; ")
}
