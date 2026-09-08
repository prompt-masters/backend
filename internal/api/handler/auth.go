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
		"data": dto.UserFromDomain(reg.User),
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
		"data": dto.LoginResponse{
			User:         dto.UserFromDomain(result.User),
			AccessToken:  result.AccessToken,
			RefreshToken: result.RefreshToken,
		},
	})
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req dto.RefreshRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{
		"data": dto.LoginResponse{
			User:         dto.UserFromDomain(result.User),
			AccessToken:  result.AccessToken,
			RefreshToken: result.RefreshToken,
		},
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

	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.UserFromDomain(user)})
}

func (h *AuthHandler) Profile(w http.ResponseWriter, r *http.Request) {
	h.Me(w, r)
}

func (h *AuthHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.WriteError(w, http.StatusUnauthorized, "A valid access token is required.")
		return
	}

	var req dto.UpdateProfileRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	user, err := h.svc.UpdateProfile(r.Context(), userID, service.UpdateProfileInput{
		Username: req.Username,
	})
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.UserFromDomain(user)})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	var req dto.RefreshRequest
	if err := response.DecodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.svc.Logout(r.Context(), req.RefreshToken); err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	user, err := h.svc.Me(r.Context(), userID)
	if err != nil {
		response.WriteDomainError(w, h.logger, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": dto.UserFromDomain(user)})
}
