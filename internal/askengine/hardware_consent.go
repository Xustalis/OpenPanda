// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import "context"

type hardwarePlanConsentKey struct{}

// WithHardwarePlanConsent scopes explicit plan-stage authorization to one
// authenticated hardware request. The marker grants nothing on its own: the
// request must also supply authorize=true and pass the normal approval policy.
// Ordinary asks deliberately keep their existing per-stage consent behavior.
func WithHardwarePlanConsent(ctx context.Context) context.Context {
	return context.WithValue(ctx, hardwarePlanConsentKey{}, true)
}

func hardwarePlanConsent(ctx context.Context) bool {
	marked, _ := ctx.Value(hardwarePlanConsentKey{}).(bool)
	return marked
}
