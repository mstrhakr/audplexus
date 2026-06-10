package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/mstrhakr/audplexus/internal/audio"
	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/errs"
	"github.com/mstrhakr/audplexus/internal/logging"
	audible "github.com/mstrhakr/go-audible"
)

// cleanText decodes HTML entities and trims whitespace. Audible's API
// occasionally returns titles, descriptions, and series names with literal
// `&amp;` / `&apos;` / `&uacute;` entities that should be decoded once at
// the source so the rest of the app sees clean Unicode.
func cleanText(s string) string {
	return strings.TrimSpace(html.UnescapeString(s))
}

var syncLog = logging.Component("sync")

// ErrSyncInProgress is returned when a sync is already running.
var ErrSyncInProgress = errors.New("sync already in progress")

// SyncMode controls which phases are executed.
type SyncMode string

const (
	SyncModeQuick SyncMode = "quick"
	SyncModeFull  SyncMode = "full"
)

// SyncPhase identifies a phase of the sync pipeline.
type SyncPhase string

const (
	PhaseAudibleSync    SyncPhase = "audible_sync"
	PhaseFileScan       SyncPhase = "file_scan"
	PhaseMetadataRepair SyncPhase = "metadata_repair"
	PhasePlexSync       SyncPhase = "plex_sync"
	PhaseCollectionSync SyncPhase = "collection_sync"
	PhaseDownloadQueue  SyncPhase = "download_queue"
)

// DefaultFullPhases returns the standard set of sync phases in idle state.
//
// Phase identifiers retain their historical names (PhasePlexSync) for URL/JS
// compatibility, but the user-visible labels are media-server-agnostic so the
// dashboard reads naturally regardless of which backend is active.
func DefaultFullPhases() []PhaseStatus {
	return []PhaseStatus{
		{Name: PhaseAudibleSync, Label: "Audible Library", Status: "idle"},
		{Name: PhaseFileScan, Label: "File System Scan", Status: "idle"},
		{Name: PhaseMetadataRepair, Label: "Metadata Repair", Status: "idle"},
		{Name: PhasePlexSync, Label: "Library Scan", Status: "idle"},
		{Name: PhaseCollectionSync, Label: "Collection Sync", Status: "idle"},
	}
}

func phaseLabel(phase SyncPhase) string {
	for _, p := range DefaultFullPhases() {
		if p.Name == phase {
			return p.Label
		}
	}
	return string(phase)
}

// SubPhaseStatus tracks progress for one destination within a parent phase.
type SubPhaseStatus struct {
	ID            string  `json:"id"`
	Label         string  `json:"label"`
	Status        string  `json:"status"` // "pending", "running", "complete", "failed"
	Message       string  `json:"message,omitempty"`
	Current       int     `json:"current,omitempty"`
	Total         int     `json:"total,omitempty"`
	Percent       float64 `json:"percent,omitempty"`
	Indeterminate bool    `json:"indeterminate,omitempty"`
}

// PhaseStatus tracks the state of a single sync phase.
type PhaseStatus struct {
	Name           SyncPhase        `json:"name"`
	Label          string           `json:"label"`
	Status         string           `json:"status"` // "pending", "running", "complete", "failed", "skipped"
	Message        string           `json:"message,omitempty"`
	Error          string           `json:"error,omitempty"`
	Current        int              `json:"current,omitempty"`
	Total          int              `json:"total,omitempty"`
	DisplayCurrent int              `json:"display_current,omitempty"`
	DisplayTotal   int              `json:"display_total,omitempty"`
	Percent        float64          `json:"percent,omitempty"`
	Indeterminate  bool             `json:"indeterminate,omitempty"`
	StartedAt      time.Time        `json:"started_at,omitempty"`
	EndedAt        time.Time        `json:"ended_at,omitempty"`
	SubPhases      []SubPhaseStatus `json:"sub_phases,omitempty"`
}

// SyncProgress tracks the current state of a library sync.
type SyncProgress struct {
	Running      bool          `json:"running"`
	Mode         SyncMode      `json:"mode"`
	Status       string        `json:"status"`
	Message      string        `json:"message,omitempty"`
	Error        string        `json:"error,omitempty"`
	BooksFound   int           `json:"books_found"`
	BooksScanned int           `json:"books_scanned"`
	BooksAdded   int           `json:"books_added"`
	FilesFound   int           `json:"files_found"`
	PlexItems    int           `json:"plex_items"`
	PlexScanned  bool          `json:"plex_scanned"`
	CurrentPhase SyncPhase     `json:"current_phase,omitempty"`
	Phases       []PhaseStatus `json:"phases,omitempty"`
	StartedAt    time.Time     `json:"started_at,omitempty"`
	CompletedAt  time.Time     `json:"completed_at,omitempty"`
}

func (p SyncProgress) Percent() float64 {
	if p.BooksFound <= 0 {
		if p.Running {
			return 0
		}
		if p.Status == "complete" {
			return 1
		}
		return 0
	}
	percent := float64(p.BooksScanned) / float64(p.BooksFound)
	if percent < 0 {
		return 0
	}
	if percent > 1 {
		return 1
	}
	return percent
}

// SubPhaseFn is called by destination fan-out callbacks to report per-destination state changes.
// id and label identify the destination; status is one of "running", "complete", "failed".
// current/total are optional progress counters (0 means indeterminate).
type SubPhaseFn func(id, label, status, message string, current, total int)

// PlexSyncFunc is a callback that the SyncService uses to perform a combined Plex scan + query.
// This avoids importing web-layer Plex code into the library package.
type PlexSyncFunc func(ctx context.Context, subFn SubPhaseFn) (plexItemCount int, err error)
type PlexReconcileFunc func(ctx context.Context, subFn SubPhaseFn, progressFn func(current, total int)) error

// SyncEvent is emitted via SSE whenever sync progress changes.
type SyncEvent struct {
	Running      bool          `json:"running"`
	Mode         SyncMode      `json:"mode"`
	Status       string        `json:"status"`
	Message      string        `json:"message,omitempty"`
	Error        string        `json:"error,omitempty"`
	BooksFound   int           `json:"books_found"`
	BooksScanned int           `json:"books_scanned"`
	BooksAdded   int           `json:"books_added"`
	FilesFound   int           `json:"files_found"`
	PlexItems    int           `json:"plex_items"`
	PlexScanned  bool          `json:"plex_scanned"`
	Percent      float64       `json:"percent"`
	CurrentPhase SyncPhase     `json:"current_phase,omitempty"`
	Phases       []PhaseStatus `json:"phases,omitempty"`
}

