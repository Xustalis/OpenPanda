// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// A stored API key belongs to the endpoint it was saved for. Every panel path
// that can change a row's base_url while carrying the old key forward — the
// settings form, the connectivity test, reuse_from on add, and the edit form —
// goes through this check. Without it a token holder can re-point a row at
// their own host and read the credential out of the request headers.

import (
	"net/url"
	"strings"
)

// endpointHost returns the host a URL dials, for error messages. sameEndpointHost
// (models.go) decides whether two URLs share a credential's host.
func endpointHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}
