package handler

import (
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/api/dto"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/service"
)

// LiveStateHandler serves live game state. Every route is registered behind
// RequireAuth, and the acting user is always the authenticated one: no route
// accepts a user ID.
type LiveStateHandler struct {
	svc    *service.LiveStateService
	logger *log.Logger
}

func NewLiveStateHandler(svc *service.LiveStateService, logger *log.Logger) *LiveStateHandler {
	return &LiveStateHandler{svc: svc, logger: logger}
}

func (h *LiveStateHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	state, err := h.svc.GetLiveState(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.LiveStateFromDomain(state)})
}

func (h *LiveStateHandler) SetReady(w http.ResponseWriter, r *http.Request) {
	h.writeReady(w, r, true)
}

func (h *LiveStateHandler) UnsetReady(w http.ResponseWriter, r *http.Request) {
	h.writeReady(w, r, false)
}

func (h *LiveStateHandler) writeReady(w http.ResponseWriter, r *http.Request, ready bool) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	if err := h.svc.SetReady(r.Context(), userID, r.PathValue("id"), ready); err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LiveStateHandler) GetDraft(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	draft, err := h.svc.GetDraft(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.DraftFromDomain(draft)})
}

func (h *LiveStateHandler) SaveDraft(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	var req dto.DraftRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	draft, err := h.svc.SaveDraft(r.Context(), userID, r.PathValue("id"), req.Content)
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.DraftFromDomain(draft)})
}
