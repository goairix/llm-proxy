package observability

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type contextKey uint8

const (
	routeInfoContextKey contextKey = iota
	serverRestoreContextKey
	outboundRestoreContextKey
)

type routeInfo struct {
	provider string
	endpoint string
}

type serverRestoreInfo struct {
	url        *url.URL
	requestURI string
}

type outboundRestoreInfo struct {
	url        *url.URL
	host       string
	requestURI string
}

// WrapHandler instruments one provider route while exposing only a normalized URL to otelhttp.
func (r *Runtime) WrapHandler(provider string, next http.Handler) http.Handler {
	if r == nil || !r.enabled {
		return next
	}
	provider = normalizeProvider(provider)
	businessHandler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		info := routeInfoFromContext(request.Context())
		attrs := []attribute.KeyValue{
			attribute.String("provider", info.provider),
			attribute.String("endpoint", info.endpoint),
		}
		options := metric.WithAttributes(attrs...)
		r.inFlight.Add(request.Context(), 1, options)
		statusWriter := &telemetryResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(statusWriter, restoreServerRequest(request))
		r.inFlight.Add(request.Context(), -1, options)

		outcome := requestOutcome(statusWriter.status, request.Context().Err())
		completedAttrs := append(append([]attribute.KeyValue(nil), attrs...), attribute.String("outcome", outcome))
		r.requests.Add(request.Context(), 1, metric.WithAttributes(completedAttrs...))
		if statusWriter.status == http.StatusTooManyRequests {
			r.rateRejections.Add(request.Context(), 1, metric.WithAttributes(completedAttrs...))
		}
	})
	instrumented := otelhttp.NewHandler(
		businessHandler,
		"llm-proxy",
		otelhttp.WithTracerProvider(r.tracerProvider),
		otelhttp.WithMeterProvider(r.meterProvider),
		otelhttp.WithPropagators(r.propagator),
		otelhttp.WithSpanNameFormatter(func(_ string, request *http.Request) string {
			return request.Method + " " + request.URL.Path
		}),
	)

	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		info := routeInfo{provider: provider, endpoint: normalizeEndpoint(provider, request.URL.Path)}
		ctx := context.WithValue(request.Context(), routeInfoContextKey, info)
		ctx = context.WithValue(ctx, serverRestoreContextKey, serverRestoreInfo{
			url:        cloneURL(request.URL),
			requestURI: request.RequestURI,
		})
		sanitized := request.Clone(ctx)
		sanitized.URL = sanitizedURL(request.URL, info)
		sanitized.RequestURI = sanitized.URL.RequestURI()
		instrumented.ServeHTTP(w, sanitized)
	})
}

// Transport instruments outbound HTTP requests without exposing dynamic paths or queries.
func (r *Runtime) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if r == nil || !r.enabled {
		return base
	}
	restoring := restoringTransport{base: base}
	instrumented := otelhttp.NewTransport(
		restoring,
		otelhttp.WithTracerProvider(r.tracerProvider),
		otelhttp.WithMeterProvider(r.meterProvider),
		otelhttp.WithPropagators(r.propagator),
		otelhttp.WithSpanNameFormatter(func(_ string, request *http.Request) string {
			return request.Method + " " + request.URL.Path
		}),
	)
	return sanitizingTransport{instrumented: instrumented}
}

// TransportFor instruments a managed upstream using only a fixed provider and endpoint route.
func (r *Runtime) TransportFor(provider, endpoint string, base http.RoundTripper) http.RoundTripper {
	info := routeInfo{provider: normalizeGatewayProvider(provider), endpoint: normalizeGatewayEndpoint(endpoint)}
	return fixedRouteTransport{info: info, next: r.Transport(base)}
}

type fixedRouteTransport struct {
	info routeInfo
	next http.RoundTripper
}

func (t fixedRouteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := context.WithValue(request.Context(), routeInfoContextKey, t.info)
	return t.next.RoundTrip(request.Clone(ctx))
}

type sanitizingTransport struct {
	instrumented http.RoundTripper
}

