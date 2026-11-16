// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import "context"

// scopeProjectKey carries the project an ask is running in. It is set by the
// engine at the start of a turn and read by the memory tool, which otherwise
// has only the model-supplied "project" argument to go on. A model that names
// another project must be refused: memory isolation is a boundary between
// projects, not a convention the model is trusted to keep.
type scopeProjectKey struct{}
type scopePersonalAccessKey struct{}

// WithPersonalMemory grants the trusted owner's conversation access to the
// personal stores, independently of its current project or working directory.
// It is a local entry capability, never a model argument or a delegation grant.
func WithPersonalMemory(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopePersonalAccessKey{}, true)
}

// WithoutPersonalMemory explicitly denies personal stores to non-owner asks,
// including project-free worker contexts. A nested denial revokes an outer grant.
func WithoutPersonalMemory(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopePersonalAccessKey{}, false)
}

func personalMemoryAccess(ctx context.Context) (allowed, explicit bool) {
	if ctx == nil {
		return false, false
	}
	allowed, explicit = ctx.Value(scopePersonalAccessKey{}).(bool)
	return allowed, explicit
}

// WithScopeProject pins the turn's project ("" for a project-free
// conversation) on the context. Pinning an empty project is meaningful: it
// says "this conversation has no project", so project-targeted memory calls
// are refused rather than defaulting to an arbitrary name.
func WithScopeProject(ctx context.Context, project string) context.Context {
	return context.WithValue(ctx, scopeProjectKey{}, project)
}

// ScopeProject reports the pinned project and whether a pin is present at
// all. Callers that did not pin a scope (tests, tooling outside an ask) get
// ok=false and keep the older explicit-argument behavior.
func ScopeProject(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(scopeProjectKey{}).(string)
	return v, ok
}
