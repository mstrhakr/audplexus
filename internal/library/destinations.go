package library

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
	"golang.org/x/sync/semaphore"
)

// DestinationManager constructs Backend instances per enabled
// library_destinations row and fans out per-book operations across them.
//
// Codex review (PR-0/B/C): the pipeline marked downloads complete BEFORE
// fire-and-forget media-server work even started. This manager runs every
// destination's OnBookOrganized synchronously, records per-destination
// state in book_library_destinations, and only returns once the fan-out
// is done — so the pipeline's "complete" signal actually means delivered.
type DestinationManager struct {
	db             database.Database
	audnexus       *audnexus.Client
	libraryDir     string
	maxConcurrency int

	// Cache of constructed Backends keyed by destination ID. Currently
	// rebuilt every fan-out call to keep the code small; can switch to
	// invalidate-on-update if construction becomes expensive.
	mu sync.Mutex
}

// NewDestinationManager builds a manager. maxConcurrency caps the number
// of destinations a single fan-out call will hit in parallel (defaults to
// 3, matching the design doc's bounded-concurrency budget).
func NewDestinationManager(db database.Database, audnexusClient *audnexus.Client, libraryDir string, maxConcurrency int) *DestinationManager {
	if maxConcurrency <= 0 {
		maxConcurrency = 3
	}
	return &DestinationManager{
		db:             db,
		audnexus:       audnexusClient,
		libraryDir:     libraryDir,
		maxConcurrency: maxConcurrency,
	}
}

// ListEnabled returns the set of (destination, backend) pairs the manager
// will fan out to. Errors are logged per-destination and the failing
// destination is excluded.
func (m *DestinationManager) ListEnabled(ctx context.Context) []DestinationBackend {
	rows, err := m.db.ListEnabledLibraryDestinations(ctx)
	if err != nil {
		dlLog.Warn().Err(err).Msg("destinations: list enabled failed")
		return nil
	}
	out := make([]DestinationBackend, 0, len(rows))
	for _, r := range rows {
		row := r
		b, err := m.buildBackend(&row)
		if err != nil {
			dlLog.Warn().Err(err).Str("destination_id", row.ID).Str("type", string(row.Type)).Msg("destinations: build backend failed; skipping")
			continue
		}
		out = append(out, DestinationBackend{Row: row, Backend: b})
	}
	return out
}

// DestinationBackend pairs a destination row with its constructed Backend.
type DestinationBackend struct {
	Row     database.LibraryDestination
	Backend mediaserver.Backend
}

// FanOut runs OnBookOrganized against every enabled destination concurrently,
// bounded by maxConcurrency, and records per-destination outcomes via
// UpsertBookDestination. Returns the aggregated outcomes for logging /
// observability. Failures of one destination do not prevent others from
// proceeding.
func (m *DestinationManager) FanOut(ctx context.Context, book mediaserver.OrganizedBook) []DestinationFanOutResult {
	dests := m.ListEnabled(ctx)
	if len(dests) == 0 {
		return nil
	}

	sem := semaphore.NewWeighted(int64(m.maxConcurrency))
	results := make(chan DestinationFanOutResult, len(dests))
	var wg sync.WaitGroup

	for _, db := range dests {
		db := db
		if err := sem.Acquire(ctx, 1); err != nil {
			results <- DestinationFanOutResult{
				Destination: db.Row,
				Outcomes:    []mediaserver.Outcome{{Operation: "fanout", Status: mediaserver.OutcomeDeferred, Detail: "context cancelled before destination scheduled", Err: err}},
			}
			continue
		}
		wg.Add(1)
		go func(d DestinationBackend) {
			defer wg.Done()
			defer sem.Release(1)

			perDestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			outcomes := d.Backend.OnBookOrganized(perDestCtx, book)
			m.recordOutcomes(ctx, book.BookID, &d.Row, outcomes)
			results <- DestinationFanOutResult{Destination: d.Row, Outcomes: outcomes}
		}(db)
	}

	go func() { wg.Wait(); close(results) }()

	var all []DestinationFanOutResult
	for r := range results {
		all = append(all, r)
	}
	return all
}

// DestinationFanOutResult bundles outcomes per destination for the caller.
type DestinationFanOutResult struct {
	Destination database.LibraryDestination
	Outcomes    []mediaserver.Outcome
}

