package web

import "testing"

func TestSanitizeReturnURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"/library", "/library"},
		{"/library?q=foo", "/library?q=foo"},
		{"//evil.com/path", "/"},
		{"http://evil.com/", "/"},
		{"javascript:alert(1)", "/"},
		{"\\\\evil.com\\path", "/"},
	}
	for _, c := range cases {
		if got := sanitizeReturnURL(c.in); got != c.want {
			t.Errorf("sanitizeReturnURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAuthExemptPaths(t *testing.T) {
	exempt := []string{"/login", "/logout", "/healthz", "/setup", "/setup/admin", "/setup/admin/skip", "/setup/marketplace", "/static/foo.css", "/static/img/logo.svg"}
	for _, p := range exempt {
		if !authExemptPath(p) {
			t.Errorf("expected %s to be exempt", p)
		}
	}
	notExempt := []string{"/", "/library", "/api/sync", "/settings", "/settings/security"}
	for _, p := range notExempt {
		if authExemptPath(p) {
			t.Errorf("expected %s NOT to be exempt", p)
		}
	}
}
