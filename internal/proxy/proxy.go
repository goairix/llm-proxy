package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// Options configures a reverse proxy.
type Options struct {
	BaseURL     string
	StripPrefix string
}

// NewReverseProxy creates a reverse proxy that removes a public route prefix
// before joining the request path with the upstream base URL.
func NewReverseProxy(options Options) (http.Handler, error) {
	target, err := url.Parse(options.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, fmt.Errorf("base URL scheme must be http or https")
	}
	if target.Hostname() == "" {
		return nil, fmt.Errorf("base URL must include a host")
	}

	reverseProxy := httputil.NewSingleHostReverseProxy(target)
	defaultDirector := reverseProxy.Director
	reverseProxy.Director = func(req *http.Request) {
		req.URL.Path = strings.TrimPrefix(req.URL.Path, options.StripPrefix)
		if req.URL.RawPath != "" {
			req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, options.StripPrefix)
		}

		defaultDirector(req)
		req.Host = target.Host
	}

	return reverseProxy, nil
}
