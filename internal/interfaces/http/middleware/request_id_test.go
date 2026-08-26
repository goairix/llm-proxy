package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	httpresponse "github.com/goairix/llm-proxy/internal/interfaces/http/response"
)

func TestRequestIDPreservesValidValue(t *testing.T) {
	var contextID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextID = httpresponse.RequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations", nil)
	req.Header.Set("x-request-id", "request_01.test-id")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if contextID != "request_01.test-id" || recorder.Header().Get("x-request-id") != contextID {
		t.Fatalf("context=%q header=%q", contextID, recorder.Header().Get("x-request-id"))
	}
}

func TestRequestIDReplacesInvalidValueWithUUIDv7(t *testing.T) {
	var contextID string
	handler := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		contextID = httpresponse.RequestID(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations", nil)
	req.Header.Set("x-request-id", "invalid request id")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	id, err := uuid.Parse(contextID)
	if err != nil || id.Version() != 7 {
		t.Fatalf("request id = %q, version=%d, err=%v", contextID, id.Version(), err)
	}
	if recorder.Header().Get("x-request-id") != contextID {
		t.Fatalf("response request id = %q", recorder.Header().Get("x-request-id"))
	}
}
