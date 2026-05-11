package library

import (
	"context"
	"time"

	"github.com/mstrhakr/audplexus/internal/audio"
	"github.com/mstrhakr/audplexus/internal/database"
)

// destinationLister is the subset of database.Database that
// resolveTagProfileForDownload needs. Defined locally so the helper
// can be unit-tested with a fake without depending on the whole DB
// interface.
type destinationLister interface {
	ListEnabledLibraryDestinations(ctx context.Context) ([]database.LibraryDestination, error)
}

// resolveTagProfileForDownload returns the tag profile to use for a
// fresh download. If any enabled library_destination is an
// Audiobookshelf destination, this returns TagProfileAudiobookRich
// regardless of the user-set default — ABS reads the `series`,
// `series-part`, and `asin` freeform atoms via ffprobe to auto-group
// books into series, so omitting them produces an objectively worse
// outcome for ABS users.
//
// The user-set default (resolved) is honored when no ABS destination
// is enabled, preserving the historical "Basic preserves the Plex
// album-collapse workaround" behavior for Plex-only setups.
//
// DB errors are logged at debug and the resolved default is returned —
// failing the download because we couldn't enumerate destinations
// would be worse than picking the wrong profile.
func resolveTagProfileForDownload(ctx context.Context, db destinationLister, resolved audio.TagProfile) audio.TagProfile {
	if resolved == audio.TagProfileAudiobookRich {
		// Already rich; nothing to override.
		return resolved
	}
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := db.ListEnabledLibraryDestinations(listCtx)
	if err != nil {
		dlLog.Debug().Err(err).Msg("tag-profile override: could not list enabled destinations; using resolved default")
		return resolved
	}
	for _, r := range rows {
		if r.Type == database.LibraryDestinationTypeABS {
			dlLog.Debug().Str("destination_id", r.ID).Msg("tag-profile override: ABS destination present, forcing audiobook-rich")
			return audio.TagProfileAudiobookRich
		}
	}
	return resolved
}
