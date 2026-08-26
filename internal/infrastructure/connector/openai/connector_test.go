package openai

import (
	"net/http"
	"testing"
	"time"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

func TestNewConnectorValidatesOptionsAndDisablesRedirects(t *testing.T) {
	opener := &recordingOpener{}
	options := Options{
		ConnectorType: catalogmodel.ConnectorOpenAI, ResponsesClient: &http.Client{}, ChatClient: &http.Client{},
		CredentialOpener: opener, CompleteTimeout: time.Minute, StreamIdleTimeout: time.Minute,
	}
	connector, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if connector.options.ResponsesClient.CheckRedirect == nil || connector.options.ChatClient.CheckRedirect == nil {
		t.Fatal("redirect policy was not installed")
	}
	for _, mutate := range []func(*Options){
		func(value *Options) { value.ConnectorType = "anthropic" },
		func(value *Options) { value.ResponsesClient = nil },
		func(value *Options) { value.ChatClient = nil },
		func(value *Options) { value.CredentialOpener = nil },
		func(value *Options) { value.CompleteTimeout = 0 },
		func(value *Options) { value.StreamIdleTimeout = 0 },
	} {
		invalid := options
		mutate(&invalid)
		if _, err := New(invalid); err == nil {
			t.Fatalf("invalid options accepted: %+v", invalid)
		}
	}
}
