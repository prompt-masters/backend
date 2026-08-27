package response

import (
	"errors"
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/service"
)

// WriteDomainError maps a service or domain error onto the matching HTTP
// response, logging unexpected errors before answering with a 500.
func WriteDomainError(w http.ResponseWriter, logger *log.Logger, err error) {
	var vErr *service.ValidationError
	if errors.As(err, &vErr) {
		WriteJSON(w, http.StatusUnprocessableEntity, Envelope{
			"error":  "Validation failed",
			"fields": vErr.Fields,
		})
		return
	}

	switch {
	case errors.Is(err, domain.ErrEmailTaken):
		WriteError(w, http.StatusConflict, "That email address is already registered.")
	case errors.Is(err, domain.ErrUsernameTaken):
		WriteError(w, http.StatusConflict, "That username is already taken.")
	case errors.Is(err, domain.ErrInvalidCredentials):
		WriteError(w, http.StatusUnauthorized, "Incorrect email or password.")
	case errors.Is(err, domain.ErrEmailNotVerified):
		WriteError(w, http.StatusForbidden, "Please verify your email address before signing in.")
	case errors.Is(err, domain.ErrInvalidVerificationToken):
		WriteError(w, http.StatusBadRequest, "This verification link is not valid or has already been used.")
	case errors.Is(err, domain.ErrVerificationTokenExpired):
		WriteError(w, http.StatusGone, "This verification link has expired.")
	default:
		logger.Printf("internal error: %v", err)
		WriteError(w, http.StatusInternalServerError, "An unexpected error occurred.")
	}
}