// SyncService handles syncing the Audible library to the local database.
type SyncService struct {
	db     database.Database
	client *audible.Client

	// accounts, when set, drives multi-account "sync all, merge" behaviour:
	// the sync iterates every enabled+authenticated account and merges their
	// libraries. When nil, the service falls back to the single client above
	// (tests / legacy embedded use).
	accounts *AccountManager

	libraryDir string

	// ffmpeg and audnexus are optional dependencies used by the
	// Metadata Repair phase. Set via SetFFmpeg / SetAudnexusClient
	// after construction so callers that don't run repair (tests,
	// embedded use) don't have to build them.
	ffmpeg   *audio.FFmpeg
	audnexus *audnexus.Client

	// Plex callback (set by web layer after construction)
	plexSyncFunc      PlexSyncFunc
	plexReconcileFunc PlexReconcileFunc

	// subPhaseFnFor returns a SubPhaseFn that writes into the named phase's SubPhases slice.
	// Created lazily — accessing via method keeps the closure clean.

	mu       sync.RWMutex
	progress SyncProgress

	// Track last sync for retry
	lastMode SyncMode

	// SSE subscriber support
	subMu       sync.Mutex
	subscribers map[int]chan SyncEvent
	nextSubID   int

	// Event throttling: batch rapid updates into a single event per ~50ms window.
	// This prevents the UI from jumping when multiple phases update concurrently.
	emitMu      sync.Mutex
	emitTimer   *time.Timer
	emitPending bool
}

// NewSyncService creates a new library sync service.
func NewSyncService(db database.Database, client *audible.Client, libraryDir string) *SyncService {
	return &SyncService{
		db:          db,
		client:      client,
		libraryDir:  libraryDir,
		subscribers: make(map[int]chan SyncEvent),
	}
}

// subPhaseFnFor returns a SubPhaseFn closure that upserts a named SubPhaseStatus into the named
// phase and emits a progress event. Safe to call concurrently from multiple goroutines.
//
// Sanitizes the incoming message via errs.CleanForDisplay before storing
// it on the SubPhaseStatus. Destination backends (Plex/Emby/…) wrap raw
// HTTP response bodies into their error strings — those bodies can
// contain HTML markup like <h1>404 Not Found</h1>, which when rendered
// by the SSE client into innerHTML blew out the row height. Cleaning
// at the chokepoint guarantees every sub-phase message shown in the
// UI is plain text and matches the wording used by the dashboard
// destination card for the same underlying failure.
func (s *SyncService) subPhaseFnFor(phase SyncPhase) SubPhaseFn {
	return func(id, label, status, message string, current, total int) {
		cleaned := errs.CleanForDisplay(message)
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range s.progress.Phases {
			if s.progress.Phases[i].Name == phase {
				sub := &s.progress.Phases[i].SubPhases
				for j := range *sub {
					if (*sub)[j].ID == id {
						(*sub)[j].Label = label
						(*sub)[j].Status = status
						(*sub)[j].Message = cleaned
						(*sub)[j].Current = current
						(*sub)[j].Total = total
						if total > 0 {
							(*sub)[j].Percent = float64(current) / float64(total)
						} else {
							(*sub)[j].Percent = 0
						}
						(*sub)[j].Indeterminate = total == 0 && status == "running"
						s.emitLocked()
						return
					}
				}
				// Not found — append new entry.
				*sub = append(*sub, SubPhaseStatus{
					ID:            id,
					Label:         label,
					Status:        status,
					Message:       cleaned,
					Current:       current,
					Total:         total,
					Indeterminate: total == 0 && status == "running",
				})
				s.emitLocked()
				return
			}
		}
	}
}

// SetFFmpeg wires the ffmpeg wrapper used by the Metadata Repair phase.
// Optional — when nil, the repair phase fails with a clear message.
func (s *SyncService) SetFFmpeg(ff *audio.FFmpeg) {
	s.ffmpeg = ff
}

// SetAccountManager wires the multi-account manager. When set, doAudibleSync
// merges every enabled+authenticated account's library. Safe to leave unset.
func (s *SyncService) SetAccountManager(m *AccountManager) { s.accounts = m }

// stampBookAccount records the owning account for a book after upsert. No-op
// for the legacy single-account path (empty account id).
func (s *SyncService) stampBookAccount(ctx context.Context, asin, accountID string) {
	if accountID == "" {
		return
	}
	if err := s.db.SetBookAccount(ctx, asin, accountID); err != nil {
		syncLog.Warn().Err(err).Str("asin", asin).Str("account_id", accountID).Msg("failed to stamp book account")
	}
}

// SetAudnexusClient wires the audnexus client used by Metadata Repair
// to enrich book metadata before re-tagging. Optional — when nil,
// repair falls back to DB-only fields.
func (s *SyncService) SetAudnexusClient(c *audnexus.Client) {
	s.audnexus = c
}

// SetPlexSyncCallback registers the combined Plex sync function.
func (s *SyncService) SetPlexSyncCallback(fn PlexSyncFunc) {
	s.plexSyncFunc = fn
}

// SetPlexReconcileCallback registers the Plex reconciliation function.
func (s *SyncService) SetPlexReconcileCallback(fn PlexReconcileFunc) {
	s.plexReconcileFunc = fn
}

// Subscribe returns a channel that receives sync progress events and an ID to unsubscribe.
func (s *SyncService) Subscribe() (int, <-chan SyncEvent) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	id := s.nextSubID
	s.nextSubID++
	ch := make(chan SyncEvent, 32)
	s.subscribers[id] = ch
	return id, ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (s *SyncService) Unsubscribe(id int) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if ch, ok := s.subscribers[id]; ok {
		close(ch)
		delete(s.subscribers, id)
	}
}

// emit sends the current progress snapshot to all subscribers.
// Must be called while s.mu is held (read or write).
func (s *SyncService) emitLocked() {
	s.throttledEmitLocked()
}

// throttledEmitLocked schedules an emit with debouncing. Multiple calls within a
// ~50ms window are coalesced into a single event. This prevents UI jitter when
// multiple concurrent phases (Audiobooks, Jellyfin, Nicflix) update rapidly.
func (s *SyncService) throttledEmitLocked() {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()

	// If a timer is already pending, nothing to do — the next event will have
	// the latest state.
	if s.emitPending {
		return
	}

	// Mark as pending and schedule the actual emit for 50ms from now.
	// This coalesces rapid concurrent updates into a single snapshot.
	s.emitPending = true
	s.emitTimer = time.AfterFunc(50*time.Millisecond, func() {
		s.mu.RLock()
		defer s.mu.RUnlock()
		s.doEmitLocked()
	})
}

