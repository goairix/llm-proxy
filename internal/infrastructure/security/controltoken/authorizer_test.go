package controltoken

import "testing"

func TestAuthorizerMatchesOnlyConfiguredToken(t *testing.T) {
	authorizer := NewAuthorizer("management-token-with-enough-entropy")
	if !authorizer.Authorize("management-token-with-enough-entropy") {
		t.Fatal("configured token was rejected")
	}
	for _, token := range []string{"", "management-token-with-enough-entropx", " management-token-with-enough-entropy"} {
		if authorizer.Authorize(token) {
			t.Fatalf("unexpected authorization for %q", token)
		}
	}
	if NewAuthorizer("").Authorize("") {
		t.Fatal("empty configured token authorizes empty input")
	}
}
