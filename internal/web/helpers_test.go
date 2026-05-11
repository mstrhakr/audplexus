package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "trim and drop trailing slash", in: "  http://abs.local:13378/  ", want: "http://abs.local:13378"},
		{name: "keep query", in: "https://example.com/root/?a=1", want: "https://example.com/root?a=1"},
		{name: "empty", in: "   ", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeURL(tc.in)
			if got != tc.want {
				t.Fatalf("normalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateRemoteURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "valid http", in: "http://abs.local:13378", wantErr: false},
		{name: "valid https", in: "https://plex.local", wantErr: false},
		{name: "missing host", in: "http://", wantErr: true},
		{name: "bad scheme", in: "ftp://example.com", wantErr: true},
		{name: "userinfo blocked", in: "https://user:pass@example.com", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRemoteURL(tc.in)
			if tc.wantErr && err == nil {
				t.Fatalf("validateRemoteURL(%q) error = nil, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateRemoteURL(%q) error = %v, want nil", tc.in, err)
			}
		})
	}
}

func TestDestinationConfiguredAndHealth(t *testing.T) {
	now := time.Now()
	ok := true
	fail := false

	plex := &database.LibraryDestination{
		Type:          database.LibraryDestinationTypePlex,
		URL:           "http://plex",
		PlexToken:     "tok",
		PlexSectionID: "12",
	}
	if !destinationConfigured(plex) {
		t.Fatalf("destinationConfigured(plex) = false, want true")
	}
	if got := summarizeHealth(plex); got != "never" {
		t.Fatalf("summarizeHealth(plex) = %q, want never", got)
	}

	plex.LastHealthCheckAt = &now
	plex.LastHealthCheckOK = &ok
	if got := summarizeHealth(plex); got != "healthy" {
		t.Fatalf("summarizeHealth(healthy) = %q, want healthy", got)
	}

	plex.LastHealthCheckOK = &fail
	if got := summarizeHealth(plex); got != "failed" {
		t.Fatalf("summarizeHealth(failed) = %q, want failed", got)
	}

	abs := &database.LibraryDestination{
		Type:      database.LibraryDestinationTypeABS,
		URL:       "http://abs",
		APIKey:    "key",
		LibraryID: "lib",
	}
	if !destinationConfigured(abs) {
		t.Fatalf("destinationConfigured(abs) = false, want true")
	}

	abs.APIKey = ""
	if destinationConfigured(abs) {
		t.Fatalf("destinationConfigured(abs missing api key) = true, want false")
	}
	if got := summarizeHealth(abs); got != "not_configured" {
		t.Fatalf("summarizeHealth(not configured) = %q, want not_configured", got)
	}
}

func TestExtractSectionID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "/library/sections/5", want: "5"},
		{in: " /library/sections/99/ ", want: "99"},
		{in: "", want: ""},
		{in: "   ", want: ""},
	}

	for _, tc := range tests {
		if got := extractSectionID(tc.in); got != tc.want {
			t.Fatalf("extractSectionID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildPlexURL(t *testing.T) {
	got, err := buildPlexURL("http://plex.local:32400/", "/library/sections", "token", map[string]string{
		"X-Plex-Container-Size": "10",
	})
	if err != nil {
		t.Fatalf("buildPlexURL() error = %v", err)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	if u.Path != "/library/sections" {
		t.Fatalf("path = %q, want /library/sections", u.Path)
	}
	q := u.Query()
	if q.Get("X-Plex-Token") != "token" {
		t.Fatalf("X-Plex-Token = %q, want token", q.Get("X-Plex-Token"))
	}
	if q.Get("X-Plex-Container-Size") != "10" {
		t.Fatalf("X-Plex-Container-Size = %q, want 10", q.Get("X-Plex-Container-Size"))
	}
}

func TestValidDestinationType(t *testing.T) {
	valid := []string{"plex", "emby", "jellyfin", "abs"}
	for _, v := range valid {
		if !validDestinationType(v) {
			t.Fatalf("validDestinationType(%q) = false, want true", v)
		}
	}

	invalid := []string{"", "ftp", "Plex", "audiobookshelf"}
	for _, v := range invalid {
		if validDestinationType(v) {
			t.Fatalf("validDestinationType(%q) = true, want false", v)
		}
	}
}

func TestDestinationTypeLabelFallback(t *testing.T) {
	unknown := database.LibraryDestinationType("custom")
	if got := destinationTypeLabel(unknown); !strings.EqualFold(got, "custom") {
		t.Fatalf("destinationTypeLabel(custom) = %q, want custom", got)
	}
}