// doEmitLocked does the actual broadcast without throttling, assuming s.mu is held.
func (s *SyncService) doEmitLocked() {
	s.emitMu.Lock()
	s.emitPending = false
	s.emitTimer = nil
	s.emitMu.Unlock()

	s.subMu.Lock()
	defer s.subMu.Unlock()

	if len(s.subscribers) == 0 {
		return
	}

	evt := SyncEvent{
		Running:      s.progress.Running,
		Mode:         s.progress.Mode,
		Status:       s.progress.Status,
		Message:      s.progress.Message,
		Error:        s.progress.Error,
		BooksFound:   s.progress.BooksFound,
		BooksScanned: s.progress.BooksScanned,
		BooksAdded:   s.progress.BooksAdded,
		FilesFound:   s.progress.FilesFound,
		PlexItems:    s.progress.PlexItems,
		PlexScanned:  s.progress.PlexScanned,
		Percent:      s.progress.Percent(),
		CurrentPhase: s.progress.CurrentPhase,
		Phases:       append([]PhaseStatus(nil), s.progress.Phases...),
	}
	for _, ch := range s.subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
}

// GetProgress returns the latest sync progress snapshot.
func (s *SyncService) GetProgress() SyncProgress {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.progress
}

// LastMode returns the mode of the last sync attempt (for retry).
func (s *SyncService) LastMode() SyncMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastMode
}

// QuickSync runs a lightweight sync: Audible API update + filesystem check for new books.
func (s *SyncService) QuickSync(ctx context.Context) (int, error) {
	return s.runSync(ctx, SyncModeQuick)
}

// FullSync runs a comprehensive sync: Audible + filesystem scan + Plex query + Plex scan.
func (s *SyncService) FullSync(ctx context.Context) (int, error) {
	return s.runSync(ctx, SyncModeFull)
}

// Sync is the legacy entry point — runs a full sync to maintain backward compatibility.
func (s *SyncService) Sync(ctx context.Context) (int, error) {
	return s.runSync(ctx, SyncModeFull)
}

// RunPhase runs a single sync phase independently with progress tracking.
func (s *SyncService) RunPhase(ctx context.Context, phase SyncPhase) error {
	s.mu.Lock()
	if s.progress.Running {
		s.mu.Unlock()
		return ErrSyncInProgress
	}

	now := time.Now()
	// Preserve existing phase states, or use defaults
	var phases []PhaseStatus
	if len(s.progress.Phases) > 0 {
		phases = append([]PhaseStatus(nil), s.progress.Phases...)
	} else {
		phases = DefaultFullPhases()
	}
	// Reset target phase to pending
	for i := range phases {
		if phases[i].Name == phase {
			phases[i] = PhaseStatus{Name: phase, Label: phases[i].Label, Status: "pending"}
		}
	}

	s.progress = SyncProgress{
		Running:      true,
		Mode:         SyncModeFull,
		Status:       "running",
		Message:      "Running " + phaseLabel(phase) + "...",
		StartedAt:    now,
		CurrentPhase: phase,
		Phases:       phases,
	}
	s.emitLocked()
	s.mu.Unlock()

	s.setPhase(phase, "running", "Running...")

	var phaseErr error
	switch phase {
	case PhaseAudibleSync:
		syncRecord := &database.SyncHistory{StartedAt: now, Status: "running"}
		_ = s.db.CreateSync(ctx, syncRecord)
		added, err := s.doAudibleSync(ctx, syncRecord)
		if err != nil {
			phaseErr = err
		} else {
			s.setPhase(phase, "complete", fmt.Sprintf("%d new books found", added))
		}
		finished := time.Now()
		syncRecord.CompletedAt = &finished
		syncRecord.BooksAdded = added
		if phaseErr != nil {
			syncRecord.Status = "failed"
			syncRecord.Error = phaseErr.Error()
		} else {
			syncRecord.Status = "complete"
		}
		_ = s.db.UpdateSync(ctx, syncRecord)

	case PhaseFileScan:
		lastEmit := 0
		reconciled, err := reconcileExistingAudiobookFilesWithProgress(ctx, s.db, s.libraryDir, func(processed, total int) {
			if processed != total && processed-lastEmit < 20 {
				return
			}
			lastEmit = processed
			s.updatePhaseProgress(PhaseFileScan, processed, total, false)
		})
		if err != nil {
			phaseErr = err
		} else {
			s.mu.Lock()
			s.progress.FilesFound = reconciled
			s.emitLocked()
			s.mu.Unlock()
			s.setPhase(phase, "complete", fmt.Sprintf("%d files reconciled", reconciled))
		}

	case PhaseMetadataRepair:
		repaired, err := RepairBookMetadata(ctx, s.db, s.ffmpeg, s.audnexus, func(processed, total int) {
			s.updatePhaseProgress(PhaseMetadataRepair, processed, total, false)
		})
		if err != nil {
			phaseErr = err
		} else {
			s.setPhase(phase, "complete", fmt.Sprintf("%d files re-tagged", repaired))
		}

	case PhasePlexSync:
		if s.plexSyncFunc == nil {
			phaseErr = fmt.Errorf("media server not configured")
		} else {
			items, err := s.plexSyncFunc(ctx, s.subPhaseFnFor(PhasePlexSync))
			if err != nil {
				phaseErr = err
			} else {
				s.mu.Lock()
				s.progress.PlexItems = items
				s.progress.PlexScanned = true
				s.emitLocked()
				s.mu.Unlock()
				s.setPhase(phase, "complete", fmt.Sprintf("%d items in Plex (scan+query)", items))
			}
		}

	case PhaseCollectionSync:
		if s.plexReconcileFunc == nil {
			phaseErr = fmt.Errorf("media server not configured")
		} else {
			completeStatus := database.BookStatusComplete
			_, completeCount, _ := s.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 1})
			err := s.plexReconcileFunc(ctx, s.subPhaseFnFor(PhaseCollectionSync), func(current, total int) {
				displayCurrent := scaleProgress(current, total, completeCount)
				s.updatePhaseProgressWithDisplay(PhaseCollectionSync, current, total, false, displayCurrent, completeCount)
			})
			if err != nil {
				phaseErr = err
			} else {
				s.setPhase(phase, "complete", "Collections verified")
			}
		}

	default:
		phaseErr = fmt.Errorf("unknown phase: %s", phase)
	}

	if phaseErr != nil {
		s.setPhase(phase, "failed", phaseErr.Error())
	}

	s.mu.Lock()
	s.progress.Running = false
	s.progress.CompletedAt = time.Now()
	if phaseErr != nil {
		s.progress.Status = "partial"
		s.progress.Message = phaseLabel(phase) + " failed"
		s.progress.Error = phaseErr.Error()
	} else {
		s.progress.Status = "complete"
		s.progress.Message = phaseLabel(phase) + " complete"
	}
	s.emitLocked()
	s.mu.Unlock()

	return phaseErr
}

