package auth

import (
	"net/http"
	"testing"
)

func reqFrom(remote string, xff string) *http.Request {
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func TestIsLoopback(t *testing.T) {
	if !IsLoopback(reqFrom("127.0.0.1:54321", ""), nil) {
		t.Error("127.0.0.1 should be loopback")
	}
	if !IsLoopback(reqFrom("[::1]:54321", ""), nil) {
		t.Error("::1 should be loopback")
	}
	if IsLoopback(reqFrom("192.168.1.5:54321", ""), nil) {
		t.Error("192.168.1.5 must not be loopback")
	}
}

func TestIsLocalAddress(t *testing.T) {
	cases := []struct {
		remote string
		want   bool
	}{
		{"127.0.0.1:1", true},
		{"10.0.0.1:1", true},
		{"192.168.1.5:1", true},
		{"172.16.0.5:1", true},
		{"8.8.8.8:1", false},
		{"1.1.1.1:1", false},
	}
	for _, c := range cases {
		if got := IsLocalAddress(reqFrom(c.remote, ""), nil); got != c.want {
			t.Errorf("IsLocalAddress(%s) = %v, want %v", c.remote, got, c.want)
		}
	}
}

func TestXFFIgnoredWhenProxyNotTrusted(t *testing.T) {
	// Untrusted proxy lying about XFF must NOT make a public IP look local.
	r := reqFrom("8.8.8.8:1", "127.0.0.1")
	if IsLoopback(r, nil) {
		t.Error("XFF should not be trusted when proxy list is empty")
	}
}

func TestXFFHonoredWhenProxyTrusted(t *testing.T) {
	// When the immediate peer is on the trusted list, we trust the
	// left-most XFF entry as the real client.
	trusted := ParseTrustedProxies("8.8.8.8/32")
	r := reqFrom("8.8.8.8:1", "127.0.0.1")
	if !IsLoopback(r, trusted) {
		t.Error("trusted proxy XFF should be honored")
	}
}

func TestParseTrustedProxies(t *testing.T) {
	prefixes := ParseTrustedProxies("10.0.0.0/8, 192.168.1.5, garbage, ")
	if len(prefixes) != 2 {
		t.Fatalf("parsed %d prefixes, want 2", len(prefixes))
	}
}
