package commander

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/ledger"
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
	// preferred lists agent names that receive a score bonus during routing.
	preferred []string
	// probeAgent reports whether an agent's CLI is usable on this machine.
	// Injectable for tests; production probes PATH (see defaultAgentProbe).
	probeAgent func(name string, ag ledger.Agent) bool
	// runAdapter is injectable for tests; production uses RunAgent.
	runAdapter func(ctx context.Context, adapter string, prompt string, cwd string) AgentResult
}

// NewRouter builds a router from this node's capability card. The model
// config is injected into agent adapter subprocesses only as the injection
// policy allows (default auto: agent-native credentials win); routing honors
// the preferred-agents bonus and falls back across scored candidates.
func NewRouter(card ledger.Card, executor *Executor, model config.ModelConfig, injection config.InjectionConfig, routing config.RoutingConfig) *Router {
	r := &Router{
		card:           card,
		executor:       executor,
		model:          model,
		injectionModel: injection.NormalizedModel(),
		preferred:      routing.PreferredAgents,
	}
	r.runAdapter = r.runAdapterDefault
	// The production probe is credential-aware (AgentViable), not just a
	// PATH check: routing to an installed-but-locked-out CLI guarantees a
	// runtime failure after a long hang.
	r.probeAgent = r.AgentViable
	return r
}

// SetPolicy swaps the injection/routing policy after construction (the core
// builds its router before the full config is wired through).
func (r *Router) SetPolicy(injection config.InjectionConfig, routing config.RoutingConfig) {
	r.injectionModel = injection.NormalizedModel()
	r.preferred = routing.PreferredAgents
}

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

// preferredBonus is the score bonus an agent listed in
// routing.preferred_agents receives during ranking.
const preferredBonus = 0.5

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

