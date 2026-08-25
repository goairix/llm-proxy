package response

import (
	"context"
	"encoding/json"
	"net/http"
)

type requestIDKey struct{}

// WithRequestID stores the safe request correlation identifier in a context.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestID returns the request correlation identifier from a context.
func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// JSON writes a JSON response with the supplied status.
func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// Error writes the stable control-plane error envelope.
func Error(w http.ResponseWriter, status int, code, message, requestID string) {
	JSON(w, status, ErrorEnvelope{Error: ErrorBody{Code: code, Message: message, RequestID: requestID}})
}
