package response

import (
	"errors"
	"fmt"
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
		WriteJSON(w, http.StatusBadRequest, Envelope{
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
	case errors.Is(err, domain.ErrInvalidRefreshToken):
		WriteError(w, http.StatusUnauthorized, "A valid refresh token is required.")
	case errors.Is(err, domain.ErrEmailNotVerified):
		WriteError(w, http.StatusForbidden, "Please verify your email address before signing in.")
	case errors.Is(err, domain.ErrNoEligibleChallenge):
		WriteError(w, http.StatusConflict, "No challenge matches the game's category and difficulty.")
	case errors.Is(err, domain.ErrNotGameHost):
		WriteError(w, http.StatusForbidden, "Only the host can do this.")
	case errors.Is(err, domain.ErrGameNotWaiting):
		WriteError(w, http.StatusConflict, "This game has already started or ended.")
	case errors.Is(err, domain.ErrGameNotCancellable):
		WriteError(w, http.StatusConflict, "This game has already ended.")
	case errors.Is(err, domain.ErrGameFull):
		WriteError(w, http.StatusConflict, "This game is full.")
	case errors.Is(err, domain.ErrAlreadyJoined):
		WriteError(w, http.StatusConflict, "You have already joined this game.")
	case errors.Is(err, domain.ErrNotInGame):
		WriteError(w, http.StatusConflict, "You are not a player in this game.")
	case errors.Is(err, domain.ErrNotEnoughPlayers):
		WriteError(w, http.StatusConflict, fmt.Sprintf("At least %d players are needed to start.", domain.MinPlayers))
	case errors.Is(err, domain.ErrNotFound):
		WriteError(w, http.StatusNotFound, "The requested resource was not found.")
	case errors.Is(err, domain.ErrInvalidVerificationToken):
		WriteError(w, http.StatusBadRequest, "This verification link is not valid or has already been used.")
	case errors.Is(err, domain.ErrVerificationTokenExpired):
		WriteError(w, http.StatusGone, "This verification link has expired.")
	default:
		logger.Printf("internal error: %v", err)
		WriteError(w, http.StatusInternalServerError, "An unexpected error occurred.")
	}
}
