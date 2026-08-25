package errors

// Code is a stable application error identifier consumed by HTTP adapters.
type Code string

const (
	InvalidRequest        Code = "invalid_request"
	AuthenticationFailed  Code = "authentication_failed"
	PermissionDenied      Code = "permission_denied"
	NotFound              Code = "not_found"
	Conflict              Code = "conflict"
	DependencyUnavailable Code = "dependency_unavailable"
	Internal              Code = "internal"
)

// Error exposes only a safe message while retaining a private cause for errors.Is/As.
type Error struct {
	Code        Code
	SafeMessage string
	Param       string
	Cause       error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.SafeMessage
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func New(code Code, safeMessage, param string, cause error) *Error {
	return &Error{Code: code, SafeMessage: safeMessage, Param: param, Cause: cause}
}
