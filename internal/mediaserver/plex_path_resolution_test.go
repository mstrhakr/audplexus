package mediaserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestResolveScanPathUsesDestinationPathWithLibraryDirFallback(t *testing.T) {
	t.Parallel()

	db := newSettingsOnlyStubDB()
	p := NewPlex(db, "/audiobooks").WithDestination(&database.LibraryDestination{
		DestinationPath: "/mnt/exports/audiobooks",
	})

	got, ok := p.resolveScanPath(context.Background(), "http://unused", "token", "10", "/audiobooks/Author/Book")
	if !ok {
		t.Fatal("resolveScanPath() = not ok, want ok")
	}
	if got != "/mnt/exports/audiobooks/Author/Book" {
		t.Fatalf("resolveScanPath() = %q, want %q", got, "/mnt/exports/audiobooks/Author/Book")
	}
}

func TestFetchSectionPathFallsBackToSectionsList(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/library/sections/10":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"Location":[]}]}}`))
		case "/library/sections":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"10","Location":[{"path":"/mnt/exports/audiobooks"}]}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p := &PlexBackend{clientID: "test-client"}
	got, err := p.fetchSectionPath(context.Background(), server.URL, "token", "10")
	if err != nil {
		t.Fatalf("fetchSectionPath() error = %v", err)
	}
	if got != "/mnt/exports/audiobooks" {
		t.Fatalf("fetchSectionPath() = %q, want %q", got, "/mnt/exports/audiobooks")
	}
}

func TestOnBookOrganizedFallsBackToFullSectionScanWhenPathUnavailable(t *testing.T) {
	t.Parallel()

	db := newSettingsOnlyStubDB()
	_ = db.SetSetting(context.Background(), "plex_url", "http://placeholder.invalid")
	_ = db.SetSetting(context.Background(), "plex_token", "token")
	_ = db.SetSetting(context.Background(), "plex_section_id", "10")

	refreshCalled := false
	refreshPathParam := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/library/sections/10":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"Location":[]}]}}`))
		case "/library/sections":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"10","Location":[]}]}}`))
		case "/library/sections/10/refresh":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			refreshCalled = true
			refreshPathParam = r.URL.Query().Get("path")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	_ = db.SetSetting(context.Background(), "plex_url", server.URL)

	p := NewPlex(db, "/not-the-real-root")
	outcomes := p.OnBookOrganized(context.Background(), OrganizedBook{
		BookID:    1,
		ASIN:      "B0TEST",
		Title:     "Book",
		LocalPath: "/audiobooks/Author/Book.m4b",
	})

	if !refreshCalled {
		t.Fatal("expected fallback full section refresh to be called")
	}
	if refreshPathParam != "" {
		t.Fatalf("refresh path query = %q, want empty (full section scan)", refreshPathParam)
	}
	if len(outcomes) == 0 {
		t.Fatal("expected at least one outcome")
	}
	if outcomes[0].Operation != OpScanTrigger || outcomes[0].Status != OutcomeSucceeded {
		t.Fatalf("scan outcome = (%s,%s), want (%s,%s)", outcomes[0].Operation, outcomes[0].Status, OpScanTrigger, OutcomeSucceeded)
	}
}
