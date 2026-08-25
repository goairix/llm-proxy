package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"go.uber.org/zap"
)

func TestHandlerHealthAndReadiness(t *testing.T) {
	readiness := appRuntime.NewReadiness()
	handler := New(readiness, zap.NewNop())

	tests := []struct {
		name       string
		handle     func(http.ResponseWriter, *http.Request)
		wantStatus int
		wantBody   string
	}{
		{name: "health", handle: handler.Health, wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "not ready", handle: handler.Ready, wantStatus: http.StatusServiceUnavailable, wantBody: "not_ready"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			tc.handle(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
			assertStatusResponse(t, recorder, tc.wantStatus, tc.wantBody)
		})
	}

	readiness.SetReady(true)
	recorder := httptest.NewRecorder()
	handler.Ready(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assertStatusResponse(t, recorder, http.StatusOK, "ready")
}

func assertStatusResponse(t *testing.T, recorder *httptest.ResponseRecorder, wantCode int, wantStatus string) {
	t.Helper()
	if recorder.Code != wantCode {
		t.Fatalf("status = %d, want %d", recorder.Code, wantCode)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != wantStatus {
		t.Fatalf("status body = %q, want %q", body.Status, wantStatus)
	}
}
