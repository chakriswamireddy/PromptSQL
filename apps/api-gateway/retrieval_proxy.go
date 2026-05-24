package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// newReverseProxy creates a reverse proxy that forwards /v1/retrieval/* requests
// to the retrieval-service, stripping hop-by-hop headers.
func newReverseProxy(target string) http.Handler {
	u, err := url.Parse(target)
	if err != nil {
		panic("invalid RETRIEVAL_SERVICE_ADDR: " + target)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.Host = u.Host
	}
	return proxy
}

// joinRoles serialises a role slice as a comma-separated string for the
// X-User-Roles header forwarded to downstream services.
func joinRoles(roles []string) string {
	return strings.Join(roles, ",")
}