func (s *SyncService) runSync(ctx context.Context, mode SyncMode) (int, error) {
	s.mu.Lock()
	if s.progress.Running {
		s.mu.Unlock()
		return 0, ErrSyncInProgress
	}

	now := time.Now()
	prevPhases := append([]PhaseStatus(nil), s.progress.Phases...)
	phases := s.buildPhases(mode, prevPhases)

	s.progress = SyncProgress{
		Running:   true,
		Mode:      mode,
		Status:    "running",
		Message:   "Starting " + string(mode) + " sync...",
		StartedAt: now,
		Phases:    phases,
	}
	s.lastMode = mode
	s.emitLocked()
	s.mu.Unlock()

	syncRecord := &database.SyncHistory{
		StartedAt: now,
		Status:    "running",
	}
	if err := s.db.CreateSync(ctx, syncRecord); err != nil {
		s.finishProgressWithError(err)
		return 0, err
	}

	// --- Phase 1: Audible Sync (both modes) ---
	s.setPhase(PhaseAudibleSync, "running", "Fetching Audible library...")
	added, err := s.doAudibleSync(ctx, syncRecord)
	if err != nil {
		s.setPhase(PhaseAudibleSync, "failed", err.Error())
		syncLog.Error().Err(err).Msg("audible sync phase failed")
		// Don't halt — continue with other phases
	} else {
		s.setPhase(PhaseAudibleSync, "complete", fmt.Sprintf("%d new books found", added))
	}

	// --- Phase 2: File Scan (full sync only) ---
	if mode == SyncModeFull {
		s.setPhase(PhaseFileScan, "running", "Scanning filesystem for existing books...")
		syncLog.Info().Msg("starting filesystem file scan")
		lastEmit := 0
		reconciled, fsErr := reconcileExistingAudiobookFilesWithProgress(ctx, s.db, s.libraryDir, func(processed, total int) {
			if processed != total && processed-lastEmit < 20 {
				return
			}
			lastEmit = processed
			s.updatePhaseProgress(PhaseFileScan, processed, total, false)
		})
		if fsErr != nil {
			s.setPhase(PhaseFileScan, "failed", fsErr.Error())
			syncLog.Error().Err(fsErr).Msg("file scan phase failed")
			failedAt := time.Now()
			syncRecord.CompletedAt = &failedAt
			syncRecord.Status = "failed"
			syncRecord.Error = fsErr.Error()
			_ = s.db.UpdateSync(ctx, syncRecord)
			s.finishProgressWithError(fsErr)
			return 0, fsErr
		} else {
			s.mu.Lock()
			s.progress.FilesFound = reconciled
			s.emitLocked()
			s.mu.Unlock()
			s.setPhase(PhaseFileScan, "complete", fmt.Sprintf("%d files reconciled", reconciled))
			syncLog.Info().Int("files_reconciled", reconciled).Msg("filesystem file scan complete")
		}
	} else {
		// Quick sync: only reconcile new books (search FS for them before queuing)
		if added > 0 {
			reconciled, fsErr := reconcileExistingAudiobookFilesWithProgress(ctx, s.db, s.libraryDir, nil)
			if fsErr != nil {
				syncLog.Error().Err(fsErr).Msg("quick reconcile failed")
				failedAt := time.Now()
				syncRecord.CompletedAt = &failedAt
				syncRecord.Status = "failed"
				syncRecord.Error = fsErr.Error()
				_ = s.db.UpdateSync(ctx, syncRecord)
				s.finishProgressWithError(fsErr)
				return 0, fsErr
			} else if reconciled > 0 {
				syncLog.Info().Int("books_reconciled", reconciled).Msg("quick sync: reconciled new books against disk")
			}
		}
	}

	// --- Phase 2b: Metadata Repair (full sync only) ---
	// Probes every complete book and re-embeds AudiobookRich tags when
	// the file's ASIN atom is missing or stale, so audiobook servers
	// (Audiobookshelf in particular) can match by ID. Non-fatal:
	// failures degrade to a "partial" sync but don't block the rest.
	if mode == SyncModeFull {
		if s.ffmpeg == nil {
			s.setPhase(PhaseMetadataRepair, "skipped", "ffmpeg unavailable")
		} else {
			s.setPhase(PhaseMetadataRepair, "running", "Checking metadata on existing files...")
			repaired, repairErr := RepairBookMetadata(ctx, s.db, s.ffmpeg, s.audnexus, func(processed, total int) {
				s.updatePhaseProgress(PhaseMetadataRepair, processed, total, false)
			})
			if repairErr != nil {
				s.setPhase(PhaseMetadataRepair, "failed", repairErr.Error())
				syncLog.Warn().Err(repairErr).Msg("metadata repair phase failed")
			} else {
				s.setPhase(PhaseMetadataRepair, "complete", fmt.Sprintf("%d files re-tagged", repaired))
				syncLog.Info().Int("repaired", repaired).Msg("metadata repair complete")
			}
		}
	}

	// --- Phase 3: Library Scan (full sync only). Phase identifier kept as
	// PhasePlexSync for URL/JS back-compat, but user-visible messages are
	// backend-agnostic — works for Plex, Emby, Jellyfin, ABS. ---
	plexItems := 0
	if mode == SyncModeFull && s.plexSyncFunc != nil {
		s.setPhase(PhasePlexSync, "running", "Syncing with library destination (scan + query)...")
		libraryScanStart := time.Now()
		items, plexErr := s.plexSyncFunc(ctx, s.subPhaseFnFor(PhasePlexSync))
		libraryScanMs := time.Since(libraryScanStart).Milliseconds()

		if plexErr != nil {
			s.setPhase(PhasePlexSync, "failed", plexErr.Error())
			syncLog.Warn().Err(plexErr).Int("library_scan_ms", int(libraryScanMs)).Msg("library scan phase failed")
		} else {
			plexItems = items
			s.mu.Lock()
			s.progress.PlexItems = plexItems
			s.progress.PlexScanned = true
			s.emitLocked()
			s.mu.Unlock()
			s.setPhase(PhasePlexSync, "complete", fmt.Sprintf("%d items in library (scan complete)", plexItems))
			syncLog.Info().Int("library_items", plexItems).Int("library_scan_ms", int(libraryScanMs)).Msg("library scan complete")
		}
	}

	// --- Phase 5: Collection Sync (full sync only) ---
	if mode == SyncModeFull && s.plexReconcileFunc != nil {
		s.setPhase(PhaseCollectionSync, "running", "Reconciling Plex collections...")
		completeStatus := database.BookStatusComplete
		_, completeCount, _ := s.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 1})
		collectionSyncStart := time.Now()
		reconcileErr := s.plexReconcileFunc(ctx, s.subPhaseFnFor(PhaseCollectionSync), func(current, total int) {
			displayCurrent := scaleProgress(current, total, completeCount)
			s.updatePhaseProgressWithDisplay(PhaseCollectionSync, current, total, false, displayCurrent, completeCount)
		})
		collectionSyncMs := time.Since(collectionSyncStart).Milliseconds()

		if reconcileErr != nil {
			s.setPhase(PhaseCollectionSync, "failed", reconcileErr.Error())
			syncLog.Warn().Err(reconcileErr).Int("collection_sync_ms", int(collectionSyncMs)).Msg("collection sync phase failed")
		} else {
			s.setPhase(PhaseCollectionSync, "complete", "Collections verified")
			syncLog.Info().Int("collection_sync_ms", int(collectionSyncMs)).Msg("plex collection sync complete")
		}
	}

	// --- Finalize ---
	finished := time.Now()
	syncRecord.CompletedAt = &finished
	syncRecord.BooksAdded = added
	syncRecord.Status = s.overallStatus()
	if syncRecord.Status == "failed" {
		syncRecord.Error = s.collectErrors()
	}
	_ = s.db.UpdateSync(ctx, syncRecord)

	s.mu.Lock()
	s.progress.Running = false
	s.progress.Status = syncRecord.Status
	s.progress.CompletedAt = finished
	if s.progress.BooksFound > 0 {
		s.progress.BooksScanned = s.progress.BooksFound
	}
	if syncRecord.Status == "complete" {
		s.progress.Message = fmt.Sprintf("%s sync complete", ucfirst(string(mode)))
	} else {
		s.progress.Message = fmt.Sprintf("%s sync finished with errors", ucfirst(string(mode)))
	}
	s.emitLocked()
	s.mu.Unlock()

	if err != nil {
		return 0, err
	}
	return added, nil
}

