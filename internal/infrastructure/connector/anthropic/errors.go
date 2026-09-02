package anthropic

import gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"

func connectorError(kind gatewayport.ConnectorErrorKind, cause error) *gatewayport.ConnectorError {
	return &gatewayport.ConnectorError{Kind: kind, Cause: cause}
}

func parameterUnsupported(param string, cause error) *gatewayport.ConnectorError {
	return &gatewayport.ConnectorError{Kind: gatewayport.ParameterUnsupported, Param: param, Cause: cause}
}

func invalidResponseError(cause error) *gatewayport.ConnectorError {
	return connectorError(gatewayport.UpstreamInvalidResponse, cause)
}
