package mediaserver

import (
	"errors"
	"time"
)

// OutcomeStatus is the typed result of a single backend operation. It replaces
// the old "spawn-a-goroutine-and-log-the-error" pattern: every operation now
// reports back what actually happened, so callers can record per-operation
// state instead of guessing from logs.
type OutcomeStatus string

const (
	// OutcomeSucceeded means the operation completed and the destination is
	// now in the desired state.
	OutcomeSucceeded OutcomeStatus = "succeeded"

	// OutcomeSkippedExisting means the destination was already in the desired
	// state and no work was needed (e.g. a book is already in the right
	// collection, an image is already uploaded). This is a successful no-op,
	// distinct from Unsupported.
	OutcomeSkippedExisting OutcomeStatus = "skipped_existing"

	// OutcomeUnsupported means this backend cannot perform the operation by
	// design (e.g. Plex has no per-item tag concept). The caller should not
	// retry. UI capability flags can mirror this so the operation isn't
	// offered, but the runtime contract is the typed outcome — backends
	// never silently no-op.
	OutcomeUnsupported OutcomeStatus = "unsupported"

	// OutcomeFailed means the operation tried and errored. Err is populated.
	// Caller decides whether to retry; idempotent operations are safe to
	// retry blindly.
	OutcomeFailed OutcomeStatus = "failed"

	// OutcomeDeferred means the operation is in flight but completion is
	// asynchronous and not directly observable (e.g. ABS folder watcher will
	// pick up the file on its own schedule). The caller can record this and
	// poll for the actual end-state via Reconcile later.
	OutcomeDeferred OutcomeStatus = "deferred"

	// OutcomeSkippedNotConfigured means the destination isn't fully set up
	// (missing URL, token, library ID). The caller should treat this as a
	// soft skip — no error, no retry, surface the configuration warning in
	// Settings instead.
	OutcomeSkippedNotConfigured OutcomeStatus = "skipped_not_configured"
)

// Operation names — stable strings used as the key in the per-op outcome
// breakdown. New backends may introduce new operation names; the schema for
// per_op_outcomes is intentionally an open set.
const (
	OpScanTrigger     = "scan_trigger"      // ask the destination to index the new file
	OpItemMatch       = "item_match"        // resolve the destination's internal item ID
	OpSeriesGrouping  = "series_grouping"   // ensure the book is in its series collection
	OpFranchiseTag    = "franchise_tag"     // set a franchise-level tag (Emby/Jellyfin only)
	OpImageUpload     = "image_upload"      // upload a cover or author image
	OpAuthorImage     = "author_image"      // attach an author/artist image
	OpBoxSetCover     = "boxset_cover"      // attach a cover image to a series boxset
)

// Outcome is the result of one logical backend operation against one book.
// Backends return a slice of Outcomes from OnBookOrganized so the caller can
// record per-operation state.
type Outcome struct {
	// Operation names what was attempted (one of the Op* constants).
	Operation string

	// Status reports what happened.
	Status OutcomeStatus

	// Detail is a short human-readable explanation. Always populated for
	// non-Succeeded outcomes; optional for Succeeded.
	Detail string

	// Err is populated only when Status == OutcomeFailed. The caller may
	// inspect with errors.Is for typed sentinels (e.g. ErrUnsupported).
	Err error

	// ServerItemID is populated when an item-resolution operation succeeded
	// (typically OpItemMatch). The caller persists this in the join table
	// so future operations can address the item directly.
	ServerItemID string

	// DurationMs is how long the operation took. Used for SLO tracking and
	// to identify slow destinations.
	DurationMs int64
}

// IsTerminal reports whether the outcome represents a final state that
// shouldn't be automatically retried. Failed is non-terminal (caller may
// retry); everything else is terminal.
func (o Outcome) IsTerminal() bool {
	return o.Status != OutcomeFailed
}

// ErrUnsupported is the sentinel returned by typed Outcome.Err when a backend
// declines an operation by design. Callers can inspect with errors.Is.
var ErrUnsupported = errors.New("operation unsupported by backend")

// ErrNotConfigured is the sentinel returned when the backend's required
// settings (URL, auth token, library ID) are missing. Callers should treat
// this as a configuration warning, not a runtime failure.
var ErrNotConfigured = errors.New("backend not configured")

// OrganizedBook is the input contract for OnBookOrganized. It carries
// everything a backend needs to act on a freshly-organized book without
// having to re-read the database. The caller (the pipeline) is responsible
// for translating the local file path into the destination's view of the
// path before invoking the backend.
type OrganizedBook struct {
	// BookID is the local primary key.
	BookID int64

	// ASIN is the Audible identifier; backends that support ASIN matching
	// (ABS) use it to resolve canonical metadata.
	ASIN string

	// Title is the canonical book title (Audnexus-corrected when available,
	// falling back to Audible).
	Title string

	// Author is the canonical author name.
	Author string

	// Series is the series name, if any. Empty for standalone books.
	Series string

	// SeriesPosition is the book's position in series (e.g. "1", "2.5").
	// Empty for standalone books.
	SeriesPosition string

	// LocalPath is the absolute path to the m4b on the Audplexus host.
	LocalPath string

	// CoverPath is the absolute path to the cover image (used for
	// destination-side cover uploads when the destination needs a sidecar
	// rather than reading embedded artwork).
	CoverPath string

	// OrganizedAt is the time the file landed in its final location. Used
	// in deferred-outcome tracking ("the watcher should pick it up within
	// N seconds of this").
	OrganizedAt time.Time
}

// SkippedConfigured returns a SkippedNotConfigured Outcome for a single
// operation. Helper to keep backend code terse.
func SkippedConfigured(op string) Outcome {
	return Outcome{
		Operation: op,
		Status:    OutcomeSkippedNotConfigured,
		Detail:    "destination not fully configured",
		Err:       ErrNotConfigured,
	}
}

// Unsupported returns an Unsupported Outcome for a single operation. Helper
// for backends that don't implement an operation by design.
func Unsupported(op, detail string) Outcome {
	return Outcome{
		Operation: op,
		Status:    OutcomeUnsupported,
		Detail:    detail,
		Err:       ErrUnsupported,
	}
}

// Failed returns a Failed Outcome with the given error. Helper to keep
// backend code terse.
func Failed(op string, err error, detail string) Outcome {
	return Outcome{
		Operation: op,
		Status:    OutcomeFailed,
		Detail:    detail,
		Err:       err,
	}
}

// Succeeded returns a Succeeded Outcome. The optional serverItemID is set
// for item-resolution operations.
func Succeeded(op, detail, serverItemID string, dur time.Duration) Outcome {
	return Outcome{
		Operation:    op,
		Status:       OutcomeSucceeded,
		Detail:       detail,
		ServerItemID: serverItemID,
		DurationMs:   dur.Milliseconds(),
	}
}

// SkippedExisting returns a SkippedExisting Outcome (no-op because the
// destination was already in the desired state).
func SkippedExisting(op, detail string) Outcome {
	return Outcome{
		Operation: op,
		Status:    OutcomeSkippedExisting,
		Detail:    detail,
	}
}