func (s *SyncService) buildPhases(mode SyncMode, prev []PhaseStatus) []PhaseStatus {
	defaultPhase := func(name SyncPhase, label string) PhaseStatus {
		return PhaseStatus{Name: name, Label: label, Status: "pending"}
	}

	findPrev := func(name SyncPhase) (PhaseStatus, bool) {
		for i := range prev {
			if prev[i].Name == name {
				return prev[i], true
			}
		}
		return PhaseStatus{}, false
	}

	if mode == SyncModeFull {
		return []PhaseStatus{
			defaultPhase(PhaseAudibleSync, "Audible Library"),
			defaultPhase(PhaseFileScan, "File System Scan"),
			defaultPhase(PhaseMetadataRepair, "Metadata Repair"),
			defaultPhase(PhasePlexSync, "Library Scan"),
			defaultPhase(PhaseCollectionSync, "Collection Sync"),
		}
	}

	phases := []PhaseStatus{defaultPhase(PhaseAudibleSync, "Audible Library")}
	for _, phase := range []struct {
		name  SyncPhase
		label string
	}{
		{name: PhaseFileScan, label: "File System Scan"},
		{name: PhaseMetadataRepair, label: "Metadata Repair"},
		{name: PhasePlexSync, label: "Library Scan"},
		{name: PhaseCollectionSync, label: "Collection Sync"},
	} {
		if prevPhase, ok := findPrev(phase.name); ok {
			phases = append(phases, prevPhase)
			continue
		}
		phases = append(phases, PhaseStatus{
			Name:    phase.name,
			Label:   phase.label,
			Status:  "skipped",
			Message: "Not run in quick sync",
			Current: 1,
			Total:   1,
			Percent: 1,
		})
	}

	return phases
}

func (s *SyncService) setPhase(phase SyncPhase, status, message string) {
	cleaned := errs.CleanForDisplay(message)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress.CurrentPhase = phase
	for i := range s.progress.Phases {
		if s.progress.Phases[i].Name == phase {
			now := time.Now()
			s.progress.Phases[i].Status = status
			s.progress.Phases[i].Message = cleaned
			if status == "running" {
				s.progress.Phases[i].StartedAt = now
				s.progress.Phases[i].EndedAt = time.Time{}
				s.progress.Phases[i].Error = ""
				s.progress.Phases[i].SubPhases = nil // reset sub-phases on each run
				if phase == PhasePlexSync {
					setPhaseProgress(&s.progress.Phases[i], 0, 0, true, status)
				}
			}
			if status == "complete" || status == "failed" || status == "skipped" {
				// Ensure phase is visible for at least 1 second from when it started running
				if !s.progress.Phases[i].StartedAt.IsZero() {
					elapsed := now.Sub(s.progress.Phases[i].StartedAt)
					minDuration := time.Second
					if elapsed < minDuration {
						// Schedule the actual transition after the minimum duration
						remainingSleep := minDuration - elapsed
						s.progress.Phases[i].EndedAt = now
						s.emitLocked()
						s.mu.Unlock()
						time.Sleep(remainingSleep)
						s.mu.Lock()
						now = time.Now()
					}
				}
				s.progress.Phases[i].EndedAt = now
			}
			if status == "failed" {
				s.progress.Phases[i].Error = cleaned
				s.progress.Phases[i].Indeterminate = false
				setPhaseProgress(&s.progress.Phases[i], s.progress.Phases[i].Current, s.progress.Phases[i].Total, false, status)
			}
			if status == "skipped" {
				setPhaseProgress(&s.progress.Phases[i], 1, 1, false, status)
			}
			if status == "complete" {
				if phase == PhasePlexSync {
					setPhaseProgress(&s.progress.Phases[i], 1, 1, false, status)
				} else {
					setPhaseProgress(&s.progress.Phases[i], s.progress.Phases[i].Total, s.progress.Phases[i].Total, false, status)
				}
			}
			break
		}
	}
	// Update the top-level message
	for i := range s.progress.Phases {
		if s.progress.Phases[i].Name == phase {
			s.progress.Message = s.progress.Phases[i].Label + ": " + cleaned
			break
		}
	}
	s.emitLocked()
}

func (s *SyncService) updatePhaseProgress(phase SyncPhase, current, total int, indeterminate bool) {
	s.updatePhaseProgressWithDisplay(phase, current, total, indeterminate, current, total)
}

