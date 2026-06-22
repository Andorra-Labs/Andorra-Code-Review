// Package httpguard provides a Host-header allowlist middleware that blocks
// DNS-rebinding attacks against local HTTP servers.
//
// This is a fork-owned copy of internal/viewer/hostguard.go (intentionally
// duplicated rather than extracted, to keep upstream untouched). The behavior
// matches the viewer copy verbatim apart from package name and env-var name.
package httpguard

import (
	"net"
	"net/http"
	"os"
	"strings"
)

// EnvAllowedHosts is the env var users can set to extend the default
// loopback allowlist with extra hostnames (comma-separated).
const EnvAllowedHosts = "OCR_WEBUI_ALLOWED_HOSTS"

// HostOnly returns the bare host portion of a Host header value.
func HostOnly(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return strings.ToLower(host)
	}
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		return strings.ToLower(h[1 : len(h)-1])
	}
	if strings.Count(h, ":") > 1 {
		return ""
	}
	return strings.ToLower(h)
}

// IsLoopbackHost reports whether host is a loopback name or IP literal.
func IsLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "0:0:0:0:0:0:0:1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// BuildAllowedHosts returns the default-deny allowlist.
func BuildAllowedHosts(bindHost, envVal string) map[string]struct{} {
	allowed := map[string]struct{}{
		"localhost": {},
		"127.0.0.1": {},
		"::1":       {},
	}
	bh := strings.ToLower(strings.TrimSpace(bindHost))
	if bh != "" && bh != "0.0.0.0" && bh != "::" && bh != "*" {
		if strings.HasPrefix(bh, "[") && strings.HasSuffix(bh, "]") {
			bh = bh[1 : len(bh)-1]
		}
		allowed[bh] = struct{}{}
	}
	for _, h := range strings.Split(envVal, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			allowed[h] = struct{}{}
		}
	}
	return allowed
}

// Middleware wraps next with a Host-header allowlist guard.
func Middleware(allowed map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := HostOnly(r.Host)
		if host == "" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if IsLoopbackHost(host) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := allowed[host]; ok {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden host", http.StatusForbidden)
	})
}

// SplitBindHost returns the host portion of a listen address.
func SplitBindHost(addr string) string {
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// ResolveAllowedHostsFromEnv combines the bind host with the OCR_WEBUI_ALLOWED_HOSTS
// env var to produce the active allowlist.
func ResolveAllowedHostsFromEnv(bindAddr string) map[string]struct{} {
	return BuildAllowedHosts(SplitBindHost(bindAddr), os.Getenv(EnvAllowedHosts))
}
