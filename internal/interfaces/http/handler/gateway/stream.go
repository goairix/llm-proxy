package gateway

import (
	"mime"
	"net/http"
	"strings"
)

func validJSONContentType(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func bearerToken(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func sanitizedRequest(writer http.ResponseWriter, request *http.Request) *http.Request {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Del("Authorization")
	copy.Header.Del("x-api-key")
	copy.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	return copy
}

func prepareSSE(writer http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return nil, false
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	return flusher, true
}
