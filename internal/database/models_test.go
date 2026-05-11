package database

import (
	"strings"
	"testing"
)

func TestLibraryDestination_HasSecretFlags(t *testing.T) {
	d := &LibraryDestination{}
	if d.HasAPIKey() || d.HasPlexToken() {
		t.Fatalf("empty destination should report no secrets")
	}

	d.APIKey = "api"
	d.PlexToken = "plex"
	if !d.HasAPIKey() || !d.HasPlexToken() {
		t.Fatalf("destination should report both secrets present")
	}
}

func TestLibraryDestination_StringAndGoStringRedact(t *testing.T) {
	d := LibraryDestination{
		ID:          "id1",
		Type:        LibraryDestinationTypePlex,
		DisplayName: "Main",
		Enabled:     true,
		URL:         "http://plex",
		LibraryID:   "lib",
		APIKey:      "top-secret-api",
		PlexToken:   "top-secret-plex",
	}

	s := d.String()
	if strings.Contains(s, "top-secret-api") || strings.Contains(s, "top-secret-plex") {
		t.Fatalf("String should redact secrets, got: %s", s)
	}
	if !strings.Contains(s, "<redacted>") {
		t.Fatalf("String should include redaction marker, got: %s", s)
	}

	gs := d.GoString()
	if gs != s {
		t.Fatalf("GoString should match String")
	}
}

func TestRedactToken(t *testing.T) {
	if got := redactToken(""); got != "<unset>" {
		t.Fatalf("redactToken(empty) = %q, want <unset>", got)
	}
	if got := redactToken("abc"); got != "<redacted>" {
		t.Fatalf("redactToken(non-empty) = %q, want <redacted>", got)
	}
}
