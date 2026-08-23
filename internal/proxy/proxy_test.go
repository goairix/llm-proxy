package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewReverseProxy_RewritesPathBeforeJoiningBasePath(t *testing.T) {
	type receivedRequest struct {
		path  string
		query string
		host  string
	}
	received := make(chan receivedRequest, 1)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- receivedRequest{
			path:  r.URL.Path,
			query: r.URL.RawQuery,
			host:  r.Host,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	handler, err := NewReverseProxy(Options{
		BaseURL:     upstream.URL + "/gateway",
		StripPrefix: "/openai",
	})
	if err != nil {
		t.Fatalf("NewReverseProxy returned unexpected error: %v", err)
	}

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := http.Get(proxyServer.URL + "/openai/v1/responses?include=usage")
	if err != nil {
		t.Fatalf("GET request failed: %v", err)
	}
	resp.Body.Close()

	got := <-received
	if want := "/gateway/v1/responses"; got.path != want {
		t.Errorf("upstream received path %q; want %q", got.path, want)
	}
	if want := "include=usage"; got.query != want {
		t.Errorf("upstream received query %q; want %q", got.query, want)
	}
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("failed to parse upstream URL: %v", err)
	}
	if got.host != target.Host {
		t.Errorf("upstream received Host %q; want %q", got.host, target.Host)
	}
}

func TestNewReverseProxy_PreservesEscapedPath(t *testing.T) {
	receivedEscapedPath := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedEscapedPath <- r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	handler, err := NewReverseProxy(Options{
		BaseURL:     upstream.URL + "/gateway",
		StripPrefix: "/openai",
	})
	if err != nil {
		t.Fatalf("NewReverseProxy returned unexpected error: %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := http.Get(proxyServer.URL + "/openai/v1/responses/resp%2Ftest")
	if err != nil {
		t.Fatalf("GET request failed: %v", err)
	}
	resp.Body.Close()

	if got, want := <-receivedEscapedPath, "/gateway/v1/responses/resp%2Ftest"; got != want {
		t.Errorf("upstream received escaped path %q; want %q", got, want)
	}
}

func TestNewReverseProxy_RejectsInvalidBaseURL(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		wantMessage string
	}{
		{name: "malformed", baseURL: "://bad", wantMessage: "parse base URL"},
		{name: "relative", baseURL: "/relative", wantMessage: "scheme"},
		{name: "unsupported scheme", baseURL: "ftp://example.com", wantMessage: "scheme"},
		{name: "missing host", baseURL: "https:///missing", wantMessage: "host"},
		{name: "missing hostname", baseURL: "http://:8080", wantMessage: "host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewReverseProxy(Options{BaseURL: tt.baseURL, StripPrefix: "/openai"})
			if err == nil {
				t.Fatal("NewReverseProxy returned nil error")
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Errorf("error %q does not contain %q", err, tt.wantMessage)
			}
		})
	}
}

func TestProviderProxies_JoinUpstreamBasePathAfterStrippingPrefix(t *testing.T) {
	tests := []struct {
		name        string
		requestPath string
		wantPath    string
		newProxy    func(string) (http.Handler, error)
	}{
		{
			name:        "OpenAI Responses API",
			requestPath: "/openai/v1/responses",
			wantPath:    "/gateway/v1/responses",
			newProxy:    NewOpenAIProxy,
		},
		{
			name:        "Anthropic Messages API",
			requestPath: "/anthropic/v1/messages",
			wantPath:    "/gateway/v1/messages",
			newProxy:    NewAnthropicProxy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receivedPath := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedPath <- r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()

			handler, err := tt.newProxy(upstream.URL + "/gateway")
			if err != nil {
				t.Fatalf("proxy constructor returned unexpected error: %v", err)
			}
			proxyServer := httptest.NewServer(handler)
			defer proxyServer.Close()

			resp, err := http.Get(proxyServer.URL + tt.requestPath)
			if err != nil {
				t.Fatalf("GET request failed: %v", err)
			}
			resp.Body.Close()

			if got := <-receivedPath; got != tt.wantPath {
				t.Errorf("upstream received path %q; want %q", got, tt.wantPath)
			}
		})
	}
}

