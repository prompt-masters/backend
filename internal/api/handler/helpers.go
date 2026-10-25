package handler

import (
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/api/dto"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/service"
)

// HelperHandler serves the paid in-round helpers. Routes are registered
// behind RequireAuth; {id} is a round ID and the player comes from the token.
type HelperHandler struct {
	svc    *service.HelperService
	logger *log.Logger
}

func NewHelperHandler(svc *service.HelperService, logger *log.Logger) *HelperHandler {
	return &HelperHandler{svc: svc, logger: logger}
}

// Use returns the handler buying helper t. A retry must repeat the
// Idempotency-Key header; it then gets the first request's outcome.
func (h *HelperHandler) Use(t domain.HelperType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _ := middleware.UserIDFromContext(r.Context())

		var req dto.HelperRequest
		if err := response.DecodeJSON(w, r, &req); err != nil {
			response.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := h.svc.Use(r.Context(), userID, service.HelperRequest{
			Type: t, RoundRef: r.PathValue("id"), Prompt: req.Prompt, TestRef: req.TestID,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		if err != nil {
			response.WriteDomainError(w, h.logger, err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.HelperFromDomain(res.Usage, res.Balance)})
	}
}

func (h *HelperHandler) Overview(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	o, err := h.svc.Overview(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.HelperOverviewFromDomain(o.Balance, o.Prices, o.Usage)})
}
