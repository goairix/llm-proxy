package health

import (
	"encoding/json"
	"net/http"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"go.uber.org/zap"
)

// Handler serves liveness and readiness endpoints.
type Handler struct {
	readiness *appRuntime.Readiness
	logger    *zap.Logger
}

// New creates a health handler.
func New(readiness *appRuntime.Readiness, logger *zap.Logger) *Handler {
	return &Handler{readiness: readiness, logger: logger}
}

// Health reports process liveness.
func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	h.writeStatus(w, http.StatusOK, "ok")
}

// Ready reports whether the server is ready to serve traffic.
func (h *Handler) Ready(w http.ResponseWriter, _ *http.Request) {
	if !h.readiness.Ready() {
		h.writeStatus(w, http.StatusServiceUnavailable, "not_ready")
		return
	}
	h.writeStatus(w, http.StatusOK, "ready")
}

func (h *Handler) writeStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
	}{Status: status}); err != nil {
		h.logger.Error("write health response", zap.Error(err))
	}
}
