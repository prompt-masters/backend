// Package handler contains the HTTP handlers for the API.
package handler

import (
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/api/dto"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/service"
)

type AuthHandler struct {
	svc    *service.AuthService
	logger *log.Logger
}

func NewAuthHandler(svc *service.AuthService, logger *log.Logger) *AuthHandler {
	return &AuthHandler{svc: svc, logger: logger}
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req dto.RegisterRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	reg, err := h.svc.Register(r.Context(), service.RegisterInput{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}

	response.WriteJSON(w, http.StatusCreated, response.Envelope{
		"data": reg.User,
	})
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req dto.LoginRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.svc.Login(r.Context(), service.LoginInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{
		"data": dto.LoginResponse{User: result.User, Token: result.Token},
	})
}

func (h *AuthHandler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	tokenValue := r.URL.Query().Get("token")
	if tokenValue == "" {
		response.WriteError(w, http.StatusBadRequest, "This verification link is not valid.")
		return
	}

	user, err := h.svc.VerifyEmail(r.Context(), tokenValue)
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": user})
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	user, err := h.svc.Me(r.Context(), userID)
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": user})
}
