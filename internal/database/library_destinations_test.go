package database

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// newTestSQLite returns a fresh SQLite DB with all migrations applied.
// Each test gets its own isolated file under t.TempDir().
func newTestSQLite(t *testing.T) *SQLiteDB {
	t.Helper()
	db, err := NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestCreateAndListLibraryDestinationsEmpty(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	dests, err := db.ListLibraryDestinations(ctx)
	if err != nil {
		t.Fatalf("ListLibraryDestinations on empty: %v", err)
	}
	if len(dests) != 0 {
		t.Fatalf("expected 0 destinations on fresh DB, got %d", len(dests))
	}
}

func TestCreateAndRetrievePlexDestination(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	d := &LibraryDestination{
		ID:            uuid.NewString(),
		DisplayName:   "Living room Plex",
		Type:          LibraryDestinationTypePlex,
		Enabled:       true,
		URL:           "http://plex.lan:32400",
		PlexToken:     "secret-plex-token",
		PlexSectionID: "5",
		AudiobookPath: "/mnt/media/audiobooks",
	}
	if err := db.CreateLibraryDestination(ctx, d); err != nil {
		t.Fatalf("CreateLibraryDestination: %v", err)
	}

	got, err := db.GetLibraryDestination(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetLibraryDestination: %v", err)
	}
	if got == nil {
		t.Fatal("GetLibraryDestination returned nil")
	}
	if got.DisplayName != "Living room Plex" || got.Type != LibraryDestinationTypePlex {
		t.Errorf("destination round-trip lost fields: %+v", got)
	}
	if got.PlexToken != "secret-plex-token" {
		t.Errorf("plex_token round-trip lost: got %q", got.PlexToken)
	}
	if !got.Enabled {
		t.Error("enabled should be true after round-trip")
	}
}

func TestCreatePlexDestinationRejectsMissingRequired(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	// Missing plex_token — schema CHECK should reject.
	d := &LibraryDestination{
		ID:            uuid.NewString(),
		DisplayName:   "Bad Plex",
		Type:          LibraryDestinationTypePlex,
		Enabled:       true,
		URL:           "http://plex.lan:32400",
		PlexSectionID: "5",
		// PlexToken intentionally empty
	}
	err := db.CreateLibraryDestination(ctx, d)
	if err == nil {
		t.Fatal("expected CreateLibraryDestination to fail with missing plex_token; got nil")
	}
}

// TestCreateRejectsWhitespaceOnlyValues guards the post-Copilot CHECK
// constraint tightening: bare length(trim(col))>0 would silently accept
// NULL columns because SQL CHECK passes on NULL. The coalesce(...,'')
// wrapping is what makes "  " and NULL both fail the constraint.
func TestCreateRejectsWhitespaceOnlyValues(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	// All required fields present but URL is whitespace-only — CHECK
	// must still reject. nullableStr trims to "" → NULL → coalesce(NULL,'')
	// → length(trim('')) = 0 → constraint fails. If anyone replaces
	// nullableStr with a passthrough later, this test is the tripwire.
	d := &LibraryDestination{
		ID:            uuid.NewString(),
		DisplayName:   "Whitespace URL",
		Type:          LibraryDestinationTypePlex,
		Enabled:       true,
		URL:           "   ",
		PlexToken:     "tok",
		PlexSectionID: "5",
	}
	if err := db.CreateLibraryDestination(ctx, d); err == nil {
		t.Fatal("expected CHECK to reject whitespace-only URL; got nil")
	}
}

func TestUpdateLibraryDestination(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	d := &LibraryDestination{
		ID:        uuid.NewString(),
		DisplayName: "Home Emby",
		Type:      LibraryDestinationTypeEmby,
		Enabled:   true,
		URL:       "http://emby.lan:8096",
		APIKey:    "emby-key-1",
		LibraryID: "lib-1",
	}
	if err := db.CreateLibraryDestination(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}

	d.DisplayName = "Home Emby (renamed)"
	d.Enabled = false
	d.APIKey = "emby-key-2-rotated"
	if err := db.UpdateLibraryDestination(ctx, d); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := db.GetLibraryDestination(ctx, d.ID)
	if got.DisplayName != "Home Emby (renamed)" {
		t.Errorf("rename did not persist: %q", got.DisplayName)
	}
	if got.Enabled {
		t.Error("disable did not persist")
	}
	if got.APIKey != "emby-key-2-rotated" {
		t.Errorf("api key rotation did not persist: %q", got.APIKey)
	}
}

func TestListEnabledLibraryDestinationsFiltersDisabled(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	enabled := &LibraryDestination{
		ID: uuid.NewString(), DisplayName: "Enabled Plex", Type: LibraryDestinationTypePlex,
		Enabled: true, URL: "http://e", PlexToken: "t", PlexSectionID: "1",
	}
	disabled := &LibraryDestination{
		ID: uuid.NewString(), DisplayName: "Disabled Plex", Type: LibraryDestinationTypePlex,
		Enabled: false, URL: "http://d", PlexToken: "t", PlexSectionID: "1",
	}
	if err := db.CreateLibraryDestination(ctx, enabled); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateLibraryDestination(ctx, disabled); err != nil {
		t.Fatal(err)
	}

	all, _ := db.ListLibraryDestinations(ctx)
	if len(all) != 2 {
		t.Errorf("ListLibraryDestinations = %d, want 2", len(all))
	}
	enabledOnly, _ := db.ListEnabledLibraryDestinations(ctx)
	if len(enabledOnly) != 1 || enabledOnly[0].DisplayName != "Enabled Plex" {
		t.Errorf("ListEnabledLibraryDestinations failed to filter: %+v", enabledOnly)
	}
}

func TestDeleteLibraryDestination(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	d := &LibraryDestination{
		ID: uuid.NewString(), DisplayName: "Plex", Type: LibraryDestinationTypePlex,
		Enabled: true, URL: "http://p", PlexToken: "t", PlexSectionID: "1",
	}
	if err := db.CreateLibraryDestination(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteLibraryDestination(ctx, d.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := db.GetLibraryDestination(ctx, d.ID)
	if got != nil {
		t.Error("destination still present after delete")
	}
}

func TestUpsertBookDestinationCreatesAndUpdates(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	// Need an existing book row + destination for the FKs to satisfy.
	book := &Book{ASIN: "B0XYZTEST", Title: "Test", Status: BookStatusComplete}
	if err := db.UpsertBook(ctx, book); err != nil {
		t.Fatalf("UpsertBook: %v", err)
	}
	dest := &LibraryDestination{
		ID: uuid.NewString(), DisplayName: "Plex", Type: LibraryDestinationTypePlex,
		Enabled: true, URL: "http://p", PlexToken: "t", PlexSectionID: "1",
	}
	if err := db.CreateLibraryDestination(ctx, dest); err != nil {
		t.Fatalf("CreateLibraryDestination: %v", err)
	}

	// First upsert: create.
	bd := &BookDestination{
		BookID:        book.ID,
		DestinationID: dest.ID,
		ServerItemID:  "12345",
		SyncState:     BookDestSyncSynced,
		AttemptCount:  1,
	}
	if err := db.UpsertBookDestination(ctx, bd); err != nil {
		t.Fatalf("UpsertBookDestination create: %v", err)
	}

	got, err := db.GetBookDestination(ctx, book.ID, dest.ID)
	if err != nil {
		t.Fatalf("GetBookDestination: %v", err)
	}
	if got == nil || got.SyncState != BookDestSyncSynced || got.ServerItemID != "12345" {
		t.Fatalf("create round-trip wrong: %+v", got)
	}

	// Second upsert: update (idempotent).
	bd.AttemptCount = 2
	bd.LastError = "transient network blip"
	bd.SyncState = BookDestSyncFailed
	if err := db.UpsertBookDestination(ctx, bd); err != nil {
		t.Fatalf("UpsertBookDestination update: %v", err)
	}
	got, _ = db.GetBookDestination(ctx, book.ID, dest.ID)
	if got.AttemptCount != 2 || got.SyncState != BookDestSyncFailed || got.LastError == "" {
		t.Fatalf("update did not persist: %+v", got)
	}
}

func TestGetBookDestinationsReturnsAll(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	book := &Book{ASIN: "B0FAN", Title: "Fanout", Status: BookStatusComplete}
	if err := db.UpsertBook(ctx, book); err != nil {
		t.Fatal(err)
	}
	dest1 := &LibraryDestination{ID: uuid.NewString(), DisplayName: "Plex", Type: LibraryDestinationTypePlex, Enabled: true, URL: "http://p", PlexToken: "t", PlexSectionID: "1"}
	dest2 := &LibraryDestination{ID: uuid.NewString(), DisplayName: "Emby", Type: LibraryDestinationTypeEmby, Enabled: true, URL: "http://e", APIKey: "k", LibraryID: "1"}
	for _, d := range []*LibraryDestination{dest1, dest2} {
		if err := db.CreateLibraryDestination(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []*LibraryDestination{dest1, dest2} {
		bd := &BookDestination{BookID: book.ID, DestinationID: d.ID, SyncState: BookDestSyncSynced}
		if err := db.UpsertBookDestination(ctx, bd); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.GetBookDestinations(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetBookDestinations: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 fan-out rows, got %d", len(got))
	}
}

func TestCascadeDeleteFromBookRemovesDestinationRows(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	book := &Book{ASIN: "B0CASC", Title: "Cascade Test", Status: BookStatusComplete}
	if err := db.UpsertBook(ctx, book); err != nil {
		t.Fatal(err)
	}
	dest := &LibraryDestination{ID: uuid.NewString(), DisplayName: "Plex", Type: LibraryDestinationTypePlex, Enabled: true, URL: "http://p", PlexToken: "t", PlexSectionID: "1"}
	if err := db.CreateLibraryDestination(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertBookDestination(ctx, &BookDestination{BookID: book.ID, DestinationID: dest.ID, SyncState: BookDestSyncSynced}); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteBook(ctx, book.ID); err != nil {
		t.Fatalf("DeleteBook: %v", err)
	}
	got, _ := db.GetBookDestination(ctx, book.ID, dest.ID)
	if got != nil {
		t.Errorf("FK cascade delete did not fire — book_library_destinations row survives: %+v", got)
	}
}