func (s *SyncService) updatePhaseProgressWithDisplay(phase SyncPhase, current, total int, indeterminate bool, displayCurrent, displayTotal int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.progress.Phases {
		if s.progress.Phases[i].Name == phase {
			setPhaseProgress(&s.progress.Phases[i], current, total, indeterminate, s.progress.Phases[i].Status)
			setPhaseDisplayProgress(&s.progress.Phases[i], displayCurrent, displayTotal)
			break
		}
	}
	s.emitLocked()
}

func setPhaseDisplayProgress(phase *PhaseStatus, current, total int) {
	if current < 0 {
		current = 0
	}
	if total < 0 {
		total = 0
	}
	if total > 0 && current > total {
		current = total
	}

	phase.DisplayCurrent = current
	phase.DisplayTotal = total
}

func scaleProgress(current, total, targetTotal int) int {
	if targetTotal <= 0 {
		return 0
	}
	if total <= 0 {
		return 0
	}
	if current <= 0 {
		return 0
	}
	if current >= total {
		return targetTotal
	}

	scaled := int((float64(current) / float64(total)) * float64(targetTotal))
	if scaled < 0 {
		return 0
	}
	if scaled > targetTotal {
		return targetTotal
	}
	return scaled
}

func setPhaseProgress(phase *PhaseStatus, current, total int, indeterminate bool, status string) {
	if current < 0 {
		current = 0
	}
	if total < 0 {
		total = 0
	}
	if total > 0 && current > total {
		current = total
	}

	phase.Current = current
	phase.Total = total
	phase.Indeterminate = indeterminate

	if indeterminate {
		phase.Percent = 0
		if status == "complete" || status == "skipped" {
			phase.Indeterminate = false
			phase.Current = 1
			phase.Total = 1
			phase.Percent = 1
		}
		return
	}

	if phase.Total > 0 {
		phase.Percent = float64(phase.Current) / float64(phase.Total)
		if phase.Percent < 0 {
			phase.Percent = 0
		}
		if phase.Percent > 1 {
			phase.Percent = 1
		}
		return
	}

	if status == "complete" || status == "skipped" {
		phase.Current = 1
		phase.Total = 1
		phase.Percent = 1
		return
	}

	phase.Percent = 0
}

func (s *SyncService) overallStatus() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	hasFailed := false
	mode := s.progress.Mode
	for _, p := range s.progress.Phases {
		if !phaseRunsInMode(mode, p.Name) {
			continue
		}
		if p.Status == "failed" {
			hasFailed = true
		}
	}
	if hasFailed {
		return "partial"
	}
	return "complete"
}

func (s *SyncService) collectErrors() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var errs []string
	mode := s.progress.Mode
	for _, p := range s.progress.Phases {
		if !phaseRunsInMode(mode, p.Name) {
			continue
		}
		if p.Status == "failed" && p.Error != "" {
			errs = append(errs, p.Label+": "+p.Error)
		}
	}
	return strings.Join(errs, "; ")
}

func phaseRunsInMode(mode SyncMode, phase SyncPhase) bool {
	if mode == SyncModeFull {
		return true
	}
	return phase == PhaseAudibleSync
}

// accountItem pairs a fetched library item with the account (client + id) it
// came from, so the merge-all sync can route entitlement checks to the right
// client and stamp each book with its owning account.
type accountItem struct {
	client    *audible.Client
	accountID string
	item      audible.Book
}

