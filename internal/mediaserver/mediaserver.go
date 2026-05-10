// Package mediaserver abstracts over a media server (Plex, Emby, ...) that
// indexes the audiobook library, exposes search/collections, and accepts
// scan triggers.
//
// Backends are selected at startup based on the `media_server_type` setting
// (or MEDIA_SERVER env var) and wired into the download pipeline. Each
// backend persists its own settings under backend-specific DB keys, so a user
// can switch backends without losing the other's configuration.
package mediaserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/logging"
)

// Type identifies a media-server backend kind.
type Type string

const (
	TypePlex     Type = "plex"
	TypeEmby     Type = "emby"
	TypeJellyfin Type = "jellyfin"
	TypeABS      Type = "abs"
)

// SettingKeyType is the DB setting key that stores the active backend type.
const SettingKeyType = "media_server_type"

// Backend is the abstraction the download pipeline calls into. The contract
// is synchronous and typed: every operation reports back via Outcome rather
// than swallowing errors in a goroutine. Idempotent — repeat calls for the
// same book are safe and return SkippedExisting on subsequent invocations.
type Backend interface {
	// Name returns a stable identifier ("plex", "emby") used in logs and UI.
	Name() string

	// Configured reports whether the backend has all required settings (URL,
	// auth, library selection). Callers can short-circuit when false; backends
	// also self-check and return SkippedNotConfigured outcomes when called
	// while not configured.
	Configured(ctx context.Context) bool

	// Capabilities returns the operations this backend supports. Used by the
	// UI to hide affordances that don't apply per-destination (e.g. hiding
	// the "franchise tagging" toggle on Plex). Advisory — runtime contract
	// remains the typed Outcome.
	Capabilities() CapabilitySet

	// OnBookOrganized runs the per-book post-organize work synchronously.
	// Each logical step (scan trigger, item match, series grouping, tagging,
	// image upload, etc.) returns one Outcome. The slice is non-nil but may
	// be empty if the backend is not configured.
	//
	// MUST honor the caller's context (timeouts, cancellation). MUST be
	// idempotent — repeat invocations for the same OrganizedBook are safe
	// and report SkippedExisting for already-applied operations.
	OnBookOrganized(ctx context.Context, book OrganizedBook) []Outcome

	// ReconcileLibrary walks the server's library, records each matched book's
	// server-side ID on the local row, and ensures all series collections are
	// populated. Synchronous, returns errors. Reports progress via progressFn.
	ReconcileLibrary(ctx context.Context, progressFn func(current, total int)) error

	// LibraryItemCount returns how many items the server has indexed in the
	// configured library. Used for diagnostics. Returns (0, nil) when not
	// configured.
	LibraryItemCount(ctx context.Context) (int, error)

	// TriggerLibraryScan kicks off a server-side library refresh and returns
	// the post-scan item count. Used by the periodic sync flow.
	TriggerLibraryScan(ctx context.Context) (int, error)
}

// Resolve picks the active backend from the DB setting (falling back to the
// MEDIA_SERVER env var, then to Plex for backwards compatibility).
func Resolve(ctx context.Context, db database.Database) Type {
	v, _ := db.GetSetting(ctx, SettingKeyType)
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		v = strings.ToLower(strings.TrimSpace(os.Getenv("MEDIA_SERVER")))
	}
	switch Type(v) {
	case TypeEmby:
		return TypeEmby
	default:
		return TypePlex
	}
}

// New constructs a Backend of the requested type. libraryDir is the local
// path Audplexus writes to (used to translate paths into the server's view).
// audnexusClient is used by backends that enrich server-side metadata (Emby
// uploads author images sourced from Audnexus); pass nil to disable that
// enrichment without breaking the rest of the backend's behavior.
func New(t Type, db database.Database, audnexusClient *audnexus.Client, libraryDir string) (Backend, error) {
	switch t {
	case TypePlex:
		return NewPlex(db, libraryDir), nil
	case TypeEmby:
		return NewEmby(db, audnexusClient, libraryDir), nil
	case TypeJellyfin:
		return NewJellyfin(db, audnexusClient, libraryDir), nil
	case TypeABS:
		return NewABS(db, libraryDir), nil
	default:
		return nil, fmt.Errorf("unknown media server type: %q", t)
	}
}

// log is the package-wide logger; per-backend files reuse it via msLog.
var msLog = logging.Component("mediaserver")
