package library

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func newTestDB(t *testing.T) *database.SQLiteDB {
	t.Helper()
	db, err := database.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestFirstBootSynthesisIsNoOpWhenDestinationsAlreadyExist(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Pre-create one destination — synthesis must not duplicate.
	if err := db.CreateLibraryDestination(ctx, &database.LibraryDestination{
		ID: "preexisting", DisplayName: "Existing", Type: database.LibraryDestinationTypePlex,
		Enabled: true, URL: "http://x", PlexToken: "t", PlexSectionID: "1",
	}); err != nil {
		t.Fatal(err)
	}

	// Set legacy settings — synthesis SHOULD ignore them.
	_ = db.SetSetting(ctx, "media_server_type", "plex")
	_ = db.SetSetting(ctx, "plex_url", "http://other")
	_ = db.SetSetting(ctx, "plex_token", "other-token")
	_ = db.SetSetting(ctx, "plex_section_id", "9")

	if err := SynthesizeLibraryDestinationsIfEmpty(ctx, db); err != nil {
		t.Fatalf("SynthesizeLibraryDestinationsIfEmpty: %v", err)
	}

	all, _ := db.ListLibraryDestinations(ctx)
	if len(all) != 1 {
		t.Errorf("expected 1 destination (pre-existing), got %d", len(all))
	}
	if all[0].ID != "preexisting" {
		t.Errorf("synthesis overwrote pre-existing destination — id was %q", all[0].ID)
	}
}

func TestFirstBootSynthesisCreatesPlexFromLegacySettings(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_ = db.SetSetting(ctx, "media_server_type", "plex")
	_ = db.SetSetting(ctx, "plex_url", "http://plex.lan:32400")
	_ = db.SetSetting(ctx, "plex_token", "tok")
	_ = db.SetSetting(ctx, "plex_section_id", "5")
	_ = db.SetSetting(ctx, "plex_section_path", "/data/audiobooks")

	if err := SynthesizeLibraryDestinationsIfEmpty(ctx, db); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	all, _ := db.ListLibraryDestinations(ctx)
	if len(all) != 1 {
		t.Fatalf("expected 1 synthesized destination, got %d", len(all))
	}
	if all[0].Type != database.LibraryDestinationTypePlex || all[0].URL != "http://plex.lan:32400" || all[0].PlexToken != "tok" || all[0].PlexSectionID != "5" {
		t.Errorf("synthesized plex destination wrong: %+v", all[0])
	}
	// Server-side path semantics: plex_section_path is the Plex-side library
	// path (translation target), not the audplexus-side source path. Must
	// land in DestinationPath, not AudiobookPath. Copilot review caught this.
	if all[0].DestinationPath != "/data/audiobooks" {
		t.Errorf("plex_section_path must synthesize into DestinationPath (server-side); got %q", all[0].DestinationPath)
	}
	if all[0].AudiobookPath != "" {
		t.Errorf("AudiobookPath must remain empty so global libraryDir is the source; got %q", all[0].AudiobookPath)
	}
}

func TestFirstBootSynthesisCreatesEmbyFromLegacySettings(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_ = db.SetSetting(ctx, "media_server_type", "emby")
	_ = db.SetSetting(ctx, "emby_url", "http://emby.lan:8096")
	_ = db.SetSetting(ctx, "emby_api_key", "key")
	_ = db.SetSetting(ctx, "emby_library_id", "lib1")
	_ = db.SetSetting(ctx, "emby_library_path", "/media/audiobooks")

	if err := SynthesizeLibraryDestinationsIfEmpty(ctx, db); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	all, _ := db.ListLibraryDestinations(ctx)
	if len(all) != 1 || all[0].Type != database.LibraryDestinationTypeEmby {
		t.Fatalf("expected one emby destination, got %+v", all)
	}
	if all[0].URL != "http://emby.lan:8096" || all[0].APIKey != "key" || all[0].LibraryID != "lib1" {
		t.Errorf("emby destination synthesis wrong: %+v", all[0])
	}
	// Same server-side semantics as plex_section_path.
	if all[0].DestinationPath != "/media/audiobooks" {
		t.Errorf("emby_library_path must synthesize into DestinationPath (server-side); got %q", all[0].DestinationPath)
	}
	if all[0].AudiobookPath != "" {
		t.Errorf("AudiobookPath must remain empty so global libraryDir is the source; got %q", all[0].AudiobookPath)
	}
}

func TestFirstBootSynthesisSkipsIncompleteConfig(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Type is set but required fields are missing — silent skip, NOT a CHECK violation.
	_ = db.SetSetting(ctx, "media_server_type", "plex")
	_ = db.SetSetting(ctx, "plex_url", "http://plex.lan:32400")
	// plex_token and plex_section_id intentionally empty

	if err := SynthesizeLibraryDestinationsIfEmpty(ctx, db); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	all, _ := db.ListLibraryDestinations(ctx)
	if len(all) != 0 {
		t.Errorf("expected synthesis to skip incomplete config, but created %d destination(s): %+v", len(all), all)
	}
}

// TestFirstBootSynthesisInfersPlexFromConfigWhenTypeUnset is the
// regression test for codex P1 finding: existing v0.2.x Plex installs
// commonly never set MEDIA_SERVER or media_server_type because Plex was
// the silent default. Without this fallback, those installs would
// upgrade to a non-nil DestinationManager + empty library_destinations
// and silently lose every post-download scan/collection trigger.
func TestFirstBootSynthesisInfersPlexFromConfigWhenTypeUnset(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Critical: NO media_server_type set, NO MEDIA_SERVER env var.
	// But plex_* settings ARE present (the typical v0.2.x Plex install).
	t.Setenv("MEDIA_SERVER", "")
	_ = db.SetSetting(ctx, "plex_url", "http://plex.lan:32400")
	_ = db.SetSetting(ctx, "plex_token", "tok")
	_ = db.SetSetting(ctx, "plex_section_id", "5")

	if err := SynthesizeLibraryDestinationsIfEmpty(ctx, db); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	all, _ := db.ListLibraryDestinations(ctx)
	if len(all) != 1 {
		t.Fatalf("expected 1 inferred Plex destination (codex P1), got %d", len(all))
	}
	if all[0].Type != database.LibraryDestinationTypePlex {
		t.Errorf("expected Plex destination inferred from plex_* settings; got %q", all[0].Type)
	}
}

// TestFirstBootSynthesisInfersEmbyFromConfigWhenTypeUnset — same shape
// for Emby. Less common than the Plex variant but worth covering.
func TestFirstBootSynthesisInfersEmbyFromConfigWhenTypeUnset(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	t.Setenv("MEDIA_SERVER", "")
	_ = db.SetSetting(ctx, "emby_url", "http://emby.lan:8096")
	_ = db.SetSetting(ctx, "emby_api_key", "key")
	_ = db.SetSetting(ctx, "emby_library_id", "lib1")

	if err := SynthesizeLibraryDestinationsIfEmpty(ctx, db); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	all, _ := db.ListLibraryDestinations(ctx)
	if len(all) != 1 || all[0].Type != database.LibraryDestinationTypeEmby {
		t.Fatalf("expected 1 Emby inferred destination, got %+v", all)
	}
}

func TestFirstBootSynthesisHandlesNoLegacyConfig(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// No env vars, no settings — fresh install. Synthesis should be a no-op.
	if err := SynthesizeLibraryDestinationsIfEmpty(ctx, db); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	all, _ := db.ListLibraryDestinations(ctx)
	if len(all) != 0 {
		t.Errorf("expected 0 destinations on fresh install, got %d", len(all))
	}
}