// syncTargets returns the (client, accountID) pairs to sync. With an
// AccountManager wired it returns every enabled, authenticated account; without
// one (tests / legacy embedded use) it falls back to the single s.client with
// an empty account id.
func (s *SyncService) syncTargets() []EnabledAccount {
	if s.accounts != nil {
		var out []EnabledAccount
		for _, a := range s.accounts.EnabledAccounts() {
			if a.Client != nil && a.Client.IsAuthenticated() {
				out = append(out, a)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if s.client != nil {
		return []EnabledAccount{{ID: "", Client: s.client}}
	}
	return nil
}

func (s *SyncService) doAudibleSync(ctx context.Context, syncRecord *database.SyncHistory) (int, error) {
	startTime := time.Now()
	syncLog.Info().Msg("starting audible library sync")

	// Audible's /library endpoint only returns purchase_date when the
	// `relationships` response group is requested — it lives on the
	// user↔asset relationship payload, not the product itself. The SDK's
	// DefaultResponseGroups omits it, so without this override the
	// Library page renders Purchased as blank for every row.
	fetchStart := time.Now()
	libraryGroups := append([]string{}, audible.DefaultResponseGroups...)
	libraryGroups = append(libraryGroups, "relationships")
	// Fetch every account's library and merge into one work list. A book owned
	// by multiple accounts is attributed to the FIRST account that reports it
	// (stable creation order); whichever owns it can download it.
	targets := s.syncTargets()
	if len(targets) == 0 {
		return 0, fmt.Errorf("no authenticated audible account to sync")
	}
	var entries []accountItem
	seenASIN := make(map[string]struct{})
	for _, t := range targets {
		accBooks, err := t.Client.GetAllLibrary(ctx, audible.WithResponseGroups(libraryGroups...))
		if err != nil {
			syncLog.Error().Err(err).Str("account_id", t.ID).Msg("failed to fetch audible library for account")
			// One account failing shouldn't abort the whole merge — skip it and
			// keep syncing the others. If ALL fail, entries stays empty and we
			// return an error below.
			continue
		}
		syncLog.Info().Str("account_id", t.ID).Int("books", len(accBooks)).Msg("fetched account library")
		for _, item := range accBooks {
			if _, dup := seenASIN[item.ASIN]; dup {
				continue
			}
			seenASIN[item.ASIN] = struct{}{}
			entries = append(entries, accountItem{client: t.Client, accountID: t.ID, item: item})
		}
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("audible library fetch failed for all accounts")
	}
	fetchMs := time.Since(fetchStart).Milliseconds()

	total := len(entries)
	syncRecord.BooksFound = total
	s.mu.Lock()
	s.progress.BooksFound = total
	for i := range s.progress.Phases {
		if s.progress.Phases[i].Name == PhaseAudibleSync {
			setPhaseProgress(&s.progress.Phases[i], 0, total, false, s.progress.Phases[i].Status)
			break
		}
	}
	s.emitLocked()
	s.mu.Unlock()
	_ = s.db.UpdateSync(ctx, syncRecord)
	syncLog.Info().Int("total_books", total).Int("fetch_ms", int(fetchMs)).Int("accounts", len(targets)).Msg("fetched merged audible library")

	added := 0
	scanned := 0
	keepASIN := make(map[string]struct{})
	entitlementCheckTimeMs := int64(0)
	dbWriteTimeMs := int64(0)
	lastProgressEmit := 0

	for _, entry := range entries {
		client := entry.client
		item := entry.item
		select {
		case <-ctx.Done():
			syncLog.Warn().Int("scanned", scanned).Int("added", added).Msg("audible sync cancelled")
			return added, ctx.Err()
		default:
		}
		book := convertBook(item)
		syncLog.Trace().Str("asin", book.ASIN).Str("title", book.Title).Msg("processing book")

		// Log progress every 500 books
		if scanned > 0 && scanned%500 == 0 {
			elapsed := time.Since(startTime)
			avgMs := elapsed.Milliseconds() / int64(scanned)
			eta := time.Duration((int64(total)-int64(scanned))*avgMs) * time.Millisecond
			syncLog.Info().
				Int("scanned", scanned).
				Int("added", added).
				Int("total", total).
				Int("elapsed_sec", int(elapsed.Seconds())).
				Int("eta_sec", int(eta.Seconds())).
				Msg("audible sync batch progress")
		}

		// Skip items not eligible for local download (e.g. ebook-only or non-owned).
		if !item.Downloadable() {
			syncLog.Trace().Str("asin", book.ASIN).Str("content_type", item.ContentType).Msg("skipping non-downloadable item")
			scanned++
			s.mu.Lock()
			s.progress.BooksScanned = scanned
			s.progress.BooksAdded = added
			for i := range s.progress.Phases {
				if s.progress.Phases[i].Name == PhaseAudibleSync {
					setPhaseProgress(&s.progress.Phases[i], scanned, total, false, s.progress.Phases[i].Status)
					break
				}
			}
			s.emitLocked()
			s.mu.Unlock()
			continue
		}

		// Check DB first — known books skip the entitlement network call entirely.
		dbCheckStart := time.Now()
		existing, err := s.db.GetBookByASIN(ctx, book.ASIN)
		dbCheckMs := time.Since(dbCheckStart).Milliseconds()
		dbWriteTimeMs += dbCheckMs

		if err != nil {
			syncLog.Error().Err(err).Str("asin", book.ASIN).Int("db_ms", int(dbCheckMs)).Msg("failed to check existing book")
			scanned++
			s.mu.Lock()
			s.progress.BooksScanned = scanned
			s.progress.BooksAdded = added
			for i := range s.progress.Phases {
				if s.progress.Phases[i].Name == PhaseAudibleSync {
					setPhaseProgress(&s.progress.Phases[i], scanned, total, false, s.progress.Phases[i].Status)
					break
				}
			}
			s.emitLocked()
			s.mu.Unlock()
			continue
		}

		// For brand-new books only: verify entitlement before adding to the DB.
		// If denied, still record the book — just flag it as unavailable so the
		// UI can show it under the "Unavailable" tab with the denial reason
		// (e.g. once-Plus-catalog titles the user no longer has access to).
		if existing == nil {
			entitleStart := time.Now()
			canDownload, cdErr := client.CanDownload(ctx, item)
			entitleMs := time.Since(entitleStart).Milliseconds()
			entitlementCheckTimeMs += entitleMs
			if cdErr != nil || !canDownload {
				reason := "Audible entitlement denied"
				if cdErr != nil {
					reason = errs.CleanEntitlementReason(cdErr.Error())
				}
				syncLog.Info().Str("asin", book.ASIN).Str("title", book.Title).Str("reason", reason).Int("entitle_ms", int(entitleMs)).Msg("marking new item as unavailable: entitlement denied")
				book.Status = database.BookStatusUnavailable
				book.UnavailableReason = reason
				if err := s.db.UpsertBook(ctx, &book); err != nil {
					syncLog.Error().Err(err).Str("asin", book.ASIN).Msg("failed to upsert unavailable book")
				} else {
					s.stampBookAccount(ctx, book.ASIN, entry.accountID)
					keepASIN[book.ASIN] = struct{}{}
					added++
				}
				scanned++
				s.mu.Lock()
				s.progress.BooksScanned = scanned
				s.progress.BooksAdded = added
				for i := range s.progress.Phases {
					if s.progress.Phases[i].Name == PhaseAudibleSync {
						setPhaseProgress(&s.progress.Phases[i], scanned, total, false, s.progress.Phases[i].Status)
						break
					}
				}
				s.emitLocked()
				s.mu.Unlock()
				if scanned%20 == 0 {
					syncRecord.BooksAdded = added
					_ = s.db.UpdateSync(ctx, syncRecord)
				}
				continue
			}
			syncLog.Debug().Str("asin", book.ASIN).Int("entitle_ms", int(entitleMs)).Msg("audible sync: entitlement check passed")
		} else if existing.Status == database.BookStatusUnavailable {
			// User may have regained access (Plus added title back). Re-check.
			entitleStart := time.Now()
			canDownload, cdErr := client.CanDownload(ctx, item)
			entitleMs := time.Since(entitleStart).Milliseconds()
			entitlementCheckTimeMs += entitleMs
			if cdErr == nil && canDownload {
				syncLog.Info().Str("asin", book.ASIN).Int("entitle_ms", int(entitleMs)).Msg("previously-unavailable book is now accessible")
				book.Status = database.BookStatusNew
				book.UnavailableReason = ""
			} else {
				book.Status = database.BookStatusUnavailable
				book.UnavailableReason = existing.UnavailableReason
				if cdErr != nil {
					book.UnavailableReason = errs.CleanEntitlementReason(cdErr.Error())
				}
			}
		}

		keepASIN[book.ASIN] = struct{}{}

		// Preserve status/file info for existing books — unless the
		// unavailable-recheck above already decided a new status for this row.
		if existing != nil {
			if book.Status == "" {
				book.Status = existing.Status
				book.UnavailableReason = existing.UnavailableReason
			}
			book.FilePath = existing.FilePath
			book.FileSize = existing.FileSize
			syncLog.Debug().Str("asin", book.ASIN).Str("status", string(book.Status)).Msg("book already exists, preserving state")
		} else {
			book.Status = database.BookStatusNew
			added++
			syncLog.Info().Str("asin", book.ASIN).Str("title", book.Title).Msg("new book discovered")
		}

		upsertStart := time.Now()
		if err := s.db.UpsertBook(ctx, &book); err != nil {
			upsertMs := time.Since(upsertStart).Milliseconds()
			dbWriteTimeMs += upsertMs
			syncLog.Error().Err(err).Str("asin", book.ASIN).Int("upsert_ms", int(upsertMs)).Msg("failed to upsert book")
			scanned++
			s.mu.Lock()
			s.progress.BooksScanned = scanned
			s.progress.BooksAdded = added
			for i := range s.progress.Phases {
				if s.progress.Phases[i].Name == PhaseAudibleSync {
					setPhaseProgress(&s.progress.Phases[i], scanned, total, false, s.progress.Phases[i].Status)
					break
				}
			}
			s.emitLocked()
			s.mu.Unlock()
			if scanned%20 == 0 {
				syncRecord.BooksAdded = added
				_ = s.db.UpdateSync(ctx, syncRecord)
			}
			continue
		}
		upsertMs := time.Since(upsertStart).Milliseconds()
		dbWriteTimeMs += upsertMs

		s.stampBookAccount(ctx, book.ASIN, entry.accountID)

		scanned++
		if scanned-lastProgressEmit >= 20 {
			s.mu.Lock()
			s.progress.BooksScanned = scanned
			s.progress.BooksAdded = added
			for i := range s.progress.Phases {
				if s.progress.Phases[i].Name == PhaseAudibleSync {
					setPhaseProgress(&s.progress.Phases[i], scanned, total, false, s.progress.Phases[i].Status)
					break
				}
			}
			s.emitLocked()
			s.mu.Unlock()
			lastProgressEmit = scanned
		}
		if scanned%20 == 0 {
			syncRecord.BooksAdded = added
			_ = s.db.UpdateSync(ctx, syncRecord)
		}
	}

	// Remove stale books that are no longer in Audible library or no longer downloadable.
	removed := 0
	allBooks, _, err := s.db.ListBooks(ctx, database.BookFilter{Limit: 100000})
	if err == nil {
		for _, dbBook := range allBooks {
			if _, keep := keepASIN[dbBook.ASIN]; !keep {
				if err := s.db.DeleteBook(ctx, dbBook.ID); err != nil {
					syncLog.Warn().Err(err).Str("asin", dbBook.ASIN).Msg("failed deleting stale book")
					continue
				}
				removed++
			}
		}
		syncLog.Info().Int("removed_books", removed).Msg("audible library sync pruned stale entries")
	} else {
		syncLog.Warn().Err(err).Msg("audible library sync failed to list books for stale pruning")
	}

	// Adjust progress/book counts to reflect kept items only.
	eligibleCount := len(keepASIN)
	if total > 0 {
		s.mu.Lock()
		s.progress.BooksFound = eligibleCount
		s.progress.BooksScanned = scanned
		s.progress.BooksAdded = added
		s.mu.Unlock()
	}

	if syncRecord != nil {
		syncRecord.BooksFound = eligibleCount
		_ = s.db.UpdateSync(ctx, syncRecord)
	}

	totalElapsed := time.Since(startTime)
	avgDbMs := int64(0)
	if scanned > 0 {
		avgDbMs = dbWriteTimeMs / int64(scanned)
	}
	avgEntitleMs := int64(0)
	if added > 0 {
		// Only new books trigger entitlement checks
		avgEntitleMs = entitlementCheckTimeMs / int64(added)
	}

	syncLog.Info().
		Int("added", added).
		Int("eligible", eligibleCount).
		Int("removed", removed).
		Int("fetch_ms", int(fetchMs)).
		Int("db_total_ms", int(dbWriteTimeMs)).
		Int("avg_db_ms", int(avgDbMs)).
		Int("entitle_total_ms", int(entitlementCheckTimeMs)).
		Int("avg_entitle_ms", int(avgEntitleMs)).
		Int("elapsed_ms", int(totalElapsed.Milliseconds())).
		Int("elapsed_sec", int(totalElapsed.Seconds())).
		Msg("audible library sync complete")
	return added, nil
}

func (s *SyncService) finishProgressWithError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress.Running = false
	s.progress.Status = "failed"
	s.progress.Message = "Sync failed"
	s.progress.Error = err.Error()
	s.progress.CompletedAt = time.Now()
	s.emitLocked()
}

// MarshalPhases returns a JSON representation of the current phase statuses.
func (s *SyncService) MarshalPhases() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, _ := json.Marshal(s.progress.Phases)
	return string(data)
}

func ucfirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// audibleDateLayouts is the priority-ordered list of layouts we accept
// when parsing Audible-supplied timestamps. RFC3339 nano covers the
// "2026-05-19T07:29:29.505Z" purchase_date shape; RFC3339 covers the
// no-fractional variant; the date-only layout covers release_date.
var audibleDateLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02",
}

