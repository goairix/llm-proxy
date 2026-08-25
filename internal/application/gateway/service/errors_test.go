package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGatewayErrorExposesOnlySafeMessageAndPreservesCause(t *testing.T) {
	cause := context.DeadlineExceeded
	err := NewError(ConnectorFailed, "供应商请求失败", "", cause)
	if err.Error() != "供应商请求失败" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("gateway error did not preserve cause")
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("safe error leaked cause: %q", err.Error())
	}
}

func TestGatewayErrorCodesAreStable(t *testing.T) {
	codes := []ErrorCode{
		InvalidRequest, AuthenticationFailed, PermissionDenied, ResourceNotFound, Conflict,
		CapabilityUnsupported, GatewayNotReady, ConnectorFailed, InternalError,
	}
	seen := make(map[ErrorCode]struct{}, len(codes))
	for _, code := range codes {
		if code == "" {
			t.Fatal("empty error code")
		}
		if _, duplicate := seen[code]; duplicate {
			t.Fatalf("duplicate error code %q", code)
		}
		seen[code] = struct{}{}
	}
}