// recordOutcomes upserts the book_library_destinations row for this
// (book, destination) with summary state derived from the outcomes.
func (m *DestinationManager) recordOutcomes(ctx context.Context, bookID int64, dest *database.LibraryDestination, outcomes []mediaserver.Outcome) {
	if bookID == 0 || dest == nil {
		return
	}

	now := time.Now().UTC()
	state := summarizeOutcomesState(outcomes)
	serverItemID := serverItemIDFromOutcomes(outcomes)
	lastErrorMsg := lastFailureMessage(outcomes)
	perOpJSON := encodePerOpOutcomes(outcomes, now)

	bd, err := m.db.GetBookDestination(ctx, bookID, dest.ID)
	if err != nil {
		dlLog.Warn().Err(err).Int64("book_id", bookID).Str("destination_id", dest.ID).Msg("destinations: get existing book_destination failed; will upsert fresh")
		bd = nil
	}

	if bd == nil {
		bd = &database.BookDestination{BookID: bookID, DestinationID: dest.ID}
	}

	bd.SyncState = state
	bd.LastAttemptedAt = &now
	bd.AttemptCount++
	bd.PerOpOutcomes = perOpJSON

	if state == database.BookDestSyncSynced {
		bd.LastSucceededAt = &now
		bd.LastError = ""
	} else if lastErrorMsg != "" {
		bd.LastError = lastErrorMsg
	}
	if serverItemID != "" {
		bd.ServerItemID = serverItemID
	}

	if err := m.db.UpsertBookDestination(ctx, bd); err != nil {
		dlLog.Warn().Err(err).Int64("book_id", bookID).Str("destination_id", dest.ID).Msg("destinations: upsert book_destination failed")
	}
}

func summarizeOutcomesState(outcomes []mediaserver.Outcome) database.BookDestinationSyncState {
	// any Failed                  -> failed
	// any SkippedNotConfigured    -> failed (destination not ready)
	// no outcomes at all          -> pending
	// otherwise                   -> synced (succeeded / skipped_existing /
	//                                unsupported / deferred — all terminal)
	if len(outcomes) == 0 {
		return database.BookDestSyncPending
	}
	for _, o := range outcomes {
		if o.Status == mediaserver.OutcomeFailed {
			return database.BookDestSyncFailed
		}
		if o.Status == mediaserver.OutcomeSkippedNotConfigured {
			return database.BookDestSyncFailed
		}
	}
	return database.BookDestSyncSynced
}

func serverItemIDFromOutcomes(outcomes []mediaserver.Outcome) string {
	// item_match is the canonical source. Fall back to scan_trigger's
	// returned id (Emby's targeted folder refresh sets it).
	for _, o := range outcomes {
		if o.Operation == mediaserver.OpItemMatch && o.ServerItemID != "" {
			return o.ServerItemID
		}
	}
	for _, o := range outcomes {
		if o.ServerItemID != "" {
			return o.ServerItemID
		}
	}
	return ""
}

func lastFailureMessage(outcomes []mediaserver.Outcome) string {
	for i := len(outcomes) - 1; i >= 0; i-- {
		o := outcomes[i]
		if o.Status == mediaserver.OutcomeFailed {
			if o.Err != nil {
				return o.Err.Error()
			}
			return o.Detail
		}
	}
	return ""
}

type perOpRecord struct {
	Status     string `json:"status"`
	At         string `json:"at"`
	Detail     string `json:"detail,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

func encodePerOpOutcomes(outcomes []mediaserver.Outcome, at time.Time) string {
	if len(outcomes) == 0 {
		return ""
	}
	m := make(map[string]perOpRecord, len(outcomes))
	atStr := at.Format(time.RFC3339Nano)
	for _, o := range outcomes {
		m[o.Operation] = perOpRecord{
			Status:     string(o.Status),
			At:         atStr,
			Detail:     o.Detail,
			DurationMs: o.DurationMs,
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// buildBackend constructs a mediaserver.Backend instance for a single
// destination row. Each backend is bound to its row via WithDestination,
// so the row's URL/api_key/library_id columns drive the runtime config —
// settings-table fallback only kicks in for the legacy single-backend
// code path that doesn't pass through here.
func (m *DestinationManager) buildBackend(row *database.LibraryDestination) (mediaserver.Backend, error) {
	switch row.Type {
	case database.LibraryDestinationTypePlex:
		return mediaserver.NewPlex(m.db, m.libraryDir).WithDestination(row), nil
	case database.LibraryDestinationTypeEmby:
		return mediaserver.NewEmby(m.db, m.audnexus, m.libraryDir).WithDestination(row), nil
	case database.LibraryDestinationTypeJellyfin:
		return mediaserver.NewJellyfin(m.db, m.audnexus, m.libraryDir).WithDestination(row), nil
	case database.LibraryDestinationTypeABS:
		return mediaserver.NewABS(m.db, m.libraryDir).WithDestination(row), nil
	default:
		return nil, fmt.Errorf("unsupported destination type: %q", row.Type)
	}
}
