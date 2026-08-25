package model

import (
	"fmt"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
)

// Status represents whether a domain resource can be used.
type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// Validate checks that the status is one of the stable domain values.
func (s Status) Validate() error {
	switch s {
	case StatusActive, StatusDisabled:
		return nil
	default:
		return fmt.Errorf("%w: unsupported status %q", sharederrors.ErrInvalid, s)
	}
}

// Active reports whether the status allows resource use.
func (s Status) Active() bool {
	return s == StatusActive
}
