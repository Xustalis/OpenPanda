package entry

import (
	"context"
	"sort"
)

// Tool is a callable tool the entry model may invoke. Name/Description/Schema
// are what the model sees (the Anthropic tool schema); Run executes the call and
// returns a short user-facing result. The model never performs side effects —
// the registry's Run is the only thing that acts on a tool call.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema for the tool's input arguments
	Run         func(ctx context.Context, args map[string]any) (string, error)
}

// spec converts the tool to its wire form (the Messages API tool schema).
func (t Tool) spec() ToolSpec {
	return ToolSpec{Name: t.Name, Description: t.Description, InputSchema: t.Schema}
}

// Registry is the set of tools the entry model may call, keyed by name.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool, replacing any existing tool with the same name.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name] = t
}

// Lookup returns a tool by name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Specs returns all registered tools in their wire form, sorted by name so the
// request is deterministic.
func (r *Registry) Specs() []ToolSpec {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	specs := make([]ToolSpec, 0, len(names))
	for _, n := range names {
		specs = append(specs, r.tools[n].spec())
	}
	return specs
}