func (t sanitizingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	info := routeInfoFromContext(request.Context())
	ctx := context.WithValue(request.Context(), outboundRestoreContextKey, outboundRestoreInfo{
		url:        cloneURL(request.URL),
		host:       request.Host,
		requestURI: request.RequestURI,
	})
	sanitized := request.Clone(ctx)
	sanitized.URL = sanitizedOutboundURL(request.URL, info)
	sanitized.RequestURI = ""
	return t.instrumented.RoundTrip(sanitized)
}

type restoringTransport struct {
	base http.RoundTripper
}

func (t restoringTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	restore, ok := request.Context().Value(outboundRestoreContextKey).(outboundRestoreInfo)
	if !ok {
		return t.base.RoundTrip(request)
	}
	restored := request.Clone(request.Context())
	restored.URL = cloneURL(restore.url)
	restored.Host = restore.host
	restored.RequestURI = restore.requestURI
	return t.base.RoundTrip(restored)
}

type telemetryResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *telemetryResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *telemetryResponseWriter) Write(body []byte) (int, error) {
	return w.ResponseWriter.Write(body)
}

func (w *telemetryResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func restoreServerRequest(request *http.Request) *http.Request {
	restore, ok := request.Context().Value(serverRestoreContextKey).(serverRestoreInfo)
	if !ok {
		return request
	}
	restored := request.Clone(request.Context())
	restored.URL = cloneURL(restore.url)
	restored.RequestURI = restore.requestURI
	return restored
}

func sanitizedURL(original *url.URL, info routeInfo) *url.URL {
	sanitized := cloneURL(original)
	sanitized.Path = "/" + info.provider + "/" + info.endpoint
	sanitized.RawPath = ""
	sanitized.RawQuery = ""
	sanitized.ForceQuery = false
	sanitized.Fragment = ""
	sanitized.RawFragment = ""
	sanitized.User = nil
	return sanitized
}

func sanitizedOutboundURL(original *url.URL, info routeInfo) *url.URL {
	sanitized := sanitizedURL(original, info)
	sanitized.Scheme = "https"
	sanitized.Host = "upstream.invalid"
	sanitized.Opaque = ""
	return sanitized
}

func cloneURL(original *url.URL) *url.URL {
	if original == nil {
		return &url.URL{}
	}
	clone := *original
	return &clone
}

func routeInfoFromContext(ctx context.Context) routeInfo {
	if info, ok := ctx.Value(routeInfoContextKey).(routeInfo); ok {
		return info
	}
	return routeInfo{provider: "unknown", endpoint: "other"}
}

func normalizeProvider(provider string) string {
	if provider == "openai" || provider == "anthropic" {
		return provider
	}
	return "unknown"
}

func normalizeGatewayProvider(provider string) string {
	if provider == "openai" || provider == "openai_compatible" || provider == "anthropic" {
		return provider
	}
	return "unknown"
}

func normalizeGatewayEndpoint(endpoint string) string {
	if endpoint == "responses" || endpoint == "chat.completions" || endpoint == "messages" {
		return endpoint
	}
	return "other"
}

func normalizeEndpoint(provider, path string) string {
	provider = normalizeProvider(provider)
	switch provider {
	case "openai":
		switch path {
		case "/openai/v1/responses":
			return "responses"
		case "/v1/responses":
			return "responses"
		case "/openai/v1/responses/compact":
			return "responses.compact"
		case "/openai/v1/chat/completions", "/v1/chat/completions":
			return "chat.completions"
		case "/openai/v1/completions":
			return "completions"
		}
	case "anthropic":
		if path == "/anthropic/v1/messages" || path == "/v1/messages" {
			return "messages"
		}
	}
	return "other"
}

func requestOutcome(status int, requestErr error) string {
	if requestErr != nil {
		if errors.Is(requestErr, context.Canceled) || errors.Is(requestErr, context.DeadlineExceeded) {
			return "canceled"
		}
		return "error"
	}
	switch {
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return "success"
	}
}
