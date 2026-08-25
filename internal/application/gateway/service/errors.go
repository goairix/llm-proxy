package service

type ErrorCode string

const (
	InvalidRequest        ErrorCode = "invalid_request"
	AuthenticationFailed  ErrorCode = "authentication_failed"
	PermissionDenied      ErrorCode = "permission_denied"
	ResourceNotFound      ErrorCode = "resource_not_found"
	Conflict              ErrorCode = "conflict"
	CapabilityUnsupported ErrorCode = "capability_unsupported"
	GatewayNotReady       ErrorCode = "gateway_not_ready"
	ConnectorFailed       ErrorCode = "connector_failed"
	InternalError         ErrorCode = "internal_error"
)

type GatewayError struct {
	Code        ErrorCode
	SafeMessage string
	Param       string
	Cause       error
}

func NewError(code ErrorCode, safeMessage, param string, cause error) *GatewayError {
	if safeMessage == "" {
		safeMessage = defaultSafeMessage(code)
	}
	return &GatewayError{Code: code, SafeMessage: safeMessage, Param: param, Cause: cause}
}

func (e *GatewayError) Error() string {
	if e == nil {
		return ""
	}
	if e.SafeMessage != "" {
		return e.SafeMessage
	}
	return defaultSafeMessage(e.Code)
}

func (e *GatewayError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func defaultSafeMessage(code ErrorCode) string {
	switch code {
	case InvalidRequest:
		return "请求参数无效"
	case AuthenticationFailed:
		return "身份验证失败"
	case PermissionDenied:
		return "没有访问权限"
	case ResourceNotFound:
		return "请求的资源不存在"
	case Conflict:
		return "请求与当前状态冲突"
	case CapabilityUnsupported:
		return "当前模型不支持所需能力"
	case GatewayNotReady:
		return "网关尚未就绪"
	case ConnectorFailed:
		return "供应商请求失败"
	default:
		return "内部服务错误"
	}
}
