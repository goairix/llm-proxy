package model

import (
	"fmt"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
)

// CapabilitySet declares the request and response features supported by a deployment.
type CapabilitySet struct {
	Text             bool `json:"text"`
	ImageInput       bool `json:"image_input"`
	Tools            bool `json:"tools"`
	StructuredOutput bool `json:"structured_output"`
	Streaming        bool `json:"streaming"`
}

// Validate rejects an empty declaration.
func (c CapabilitySet) Validate() error {
	if !c.Text && !c.ImageInput && !c.Tools && !c.StructuredOutput && !c.Streaming {
		return fmt.Errorf("%w: at least one deployment capability is required", sharederrors.ErrInvalid)
	}
	return nil
}
