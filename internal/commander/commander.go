package commander

import (
	"context"
	"fmt"
	"strings"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/ledger"
)

// Router matches a task's required abilities against a node's capability
// card and produces an execution plan (native command / agent adapter /
// manual notification).
type Router struct {
	card     ledger.Card
	executor *Executor
	model    config.ModelConfig
	// runAdapter is injectable for tests; production uses RunAgent.
	runAdapter func(ctx context.Context, adapter string, prompt string, cwd string) AgentResult
}

// NewRouter builds a router from this node's capability card. The model config
// is injected into agent adapter subprocesses (base URL + key + model).
func NewRouter(card ledger.Card, executor *Executor, model config.ModelConfig) *Router {
	r := &Router{card: card, executor: executor, model: model}
	r.runAdapter = r.runAdapterDefault
	return r
}

// Plan describes how to execute a task on this node.
type Plan struct {
	Kind    string // native | agent | manual
	Ability string
	Command string
	Args    []string
	Agent   string
	Adapter string
	Notify  string
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

// MatchAgent finds a configured agent that satisfies any of required — either
// by name ("agent:<name>", the form the device summary advertises) or by one
// of its declared capabilities.
func (r *Router) MatchAgent(required []string) (string, ledger.Agent, bool) {
	for _, req := range required {
		if name, ok := strings.CutPrefix(req, "agent:"); ok {
			if ag, exists := r.card.Agents[name]; exists {
				return name, ag, true
			}
			continue
		}
		for name, ag := range r.card.Agents {
			for _, cap := range ag.Capabilities {
				if ledger.AbilityMatches(cap, req) {
					return name, ag, true
				}
			}
		}
	}
	return "", ledger.Agent{}, false
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
		return Plan{Kind: "native", Ability: ab.ID, Command: ab.Command, Args: ab.Args}, nil
	}
	if name, ag, ok := r.MatchAgent(required); ok {
		return Plan{Kind: "agent", Ability: name, Agent: name, Adapter: ag.Adapter}, nil
	}
	if ab, ok := r.MatchManual(required); ok {
		return Plan{Kind: "manual", Ability: ab.ID, Notify: ab.Notify}, nil
	}
	return Plan{}, fmt.Errorf("no capability matches required: %v", required)
}

// Execute runs the plan and returns a formatted result.
func (r *Router) Execute(ctx context.Context, plan Plan, prompt string, cwd string) Result {
	switch plan.Kind {
	case "native":
		return r.execNative(ctx, plan)
	case "agent":
		return r.execAgent(ctx, plan, prompt, cwd)
	case "manual":
		return Result{OK: false, ExitCode: 0, Stdout: plan.Notify, NeedManual: true}
	default:
		return Result{OK: false, ExitCode: 1, Stderr: "unknown plan kind " + plan.Kind}
	}
}

func (r *Router) execNative(ctx context.Context, plan Plan) Result {
	nr := r.executor.Run(ctx, plan.Command, plan.Args...)
	return Result{
		OK:       nr.OK,
		ExitCode: nr.ExitCode,
		Stdout:   nr.Stdout,
		Stderr:   nr.Stderr,
	}
}

func (r *Router) execAgent(ctx context.Context, plan Plan, prompt string, cwd string) Result {
	ar := r.runAdapter(ctx, plan.Adapter, prompt, cwd)
	return Result{
		OK:       ar.OK,
		ExitCode: ar.ExitCode,
		Stdout:   ar.Result,
		Stderr:   "",
		Tokens:   ar.Tokens,
		Cost:     ar.Cost,
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
// the model config so the adapter can reach the configured provider.
func (r *Router) runAdapterDefault(ctx context.Context, adapter string, prompt string, cwd string) AgentResult {
	// The Go core injects secrets via env; adapters read them from os.environ.
	return runAdapterProcess(ctx, adapter, prompt, cwd, r.model)
}