// parseAudibleDate tolerates the multiple shapes Audible returns for
// purchase_date / release_date. Returns the zero time when the input is
// empty or in none of the known formats.
func parseAudibleDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range audibleDateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func convertBook(b audible.Book) database.Book {
	authors := make([]string, len(b.Authors))
	for i, a := range b.Authors {
		authors[i] = cleanText(a.Name)
	}
	narrators := make([]string, len(b.Narrators))
	for i, n := range b.Narrators {
		narrators[i] = cleanText(n.Name)
	}

	var authorASIN string
	if len(b.Authors) > 0 {
		authorASIN = b.Authors[0].ASIN
	}

	var series, seriesPos string
	if len(b.Series) > 0 {
		series = cleanText(b.Series[0].Title)
		seriesPos = strings.TrimSpace(b.Series[0].Sequence)
	}

	coverURL := b.ProductImages.Image2400
	if coverURL == "" {
		coverURL = b.ProductImages.Image1024
	}
	if coverURL == "" {
		coverURL = b.ProductImages.Image500
	}

	// Audible is inconsistent across fields and accounts: purchase_date
	// comes back as RFC3339 ("2026-05-19T07:29:29.505Z"), release_date
	// as plain ISO date ("2023-08-23"), and occasionally either field
	// is just empty. parseAudibleDate tries the formats we've seen in
	// the wild in order; failures fall through to the zero time, which
	// the UI already renders as a blank cell.
	purchaseDate := parseAudibleDate(b.PurchaseDate)
	releaseDate := parseAudibleDate(b.ReleaseDate)

	drmType := b.ContentDeliveryType
	if drmType == "" {
		drmType = b.FormatType
	}

	return database.Book{
		ASIN:           b.BestID(),
		Title:          cleanText(b.Title),
		Author:         strings.Join(authors, ", "),
		AuthorASIN:     authorASIN,
		Narrator:       strings.Join(narrators, ", "),
		Publisher:      cleanText(b.Publisher),
		Description:    cleanText(b.PublisherSummary),
		Duration:       int64(b.RuntimeMinutes) * 60,
		Series:         series,
		SeriesPosition: seriesPos,
		CoverURL:       coverURL,
		PurchaseDate:   purchaseDate,
		ReleaseDate:    releaseDate,
		DRMType:        drmType,
	}
}
