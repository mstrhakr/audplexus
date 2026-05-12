package web

import (
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestNormalizeDiagnosticsPathKey(t *testing.T) {
	t.Parallel()

	got := normalizeDiagnosticsPathKey("/mnt/library/Some Artist/Some Album/track01.mp3")
	if got != "some artist/some album" {
		t.Fatalf("normalizeDiagnosticsPathKey() = %q, want %q", got, "some artist/some album")
	}
}

func TestMatchBookAgainstInventoryPlexUsesPathSuffix(t *testing.T) {
	t.Parallel()

	server := &Server{}
	book := database.Book{
		ID:       1,
		ASIN:     "B000TEST",
		Title:    "Different Root",
		FilePath: "/audiobooks/Some Artist/Some Album/book.m4b",
	}
	inv := diagnosticsDestinationInventory{
		Destination: database.LibraryDestination{Type: database.LibraryDestinationTypePlex},
		Items: []diagnosticsRemoteItem{{
			ID:    "211150",
			Title: "Some Album",
			Path:  "/plex/root/Some Artist/Some Album/track01.mp3",
		}},
	}

	matched, method, reason, remote := server.matchBookAgainstInventory(book, true, inv)
	if !matched {
		t.Fatalf("matchBookAgainstInventory() matched = false, reason = %q", reason)
	}
	if method != "path_suffix" {
		t.Fatalf("matchBookAgainstInventory() method = %q, want %q", method, "path_suffix")
	}
	if remote.ID != "211150" {
		t.Fatalf("matchBookAgainstInventory() remote.ID = %q, want %q", remote.ID, "211150")
	}
}