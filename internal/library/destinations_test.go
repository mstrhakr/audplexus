package library

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
)

// integration-style test: real SQLite, real synthesis, real fan-out.
// Backends won't actually reach Plex/Emby (no URLs configured), so they
// return SkippedNotConfigured outcomes — which is exactly what we want
// to assert the contract (no silent no-ops, per-destination state recorded).
func TestDestinationManagerFanOutRecordsPerDestinationState(t *testing.T) {
	db, err := database.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ctx := context.Background()

	// Create a book.
	book := &database.Book{ASIN: "B0FANOUT", Title: "FanOut Book", Status: database.BookStatusComplete}
	if err := db.UpsertBook(ctx, book); err != nil {
		t.Fatalf("UpsertBook: %v", err)
	}

	// Create two destinations with placeholder URLs that DON'T resolve.
	// Backends now read URL/token/library_id from the destination row
	// (PR-F's WithDestination binding), so they'll attempt the call and
	// fail at the DNS/dial layer — outcome is "failed", not "skipped".
	// That's the correct new contract: row populated → backend tries
	// → typed Outcome reports the real error.
	dest1 := &database.LibraryDestination{
		ID: "dest-plex", DisplayName: "Plex", Type: database.LibraryDestinationTypePlex,
		Enabled: true, URL: "http://plex.invalid", PlexToken: "t", PlexSectionID: "5",
	}
	dest2 := &database.LibraryDestination{
		ID: "dest-emby", DisplayName: "Emby", Type: database.LibraryDestinationTypeEmby,
		Enabled: true, URL: "http://emby.invalid", APIKey: "k", LibraryID: "1",
	}
	for _, d := range []*database.LibraryDestination{dest1, dest2} {
		if err := db.CreateLibraryDestination(ctx, d); err != nil {
			t.Fatalf("CreateLibraryDestination: %v", err)
		}
	}

	mgr := NewDestinationManager(db, nil, "/audiobooks", 2)

	results := mgr.FanOut(ctx, mediaserver.OrganizedBook{
		BookID: book.ID, Title: book.Title, ASIN: book.ASIN,
		LocalPath: "/audiobooks/test.m4b",
	})

	if len(results) != 2 {
		t.Fatalf("FanOut returned %d results, want 2 (one per destination)", len(results))
	}

	// Every destination should have a row recorded in book_library_destinations.
	bds, err := db.GetBookDestinations(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetBookDestinations: %v", err)
	}
	if len(bds) != 2 {
		t.Fatalf("expected 2 book_library_destinations rows, got %d", len(bds))
	}
	for _, bd := range bds {
		if bd.AttemptCount != 1 {
			t.Errorf("dest=%s expected attempt_count=1, got %d", bd.DestinationID, bd.AttemptCount)
		}
		if bd.LastAttemptedAt == nil {
			t.Errorf("dest=%s last_attempted_at should be populated", bd.DestinationID)
		}
		if bd.PerOpOutcomes == "" {
			t.Errorf("dest=%s per_op_outcomes should be a non-empty JSON object", bd.DestinationID)
		}
		if !strings.Contains(bd.PerOpOutcomes, "\"failed\"") {
			// Backends now read URL from the destination row. With an
			// invalid hostname, the scan trigger fails at DNS/dial — that
			// is the exact contract we want: typed outcome reports the
			// real failure rather than silently no-op'ing.
			t.Errorf("dest=%s expected failed status in per_op_outcomes, got %q", bd.DestinationID, bd.PerOpOutcomes)
		}
		if bd.SyncState != database.BookDestSyncFailed {
			t.Errorf("dest=%s expected sync_state=failed, got %q", bd.DestinationID, bd.SyncState)
		}
	}
}

func TestDestinationManagerFanOutNoEnabledDestinations(t *testing.T) {
	db, err := database.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	mgr := NewDestinationManager(db, nil, "/audiobooks", 2)

	// No destinations created — fan-out is a no-op.
	results := mgr.FanOut(context.Background(), mediaserver.OrganizedBook{BookID: 1})
	if results != nil && len(results) != 0 {
		t.Errorf("expected 0 results with no destinations, got %d", len(results))
	}
}

func TestSummarizeOutcomesState(t *testing.T) {
	cases := []struct {
		name     string
		outcomes []mediaserver.Outcome
		want     database.BookDestinationSyncState
	}{
		{
			name:     "all succeeded → synced",
			outcomes: []mediaserver.Outcome{{Operation: "scan_trigger", Status: mediaserver.OutcomeSucceeded}, {Operation: "item_match", Status: mediaserver.OutcomeSucceeded}},
			want:     database.BookDestSyncSynced,
		},
		{
			name:     "any failed → failed",
			outcomes: []mediaserver.Outcome{{Operation: "scan_trigger", Status: mediaserver.OutcomeSucceeded}, {Operation: "item_match", Status: mediaserver.OutcomeFailed}},
			want:     database.BookDestSyncFailed,
		},
		{
			name:     "skipped_not_configured short-circuit → failed (destination not ready)",
			outcomes: []mediaserver.Outcome{{Operation: "scan_trigger", Status: mediaserver.OutcomeSkippedNotConfigured}},
			want:     database.BookDestSyncFailed,
		},
		{
			name:     "deferred + succeeded → synced",
			outcomes: []mediaserver.Outcome{{Operation: "scan_trigger", Status: mediaserver.OutcomeSucceeded}, {Operation: "item_match", Status: mediaserver.OutcomeDeferred}},
			want:     database.BookDestSyncSynced,
		},
		{
			name:     "empty outcomes → pending",
			outcomes: nil,
			want:     database.BookDestSyncPending,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeOutcomesState(tc.outcomes)
			if got != tc.want {
				t.Errorf("summarizeOutcomesState = %q, want %q", got, tc.want)
			}
		})
	}
}
