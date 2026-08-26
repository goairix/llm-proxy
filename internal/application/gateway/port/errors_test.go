package port

import (
	"errors"
	"strings"
	"testing"
)

func TestConnectorErrorExposesOnlySafeMessageAndPreservesCause(t *testing.T) {
	cause := errors.New("POST https://secret.internal/v1/responses: api-key=upstream-secret")
	err := &ConnectorError{Kind: UpstreamInvalidResponse, SafeMessage: "供应商返回无效响应", Cause: cause}
	if err.Error() != "供应商返回无效响应" || strings.Contains(err.Error(), "secret.internal") || strings.Contains(err.Error(), "upstream-secret") {
		t.Fatalf("Error()=%q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("connector cause was not preserved")
	}
}

func TestConnectorErrorHasStableDefaultMessages(t *testing.T) {
	for _, kind := range []ConnectorErrorKind{
		ParameterUnsupported, UpstreamAuthentication, UpstreamRateLimited, UpstreamTimeout,
		UpstreamUnavailable, UpstreamRequestRejected, UpstreamInvalidResponse, CredentialUnavailable,
	} {
		if message := (&ConnectorError{Kind: kind}).Error(); message == "" {
			t.Fatalf("kind %q has empty safe message", kind)
		}
	}
}
