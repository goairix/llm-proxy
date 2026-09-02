package port

type ConnectorErrorKind string

const (
	ParameterUnsupported    ConnectorErrorKind = "parameter_unsupported"
	UpstreamAuthentication  ConnectorErrorKind = "authentication"
	UpstreamRateLimited     ConnectorErrorKind = "rate_limited"
	UpstreamTimeout         ConnectorErrorKind = "timeout"
	UpstreamUnavailable     ConnectorErrorKind = "unavailable"
	UpstreamRequestRejected ConnectorErrorKind = "request_rejected"
	UpstreamInvalidResponse ConnectorErrorKind = "invalid_response"
	CredentialUnavailable   ConnectorErrorKind = "credential_unavailable"
)

type ConnectorError struct {
	Kind        ConnectorErrorKind
	Param       string
	SafeMessage string
	Cause       error
}

func (e *ConnectorError) Error() string {
	if e == nil {
		return ""
	}
	if e.SafeMessage != "" {
		return e.SafeMessage
	}
	switch e.Kind {
	case ParameterUnsupported:
		return "供应商不支持请求参数"
	case UpstreamAuthentication:
		return "供应商身份验证失败"
	case UpstreamRateLimited:
		return "供应商请求频率受限"
	case UpstreamTimeout:
		return "供应商请求超时"
	case UpstreamUnavailable:
		return "供应商暂不可用"
	case UpstreamRequestRejected:
		return "供应商拒绝请求"
	case UpstreamInvalidResponse:
		return "供应商返回无效响应"
	case CredentialUnavailable:
		return "供应商凭据不可用"
	default:
		return "供应商请求失败"
	}
}

func (e *ConnectorError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
