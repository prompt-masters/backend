package handler

import (
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/api/dto"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/service"
)

// PromptTestHandler serves a player's prompt tests. Routes are registered
// behind RequireAuth; {id} is a round ID. The player always comes from the
// access token, so no request can reach another player's tests.
type PromptTestHandler struct {
	svc    *service.PromptTestService
	logger *log.Logger
}

func NewPromptTestHandler(svc *service.PromptTestService, logger *log.Logger) *PromptTestHandler {
	return &PromptTestHandler{svc: svc, logger: logger}
}

func (h *PromptTestHandler) Start(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	var req dto.PromptTestRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	test, err := h.svc.Start(r.Context(), userID, r.PathValue("id"), req.Prompt)
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusAccepted, response.Envelope{"data": dto.PromptTestStartedFromDomain(test)})
}

func (h *PromptTestHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	tests, err := h.svc.List(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": response.Envelope{"tests": dto.PromptTestsFromDomain(tests)}})
}
