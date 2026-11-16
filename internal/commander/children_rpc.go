// SPDX-License-Identifier: AGPL-3.0-or-later
package commander

import "context"

type childRPCKey struct{}
type childRPCConfig struct{ url, token, attempt string }

// WithChildRPC supplies an ephemeral capability to the MCP subprocess. Never
// write the token into project files, activity transcripts or wire payloads.
func WithChildRPC(ctx context.Context, url, token, attempt string) context.Context {
	return context.WithValue(ctx, childRPCKey{}, childRPCConfig{url, token, attempt})
}
func childRPCEnv(ctx context.Context, env []string) []string {
	cfg, ok := ctx.Value(childRPCKey{}).(childRPCConfig)
	if !ok {
		return env
	}
	out := append([]string(nil), env...)
	return append(out, "PANDA_CHILD_RPC_URL="+cfg.url, "PANDA_CHILD_RPC_TOKEN="+cfg.token, "PANDA_ATTEMPT_ID="+cfg.attempt)
}

type immediateCoordinationKey struct{}
type coordinationModeKey struct{}

func WithImmediateCoordination(ctx context.Context) context.Context {
	return context.WithValue(ctx, immediateCoordinationKey{}, true)
}
func needsImmediateCoordination(ctx context.Context) bool {
	value, _ := ctx.Value(immediateCoordinationKey{}).(bool)
	return value
}
func coordinationMode(ctx context.Context) string {
	mode, _ := ctx.Value(coordinationModeKey{}).(string)
	if mode == "" {
		return "ROUND_END"
	}
	return mode
}
func withCoordinationAccess(ctx context.Context, ready bool) context.Context {
	mode := "ROUND_END"
	if _, bound := ctx.Value(childRPCKey{}).(childRPCConfig); ready && bound && !Restricted(ctx) {
		mode = "IMMEDIATE"
	}
	return context.WithValue(ctx, coordinationModeKey{}, mode)
}
