// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

// Turning a candidate into a registry row is the one place discovery meets
// config: it resolves the credential reference, borrows the catalogue's
// endpoint and limits for a known provider, and keeps a relay's own address
// when it differs. Everything above this line is read-only; everything below it
// writes.

import (
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

// Row builds the ModelConfig a candidate would be registered as.
//
// The key is resolved here and nowhere else: a candidate carries a reference
// ("env:DASHSCOPE_API_KEY", "codex:custom"), and this is the boundary where
// that becomes a secret the entry client can use. The row that comes back is
// what the config file will hold, so it is also the last chance to refuse a
// candidate that would be undialable.
func (c Candidate) Row(opt Options) (config.ModelConfig, error) {
	if !c.Importable {
		return config.ModelConfig{}, fmt.Errorf("%s: no endpoint this build can dial", c.Model)
	}
	// An `env:` reference is stored as a reference, not resolved: copying the
	// secret into config.yaml is the one thing this registry's own header tells
	// people not to do. A literal (a local proxy's placeholder) has to be
	// written, because nothing else holds it.
	key, keyEnv := "", ""
	if name, ok := strings.CutPrefix(c.KeyRef, "env:"); ok {
		keyEnv = name
	} else if c.KeyRef != "" {
		key = ResolveKey(c.KeyRef, opt)
	}

	mc := config.ModelConfig{
		Provider:      c.Provider,
		APIType:       c.APIType,
		BaseURL:       strings.TrimRight(c.BaseURL, "/"),
		Model:         c.Model,
		APIKey:        key,
		KeyEnv:        keyEnv,
		ContextWindow: c.ContextWindow,
		NoAuth:        c.NoAuth || (c.Auth == "" && key == "" && keyEnv == ""),
	}

	// A known provider owns its endpoint, dialect and limits: borrow them so the
	// row gets the catalogue's token policy instead of a bare URL. A candidate
	// that names a different address (a relay behind a known vendor) keeps its
	// own.
	if c.Provider != "" && c.Provider != "custom" {
		if p, ok := providers.Lookup(c.Provider); ok {
			if built, ok := providers.ModelConfig(p.ID, c.Model, key); ok {
				mc = built
				mc.KeyEnv = keyEnv
				if c.ContextWindow > 0 {
					mc.ContextWindow = c.ContextWindow
				}
				mc.NoAuth = c.NoAuth || (c.Auth == "" && mc.APIKey == "" && mc.KeyEnv == "")
				if c.BaseURL != "" && !sameURL(c.BaseURL, built.BaseURL) {
					mc.BaseURL = strings.TrimRight(c.BaseURL, "/")
					mc.APIType = c.APIType
				}
			}
		}
	}
	if mc.Model == "" {
		return config.ModelConfig{}, fmt.Errorf("candidate has no model id")
	}
	// Applied last: the catalogue branch above replaces the whole row when the
	// provider is a known one, and a subscription credential must survive that.
	mc.Auth = c.Auth
	return mc, nil
}

func sameURL(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}
