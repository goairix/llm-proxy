package middleware

import (
	"net/http"
	"net/netip"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// maxErrorBodyCapture is the maximum number of bytes to capture from an error
// response body for logging purposes.
const maxErrorBodyCapture = 4096

// responseWriter wraps http.ResponseWriter to capture the status code and
// the number of bytes written in the response body.
type responseWriter struct {
	http.ResponseWriter
	status    int
	bytes     int
	errorBody []byte // captured response body when status >= 400
}

// newResponseWriter returns a responseWriter that defaults to status 200.
func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, status: http.StatusOK}
}

// WriteHeader captures the status code and delegates to the underlying writer.
func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Write captures the number of bytes written and delegates to the underlying writer.
// For error responses (status >= 400), it also captures the response body up to maxErrorBodyCapture.
func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	if rw.status >= 400 && len(rw.errorBody) < maxErrorBodyCapture {
		remaining := maxErrorBodyCapture - len(rw.errorBody)
		if remaining > n {
			remaining = n
		}
		rw.errorBody = append(rw.errorBody, b[:remaining]...)
	}
	return n, err
}

// Flush implements http.Flusher to support SSE streaming.
// It delegates to the underlying ResponseWriter if it also implements http.Flusher.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// clientIP extracts the real client IP from the request.
// Priority: X-Forwarded-For (first entry) > X-Real-IP > RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// RemoteAddr is "host:port" — strip the port.
	if addr, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return addr.Addr().String()
	}
	return r.RemoteAddr
}

// maskAPIKey returns the last 4 characters of the key, prefixed with "****".
// If key is shorter than 4 chars, returns "****".
func maskAPIKey(key string) string {
	if len(key) < 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}

// extractProvider returns "openai" or "anthropic" based on the URL path prefix.
// Returns "unknown" for unrecognized paths.
func extractProvider(path string) string {
	switch {
	case strings.HasPrefix(path, "/openai"):
		return "openai"
	case strings.HasPrefix(path, "/anthropic"):
		return "anthropic"
	default:
		return "unknown"
	}
}

// Logging returns an HTTP middleware that logs each request using zap.
// Log fields: provider, path, api_key (masked last 4), status, latency_ms, req_bytes, resp_bytes.
func Logging(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Resolve api key: prefer x-api-key, fall back to Authorization.
			apiKey := r.Header.Get("x-api-key")
			if apiKey == "" {
				auth := r.Header.Get("Authorization")
				apiKey = strings.TrimPrefix(auth, "Bearer ")
			}

			// Determine request body size; use 0 when unknown (-1).
			var reqBytes int64
			if r.ContentLength > 0 {
				reqBytes = r.ContentLength
			}

			rw := newResponseWriter(w)
			next.ServeHTTP(rw, r)

			latencyMs := time.Since(start).Milliseconds()
			provider := extractProvider(r.URL.Path)

			fields := []zap.Field{
				zap.String("provider", provider),
				zap.String("path", r.URL.Path),
				zap.String("client_ip", clientIP(r)),
				zap.String("api_key", maskAPIKey(apiKey)),
				zap.Int("status", rw.status),
				zap.Int64("latency_ms", latencyMs),
				zap.Int64("req_bytes", reqBytes),
				zap.Int("resp_bytes", rw.bytes),
			}
			spanContext := trace.SpanContextFromContext(r.Context())
			if spanContext.IsValid() {
				fields = append(fields,
					zap.String("trace_id", spanContext.TraceID().String()),
					zap.String("span_id", spanContext.SpanID().String()),
				)
			}

			switch {
			case rw.status >= 500:
				if len(rw.errorBody) > 0 {
					fields = append(fields, zap.String("upstream_error", string(rw.errorBody)))
				}
				logger.Error("upstream_server_error", fields...)
			case rw.status >= 400:
				if len(rw.errorBody) > 0 {
					fields = append(fields, zap.String("upstream_error", string(rw.errorBody)))
				}
				logger.Warn("upstream_client_error", fields...)
			default:
				logger.Info("request", fields...)
			}
		})
	}
}
