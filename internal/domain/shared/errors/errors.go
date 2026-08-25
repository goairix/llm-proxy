// Package errors defines stable domain errors without infrastructure details.
package errors

import "errors"

var (
	ErrInvalid               = errors.New("invalid domain value")
	ErrConflict              = errors.New("domain conflict")
	ErrDependencyUnavailable = errors.New("dependency unavailable")
)
