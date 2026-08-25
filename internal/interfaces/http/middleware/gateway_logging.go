package middleware

import (
	"net/http"
	"strings"
	"time"

	httpresponse "github.com/goairix/llm-proxy/internal/interfaces/http/response"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// GatewayLogging records fixed, low-cardinality data-plane metadata.
func GatewayLogging(logger *zap.Logger, protocol, endpoint string) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			start := time.Now()
			virtualKey := gatewayVirtualKey(request, protocol)
			var requestBytes int64
			if request.ContentLength > 0 {
				requestBytes = request.ContentLength
			}

			captured := &gatewayLogResponseWriter{delegate: writer, status: http.StatusOK}
			var downstream http.ResponseWriter = captured
			if flusher, ok := writer.(http.Flusher); ok {
				downstream = &gatewayLogFlushingWriter{gatewayLogResponseWriter: captured, flusher: flusher}
			}
			next.ServeHTTP(downstream, request)

			fields := []zap.Field{
				zap.String("protocol", protocol), zap.String("endpoint", endpoint),
				zap.String("virtual_key", maskAPIKey(virtualKey)),
				zap.Int("status", captured.status), zap.Int64("latency_ms", time.Since(start).Milliseconds()),
				zap.Int64("req_bytes", requestBytes), zap.Int("resp_bytes", captured.bytes),
				zap.String("request_id", httpresponse.RequestID(request.Context())),
			}
			spanContext := trace.SpanContextFromContext(request.Context())
			if spanContext.IsValid() {
				fields = append(fields,
					zap.String("trace_id", spanContext.TraceID().String()),
					zap.String("span_id", spanContext.SpanID().String()),
				)
			}
			logger.Info("gateway_request", fields...)
		})
	}
}

func gatewayVirtualKey(request *http.Request, protocol string) string {
	if protocol == "anthropic" {
		if value := strings.TrimSpace(request.Header.Get("x-api-key")); value != "" {
			return value
		}
	}
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

type gatewayLogResponseWriter struct {
	delegate    http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (w *gatewayLogResponseWriter) Header() http.Header { return w.delegate.Header() }
func (w *gatewayLogResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.delegate.WriteHeader(status)
}
func (w *gatewayLogResponseWriter) Write(value []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	written, err := w.delegate.Write(value)
	w.bytes += written
	return written, err
}

type gatewayLogFlushingWriter struct {
	*gatewayLogResponseWriter
	flusher http.Flusher
}

func (w *gatewayLogFlushingWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.flusher.Flush()
}
