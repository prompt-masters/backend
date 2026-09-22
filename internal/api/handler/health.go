package handler

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/prompt-masters/backend/internal/api/response"
)

const healthCheckTimeout = 2 * time.Second

// HealthCheck probes one dependency. A failing critical check makes the
// service unavailable; a failing non-critical one only degrades it.
type HealthCheck struct {
	Name     string
	Critical bool
	Check    func(ctx context.Context) error
}

type HealthHandler struct {
	checks []HealthCheck
	logger *log.Logger
}

func NewHealthHandler(checks []HealthCheck, logger *log.Logger) *HealthHandler {
	return &HealthHandler{checks: checks, logger: logger}
}

// Health reports "ok" when every dependency is up, "degraded" (still 200) when
// only non-critical ones are down, and "unavailable" (503) otherwise.
func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	status, code := "ok", http.StatusOK
	results := make(map[string]string, len(h.checks))

	for _, c := range h.checks {
		ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
		err := c.Check(ctx)
		cancel()

		if err == nil {
			results[c.Name] = "up"
			continue
		}
		results[c.Name] = "down"
		h.logger.Printf("health check failed: dependency=%s critical=%t error=%v", c.Name, c.Critical, err)
		if c.Critical {
			status, code = "unavailable", http.StatusServiceUnavailable
		} else if status == "ok" {
			status = "degraded"
		}
	}

	response.WriteJSON(w, code, response.Envelope{"status": status, "checks": results})
}
