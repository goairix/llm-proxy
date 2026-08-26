package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestControlPlaneLoggingNeverRecordsAuthorization(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	handler := RequestID(ControlPlaneLogging(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations", nil)
	req.Header.Set("Authorization", "Bearer management-token-must-never-be-logged")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if observed.Len() != 1 {
		t.Fatalf("log entries = %d", observed.Len())
	}
	entry := observed.All()[0]
	if strings.Contains(entry.Message, "management-token") {
		t.Fatal("log message contains management token")
	}
	for key, value := range entry.ContextMap() {
		if strings.Contains(key, "authorization") || strings.Contains(key, "token") || strings.Contains(key, "api_key") || strings.Contains(valueString(value), "management-token") {
			t.Fatalf("unsafe log field %q=%v", key, value)
		}
	}
}

func valueString(value any) string {
	text, _ := value.(string)
	return text
}
