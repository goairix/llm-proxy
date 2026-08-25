package errors

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorDoesNotExposeCause(t *testing.T) {
	cause := errors.New("postgres password=provider-secret")
	err := New(Internal, "服务内部错误", "", cause)

	if got := err.Error(); got != "服务内部错误" {
		t.Fatalf("Error() = %q", got)
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatal("Error() exposed the private cause")
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause is not available to errors.Is")
	}
}
