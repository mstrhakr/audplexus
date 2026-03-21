package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mstrhakr/audible-plex-downloader/internal/database"
	"github.com/mstrhakr/audible-plex-downloader/internal/logging"
	audible "github.com/mstrhakr/go-audible"
)

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
	PhasePlexSync       SyncPhase = "plex_sync"
	PhaseCollectionSync SyncPhase = "collection_sync"
	PhaseDownloadQueue  SyncPhase = "download_queue"
)

// DefaultFullPhases returns the standard set of sync phases in idle state.
func DefaultFullPhases() []PhaseStatus {
	return []PhaseStatus{
		{Name: PhaseAudibleSync, Label: "Audible Library", Status: "idle"},
		{Name: PhaseFileScan, Label: "File System Scan", Status: "idle"},
		{Name: PhasePlexSync, Label: "Plex Sync", Status: "idle"},
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

// PhaseStatus tracks the state of a single sync phase.
type PhaseStatus struct {
	Name           SyncPhase `json:"name"`
	Label          string    `json:"label"`
	Status         string    `json:"status"` // "pending", "running", "complete", "failed", "skipped"
	Message        string    `json:"message,omitempty"`
	Error          string    `json:"error,omitempty"`
	Current        int       `json:"current,omitempty"`
	Total          int       `json:"total,omitempty"`
	DisplayCurrent int       `json:"display_current,omitempty"`
	DisplayTotal   int       `json:"display_total,omitempty"`
	Percent        float64   `json:"percent,omitempty"`
	Indeterminate  bool      `json:"indeterminate,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	EndedAt        time.Time `json:"ended_at,omitempty"`
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

// PlexSyncFunc is a callback that the SyncService uses to perform a combined Plex scan + query.
// This avoids importing web-layer Plex code into the library package.
type PlexSyncFunc func(ctx context.Context) (plexItemCount int, err error)
type PlexReconcileFunc func(ctx context.Context, progressFn func(current, total int)) error

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

	libraryDir string

	// Plex callback (set by web layer after construction)
	plexSyncFunc      PlexSyncFunc
	plexReconcileFunc PlexReconcileFunc

	mu       sync.RWMutex
	progress SyncProgress

	// Track last sync for retry
	lastMode SyncMode

	// SSE subscriber support
	subMu       sync.Mutex
	subscribers map[int]chan SyncEvent
	nextSubID   int
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

	case PhasePlexSync:
		if s.plexSyncFunc == nil {
			phaseErr = fmt.Errorf("Plex not configured")
		} else {
			items, err := s.plexSyncFunc(ctx)
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
			phaseErr = fmt.Errorf("Plex not configured")
		} else {
			completeStatus := database.BookStatusComplete
			_, completeCount, _ := s.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 1})
			err := s.plexReconcileFunc(ctx, func(current, total int) {
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
			syncLog.Warn().Err(fsErr).Msg("file scan phase failed")
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
				syncLog.Warn().Err(fsErr).Msg("quick reconcile failed")
			} else if reconciled > 0 {
				syncLog.Info().Int("books_reconciled", reconciled).Msg("quick sync: reconciled new books against disk")
			}
		}
	}

	// --- Phase 3: Plex Sync (full sync only) ---
	plexItems := 0
	if mode == SyncModeFull && s.plexSyncFunc != nil {
		s.setPhase(PhasePlexSync, "running", "Syncing with Plex (scan + query)...")
		items, plexErr := s.plexSyncFunc(ctx)
		if plexErr != nil {
			s.setPhase(PhasePlexSync, "failed", plexErr.Error())
			syncLog.Warn().Err(plexErr).Msg("plex sync phase failed")
		} else {
			plexItems = items
			s.mu.Lock()
			s.progress.PlexItems = plexItems
			s.progress.PlexScanned = true
			s.emitLocked()
			s.mu.Unlock()
			s.setPhase(PhasePlexSync, "complete", fmt.Sprintf("%d items in Plex (scan complete)", plexItems))
			syncLog.Info().Int("plex_items", plexItems).Msg("synced with Plex")
		}
	}

	// --- Phase 4: Collection Sync (full sync only) ---

	// --- Phase 5: Collection Sync (full sync only) ---
	if mode == SyncModeFull && s.plexReconcileFunc != nil {
		s.setPhase(PhaseCollectionSync, "running", "Reconciling Plex collections...")
		completeStatus := database.BookStatusComplete
		_, completeCount, _ := s.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 1})
		reconcileErr := s.plexReconcileFunc(ctx, func(current, total int) {
			displayCurrent := scaleProgress(current, total, completeCount)
			s.updatePhaseProgressWithDisplay(PhaseCollectionSync, current, total, false, displayCurrent, completeCount)
		})
		if reconcileErr != nil {
			s.setPhase(PhaseCollectionSync, "failed", reconcileErr.Error())
			syncLog.Warn().Err(reconcileErr).Msg("collection sync phase failed")
		} else {
			s.setPhase(PhaseCollectionSync, "complete", "Collections verified")
			syncLog.Info().Msg("plex collection sync complete")
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
			defaultPhase(PhasePlexSync, "Plex Sync"),
			defaultPhase(PhaseCollectionSync, "Collection Sync"),
		}
	}

	phases := []PhaseStatus{defaultPhase(PhaseAudibleSync, "Audible Library")}
	for _, phase := range []struct {
		name  SyncPhase
		label string
	}{
		{name: PhaseFileScan, label: "File System Scan"},
		{name: PhasePlexSync, label: "Plex Sync"},
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress.CurrentPhase = phase
	for i := range s.progress.Phases {
		if s.progress.Phases[i].Name == phase {
			now := time.Now()
			s.progress.Phases[i].Status = status
			s.progress.Phases[i].Message = message
			if status == "running" {
				s.progress.Phases[i].StartedAt = now
				s.progress.Phases[i].EndedAt = time.Time{}
				s.progress.Phases[i].Error = ""
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
				s.progress.Phases[i].Error = message
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
			s.progress.Message = s.progress.Phases[i].Label + ": " + message
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

func (s *SyncService) doAudibleSync(ctx context.Context, syncRecord *database.SyncHistory) (int, error) {
	syncLog.Info().Msg("starting audible library sync")

	books, err := s.client.GetAllLibrary(ctx)
	if err != nil {
		syncLog.Error().Err(err).Msg("failed to fetch audible library")
		return 0, err
	}

	syncRecord.BooksFound = len(books)
	s.mu.Lock()
	s.progress.BooksFound = len(books)
	for i := range s.progress.Phases {
		if s.progress.Phases[i].Name == PhaseAudibleSync {
			setPhaseProgress(&s.progress.Phases[i], 0, len(books), false, s.progress.Phases[i].Status)
			break
		}
	}
	s.emitLocked()
	s.mu.Unlock()
	_ = s.db.UpdateSync(ctx, syncRecord)
	syncLog.Info().Int("total_books", len(books)).Msg("fetched audible library")

	added := 0
	scanned := 0
	keepASIN := make(map[string]struct{})

	for _, item := range books {
		book := convertBook(item)
		syncLog.Trace().Str("asin", book.ASIN).Str("title", book.Title).Msg("processing book")

		// Skip items not eligible for local download (e.g. ebook-only or non-owned).
		if !item.Downloadable() || (item.ContentType != "" && !strings.EqualFold(item.ContentType, "audiobook")) {
			syncLog.Info().Str("asin", book.ASIN).Str("title", book.Title).Str("content_type", item.ContentType).Bool("is_downloadable", item.IsDownloadable).Msg("skipping non-downloadable or non-audiobook item")
			scanned++
			s.mu.Lock()
			s.progress.BooksScanned = scanned
			s.progress.BooksAdded = added
			for i := range s.progress.Phases {
				if s.progress.Phases[i].Name == PhaseAudibleSync {
					setPhaseProgress(&s.progress.Phases[i], scanned, len(books), false, s.progress.Phases[i].Status)
					break
				}
			}
			if scanned%10 == 0 {
				s.emitLocked()
			}
			s.mu.Unlock()
			continue
		}

		keepASIN[book.ASIN] = struct{}{}

		existing, err := s.db.GetBookByASIN(ctx, book.ASIN)
		if err != nil {
			syncLog.Error().Err(err).Str("asin", book.ASIN).Msg("failed to check existing book")
			scanned++
			s.mu.Lock()
			s.progress.BooksScanned = scanned
			s.progress.BooksAdded = added
			for i := range s.progress.Phases {
				if s.progress.Phases[i].Name == PhaseAudibleSync {
					setPhaseProgress(&s.progress.Phases[i], scanned, len(books), false, s.progress.Phases[i].Status)
					break
				}
			}
			if scanned%10 == 0 {
				s.emitLocked()
			}
			s.mu.Unlock()
			continue
		}

		// Preserve status/file info for existing books
		if existing != nil {
			book.Status = existing.Status
			book.FilePath = existing.FilePath
			book.FileSize = existing.FileSize
			syncLog.Debug().Str("asin", book.ASIN).Str("status", string(existing.Status)).Msg("book already exists, preserving state")
		} else {
			book.Status = database.BookStatusNew
			added++
			syncLog.Info().Str("asin", book.ASIN).Str("title", book.Title).Msg("new book discovered")
		}

		if err := s.db.UpsertBook(ctx, &book); err != nil {
			syncLog.Error().Err(err).Str("asin", book.ASIN).Msg("failed to upsert book")
			scanned++
			s.mu.Lock()
			s.progress.BooksScanned = scanned
			s.progress.BooksAdded = added
			for i := range s.progress.Phases {
				if s.progress.Phases[i].Name == PhaseAudibleSync {
					setPhaseProgress(&s.progress.Phases[i], scanned, len(books), false, s.progress.Phases[i].Status)
					break
				}
			}
			if scanned%10 == 0 {
				s.emitLocked()
			}
			s.mu.Unlock()
			if scanned%20 == 0 {
				syncRecord.BooksAdded = added
				_ = s.db.UpdateSync(ctx, syncRecord)
			}
			continue
		}

		scanned++
		s.mu.Lock()
		s.progress.BooksScanned = scanned
		s.progress.BooksAdded = added
		for i := range s.progress.Phases {
			if s.progress.Phases[i].Name == PhaseAudibleSync {
				setPhaseProgress(&s.progress.Phases[i], scanned, len(books), false, s.progress.Phases[i].Status)
				break
			}
		}
		if scanned%10 == 0 {
			s.emitLocked()
		}
		s.mu.Unlock()
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
	if len(books) > 0 {
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

	syncLog.Info().Int("added", added).Int("eligible", eligibleCount).Int("removed", removed).Msg("audible library sync complete")
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

func convertBook(b audible.Book) database.Book {
	authors := make([]string, len(b.Authors))
	for i, a := range b.Authors {
		authors[i] = a.Name
	}
	narrators := make([]string, len(b.Narrators))
	for i, n := range b.Narrators {
		narrators[i] = n.Name
	}

	var authorASIN string
	if len(b.Authors) > 0 {
		authorASIN = b.Authors[0].ASIN
	}

	var series, seriesPos string
	if len(b.Series) > 0 {
		series = b.Series[0].Title
		seriesPos = b.Series[0].Sequence
	}

	coverURL := b.ProductImages.Image2400
	if coverURL == "" {
		coverURL = b.ProductImages.Image1024
	}
	if coverURL == "" {
		coverURL = b.ProductImages.Image500
	}

	purchaseDate, _ := time.Parse("2006-01-02", b.PurchaseDate)
	releaseDate, _ := time.Parse("2006-01-02", b.ReleaseDate)

	drmType := b.ContentDeliveryType
	if drmType == "" {
		drmType = b.FormatType
	}

	return database.Book{
		ASIN:           b.BestID(),
		Title:          b.Title,
		Author:         strings.Join(authors, ", "),
		AuthorASIN:     authorASIN,
		Narrator:       strings.Join(narrators, ", "),
		Publisher:      b.Publisher,
		Description:    b.PublisherSummary,
		Duration:       int64(b.RuntimeMinutes) * 60,
		Series:         series,
		SeriesPosition: seriesPos,
		CoverURL:       coverURL,
		PurchaseDate:   purchaseDate,
		ReleaseDate:    releaseDate,
		DRMType:        drmType,
	}
}
