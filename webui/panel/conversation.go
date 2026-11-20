// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/conversation"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func (h *handler) conversationService(w http.ResponseWriter, r *http.Request) *askengine.ConversationService {
	eng := h.currentEngine()
	if eng == nil || h.sessions == nil {
		writeErr(w, 503, errors.New("conversation service unavailable"))
		return nil
	}
	if _, err := h.sessions.Get(r.PathValue("id")); err != nil {
		writeConversationError(w, err)
		return nil
	}
	return eng.Conversations(h.sessions)
}

func (h *handler) conversationState(w http.ResponseWriter, r *http.Request) {
	service := h.conversationService(w, r)
	if service == nil {
		return
	}
	state, err := service.State(r.Context(), r.PathValue("id"))
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, state)
}

func (h *handler) conversationControl(w http.ResponseWriter, r *http.Request) {
	service := h.conversationService(w, r)
	if service == nil {
		return
	}
	var req struct {
		Action      string `json:"action"`
		Planning    *bool  `json:"planning"`
		GoalID      string `json:"goal_id"`
		Objective   string `json:"objective"`
		TokenBudget *int64 `json:"token_budget"`
		Version     int64  `json:"version"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 250000)).Decode(&req) != nil {
		writeErr(w, 400, errors.New("invalid conversation control"))
		return
	}
	sid := r.PathValue("id")
	var err error
	switch req.Action {
	case "mode":
		if req.Planning == nil {
			writeErr(w, http.StatusBadRequest, errors.New("planning state required"))
			return
		} else {
			err = service.SetPlanning(r.Context(), sid, *req.Planning)
		}
	case "confirm":
		_, err = service.ConfirmPlan(r.Context(), sid, req.Version)
	case "create":
		_, err = service.Create(r.Context(), sid, req.Objective, req.TokenBudget)
	case "edit", "pause", "resume", "clear", "stop":
		err = service.Control(r.Context(), sid, req.GoalID, req.Action, req.Objective, req.TokenBudget)
	default:
		writeErr(w, http.StatusBadRequest, errors.New("unknown conversation action"))
		return
	}
	if err != nil {
		writeConversationError(w, err)
		return
	}
	state, err := service.State(r.Context(), sid)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, state)
}

func writeConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conversation.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err)
	case errors.Is(err, conversation.ErrConflict), errors.Is(err, conversation.ErrWaiting):
		writeErr(w, http.StatusConflict, err)
	case errors.Is(err, sessions.ErrNotFound):
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
	default:
		writeErr(w, http.StatusInternalServerError, errors.New("conversation operation failed"))
	}
}
