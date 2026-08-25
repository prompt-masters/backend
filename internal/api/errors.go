package api

import (
	"encoding/json"
	"log"
	"net/http"
)

// Error codes returned in the error_code field. They are part of the API
// contract: clients branch on these, not on the message.
const (
	CodeMalformedRequest      = "MALFORMED_REQUEST"
	CodeValidationError       = "VALIDATION_ERROR"
	CodeEmailAlreadyExists    = "EMAIL_ALREADY_EXISTS"
	CodeUsernameAlreadyExists = "USERNAME_ALREADY_EXISTS"
	CodeInvalidToken          = "INVALID_TOKEN"
	CodeInvalidCredentials    = "INVALID_CREDENTIALS"
	CodeTokenExpired          = "TOKEN_EXPIRED"
	CodeInternalError         = "INTERNAL_ERROR"
)

// errorResponse is the single shape every failure takes. Fields is populated
// only for validation errors.
type errorResponse struct {
	ErrorCode string            `json:"error_code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, logger *log.Logger, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already out, so there is nothing to tell the
		// client. Record it instead.
		logger.Printf("writing response body: %v", err)
	}
}

func writeError(w http.ResponseWriter, logger *log.Logger, status int, code, message string) {
	writeJSON(w, logger, status, errorResponse{ErrorCode: code, Message: message})
}

// writeInternalError reports a generic failure to the client and the real
// cause to the log. Internal errors carry connection strings, table names and
// user data, none of which belongs in a response.
func writeInternalError(w http.ResponseWriter, logger *log.Logger, cause error) {
	logger.Printf("internal error: %v", cause)
	writeError(w, logger, http.StatusInternalServerError, CodeInternalError, "An unexpected error occurred.")
}
