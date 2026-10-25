package handler

import (
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/api/dto"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/service"
)

// RoundHandler serves final submissions, scores and judging retries. Routes
// are registered behind RequireAuth; the player comes from the access token.
type RoundHandler struct {
	svc    *service.RoundService
	logger *log.Logger
}

func NewRoundHandler(svc *service.RoundService, logger *log.Logger) *RoundHandler {
	return &RoundHandler{svc: svc, logger: logger}
}

// Submit answers 201 for a new submission and 200 for a repeat of the
// player's existing one.
func (h *RoundHandler) Submit(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	var req dto.SubmitRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub, created, err := h.svc.Submit(r.Context(), userID, r.PathValue("id"), req.TestID)
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	response.WriteJSON(w, status, response.Envelope{"data": dto.SubmissionFromDomain(sub)})
}

func (h *RoundHandler) GetSubmission(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	sub, err := h.svc.Get(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.SubmissionFromDomain(sub)})
}

func (h *RoundHandler) GetScore(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	score, err := h.svc.Score(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.ScoreFromDomain(score)})
}

func (h *RoundHandler) RetryJudging(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	if err := h.svc.RetryJudging(r.Context(), userID, r.PathValue("id")); err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
