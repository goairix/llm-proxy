package middleware

import (
	"net/http"
	"time"

	httpresponse "github.com/goairix/llm-proxy/internal/interfaces/http/response"
	"go.uber.org/zap"
)

// ControlPlaneLogging records only safe, low-cardinality control-plane metadata.
func ControlPlaneLogging(logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			var requestBytes int64
			if r.ContentLength > 0 {
				requestBytes = r.ContentLength
			}
			writer := newResponseWriter(w)
			next.ServeHTTP(writer, r)
			pattern := r.Pattern
			if pattern == "" {
				pattern = "control_plane"
			}
			logger.Info("control_plane_request",
				zap.String("route_pattern", pattern),
				zap.Int("status", writer.status),
				zap.Int64("latency_ms", time.Since(start).Milliseconds()),
				zap.Int64("req_bytes", requestBytes),
				zap.Int("resp_bytes", writer.bytes),
				zap.String("request_id", httpresponse.RequestID(r.Context())),
			)
		})
	}
}
