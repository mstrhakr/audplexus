package auth

import (
	"net"
	"net/http"
	"strings"
)

// SettingKeyTrustedProxies holds a comma-separated list of CIDRs. Only
// requests whose RemoteAddr falls in one of these ranges have their
// X-Forwarded-For header consulted for the local-address check.
//
// Empty value (the default) means "don't trust XFF at all", which is the
// safe default for a single-binary install behind no proxy.
const SettingKeyTrustedProxies = "trusted_proxies"

// SettingKeyCookieDomain optionally constrains the Domain attribute on the
// session + CSRF cookies. Leave empty to scope cookies to the exact host
// that served the response (the safe default for a single-host install).
// Set to e.g. "audplexus.example.com" to scope explicitly when the install
// is reachable from multiple hostnames, or leave empty when running
// alongside other apps on subdomains of a shared parent.
const SettingKeyCookieDomain = "cookie_domain"

// ParseTrustedProxies turns the comma-separated CIDR setting into prefixes.
// Invalid entries are silently dropped — we don't want a typo in settings
// to crash request handling.
func ParseTrustedProxies(setting string) []*net.IPNet {
	if setting == "" {
		return nil
	}
	var out []*net.IPNet
	for _, raw := range strings.Split(setting, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		// Allow bare IPs as /32 or /128.
		if !strings.Contains(raw, "/") {
			if ip := net.ParseIP(raw); ip != nil {
				if ip.To4() != nil {
					raw += "/32"
				} else {
					raw += "/128"
				}
			}
		}
		_, ipnet, err := net.ParseCIDR(raw)
		if err != nil {
			continue
		}
		out = append(out, ipnet)
	}
	return out
}

// requestIP returns the parsed IP of the immediate TCP peer. This deliberately
// ignores X-Forwarded-For — that header is set by the caller and cannot be
// trusted for security decisions unless the immediate peer is itself trusted.
func requestIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// clientIP is the IP we treat as the "client" for local-address checks. If
// the immediate peer is a trusted proxy, we honor the leftmost entry in
// X-Forwarded-For; otherwise we use the peer directly.
func clientIP(r *http.Request, trusted []*net.IPNet) net.IP {
	peer := requestIP(r)
	if peer == nil {
		return nil
	}
	if !ipIn(peer, trusted) {
		return peer
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return peer
	}
	first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	if ip := net.ParseIP(first); ip != nil {
		return ip
	}
	return peer
}

func ipIn(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// IsLoopback reports whether the request's client IP is loopback (127/8, ::1).
func IsLoopback(r *http.Request, trusted []*net.IPNet) bool {
	ip := clientIP(r, trusted)
	return ip != nil && ip.IsLoopback()
}

// IsLocalAddress reports whether the request's client IP is on a local-only
// network — loopback, RFC1918, link-local, or ULA. This is the broader of
// the two "local" definitions matching Sonarr's DisabledForLocalAddresses.
func IsLocalAddress(r *http.Request, trusted []*net.IPNet) bool {
	ip := clientIP(r, trusted)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
		return true
	}
	return false
}
