package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHealth(t *testing.T) {
	up := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("connection refused") }

	tests := []struct {
		name       string
		checks     []HealthCheck
		wantCode   int
		wantStatus string
		wantChecks map[string]string
	}{
		{
			name:       "everything up",
			checks:     []HealthCheck{{Name: "postgres", Critical: true, Check: up}, {Name: "redis", Check: up}},
			wantCode:   http.StatusOK,
			wantStatus: "ok",
			wantChecks: map[string]string{"postgres": "up", "redis": "up"},
		},
		{
			name:       "redis down degrades",
			checks:     []HealthCheck{{Name: "postgres", Critical: true, Check: up}, {Name: "redis", Check: down}},
			wantCode:   http.StatusOK,
			wantStatus: "degraded",
			wantChecks: map[string]string{"postgres": "up", "redis": "down"},
		},
		{
			name:       "postgres down is unavailable",
			checks:     []HealthCheck{{Name: "postgres", Critical: true, Check: down}, {Name: "redis", Check: down}},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: "unavailable",
			wantChecks: map[string]string{"postgres": "down", "redis": "down"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthHandler(tt.checks, log.New(io.Discard, "", 0))
			rec := httptest.NewRecorder()
			h.Health(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

			if rec.Code != tt.wantCode {
				t.Errorf("status code = %d, want %d", rec.Code, tt.wantCode)
			}
			var body struct {
				Status string            `json:"status"`
				Checks map[string]string `json:"checks"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if body.Status != tt.wantStatus || !reflect.DeepEqual(body.Checks, tt.wantChecks) {
				t.Errorf("body = %+v, want status %q checks %v", body, tt.wantStatus, tt.wantChecks)
			}
		})
	}
}
