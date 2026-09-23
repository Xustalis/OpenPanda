package panel

// GET/POST /api/onboarding — the first-run wizard's persistence surface. The
// TUI walks language → terms → approval mode → model on first launch and
// records the outcome under the `ui` config section; the web console does
// the same through this endpoint so a browser-first install lands in the
// same configured state.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// onboardingState is what the wizard needs to decide whether to open and
// what to prefill.
type onboardingState struct {
	Locale        string `json:"locale"`
	TermsAccepted bool   `json:"terms_accepted"`
	Onboarded     bool   `json:"onboarded"`
	ApprovalMode  string `json:"approval_mode"`
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
	return onboardingState{
		Locale:          h.cfg.UI.Locale,
		TermsAccepted:   h.cfg.UI.TermsAccepted,
		Onboarded:       h.cfg.UI.Onboarded,
		ApprovalMode:    h.cfg.Approval.NormalizedMode(),
		ModelConfigured: strings.TrimSpace(mc.BaseURL) != "" && (mc.NoAuth || strings.TrimSpace(mc.APIKey) != ""),
	}
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
		if err := config.UpdateSectionField(h.configPath, []string{"ui"}, "locale", loc); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.cfg.UI.Locale = loc
	}
	if req.TermsAccepted != nil {
		if err := config.UpdateSectionFieldBool(h.configPath, []string{"ui"}, "terms_accepted", *req.TermsAccepted); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.cfg.UI.TermsAccepted = *req.TermsAccepted
	}
	if req.ApprovalMode != nil {
		mode := strings.TrimSpace(*req.ApprovalMode)
		switch mode {
		case config.ApprovalModeAlways, config.ApprovalModeOnRequest, config.ApprovalModeNever:
		default:
			writeErr(w, http.StatusBadRequest, errors.New("approval_mode must be always, on-request, or never"))
			return
		}
		if err := config.UpdateSectionField(h.configPath, []string{"approval"}, "mode", mode); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.cfg.Approval.Mode = mode
	}
	if req.Onboarded != nil {
		if err := config.UpdateSectionFieldBool(h.configPath, []string{"ui"}, "onboarded", *req.Onboarded); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.cfg.UI.Onboarded = *req.Onboarded
	}
	writeJSON(w, h.onboardingState())
}
