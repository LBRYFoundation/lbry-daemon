package rpc

import "net"
import "net/http"
import "strings"

import "lbry/daemon/environment"

// isOriginAllowed reports whether a browser Origin may use the API without credentials. A request without
// an Origin header (curl, the Electron main process, which strips it) is allowed.
func isOriginAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	allowed := environment.AllowedOrigin()
	return allowed != "" && (allowed == "*" || allowed == origin)
}

// isTrustedLocal decides whether a request may be treated as admin without credentials: it comes from a
// loopback address, names a loopback Host (stops DNS rebinding) and has no Origin other than the allowed
// one (stops other web pages).
func isTrustedLocal(req *http.Request) bool {
	return isLoopbackAddress(req.RemoteAddr) && isLoopbackHost(req.Host) && isOriginAllowed(req.Header.Get("Origin"))
}

func isLoopbackAddress(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackHost(hostHeader string) bool {
	host, _, err := net.SplitHostPort(hostHeader)
	if err != nil {
		host = strings.Trim(hostHeader, "[]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
