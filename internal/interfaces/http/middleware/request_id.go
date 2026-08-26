package middleware

import (
	"net/http"

	"github.com/google/uuid"

	httpresponse "github.com/goairix/llm-proxy/internal/interfaces/http/response"
)

const requestIDHeader = "x-request-id"

// RequestID accepts a safe caller identifier or generates a UUIDv7.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(requestIDHeader)
		if !validRequestID(requestID) {
			requestID = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set(requestIDHeader, requestID)
		next.ServeHTTP(w, r.WithContext(httpresponse.WithRequestID(r.Context(), requestID)))
	})
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_', char == '.':
		default:
			return false
		}
	}
	return true
}