func TestNewOpenAIProxy_ResponsesAPI(t *testing.T) {
	type receivedRequest struct {
		method      string
		path        string
		query       string
		authorize   string
		contentType string
		body        string
	}

	received := make(chan receivedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		received <- receivedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			query:       r.URL.RawQuery,
			authorize:   r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"),
			body:        string(body),
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "req_responses_test")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"id":"resp_test","status":"queued"}`)
	}))
	defer upstream.Close()

	handler, err := NewOpenAIProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewOpenAIProxy returned unexpected error: %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	requestBody := `{"model":"gpt-5","input":"Hello","background":true}`
	req, err := http.NewRequest(
		http.MethodPost,
		proxyServer.URL+"/openai/v1/responses?include=reasoning.encrypted_content",
		bytes.NewBufferString(requestBody),
	)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-responses-test")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	gotRequest := <-received
	if gotRequest.method != http.MethodPost {
		t.Errorf("upstream received method %q; want %q", gotRequest.method, http.MethodPost)
	}
	if want := "/v1/responses"; gotRequest.path != want {
		t.Errorf("upstream received path %q; want %q", gotRequest.path, want)
	}
	if want := "include=reasoning.encrypted_content"; gotRequest.query != want {
		t.Errorf("upstream received query %q; want %q", gotRequest.query, want)
	}
	if want := "Bearer sk-responses-test"; gotRequest.authorize != want {
		t.Errorf("upstream received Authorization %q; want %q", gotRequest.authorize, want)
	}
	if want := "application/json"; gotRequest.contentType != want {
		t.Errorf("upstream received Content-Type %q; want %q", gotRequest.contentType, want)
	}
	if gotRequest.body != requestBody {
		t.Errorf("upstream received body %q; want %q", gotRequest.body, requestBody)
	}

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("response status = %d; want %d", resp.StatusCode, http.StatusAccepted)
	}
	if want := "application/json"; resp.Header.Get("Content-Type") != want {
		t.Errorf("response Content-Type = %q; want %q", resp.Header.Get("Content-Type"), want)
	}
	if want := "req_responses_test"; resp.Header.Get("x-request-id") != want {
		t.Errorf("response x-request-id = %q; want %q", resp.Header.Get("x-request-id"), want)
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if want := `{"id":"resp_test","status":"queued"}`; string(responseBody) != want {
		t.Errorf("response body = %q; want %q", responseBody, want)
	}
}

func TestNewOpenAIProxy_ResponsesAPIStreaming(t *testing.T) {
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseUpstream) })
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n")
		w.(http.Flusher).Flush()

		<-releaseUpstream
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()

	handler, err := NewOpenAIProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewOpenAIProxy returned unexpected error: %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	var resp *http.Response
	defer func() {
		release()
		if resp != nil {
			resp.Body.Close()
		}
	}()

	resp, err = http.Post(
		proxyServer.URL+"/openai/v1/responses",
		"application/json",
		strings.NewReader(`{"model":"gpt-5","input":"Hello","stream":true}`),
	)
	if err != nil {
		t.Fatalf("POST request failed: %v", err)
	}

	if want := "text/event-stream"; resp.Header.Get("Content-Type") != want {
		t.Errorf("response Content-Type = %q; want %q", resp.Header.Get("Content-Type"), want)
	}

	firstLine := make(chan string, 1)
	readErr := make(chan error, 1)
	reader := bufio.NewReader(resp.Body)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			readErr <- err
			return
		}
		firstLine <- line
	}()

	select {
	case line := <-firstLine:
		if want := "event: response.output_text.delta\n"; line != want {
			t.Errorf("first SSE line = %q; want %q", line, want)
		}
	case err := <-readErr:
		t.Fatalf("failed to read first SSE line: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first SSE event before upstream completion")
	}

	release()
	remainder, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read remaining SSE events: %v", err)
	}
	if !strings.Contains(string(remainder), "event: response.completed\n") {
		t.Errorf("remaining SSE response %q does not contain completion event", remainder)
	}
}

// TestNewOpenAIProxy_StripPrefix verifies that the "/openai" prefix is stripped
// before the request reaches the upstream server.
func TestNewOpenAIProxy_StripPrefix(t *testing.T) {
	var receivedPath string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := NewOpenAIProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewOpenAIProxy returned unexpected error: %v", err)
	}

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := http.Get(proxyServer.URL + "/openai/v1/chat/completions")
	if err != nil {
		t.Fatalf("GET request failed: %v", err)
	}
	resp.Body.Close()

	want := "/v1/chat/completions"
	if receivedPath != want {
		t.Errorf("upstream received path %q; want %q", receivedPath, want)
	}
}

// TestNewAnthropicProxy_StripPrefix verifies that the "/anthropic" prefix is
// stripped before the request reaches the upstream server.
func TestNewAnthropicProxy_StripPrefix(t *testing.T) {
	var receivedPath string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := NewAnthropicProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProxy returned unexpected error: %v", err)
	}

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := http.Get(proxyServer.URL + "/anthropic/v1/messages")
	if err != nil {
		t.Fatalf("GET request failed: %v", err)
	}
	resp.Body.Close()

	want := "/v1/messages"
	if receivedPath != want {
		t.Errorf("upstream received path %q; want %q", receivedPath, want)
	}
}

// TestNewOpenAIProxy_HeadersForwarded verifies that the Authorization header
// sent by the client is forwarded unchanged to the upstream server.
func TestNewOpenAIProxy_HeadersForwarded(t *testing.T) {
	var receivedAuth string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := NewOpenAIProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewOpenAIProxy returned unexpected error: %v", err)
	}

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/openai/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	wantAuth := "Bearer sk-test1234"
	req.Header.Set("Authorization", wantAuth)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if receivedAuth != wantAuth {
		t.Errorf("upstream received Authorization %q; want %q", receivedAuth, wantAuth)
	}
}

// TestNewAnthropicProxy_HeadersForwarded verifies that the x-api-key header
// sent by the client is forwarded unchanged to the upstream server.
func TestNewAnthropicProxy_HeadersForwarded(t *testing.T) {
	var receivedAPIKey string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAPIKey = r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := NewAnthropicProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProxy returned unexpected error: %v", err)
	}

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	wantKey := "sk-ant-test5678"
	req.Header.Set("x-api-key", wantKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if receivedAPIKey != wantKey {
		t.Errorf("upstream received x-api-key %q; want %q", receivedAPIKey, wantKey)
	}
}
