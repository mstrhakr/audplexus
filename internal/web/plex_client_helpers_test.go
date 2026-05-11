package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestPlexClientIDAndAuthURL(t *testing.T) {
	s := &Server{}
	id := s.plexClientID()
	if !strings.HasPrefix(id, "apd-") {
		t.Fatalf("plexClientID() = %q, want prefix apd-", id)
	}
	if strings.Contains(id, " ") {
		t.Fatalf("plexClientID() should not contain spaces: %q", id)
	}

	u := s.plexAuthURL("PIN123")
	if !strings.Contains(u, "https://app.plex.tv/auth#?") || !strings.Contains(u, "code=PIN123") || !strings.Contains(u, "clientID=") {
		t.Fatalf("plexAuthURL() missing expected fields: %q", u)
	}
}

func TestGetPlexSettingsPrefersDBThenEnvFallback(t *testing.T) {
	stub := database.NewStubDB()
	s := &Server{db: stub}
	ctx := context.Background()

	// DB values win when present.
	_ = stub.SetSetting(ctx, "plex_url", "http://db-plex")
	_ = stub.SetSetting(ctx, "plex_token", "db-token")
	t.Setenv("PLEX_URL", "http://env-plex")
	t.Setenv("PLEX_TOKEN", "env-token")
	urlFromDB, tokenFromDB := s.getPlexSettings(ctx)
	if urlFromDB != "http://db-plex" || tokenFromDB != "db-token" {
		t.Fatalf("getPlexSettings db precedence mismatch: (%q,%q)", urlFromDB, tokenFromDB)
	}

	// Env fallback when DB empty.
	stub2 := database.NewStubDB()
	s2 := &Server{db: stub2}
	urlFromEnv, tokenFromEnv := s2.getPlexSettings(ctx)
	if urlFromEnv != "http://env-plex" || tokenFromEnv != "env-token" {
		t.Fatalf("getPlexSettings env fallback mismatch: (%q,%q)", urlFromEnv, tokenFromEnv)
	}
}

func TestAddPlexHeaders(t *testing.T) {
	s := &Server{}
	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	s.addPlexHeaders(req, "my-token")
	if req.Header.Get("Accept") != "application/json" {
		t.Fatalf("Accept header missing")
	}
	if req.Header.Get("X-Plex-Token") != "my-token" {
		t.Fatalf("X-Plex-Token header mismatch")
	}
	if req.Header.Get("X-Plex-Client-Identifier") == "" {
		t.Fatalf("X-Plex-Client-Identifier should be set")
	}

	req2, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	s.addPlexHeaders(req2, "")
	if req2.Header.Get("X-Plex-Token") != "" {
		t.Fatalf("X-Plex-Token should not be set for empty token")
	}
}

func TestBuildPlexURLInvalidBase(t *testing.T) {
	_, err := buildPlexURL("\t\n", "/library/sections", "token", nil)
	if err == nil {
		t.Fatalf("buildPlexURL should error for invalid base URL")
	}
}
