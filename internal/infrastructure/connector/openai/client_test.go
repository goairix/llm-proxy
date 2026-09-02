package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
)

func TestBuildRequestUsesOnlyDecryptedProviderKey(t *testing.T) {
	provider := gatewaysnapshot.Provider{BaseURL: "https://example.invalid/prefix"}
	key := []byte("upstream-secret")
	request, err := newUpstreamRequest(context.Background(), provider, "chat/completions", []byte(`{}`), key, false)
	if err != nil {
		t.Fatal(err)
	}
	if request.URL.String() != "https://example.invalid/prefix/v1/chat/completions" {
		t.Fatalf("url=%s", request.URL)
	}
	if request.Host != "example.invalid" || request.Header.Get("Authorization") != "Bearer upstream-secret" {
		t.Fatalf("host=%q authorization=%q", request.Host, request.Header.Get("Authorization"))
	}
	for _, forbidden := range []string{"X-Client-Secret", "X-Api-Key", "Cookie"} {
		if request.Header.Get(forbidden) != "" {
			t.Fatalf("header %s leaked", forbidden)
		}
	}
}

func TestUpstreamClientDoesNotFollowRedirects(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	client := cloneClientWithoutRedirects(&http.Client{})
	response, err := client.Get(redirect.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || targetCalls != 0 {
		t.Fatalf("status=%d target_calls=%d", response.StatusCode, targetCalls)
	}
}

func TestReadLimitedRejectsOversizedBody(t *testing.T) {
	_, err := readLimited(io.NopCloser(strings.NewReader("12345")), 4)
	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("error=%v", err)
	}
	payload, err := readLimited(io.NopCloser(strings.NewReader("1234")), 4)
	if err != nil || string(payload) != "1234" {
		t.Fatalf("payload=%q error=%v", payload, err)
	}
}
