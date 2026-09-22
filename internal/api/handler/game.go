package handler

import (
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/api/dto"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/service"
)

// GameHandler serves the game room endpoints. Every route is registered
// behind RequireAuth; {id} is a game UUID or an active room code.
type GameHandler struct {
	svc    *service.GameService
	logger *log.Logger
}

func NewGameHandler(svc *service.GameService, logger *log.Logger) *GameHandler {
	return &GameHandler{svc: svc, logger: logger}
}

func (h *GameHandler) List(w http.ResponseWriter, r *http.Request) {
	games, err := h.svc.List(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.GameSummariesFromDomain(games)})
}

func (h *GameHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	var req dto.GameSettingsRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	game, err := h.svc.Create(r.Context(), userID, settingsInput(req))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Envelope{"data": dto.GameFromDomain(game)})
}

func (h *GameHandler) Get(w http.ResponseWriter, r *http.Request) {
	game, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.GameFromDomain(game)})
}

func (h *GameHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	var req dto.GameSettingsRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	game, err := h.svc.UpdateSettings(r.Context(), userID, r.PathValue("id"), settingsInput(req))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.GameFromDomain(game)})
}

func (h *GameHandler) Join(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	game, err := h.svc.Join(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.GameFromDomain(game)})
}

func (h *GameHandler) Leave(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	if err := h.svc.Leave(r.Context(), userID, r.PathValue("id")); err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *GameHandler) Start(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	game, err := h.svc.Start(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.GameFromDomain(game)})
}

func (h *GameHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	if err := h.svc.Cancel(r.Context(), userID, r.PathValue("id")); err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func settingsInput(req dto.GameSettingsRequest) service.GameSettingsInput {
	return service.GameSettingsInput{
		Rounds:       req.Rounds,
		TimePerRound: req.TimePerRound,
		Difficulty:   req.Difficulty,
		Category:     req.Category,
		AIModel:      req.AIModel,
		MaxPlayers:   req.MaxPlayers,
	}
}
