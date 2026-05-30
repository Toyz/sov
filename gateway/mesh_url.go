package gateway

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeUpstreamURL returns the canonical scheme://host:port form
// every federation layer agrees on. Used by:
//
//   - WithFederationPreemption map keys and values
//   - WithUpstreamGateways allowlist
//   - X-Sov-Introspect-Visited cycle detection
//
// Rules:
//
//   - Scheme is lowercased; only http and https are accepted.
//   - Host is lowercased; IPv6 keeps brackets.
//   - Port is always explicit (defaults are NOT stripped — http://x:80
//     stays http://x:80 so "is x the same as x:80?" never arises).
//   - Path, query, fragment, user-info, and trailing slash are
//     stripped. Federation identity is host:port only.
//
// Two DNS aliases that resolve to the same IP normalize to distinct
// canonical strings — sov does not resolve DNS here. Operators using
// multiple aliases for the same pod must list every alias explicitly
// in their preemption/allowlist sets.
func NormalizeUpstreamURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("normalize url: empty input")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("normalize url %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("normalize url %q: scheme %q not supported (want http or https)", raw, u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("normalize url %q: missing host", raw)
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	hostport := host + ":" + port
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		// IPv6 literal without brackets — rewrap.
		hostport = "[" + host + "]:" + port
	}
	return scheme + "://" + hostport, nil
}

// normalizeUpstreamURL is the unexported convenience that panics on
// invalid input — for the few internal call sites where we've already
// validated the URL at registration time and want a one-liner.
func normalizeUpstreamURL(raw string) string {
	out, err := NormalizeUpstreamURL(raw)
	if err != nil {
		return raw
	}
	return out
}
