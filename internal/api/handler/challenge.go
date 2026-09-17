package handler

import (
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/api/dto"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/service"
)

type ChallengeHandler struct {
	svc    *service.ChallengeService
	logger *log.Logger
}

func NewChallengeHandler(svc *service.ChallengeService, logger *log.Logger) *ChallengeHandler {
	return &ChallengeHandler{svc: svc, logger: logger}
}

func (h *ChallengeHandler) List(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	challenges, err := h.svc.List(r.Context(), service.ListChallengesInput{
		Category:   query.Get("category"),
		Difficulty: query.Get("difficulty"),
	})
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.ChallengesFromDomain(challenges)})
}

func (h *ChallengeHandler) Get(w http.ResponseWriter, r *http.Request) {
	// A malformed ID cannot name an existing challenge, so it is a 404 too.
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, domain.ErrNotFound)
		return
	}

	challenge, err := h.svc.Get(r.Context(), id)
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.ChallengeFromDomain(challenge)})
}

func (h *ChallengeHandler) Categories(w http.ResponseWriter, r *http.Request) {
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": h.svc.Categories()})
}
