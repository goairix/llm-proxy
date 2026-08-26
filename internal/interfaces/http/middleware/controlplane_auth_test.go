package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	controltoken "github.com/goairix/llm-proxy/internal/infrastructure/security/controltoken"
)

func TestControlPlaneAuthRequiresValidBearerToken(t *testing.T) {
	authorizer := controltoken.NewAuthorizer("management-token-with-enough-entropy")
	calls := 0
	forwardedAuthorization := "not-called"
	handler := RequestID(ControlPlaneAuth(authorizer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		forwardedAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	})))

	for _, authorization := range []string{"", "Bearer wrong-token", "Basic management-token-with-enough-entropy"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/organizations", nil)
		req.Header.Set("Authorization", authorization)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("Authorization=%q status=%d", authorization, recorder.Code)
		}
		var body struct {
			Error struct {
				Code      string `json:"code"`
				RequestID string `json:"request_id"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != "authentication_failed" || body.Error.RequestID == "" {
			t.Fatalf("body = %+v", body)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/organizations", nil)
	req.Header.Set("Authorization", "Bearer management-token-with-enough-entropy")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent || calls != 1 || forwardedAuthorization != "" {
		t.Fatalf("status=%d calls=%d forwarded_authorization=%q", recorder.Code, calls, forwardedAuthorization)
	}
}
