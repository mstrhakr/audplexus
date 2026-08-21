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
	// While not yet onboarded, the setup wizard is exempt so a first-run
	// visitor can bootstrap. Login/logout/healthz/static are always exempt.
	s := &Server{}
	s.onboarded.Store(false)
	exemptDuringSetup := []string{"/login", "/logout", "/healthz", "/setup", "/setup/admin", "/setup/admin/skip", "/setup/marketplace", "/static/foo.css", "/static/img/logo.svg"}
	for _, p := range exemptDuringSetup {
		if !s.authExemptPath(p) {
			t.Errorf("expected %s to be exempt during setup", p)
		}
	}
	notExempt := []string{"/", "/library", "/api/sync", "/settings", "/settings/security"}
	for _, p := range notExempt {
		if s.authExemptPath(p) {
			t.Errorf("expected %s NOT to be exempt", p)
		}
	}

	// Once onboarded, /setup/* drops back behind auth — otherwise an
	// anonymous LAN visitor could call /setup/restart and re-bootstrap.
	s.onboarded.Store(true)
	setupPaths := []string{"/setup", "/setup/admin", "/setup/admin/skip", "/setup/marketplace", "/setup/restart"}
	for _, p := range setupPaths {
		if s.authExemptPath(p) {
			t.Errorf("expected %s NOT to be exempt after onboarding", p)
		}
	}
	stillExempt := []string{"/login", "/logout", "/healthz", "/static/foo.css"}
	for _, p := range stillExempt {
		if !s.authExemptPath(p) {
			t.Errorf("expected %s to remain exempt after onboarding", p)
		}
	}
}
