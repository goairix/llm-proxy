package model

import (
	"fmt"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
)

// UpstreamProtocol identifies the wire protocol used for one deployment.
type UpstreamProtocol string

const (
	UpstreamResponses         UpstreamProtocol = "responses"
	UpstreamChatCompletions   UpstreamProtocol = "chat_completions"
	UpstreamAnthropicMessages UpstreamProtocol = "anthropic_messages"
	UpstreamFake              UpstreamProtocol = "fake"
)

// Validate checks that the protocol is explicitly supported by the gateway.
func (p UpstreamProtocol) Validate() error {
	switch p {
	case UpstreamResponses, UpstreamChatCompletions, UpstreamAnthropicMessages, UpstreamFake:
		return nil
	default:
		return fmt.Errorf("%w: unsupported upstream protocol %q", sharederrors.ErrInvalid, p)
	}
}
