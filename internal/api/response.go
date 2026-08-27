package api

import (
	"encoding/json"
	"net/http"
)

type Envelope map[string]any

func WriteJSON(w http.ResponseWriter, status int, data Envelope) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(data)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, Envelope{"error": msg})
}

func WriteSuccess(w http.ResponseWriter, data any) error {
	return WriteJSON(w, http.StatusOK, Envelope{"data": data})
}
