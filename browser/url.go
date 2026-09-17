package browser

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var addressScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// normalizeURL accepts browser address-bar input, not relative document URLs.
// Keep the scheme allowlist authoritative: an unrecognized scheme must never
// become an HTTP host merely because it is followed by a colon.
func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fail("invalid_params", "url must not be empty")
	}
	if scheme := addressScheme.FindString(raw); scheme != "" {
		switch strings.ToLower(strings.TrimSuffix(scheme, ":")) {
		case "http", "https", "file", "about", "data":
			return checkedURL(raw)
		default:
			if !addressHostPort(raw) && !addressIPv6(raw) {
				return "", fail("invalid_params", "unsupported URL scheme %q", strings.TrimSuffix(scheme, ":"))
			}
		}
	}
	if strings.HasPrefix(raw, "//") {
		return checkedURL("http:" + raw)
	}
	// Accept a bare IPv6 literal as well as the standard bracketed form. A
	// literal with a port must use brackets to avoid ambiguity with the IP.
	if addressIPv6(raw) {
		host, suffix := addressAuthority(raw)
		raw = "[" + host + "]" + suffix
	}
	return checkedURL("http://" + raw)
}

func addressAuthority(raw string) (string, string) {
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		return raw[:i], raw[i:]
	}
	return raw, ""
}

func addressIPv6(raw string) bool {
	host, _ := addressAuthority(raw)
	return strings.Contains(host, ":") && net.ParseIP(host) != nil
}

func addressHostPort(raw string) bool {
	authority, _ := addressAuthority(raw)
	host, port, err := net.SplitHostPort(authority)
	if err != nil || port == "" {
		return false
	}
	// A single-label name with a colon is ambiguous with a custom scheme.
	// Only localhost and dotted hostnames unambiguously denote web addresses.
	if !strings.EqualFold(host, "localhost") && !strings.Contains(host, ".") {
		return false
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func checkedURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fail("invalid_params", "invalid URL")
	}
	if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
		if u.Hostname() == "" {
			return "", fail("invalid_params", "HTTP URL requires a host")
		}
		if port := u.Port(); port != "" {
			if _, err := strconv.ParseUint(port, 10, 16); err != nil {
				return "", fail("invalid_params", "invalid URL port")
			}
		}
		if strings.HasPrefix(u.Host, "[") {
			// url.Parse checks brackets but does not validate the IP literal.
			host := strings.SplitN(u.Hostname(), "%", 2)[0]
			if !strings.Contains(host, ":") || net.ParseIP(host) == nil {
				return "", fail("invalid_params", "invalid IPv6 URL host")
			}
		} else if strings.Contains(u.Hostname(), ":") {
			return "", fail("invalid_params", "IPv6 URL hosts require brackets")
		}
	}
	return raw, nil
}
