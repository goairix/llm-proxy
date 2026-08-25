package middleware

import (
	"net/http"
	"strings"

	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
	httpresponse "github.com/goairix/llm-proxy/internal/interfaces/http/response"
)

// ControlPlaneAuth protects control-plane resources with their dedicated token.
func ControlPlaneAuth(authorizer controlport.ControlPlaneAuthorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || authorizer == nil || !authorizer.Authorize(token) {
				httpresponse.Error(w, http.StatusUnauthorized, "authentication_failed", "管理令牌无效", httpresponse.RequestID(r.Context()))
				return
			}
			r.Header.Del("Authorization")
			next.ServeHTTP(w, r)
		})
	}
}
