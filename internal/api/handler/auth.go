// Package handler contains the HTTP handlers for the API.
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"

	"github.com/prompt-masters/backend/internal/api/dto"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/service"
)

const maxBodySize = 16 << 10

type AuthHandler struct {
	svc    *service.AuthService
	logger *log.Logger
}

func NewAuthHandler(svc *service.AuthService, logger *log.Logger) *AuthHandler {
	return &AuthHandler{svc: svc, logger: logger}
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req dto.RegisterRequest
	if err := decodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	reg, err := h.svc.Register(r.Context(), service.RegisterInput{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		h.writeError(w, err)
		return
	}

	response.WriteJSON(w, http.StatusCreated, response.Envelope{
		"data": reg.User,
	})
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req dto.LoginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.svc.Login(r.Context(), service.LoginInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		h.writeError(w, err)
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
		h.writeError(w, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": user})
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())

	user, err := h.svc.Me(r.Context(), userID)
	if err != nil {
		h.writeError(w, err)
		return
	}

	response.WriteJSON(w, http.StatusOK, response.Envelope{"data": user})
}

func (h *AuthHandler) writeError(w http.ResponseWriter, err error) {
	var vErr *service.ValidationError
	if errors.As(err, &vErr) {
		response.WriteJSON(w, http.StatusUnprocessableEntity, response.Envelope{
			"error":  "Validation failed",
			"fields": vErr.Fields,
		})
		return
	}

	switch {
	case errors.Is(err, domain.ErrEmailTaken):
		response.WriteError(w, http.StatusConflict, "That email address is already registered.")
	case errors.Is(err, domain.ErrUsernameTaken):
		response.WriteError(w, http.StatusConflict, "That username is already taken.")
	case errors.Is(err, domain.ErrInvalidCredentials):
		response.WriteError(w, http.StatusUnauthorized, "Incorrect email or password.")
	case errors.Is(err, domain.ErrEmailNotVerified):
		response.WriteError(w, http.StatusForbidden, "Please verify your email address before signing in.")
	case errors.Is(err, domain.ErrInvalidVerificationToken):
		response.WriteError(w, http.StatusBadRequest, "This verification link is not valid or has already been used.")
	case errors.Is(err, domain.ErrVerificationTokenExpired):
		response.WriteError(w, http.StatusGone, "This verification link has expired.")
	default:
		h.logger.Printf("internal error: %v", err)
		response.WriteError(w, http.StatusInternalServerError, "An unexpected error occurred.")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := requireJSONContentType(r); err != nil {
		return err
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return describeDecodeError(err)
	}
	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func requireJSONContentType(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return errors.New("Content-Type must be application/json")
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	return nil
}

func describeDecodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		return errors.New("request body is not valid JSON")
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return errors.New("field " + typeErr.Field + " has the wrong type")
		}
		return errors.New("request body is not a JSON object")
	case errors.As(err, &maxBytesErr):
		return errors.New("request body is too large")
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("request body is empty or truncated")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return errors.New("request body contains an unrecognised field")
	default:
		return errors.New("request body could not be read")
	}
}
