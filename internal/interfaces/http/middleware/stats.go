package middleware

import (
	"net/http"
	"time"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
)

// statsResponseWriter captures status code and response bytes for metrics.
type statsResponseWriter struct {
	http.ResponseWriter
	status   int
	bytes    int
	observer appRuntime.UsageObserver
	writeErr error
}

func (w *statsResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statsResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	if n > 0 && w.observer != nil {
		w.observer.Observe(w.Header().Get("Content-Type"), b[:n])
	}
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	return n, err
}

func (w *statsResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Stats records per-request metrics and token usage.
// It must wrap the rate limiter so every request, including 429 responses, is counted.
func Stats(provider string, stats *dashboard.Stats, observers appRuntime.UsageObserverFactory) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stats.Total.Add(1)
			switch provider {
			case "openai":
				stats.OpenAI.Add(1)
			case "anthropic":
				stats.Anthropic.Add(1)
			}
			if r.ContentLength > 0 {
				stats.ReqBytes.Add(r.ContentLength)
			}

			start := time.Now()
			var observer appRuntime.UsageObserver
			if observers != nil {
				observer = observers(provider, r.Method, r.URL.Path)
			}
			srw := &statsResponseWriter{
				ResponseWriter: w,
				status:         http.StatusOK,
				observer:       observer,
			}
			next.ServeHTTP(srw, r)

			if observer != nil && srw.status >= 200 && srw.status < 300 {
				result := observer.Finish(srw.status, srw.writeErr)
				if result.Present {
					stats.AddTokenUsage(provider, result.Usage)
				} else {
					stats.AddMissingUsage(provider)
				}
			}

			stats.TotalLatencyMs.Add(time.Since(start).Milliseconds())
			stats.RespBytes.Add(int64(srw.bytes))

			switch srw.status {
			case http.StatusTooManyRequests:
				stats.RateLimited.Add(1)
			default:
				if srw.status >= 400 {
					stats.Errors.Add(1)
				}
			}
		})
	}
}
