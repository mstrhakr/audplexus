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

// TestMatchBookAgainstInventoryABSExactASIN covers the happy path: ABS items
// whose metadata.asin field exactly matches the local book's ASIN.
func TestMatchBookAgainstInventoryABSExactASIN(t *testing.T) {
	t.Parallel()

	server := &Server{}
	book := database.Book{ID: 1, ASIN: "B088C4DBYP", Title: "Heaven's River"}
	inv := diagnosticsDestinationInventory{
		Destination: database.LibraryDestination{Type: database.LibraryDestinationTypeABS},
		Items: []diagnosticsRemoteItem{{
			ID:    "abs-1",
			Title: "Heaven's River",
			Path:  "/audiobooks/Dennis E. Taylor/Heaven's River B088C4DBYP [us]",
			ASIN:  "B088C4DBYP",
		}},
	}

	matched, method, reason, remote := server.matchBookAgainstInventory(book, true, inv)
	if !matched {
		t.Fatalf("matchBookAgainstInventory() matched = false, reason = %q", reason)
	}
	if method != "asin_exact" {
		t.Fatalf("matchBookAgainstInventory() method = %q, want %q", method, "asin_exact")
	}
	if remote.ID != "abs-1" {
		t.Fatalf("matchBookAgainstInventory() remote.ID = %q, want %q", remote.ID, "abs-1")
	}
}

// TestMatchBookAgainstInventoryABSPathFallback covers the edition-mismatch case:
// ABS auto-matched the book to a different edition during library scan, so
// metadata.asin points at a different ISBN/ASIN. The book folder, however, was
// named by the Audplexus organizer with the correct identifier, so we should
// fall back to parsing the ASIN out of the destination item's path.
func TestMatchBookAgainstInventoryABSPathFallback(t *testing.T) {
	t.Parallel()

	server := &Server{}
	book := database.Book{ID: 1, ASIN: "1524779261", Title: "Atomic Habits"}
	inv := diagnosticsDestinationInventory{
		Destination: database.LibraryDestination{Type: database.LibraryDestinationTypeABS},
		Items: []diagnosticsRemoteItem{{
			ID:    "abs-42",
			Title: "Atomic Habits",
			Path:  "/audiobooks/James Clear/Atomic Habits 1524779261 [us]",
			// ABS picked up an alternate UK edition during its scan.
			ASIN: "1473565421",
		}},
	}

	matched, method, reason, remote := server.matchBookAgainstInventory(book, true, inv)
	if !matched {
		t.Fatalf("matchBookAgainstInventory() matched = false, reason = %q", reason)
	}
	if method != "asin_path" {
		t.Fatalf("matchBookAgainstInventory() method = %q, want %q", method, "asin_path")
	}
	if remote.ID != "abs-42" {
		t.Fatalf("matchBookAgainstInventory() remote.ID = %q, want %q", remote.ID, "abs-42")
	}
}

// TestMatchBookAgainstInventoryABSMissingWhenPathAlsoDoesNotMatch ensures we
// still report "missing" when neither metadata.asin nor any item path contains
// the local ASIN. This guards against the path fallback being too greedy.
func TestMatchBookAgainstInventoryABSMissingWhenPathAlsoDoesNotMatch(t *testing.T) {
	t.Parallel()

	server := &Server{}
	book := database.Book{ID: 1, ASIN: "B0FAKE0001", Title: "Not In Library"}
	inv := diagnosticsDestinationInventory{
		Destination: database.LibraryDestination{Type: database.LibraryDestinationTypeABS},
		Items: []diagnosticsRemoteItem{{
			ID:    "abs-99",
			Title: "Some Other Book",
			Path:  "/audiobooks/Someone Else/Some Other Book B099OTHER1 [us]",
			ASIN:  "B099OTHER1",
		}},
	}

	matched, _, reason, _ := server.matchBookAgainstInventory(book, true, inv)
	if matched {
		t.Fatalf("matchBookAgainstInventory() matched = true, want false")
	}
	if reason != "missing: ASIN not found in destination" {
		t.Fatalf("matchBookAgainstInventory() reason = %q, want missing", reason)
	}
}