// RankAgents scores every agent that satisfies any of required and returns
// the candidates sorted by descending score (ties broken by name, so the
// choice stays deterministic across map iteration orders).
//
// Score = capability match (1.0 exact / 0.9 token-subset) × cost_tier
// discount + preferred-agents bonus. A requirement of the form
// "agent:<name>" pins that exact agent — an explicit user choice never fans
// out to substitutes.
func (r *Router) RankAgents(required []string) []AgentCandidate {
	for _, req := range required {
		if name, ok := strings.CutPrefix(req, "agent:"); ok {
			if ag, exists := r.card.Agents[name]; exists {
				return []AgentCandidate{{Name: name, Agent: ag, Score: 1 + r.bonus(name)}}
			}
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
			continue
		}
		score := best*costMultiplier(ag.CostTier) + r.bonus(name)
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

// Route decides how to execute a task with the given required abilities.
// Priority: native > agent > manual (design doc §6.4).
func (r *Router) Route(required []string) (Plan, error) {
	if ab, ok := r.MatchNative(required); ok {
		tier := ab.Tier
		if tier == 0 {
			tier = defense.TierFromCommand(ab.Command, ab.Args...)
		}
		return Plan{Kind: "native", Ability: ab.ID, Command: ab.Command, Args: ab.Args, Tier: tier}, nil
	}
	if cands := r.RankAgents(required); len(cands) > 0 {
		top := cands[0]
		// P1-15: agent plans carry a tier like native ones. An undeclared tier
		// defaults to 2 — an LLM agent can execute arbitrary commands, so the
		// absence of a declaration must fail closed, not open.
		tier := top.Agent.Tier
		if tier == 0 {
			tier = defense.TierIrreversible
		}
		plan := Plan{Kind: "agent", Ability: top.Name, Agent: top.Name, Adapter: top.Agent.Adapter, Tier: tier}
		for _, c := range cands[1:] {
			plan.Alternates = append(plan.Alternates, c.Name)
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
	attempts := append([]string{plan.Agent}, plan.Alternates...)
	var unavailable []string
	for _, name := range attempts {
		ag, ok := r.card.Agents[name]
		if !ok {
			unavailable = append(unavailable, name+" (not on card)")
			continue
		}
		if !r.probeAgent(name, ag) {
			unavailable = append(unavailable, name+" (cli unavailable)")
			continue
		}
		ar := r.runAdapter(ctx, ag.Adapter, prompt, cwd)
		// One bounded retry on provider-side turbulence (rate limit /
		// overload / 5xx): these resolve in seconds, and the narrow
		// transientAgentFailure patterns keep real task failures — and
		// their side effects — from ever being re-run.
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
				retry := r.runAdapter(ctx, ag.Adapter, prompt, cwd)
				if retry.OK {
					retry.Result = "[retried once after transient provider error] " + retry.Result
					ar = retry
				}
			}
		}
		// On failure the adapter's diagnosis lives in ar.Result (Stdout);
		// mirroring it into Stderr keeps store.Fail and the task-result
		// payload from recording an empty reason.
		stderr := ""
		if !ar.OK {
			stderr = ar.Result
		}
		return Result{
			OK:       ar.OK,
			ExitCode: ar.ExitCode,
			Stdout:   ar.Result,
			Stderr:   stderr,
			Tokens:   ar.Tokens,
			Cost:     ar.Cost,
			Agent:    name,
		}
	}
	return Result{
		OK:       false,
		ExitCode: 1,
		Stderr:   "no usable agent (tried " + strings.Join(unavailable, ", ") + "); install an agent CLI or route manually",
	}
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
}

// AgentResult is what an adapter returns.
type AgentResult struct {
	OK       bool
	Result   string
	ExitCode int
	Tokens   int
	Cost     float64
}

// runAdapterDefault shells out to a Python adapter in adapters/, injecting
// the model config only when the injection policy says so (default auto:
// agent-native credentials win).
func (r *Router) runAdapterDefault(ctx context.Context, adapter string, prompt string, cwd string) AgentResult {
	dec := r.InjectionDecision(adapter)
	var env []string
	if dec.Inject {
		// The model endpoint must be HTTPS so the API key never travels
		// cleartext, and pinned to the configured host so the allowlist is
		// never empty (D7). Only enforced when we actually inject the env.
		if r.model.BaseURL != "" {
			if err := security.NewNetworkGuard(security.EndpointHost(r.model.BaseURL)).CheckURL(r.model.BaseURL); err != nil {
				return AgentResult{OK: false, Result: security.Redact(err.Error()), ExitCode: 1}
			}
		}
		env = modelEnvForAdapter(r.model, adapter)
	}
	// Native Agent credentials must survive the minimal sandbox even when PANDA
	// does not inject its own model. Only adapter-specific keys are forwarded.
	env = mergeAdapterEnv(adapterCredentialEnv(adapter), env)
	return runAdapterProcess(ctx, adapter, prompt, cwd, env)
}

// agentBinary derives the CLI binary for an agent: the card's install_check
// ("which claude") wins, then the canonical probe binary from the agent
// registry (internal/agents) for the adapter. "" means the probe cannot
// decide and the agent is treated as available (the adapter itself will fail
// loudly if its CLI is really missing).
func agentBinary(name string, ag ledger.Agent) string {
	if fields := strings.Fields(ag.InstallCheck); len(fields) == 2 &&
		(fields[0] == "which" || fields[0] == "command") {
		return fields[1]
	}
	if k, ok := agents.ByAdapter(ag.Adapter); ok {
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
// its CLI resolves on PATH AND it can reach a model — either its own
// credentials (registry manifest) or panda's configured model via injection.
// An installed-but-locked-out CLI (e.g. claude.exe without login state on a
// node with no model key) fails only after minutes of runtime hang; probing
// viability up front keeps both the local fallback chain and the capability
// summary peers route on from ever selecting such an agent.
func (r *Router) AgentViable(name string, ag ledger.Agent) bool {
	bin := agentBinary(name, ag)
	if bin != "" {
		if _, err := exec.LookPath(bin); err != nil {
			return false
		}
	}
	// Unknown adapters keep the legacy "let the adapter try" behavior: their
	// credential contract is not in the registry, so viability cannot be
	// judged here.
	if _, known := agents.ByAdapter(ag.Adapter); !known {
		return true
	}
	if own, _ := probeAgentCredentials(ag.Adapter); own {
		return true
	}
	// No credentials of its own: the agent runs only when panda's model can
	// be injected into it — strategy allows it, a key is configured, and the
	// registry declares a safe env mapping for the adapter.
	if r.injectionModel == config.InjectionModelNever {
		return false
	}
	return r.model.APIKey != "" && supportsModelInjection(ag.Adapter, r.model)
}
