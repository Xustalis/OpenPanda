// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// GET/POST /api/onboarding — the first-run wizard's persistence surface. The
// TUI walks language → terms → approval mode → model on first launch and
// records the outcome under the `ui` config section; the web console does
// the same through this endpoint so a browser-first install lands in the
// same configured state.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/version"
)

// onboardingState is what the wizard needs to decide whether to open and
// what to prefill.
type onboardingState struct {
	Locale        string `json:"locale"`
	TermsAccepted bool   `json:"terms_accepted"`
	// TermsVersion lets the wizard re-show the license step after a terms
	// change — MIT-era installs hold terms_accepted without a version.
	TermsVersion int `json:"terms_version"`
	// TermsAcceptedAt is the UTC RFC3339 timestamp of the current consent,
	// mirrored into the signed audit chain as tamper-evident evidence.
	TermsAcceptedAt string `json:"terms_accepted_at"`
	Onboarded       bool   `json:"onboarded"`
	ApprovalMode    string `json:"approval_mode"`
	// ModelConfigured tells the wizard whether the model step can be
	// skipped without leaving chat unusable.
	ModelConfigured bool `json:"model_configured"`
}

func (h *handler) getOnboarding(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config unavailable"))
		return
	}
	writeJSON(w, h.onboardingState())
}

func (h *handler) onboardingState() onboardingState {
	mc := h.engineModel()
	st := onboardingState{
		// OAuth (model.auth) counts as configured — api_key stays empty there.
		ModelConfigured: strings.TrimSpace(mc.BaseURL) != "" && (mc.NoAuth || strings.TrimSpace(mc.APIKey) != "" || strings.TrimSpace(mc.Auth) != ""),
	}
	h.readCfg(func(c *config.Config) {
		st.Locale = c.UI.Locale
		st.TermsAccepted = c.UI.TermsAccepted
		st.TermsVersion = c.UI.TermsVersion
		st.TermsAcceptedAt = c.UI.TermsAcceptedAt
		st.Onboarded = c.UI.Onboarded
		st.ApprovalMode = c.Approval.NormalizedMode()
	})
	return st
}

type onboardingRequest struct {
	Locale        *string `json:"locale,omitempty"`
	TermsAccepted *bool   `json:"terms_accepted,omitempty"`
	Onboarded     *bool   `json:"onboarded,omitempty"`
	ApprovalMode  *string `json:"approval_mode,omitempty"`
}

func (h *handler) postOnboarding(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config unavailable"))
		return
	}
	var req onboardingRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Locale != nil {
		loc := strings.TrimSpace(*req.Locale)
		switch loc {
		case "", "en", "zh-CN", "ja", "es", "de":
		default:
			writeErr(w, http.StatusBadRequest, errors.New("unsupported locale"))
			return
		}
		if err := h.mutateCfgErr(func(c *config.Config) error {
			if h.configPath != "" {
				if err := config.UpdateSectionField(h.configPath, []string{"ui"}, "locale", loc); err != nil {
					return err
				}
			}
			c.UI.Locale = loc
			return nil
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.TermsAccepted != nil {
		var accepted bool
		var ts, who string
		if err := h.mutateCfgErr(func(c *config.Config) error {
			if h.configPath != "" {
				if err := config.UpdateSectionFieldBool(h.configPath, []string{"ui"}, "terms_accepted", *req.TermsAccepted); err != nil {
					return err
				}
				// Accepting stamps the current terms revision so a later
				// license change can distinguish stale consent.
				if *req.TermsAccepted {
					if err := config.UpdateSectionFieldInt(h.configPath, []string{"ui"}, "terms_version", config.TermsVersionCurrent); err != nil {
						return err
					}
					ts = time.Now().UTC().Format(time.RFC3339)
					if err := config.UpdateSectionField(h.configPath, []string{"ui"}, "terms_accepted_at", ts); err != nil {
						return err
					}
				}
			}
			c.UI.TermsAccepted = *req.TermsAccepted
			if *req.TermsAccepted {
				c.UI.TermsVersion = config.TermsVersionCurrent
				if ts == "" {
					// In-memory config path (no configPath): still stamp.
					ts = time.Now().UTC().Format(time.RFC3339)
				}
				c.UI.TermsAcceptedAt = ts
				accepted = true
				who = "local"
				if strings.TrimSpace(c.Node.Name) != "" {
					who = c.Node.Name
				}
			}
			return nil
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if accepted {
			h.recordTermsAudit(who)
		}
	}
	if req.ApprovalMode != nil {
		mode := strings.TrimSpace(*req.ApprovalMode)
		switch mode {
		case config.ApprovalModeAlways, config.ApprovalModeOnRequest, config.ApprovalModeNever:
		default:
			writeErr(w, http.StatusBadRequest, errors.New("approval_mode must be always, on-request, or never"))
			return
		}
		if err := h.mutateCfgErr(func(c *config.Config) error {
			if h.configPath != "" {
				if err := config.UpdateSectionField(h.configPath, []string{"approval"}, "mode", mode); err != nil {
					return err
				}
			}
			c.Approval.Mode = mode
			return nil
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Onboarded != nil {
		if err := h.mutateCfgErr(func(c *config.Config) error {
			if h.configPath != "" {
				if err := config.UpdateSectionFieldBool(h.configPath, []string{"ui"}, "onboarded", *req.Onboarded); err != nil {
					return err
				}
			}
			c.UI.Onboarded = *req.Onboarded
			return nil
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, h.onboardingState())
}

// recordTermsAudit appends the consent to the signed audit chain so the
// acceptance is provable later (`panda audit verify`, GET /api/audit) even
// if config.yaml is edited afterwards. Best effort: without an open store
// the config timestamp remains the only record.
func (h *handler) recordTermsAudit(who string) {
	if h.db == nil {
		return
	}
	_ = core.NewSignedAudit(h.db).Record(context.Background(), security.Entry{
		Who:    who,
		What:   "terms:accept",
		Target: fmt.Sprintf("terms_version:%d", config.TermsVersionCurrent),
		Result: "ok",
		Detail: "via=web build=" + version.Version,
	})
}
