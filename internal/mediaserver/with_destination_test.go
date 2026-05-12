package mediaserver

import (
	"context"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

// TestWithDestinationOverridesSettingsTable confirms that a destination row
// passed via WithDestination wins over settings-table values. Without this
// override, two destinations of the same type can't have independent config.
func TestWithDestinationOverridesSettingsTable(t *testing.T) {
	db := newSettingsOnlyStubDB()
	// Settings-table values represent the "old" / "single backend" config.
	_ = db.SetSetting(context.Background(), "plex_url", "http://settings-table-plex.invalid")
	_ = db.SetSetting(context.Background(), "plex_token", "settings-token")
	_ = db.SetSetting(context.Background(), "plex_section_id", "1")
	_ = db.SetSetting(context.Background(), "emby_url", "http://settings-table-emby.invalid")
	_ = db.SetSetting(context.Background(), "emby_api_key", "settings-key")
	_ = db.SetSetting(context.Background(), "emby_library_id", "settings-lib")

	// Bind to a row with a DIFFERENT URL.
	row := &database.LibraryDestination{
		ID:            "household-plex",
		Type:          database.LibraryDestinationTypePlex,
		URL:           "http://household.plex.lan",
		PlexToken:     "household-token",
		PlexSectionID: "5",
	}
	plex := NewPlex(db, "/audiobooks").WithDestination(row)

	url, tok, sec := plex.settings(context.Background())
	if url != "http://household.plex.lan" {
		t.Errorf("Plex.settings url = %q, want destination row's value (settings table should NOT win)", url)
	}
	if tok != "household-token" {
		t.Errorf("Plex.settings token = %q, want %q", tok, "household-token")
	}
	if sec != "5" {
		t.Errorf("Plex.settings section = %q, want %q", sec, "5")
	}

	// Same for Emby with its own row.
	emRow := &database.LibraryDestination{
		ID:        "parents-emby",
		Type:      database.LibraryDestinationTypeEmby,
		URL:       "http://parents.emby.lan",
		APIKey:    "parents-key",
		LibraryID: "parents-lib",
	}
	emby := NewEmby(db, nil, "/audiobooks").WithDestination(emRow)
	u, k, l := emby.settings(context.Background())
	if u != "http://parents.emby.lan" || k != "parents-key" || l != "parents-lib" {
		t.Errorf("Emby.settings did not honor destination row: got (%q,%q,%q)", u, k, l)
	}
}

