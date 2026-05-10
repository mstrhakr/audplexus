package library

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/mstrhakr/audplexus/internal/database"
)

// SynthesizeLibraryDestinationsIfEmpty is the first-boot synthesis pass that
// creates a single library_destinations row from existing single-backend
// settings (the legacy MEDIA_SERVER env + settings table keys). It is a
// no-op when at least one destination already exists.
//
// Codex review flagged the original "SQL migration reads MEDIA_SERVER env
// var" plan as wrong — migrations can't do that. Synthesis is application
// code at first boot after PR-B schema lands, exactly here.
//
// The synthesized destination becomes the implicit "first" destination for
// dual-write: UpdateBookMediaServerInfo writes to both the legacy
// books.media_server_id columns AND a book_library_destinations row keyed
// by this destination's ID.
func SynthesizeLibraryDestinationsIfEmpty(ctx context.Context, db database.Database) error {
	existing, err := db.ListLibraryDestinations(ctx)
	if err != nil {
		return fmt.Errorf("list library_destinations during first-boot synthesis: %w", err)
	}
	if len(existing) > 0 {
		// Already populated. Either by a previous boot or by a user via UI.
		return nil
	}

	t := resolveLegacyMediaServerType(ctx, db)
	if t == "" {
		// No legacy config, nothing to synthesize. User will add destinations
		// via the Settings UI.
		return nil
	}

	d, err := buildSynthesizedDestination(ctx, db, t)
	if err != nil {
		return fmt.Errorf("build synthesized destination: %w", err)
	}
	if d == nil {
		// Type was set but required config fields were empty — skip silently.
		// This happens for fresh installs where MEDIA_SERVER=plex is set
		// but no plex_url/plex_token has been configured yet.
		return nil
	}

	if err := db.CreateLibraryDestination(ctx, d); err != nil {
		return fmt.Errorf("create synthesized library_destination: %w", err)
	}
	return nil
}

func resolveLegacyMediaServerType(ctx context.Context, db database.Database) database.LibraryDestinationType {
	v, _ := db.GetSetting(ctx, "media_server_type")
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		v = strings.ToLower(strings.TrimSpace(os.Getenv("MEDIA_SERVER")))
	}
	switch database.LibraryDestinationType(v) {
	case database.LibraryDestinationTypePlex:
		return database.LibraryDestinationTypePlex
	case database.LibraryDestinationTypeEmby:
		return database.LibraryDestinationTypeEmby
	case database.LibraryDestinationTypeJellyfin:
		return database.LibraryDestinationTypeJellyfin
	case database.LibraryDestinationTypeABS:
		return database.LibraryDestinationTypeABS
	}

	// CRITICAL upgrade-path fix (codex P1): existing v0.2.x Plex installs
	// commonly never set media_server_type or MEDIA_SERVER because Plex was
	// the silent default. Without this fallback those installs would
	// upgrade to a non-nil DestinationManager with an empty
	// library_destinations table, causing FanOut to no-op and the legacy
	// pipeline_stages.go fallback to also no-op (it only fires when
	// dm.destinations is nil). Detect "Plex configured via legacy
	// settings/env but no explicit type" and synthesize a Plex row.
	if hasPlexLegacyConfig(ctx, db) {
		return database.LibraryDestinationTypePlex
	}
	if hasEmbyLegacyConfig(ctx, db) {
		return database.LibraryDestinationTypeEmby
	}
	return ""
}

func hasPlexLegacyConfig(ctx context.Context, db database.Database) bool {
	return settingOrEnv(ctx, db, "plex_url", "PLEX_URL") != "" &&
		settingOrEnv(ctx, db, "plex_token", "PLEX_TOKEN") != "" &&
		settingOrEnv(ctx, db, "plex_section_id", "PLEX_SECTION_ID") != ""
}

