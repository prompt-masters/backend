// Package middleware contains HTTP middleware shared by the API routes.
package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/util"
)

type contextKey struct{}

var userIDKey = contextKey{}

// RequireAuth rejects requests without a valid Bearer token and stores the
// authenticated user ID in the request context.
func RequireAuth(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := userIDFromRequest(r, secret)
		if err != nil {
			response.WriteError(w, http.StatusUnauthorized, "A valid access token is required.")
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserIDFromContext returns the authenticated user ID set by RequireAuth.
func UserIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	userID, ok := ctx.Value(userIDKey).(uuid.UUID)
	return userID, ok
}

func userIDFromRequest(r *http.Request, secret string) (uuid.UUID, error) {
	header := r.Header.Get("Authorization")
	value, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || value == "" {
		return uuid.Nil, util.ErrInvalidToken
	}

	return util.ParseJWT(secret, value)
}