func hasEmbyLegacyConfig(ctx context.Context, db database.Database) bool {
	return settingOrEnv(ctx, db, "emby_url", "EMBY_URL") != "" &&
		settingOrEnv(ctx, db, "emby_api_key", "EMBY_API_KEY") != "" &&
		settingOrEnv(ctx, db, "emby_library_id", "EMBY_LIBRARY_ID") != ""
}

func buildSynthesizedDestination(ctx context.Context, db database.Database, t database.LibraryDestinationType) (*database.LibraryDestination, error) {
	d := &database.LibraryDestination{
		ID:      uuid.NewString(),
		Type:    t,
		Enabled: true,
	}

	switch t {
	case database.LibraryDestinationTypePlex:
		d.DisplayName = "Plex"
		d.URL = settingOrEnv(ctx, db, "plex_url", "PLEX_URL")
		d.PlexToken = settingOrEnv(ctx, db, "plex_token", "PLEX_TOKEN")
		d.PlexSectionID = settingOrEnv(ctx, db, "plex_section_id", "PLEX_SECTION_ID")
		// plex_section_path is the SERVER-SIDE library path (target of path
		// translation in PlexBackend.resolveScanPath), not the audplexus-side
		// path. Persist it as DestinationPath; leave AudiobookPath empty so
		// the global libraryDir is the source. Copilot review caught this
		// inversion — flipping the fields would have shown up wrong in the
		// "Audplexus-side audiobook path" Settings field for upgrades.
		d.DestinationPath = settingOrEnv(ctx, db, "plex_section_path", "")
		// Required-fields gate so the CHECK constraint doesn't reject the row.
		if d.URL == "" || d.PlexToken == "" || d.PlexSectionID == "" {
			return nil, nil
		}
	case database.LibraryDestinationTypeEmby:
		d.DisplayName = "Emby"
		d.URL = settingOrEnv(ctx, db, "emby_url", "EMBY_URL")
		d.APIKey = settingOrEnv(ctx, db, "emby_api_key", "EMBY_API_KEY")
		d.LibraryID = settingOrEnv(ctx, db, "emby_library_id", "EMBY_LIBRARY_ID")
		// emby_library_path is the SERVER-SIDE library path (used by
		// EmbyBackend.libraryServerPath as the translation target), same
		// inversion as plex_section_path above. Goes to DestinationPath.
		d.DestinationPath = settingOrEnv(ctx, db, "emby_library_path", "EMBY_LIBRARY_PATH")
		if d.URL == "" || d.APIKey == "" || d.LibraryID == "" {
			return nil, nil
		}
	case database.LibraryDestinationTypeJellyfin:
		d.DisplayName = "Jellyfin"
		d.URL = settingOrEnv(ctx, db, "jellyfin_url", "JELLYFIN_URL")
		d.APIKey = settingOrEnv(ctx, db, "jellyfin_api_key", "JELLYFIN_API_KEY")
		d.LibraryID = settingOrEnv(ctx, db, "jellyfin_library_id", "JELLYFIN_LIBRARY_ID")
		if d.URL == "" || d.APIKey == "" || d.LibraryID == "" {
			return nil, nil
		}
	case database.LibraryDestinationTypeABS:
		d.DisplayName = "Audiobookshelf"
		d.URL = settingOrEnv(ctx, db, "abs_url", "ABS_URL")
		d.APIKey = settingOrEnv(ctx, db, "abs_api_key", "ABS_API_KEY")
		d.LibraryID = settingOrEnv(ctx, db, "abs_library_id", "ABS_LIBRARY_ID")
		if d.URL == "" || d.APIKey == "" || d.LibraryID == "" {
			return nil, nil
		}
	default:
		return nil, fmt.Errorf("unsupported synthesis type: %q", t)
	}
	return d, nil
}

func settingOrEnv(ctx context.Context, db database.Database, settingKey, envKey string) string {
	v, _ := db.GetSetting(ctx, settingKey)
	v = strings.TrimSpace(v)
	if v == "" && envKey != "" {
		v = strings.TrimSpace(os.Getenv(envKey))
	}
	return v
}
