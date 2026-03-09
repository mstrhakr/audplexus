package library

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mstrhakr/audible-plex-downloader/internal/audio"
	"github.com/mstrhakr/audible-plex-downloader/internal/audnexus"
	"github.com/mstrhakr/audible-plex-downloader/internal/database"
	"github.com/mstrhakr/audible-plex-downloader/internal/logging"
	"github.com/mstrhakr/audible-plex-downloader/internal/organizer"
	audible "github.com/mstrhakr/go-audible"
)

var dlLog = logging.Component("download")

// DownloadManager handles the full audiobook pipeline:
// download → decrypt → enrich metadata → organize into library.
type DownloadManager struct {
	db           database.Database
	client       *audible.Client
	ffmpeg       *audio.FFmpeg
	audnexus     *audnexus.Client
	organizer    *organizer.PlexOrganizer
	libraryDir   string
	downloadDir  string
	outputFmt    string // "m4b" or "mp3"
	embedCover   bool
	plexClientID string

	// Pipeline concurrency settings
	downloadConcurrency int
	decryptConcurrency  int
	processConcurrency  int

	// Pipeline stage channels
	decryptQueue chan *pipelineItem
	processQueue chan *pipelineItem

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}

	pauseMu     sync.RWMutex
	paused      bool
	pauseReason string
	pausedAt    time.Time

	subMu       sync.Mutex
	subscribers map[int]chan DownloadEvent
	nextSubID   int
}

// QueueState reports whether downloads are paused and why.
type QueueState struct {
	Paused   bool      `json:"paused"`
	Reason   string    `json:"reason,omitempty"`
	PausedAt time.Time `json:"paused_at,omitempty"`
}

// pipelineItem represents work moving through the pipeline stages.
type pipelineItem struct {
	BookID        int64
	ASIN          string
	Title         string
	DownloadItem  *database.DownloadQueue
	DecryptedPath string

	// Metadata prefetch payload (started during decrypt stage).
	Book       *database.Book
	Enriched   *audnexus.EnrichedBook
	BookErr    error
	EnrichErr  error
	EnrichDone chan struct{}
}

// DownloadEvent is emitted during the pipeline for progress tracking.
type DownloadEvent struct {
	ASIN         string  `json:"asin"`
	BookID       int64   `json:"book_id"`
	Title        string  `json:"title,omitempty"`
	Type         string  `json:"type"`  // "started", "progress", "complete", "failed", "paused", "resumed"
	Stage        string  `json:"stage"` // "downloading", "decrypting", "processing", "complete"
	Progress     float64 `json:"progress"`
	BytesWritten int64   `json:"bytes_written"`
	TotalBytes   int64   `json:"total_bytes"`
	Speed        float64 `json:"speed,omitempty"` // bytes per second
	Error        string  `json:"error,omitempty"`
}

// Pause stops workers from claiming new queue items.
func (dm *DownloadManager) Pause(reason string) bool {
	dm.pauseMu.Lock()
	if dm.paused {
		dm.pauseMu.Unlock()
		return false
	}
	if reason == "" {
		reason = "queue paused"
	}
	dm.paused = true
	dm.pauseReason = reason
	dm.pausedAt = time.Now()
	dm.pauseMu.Unlock()

	dlLog.Warn().Str("reason", reason).Msg("download queue paused")
	dm.emit(DownloadEvent{Type: "paused", Stage: "queue", Error: reason})
	return true
}

// Resume allows workers to claim pending queue items again.
func (dm *DownloadManager) Resume() bool {
	dm.pauseMu.Lock()
	if !dm.paused {
		dm.pauseMu.Unlock()
		return false
	}
	dm.paused = false
	dm.pauseReason = ""
	dm.pausedAt = time.Time{}
	dm.pauseMu.Unlock()

	dlLog.Info().Msg("download queue resumed")
	dm.emit(DownloadEvent{Type: "resumed", Stage: "queue"})
	return true
}

// QueueState returns the current queue pause state.
func (dm *DownloadManager) QueueState() QueueState {
	dm.pauseMu.RLock()
	defer dm.pauseMu.RUnlock()

	state := QueueState{
		Paused: dm.paused,
		Reason: dm.pauseReason,
	}
	if dm.paused {
		state.PausedAt = dm.pausedAt
	}
	return state
}

func (dm *DownloadManager) isPaused() bool {
	dm.pauseMu.RLock()
	defer dm.pauseMu.RUnlock()
	return dm.paused
}

func (dm *DownloadManager) pauseForPermissionError(err error, stage string) {
	if !isPermissionError(err) {
		return
	}
	msg := "permission error while " + stage + "; fix volume/file permissions and resume the queue"
	if dm.Pause(msg) {
		dlLog.Warn().Err(err).Str("stage", stage).Msg("queue auto-paused after permission error")
	}
}

func (dm *DownloadManager) requeueClaimedDownload(ctx context.Context, item *database.DownloadQueue) {
	if item == nil {
		return
	}
	item.Status = database.DownloadStatusPending
	item.StartedAt = nil
	if err := dm.db.UpdateDownload(ctx, item); err != nil {
		dlLog.Warn().Err(err).Int64("queue_id", item.ID).Msg("failed to requeue claimed download while paused")
	}
}

// NewDownloadManager creates a new download manager with the full processing pipeline.
func NewDownloadManager(
	db database.Database,
	client *audible.Client,
	ffmpeg *audio.FFmpeg,
	anClient *audnexus.Client,
	org *organizer.PlexOrganizer,
	libraryDir string,
	downloadDir string,
	outputFmt string,
	embedCover bool,
	downloadConcurrency int,
	decryptConcurrency int,
	processConcurrency int,
) *DownloadManager {
	numCPU := runtime.NumCPU()

	// Auto-detect download concurrency (I/O bound, can be higher)
	if downloadConcurrency <= 0 {
		downloadConcurrency = numCPU
		if downloadConcurrency < 2 {
			downloadConcurrency = 2
		}
		if downloadConcurrency > 6 {
			downloadConcurrency = 6
		}
	}

	// Auto-detect decrypt concurrency (CPU bound, use fewer cores)
	if decryptConcurrency <= 0 {
		decryptConcurrency = numCPU / 2
		if decryptConcurrency < 1 {
			decryptConcurrency = 1
		}
		if decryptConcurrency > 4 {
			decryptConcurrency = 4
		}
	}

	// Auto-detect process concurrency (mixed I/O and CPU)
	if processConcurrency <= 0 {
		processConcurrency = numCPU / 2
		if processConcurrency < 1 {
			processConcurrency = 1
		}
		if processConcurrency > 2 {
			processConcurrency = 2
		}
	}

	dlLog.Info().
		Int("num_cpu", numCPU).
		Int("download_workers", downloadConcurrency).
		Int("decrypt_workers", decryptConcurrency).
		Int("process_workers", processConcurrency).
		Msg("pipeline concurrency configured")

	if outputFmt == "" {
		outputFmt = "m4b"
	}

	return &DownloadManager{
		db:                  db,
		client:              client,
		ffmpeg:              ffmpeg,
		audnexus:            anClient,
		organizer:           org,
		libraryDir:          libraryDir,
		downloadDir:         downloadDir,
		outputFmt:           outputFmt,
		embedCover:          embedCover,
		plexClientID:        buildPlexClientID(),
		downloadConcurrency: downloadConcurrency,
		decryptConcurrency:  decryptConcurrency,
		processConcurrency:  processConcurrency,
		decryptQueue:        make(chan *pipelineItem, 100),
		processQueue:        make(chan *pipelineItem, 100),
		stopCh:              make(chan struct{}),
		subscribers:         make(map[int]chan DownloadEvent),
	}
}

// Subscribe returns a channel that receives pipeline events and an ID to unsubscribe.
func (dm *DownloadManager) Subscribe() (int, <-chan DownloadEvent) {
	dm.subMu.Lock()
	defer dm.subMu.Unlock()
	id := dm.nextSubID
	dm.nextSubID++
	ch := make(chan DownloadEvent, 32)
	dm.subscribers[id] = ch
	return id, ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (dm *DownloadManager) Unsubscribe(id int) {
	dm.subMu.Lock()
	defer dm.subMu.Unlock()
	if ch, ok := dm.subscribers[id]; ok {
		close(ch)
		delete(dm.subscribers, id)
	}
}

func (dm *DownloadManager) emit(event DownloadEvent) {
	dm.subMu.Lock()
	defer dm.subMu.Unlock()
	for _, ch := range dm.subscribers {
		select {
		case ch <- event:
		default:
			// Drop if subscriber is slow
		}
	}
}

// Start begins processing the download queue.
func (dm *DownloadManager) Start(ctx context.Context) {
	dm.mu.Lock()
	if dm.running {
		dm.mu.Unlock()
		return
	}
	dm.running = true
	dm.stopCh = make(chan struct{})
	dm.mu.Unlock()

	dm.reconcileStatuses(ctx)
	dm.reconcileLibraryFiles(ctx)
	dm.cleanupOrphanedFiles(ctx)

	// Start worker pools for each pipeline stage
	for i := 0; i < dm.downloadConcurrency; i++ {
		go dm.downloadWorker(ctx, i+1)
	}
	for i := 0; i < dm.decryptConcurrency; i++ {
		go dm.decryptWorker(ctx, i+1)
	}
	for i := 0; i < dm.processConcurrency; i++ {
		go dm.processWorker(ctx, i+1)
	}

	dlLog.Info().
		Int("download_workers", dm.downloadConcurrency).
		Int("decrypt_workers", dm.decryptConcurrency).
		Int("process_workers", dm.processConcurrency).
		Msg("pipeline workers started")
}

func (dm *DownloadManager) reconcileLibraryFiles(ctx context.Context) {
	updated, err := reconcileExistingAudiobookFiles(ctx, dm.db, dm.libraryDir)
	if err != nil {
		dlLog.Warn().Err(err).Msg("reconcile: failed to scan existing audiobook files")
		return
	}
	if updated > 0 {
		dlLog.Info().Int("books_reconciled", updated).Msg("reconcile: reconciled library files against disk")
	}
}

// reconcileStatuses fixes book statuses that are out of sync with their download queue entries.
// On startup, books stuck in transitional states (downloading, decrypting, processing) with
// no active download are reset to queued so they get reprocessed.
func (dm *DownloadManager) reconcileStatuses(ctx context.Context) {
	fixed := 0
	cleaned := 0

	// Find books that completed the full pipeline (have a FilePath) but are stuck in a non-complete state
	completeStatus := database.DownloadStatusComplete
	completedDownloads, err := dm.db.ListDownloads(ctx, &completeStatus)
	if err != nil {
		dlLog.Error().Err(err).Msg("reconcile: failed to list completed downloads")
		return
	}
	for _, dl := range completedDownloads {
		book, err := dm.db.GetBook(ctx, dl.BookID)
		if err != nil || book == nil {
			continue
		}
		// Only mark complete if the file actually exists on disk (full pipeline finished)
		if book.Status == database.BookStatusComplete {
			continue
		}
		if book.FilePath != "" {
			if _, err := os.Stat(book.FilePath); err == nil {
				dlLog.Info().Str("asin", book.ASIN).Str("old_status", string(book.Status)).Msg("reconcile: book file exists, marking complete")
				_ = dm.db.UpdateBookStatus(ctx, book.ID, database.BookStatusComplete)
				fixed++
				continue
			}
		}
	}

	// Reset download queue entries stuck in "active" status (likely from crash)
	activeStatus := database.DownloadStatusActive
	activeDownloads, err := dm.db.ListDownloads(ctx, &activeStatus)
	if err != nil {
		dlLog.Error().Err(err).Msg("reconcile: failed to list active downloads")
		return
	}

	// Reset all active downloads to pending on startup (they'll be retried)
	for _, dl := range activeDownloads {
		dlLog.Info().Str("asin", dl.ASIN).Msg("reconcile: resetting stuck active download to pending")
		dl.Status = database.DownloadStatusPending
		dl.StartedAt = nil
		dl.Progress = 0
		_ = dm.db.UpdateDownload(ctx, &dl)

		// Clean up any partial files from the interrupted download
		dm.cleanupDownloadFiles(dl.ASIN)
		cleaned++
	}

	activeBookIDs := make(map[int64]bool)
	for _, dl := range activeDownloads {
		activeBookIDs[dl.BookID] = true
	}

	// Reset books stuck in transitional states (downloading/decrypting/processing)
	for _, status := range []database.BookStatus{
		database.BookStatusDownloading,
		database.BookStatusDecrypting,
		database.BookStatusProcessing,
	} {
		stuckBooks, _, err := dm.db.ListBooks(ctx, database.BookFilter{Status: &status, Limit: 1000})
		if err != nil {
			dlLog.Error().Err(err).Str("status", string(status)).Msg("reconcile: failed to list stuck books")
			continue
		}
		for _, book := range stuckBooks {
			dlLog.Info().Str("asin", book.ASIN).Str("status", string(status)).Msg("reconcile: resetting stuck book to queued")
			_ = dm.db.UpdateBookStatus(ctx, book.ID, database.BookStatusQueued)
			fixed++
		}
	}

	if fixed > 0 || cleaned > 0 {
		dlLog.Info().Int("books_fixed", fixed).Int("files_cleaned", cleaned).Msg("reconcile: corrected statuses and cleaned files on startup")
	}
}

// Stop stops the download manager.
func (dm *DownloadManager) Stop() {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	if dm.running {
		close(dm.stopCh)
		dm.running = false
	}
}

// downloadWorker polls the database for pending downloads and processes them.
func (dm *DownloadManager) downloadWorker(ctx context.Context, workerID int) {
	log := dlLog.WithField("worker", fmt.Sprintf("download-%d", workerID))
	log.Debug().Msg("download worker started")

	for {
		if dm.isPaused() {
			time.Sleep(2 * time.Second)
			continue
		}

		select {
		case <-ctx.Done():
			log.Info().Msg("download worker stopping: context cancelled")
			return
		case <-dm.stopCh:
			log.Info().Msg("download worker stopping: stop signal")
			return
		default:
			// Get next pending download from database
			item, err := dm.db.GetNextPendingDownload(ctx)
			if err != nil {
				log.Error().Err(err).Msg("failed to get next download")
				time.Sleep(5 * time.Second)
				continue
			}
			if item == nil {
				log.Trace().Msg("no pending downloads, sleeping")
				time.Sleep(10 * time.Second)
				continue
			}

			if dm.isPaused() {
				dm.requeueClaimedDownload(ctx, item)
				time.Sleep(2 * time.Second)
				continue
			}

			// Process the download stage only
			dm.handleDownloadStage(ctx, item)
		}
	}
}

// decryptWorker processes items from the decrypt queue.
func (dm *DownloadManager) decryptWorker(ctx context.Context, workerID int) {
	log := dlLog.WithField("worker", fmt.Sprintf("decrypt-%d", workerID))
	log.Debug().Msg("decrypt worker started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("decrypt worker stopping: context cancelled")
			return
		case <-dm.stopCh:
			log.Info().Msg("decrypt worker stopping: stop signal")
			return
		case item := <-dm.decryptQueue:
			dm.handleDecryptStage(ctx, item)
		}
	}
}

// processWorker processes items from the process queue.
func (dm *DownloadManager) processWorker(ctx context.Context, workerID int) {
	log := dlLog.WithField("worker", fmt.Sprintf("process-%d", workerID))
	log.Debug().Msg("process worker started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("process worker stopping: context cancelled")
			return
		case <-dm.stopCh:
			log.Info().Msg("process worker stopping: stop signal")
			return
		case item := <-dm.processQueue:
			dm.handleProcessStage(ctx, item)
		}
	}
}

// failItem marks both the download queue entry and book as failed.
func (dm *DownloadManager) failItem(ctx context.Context, item *database.DownloadQueue, title string, err error) {
	now := time.Now()
	item.Status = database.DownloadStatusFailed
	item.Error = err.Error()
	item.CompletedAt = &now
	_ = dm.db.UpdateDownload(ctx, item)
	_ = dm.db.UpdateBookStatus(ctx, item.BookID, database.BookStatusFailed)
	dm.emit(DownloadEvent{ASIN: item.ASIN, BookID: item.BookID, Title: title, Type: "failed", Stage: "downloading", Error: err.Error()})
	dm.pauseForPermissionError(err, "processing downloads")
}

// downloadInfo holds the decryption keys saved alongside the downloaded file.
type downloadInfo struct {
	ContentType string `json:"content_type"`
	Key         string `json:"key"`
	IV          string `json:"iv"`
}

// decryptBook reads the download info JSON and decrypts the encrypted audiobook file.
func (dm *DownloadManager) decryptBook(ctx context.Context, item *pipelineItem, enriched *audnexus.EnrichedBook) (string, error) {
	if dm.ffmpeg == nil {
		return "", fmt.Errorf("ffmpeg not available")
	}
	if item == nil {
		return "", fmt.Errorf("nil pipeline item")
	}
	if enriched == nil {
		return "", fmt.Errorf("nil metadata")
	}

	asin := item.ASIN
	bookID := item.BookID
	bookTitle := item.Title

	// Read the download info JSON
	infoPath := filepath.Join(dm.downloadDir, asin+".json")
	infoData, err := os.ReadFile(infoPath)
	if err != nil {
		return "", fmt.Errorf("read download info: %w", err)
	}

	var info downloadInfo
	if err := json.Unmarshal(infoData, &info); err != nil {
		return "", fmt.Errorf("parse download info: %w", err)
	}

	// Determine input file path
	inputExt := ".aax"
	if info.Key != "" && info.IV != "" {
		inputExt = ".aaxc"
	}
	inputPath := filepath.Join(dm.downloadDir, asin+inputExt)

	// Output as m4b (decrypted container copy)
	outputPath := filepath.Join(dm.downloadDir, asin+".m4b")
	meta := enriched.ToAudioMetadata()

	coverPath := ""
	if dm.embedCover {
		downloaded, err := dm.downloadCoverToTemp(ctx, enriched.CoverURL(), asin)
		if err != nil {
			dlLog.Warn().Err(err).Str("asin", asin).Msg("cover prefetch failed; continuing without embedded cover")
		} else {
			coverPath = downloaded
			meta.CoverPath = coverPath
		}
	}

	// Use file size as a stable denominator for decrypt progress.
	var totalInputBytes int64
	if st, statErr := os.Stat(inputPath); statErr == nil {
		totalInputBytes = st.Size()
	}

	var lastEmit time.Time
	var lastLogPct int
	progressEmit := func(info audio.ProgressInfo) {
		now := time.Now()
		if now.Sub(lastEmit) < 500*time.Millisecond && info.Progress != "end" {
			return
		}
		lastEmit = now

		progress := 0.0
		if totalInputBytes > 0 && info.TotalSize > 0 {
			progress = float64(info.TotalSize) / float64(totalInputBytes)
			if progress > 1 {
				progress = 1
			}
		}

		speed := parseFFmpegSpeed(info.Speed)
		pct := int(progress * 100)
		if pct/10 > lastLogPct/10 {
			evt := dlLog.Info().
				Str("asin", asin).
				Int("pct", pct).
				Str("stage", "decrypting")

			if info.OutTime != "" && info.OutTime != "N/A" {
				evt = evt.Str("out_time", info.OutTime)
			}
			if speed > 0 {
				evt = evt.Float64("speed", speed)
			}

			evt.Msg("ffmpeg progress")
			lastLogPct = pct
		}

		dm.emit(DownloadEvent{
			ASIN:         asin,
			BookID:       bookID,
			Title:        bookTitle,
			Type:         "progress",
			Stage:        "decrypting",
			Progress:     progress,
			BytesWritten: info.TotalSize,
			TotalBytes:   totalInputBytes,
			Speed:        speed,
		})
	}
	if info.Key != "" && info.IV != "" {
		if err := dm.ffmpeg.DecryptAAXCWithMetadata(inputPath, outputPath, info.Key, info.IV, meta, progressEmit); err != nil {
			return "", err
		}
	} else {
		// Need activation bytes for AAX
		activationResp, err := dm.client.GetActivationBytes(ctx)
		if err != nil {
			return "", fmt.Errorf("get activation bytes: %w", err)
		}
		if err := dm.ffmpeg.DecryptAAXWithMetadata(inputPath, outputPath, activationResp.ActivationBytes, meta, progressEmit); err != nil {
			return "", err
		}
	}

	return outputPath, nil
}

func (dm *DownloadManager) downloadCoverToTemp(ctx context.Context, coverURL, asin string) (string, error) {
	if coverURL == "" {
		return "", nil
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cover download returned status %d", resp.StatusCode)
	}

	path := filepath.Join(dm.downloadDir, asin+".cover.jpg")
	out, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := io.Copy(out, io.LimitReader(resp.Body, 10*1024*1024)); err != nil {
		return "", err
	}

	return path, nil
}

func parseFFmpegSpeed(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "N/A" {
		return 0
	}
	raw = strings.TrimSuffix(raw, "x")
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// cleanupDownloadFiles removes intermediate files after successful processing.
func (dm *DownloadManager) cleanupDownloadFiles(asin string) {
	// Clean up all intermediate files including tagged versions
	patterns := []string{
		asin + ".aax",
		asin + ".aaxc",
		asin + ".json",
		asin + ".cover.jpg",
		asin + ".m4b",
		asin + ".m4b.tagged.m4b",
		asin + ".m4b.tagged",
	}

	for _, filename := range patterns {
		p := filepath.Join(dm.downloadDir, filename)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			dlLog.Debug().Err(err).Str("path", p).Msg("cleanup: failed to remove file")
		}
	}
}

// cleanupOrphanedFiles removes leftover files in downloads directory that don't have active downloads.
func (dm *DownloadManager) cleanupOrphanedFiles(ctx context.Context) {
	entries, err := os.ReadDir(dm.downloadDir)
	if err != nil {
		dlLog.Warn().Err(err).Msg("cleanup: failed to read download directory")
		return
	}

	// Get all active and pending ASINs
	activeASINs := make(map[string]bool)

	for _, status := range []database.DownloadStatus{
		database.DownloadStatusPending,
		database.DownloadStatusActive,
	} {
		downloads, err := dm.db.ListDownloads(ctx, &status)
		if err != nil {
			continue
		}
		for _, dl := range downloads {
			activeASINs[dl.ASIN] = true
		}
	}

	cleaned := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()

		// Extract ASIN from filename (before first dot)
		asin := name
		if idx := strings.Index(name, "."); idx != -1 {
			asin = name[:idx]
		}

		// Skip if this file belongs to an active/pending download
		if activeASINs[asin] {
			continue
		}

		// Check if file matches our temp file patterns
		if strings.HasSuffix(name, ".aax") ||
			strings.HasSuffix(name, ".aaxc") ||
			strings.HasSuffix(name, ".json") ||
			strings.HasSuffix(name, ".cover.jpg") ||
			strings.HasSuffix(name, ".m4b") ||
			strings.HasSuffix(name, ".tagged.m4b") ||
			strings.HasSuffix(name, ".tagged") {

			path := filepath.Join(dm.downloadDir, name)
			if err := os.Remove(path); err == nil {
				dlLog.Debug().Str("file", name).Msg("cleanup: removed orphaned file")
				cleaned++
			} else {
				dlLog.Debug().Err(err).Str("file", name).Msg("cleanup: failed to remove orphaned file")
			}
		}
	}

	if cleaned > 0 {
		dlLog.Info().Int("count", cleaned).Msg("cleanup: removed orphaned download files")
	}
}

// QueueBook adds a book to the download queue.
// The returned bool reports whether a new queue item was created.
func (dm *DownloadManager) QueueBook(ctx context.Context, bookID int64, asin string, priority int) (bool, error) {
	book, err := dm.db.GetBook(ctx, bookID)
	if err != nil {
		return false, fmt.Errorf("load book: %w", err)
	}
	if changed, err := reconcileBookFromLibrary(ctx, dm.db, book, dm.libraryDir); err != nil {
		return false, fmt.Errorf("reconcile existing file: %w", err)
	} else if changed || (book != nil && book.Status == database.BookStatusComplete && book.FilePath != "") {
		dlLog.Debug().Str("asin", asin).Str("file_path", book.FilePath).Msg("book already exists on disk, skipping queue")
		return false, nil
	}

	item := &database.DownloadQueue{
		BookID:   bookID,
		ASIN:     asin,
		Priority: priority,
	}
	if err := dm.db.EnqueueDownload(ctx, item); err != nil {
		return false, fmt.Errorf("queue download: %w", err)
	}
	if err := dm.db.UpdateBookStatus(ctx, bookID, database.BookStatusQueued); err != nil {
		return false, err
	}
	return true, nil
}

// QueueNewBooks queues all books with "new" status for download,
// limited to maxQueue entries. If maxQueue <= 0, queues all.
func (dm *DownloadManager) QueueNewBooks(ctx context.Context) (int, error) {
	return dm.QueueNewBooksLimit(ctx, 0) // 0 means unlimited (for backward compat)
}

// QueueNewBooksLimit queues books with "new" status for download, respecting a limit.
// If limit <= 0, queues all new books.
func (dm *DownloadManager) QueueNewBooksLimit(ctx context.Context, limit int) (int, error) {
	dm.reconcileLibraryFiles(ctx)

	status := database.BookStatusNew
	books, _, err := dm.db.ListBooks(ctx, database.BookFilter{Status: &status, Limit: 1000})
	if err != nil {
		return 0, err
	}

	// Apply limit if specified
	if limit > 0 && len(books) > limit {
		books = books[:limit]
	}

	queued := 0
	for _, book := range books {
		didQueue, err := dm.QueueBook(ctx, book.ID, book.ASIN, 0)
		if err != nil {
			dlLog.Error().Err(err).Str("asin", book.ASIN).Msg("failed to queue book")
			continue
		}
		if didQueue {
			dlLog.Debug().Str("asin", book.ASIN).Str("title", book.Title).Msg("queued book for download")
			queued++
		}
	}
	return queued, nil
}

// RefillQueue checks if more books should be queued and queues them up to the limit.
// This should be called periodically or after downloads complete.
func (dm *DownloadManager) RefillQueue(ctx context.Context, queueLimit int) error {
	// Count pending/active downloads
	var activeCount int
	for _, status := range []database.DownloadStatus{
		database.DownloadStatusPending,
		database.DownloadStatusActive,
	} {
		downloads, err := dm.db.ListDownloads(ctx, &status)
		if err != nil {
			return fmt.Errorf("list downloads: %w", err)
		}
		activeCount += len(downloads)
	}

	// If we're below the limit, queue more books
	if activeCount < queueLimit {
		spacesToFill := queueLimit - activeCount
		dm.QueueNewBooksLimit(ctx, spacesToFill)
	}

	return nil
}

// fileDownloadWriter implements audible.DownloadWriter, writing to a temp file.
type fileDownloadWriter struct {
	asin        string
	downloadDir string
	file        *os.File
	info        *audible.DownloadInfo
	onProgress  func(written, total int64)
}

func (w *fileDownloadWriter) OnStart(asin string, contentLength int64, info *audible.DownloadInfo) error {
	w.info = info

	// Determine file extension from content type
	ext := ".aax"
	if info.LicenseResponse != nil && info.LicenseResponse.Key != "" {
		ext = ".aaxc"
	}

	filePath := filepath.Join(w.downloadDir, asin+ext)
	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("create download file: %w", err)
	}
	w.file = f

	// Save download info alongside the file for later decryption
	infoPath := filepath.Join(w.downloadDir, asin+".json")
	infoFile, err := os.Create(infoPath)
	if err != nil {
		w.file.Close()
		return fmt.Errorf("create info file: %w", err)
	}
	defer infoFile.Close()

	// Write info as JSON
	enc := fmt.Sprintf(`{"content_type":"%s"`, info.ContentType)
	if info.LicenseResponse != nil {
		enc += fmt.Sprintf(`,"key":"%s","iv":"%s"`, info.LicenseResponse.Key, info.LicenseResponse.IV)
	}
	enc += "}"
	infoFile.WriteString(enc)

	return nil
}

func (w *fileDownloadWriter) Write(p []byte) (n int, err error) {
	return w.file.Write(p)
}

func (w *fileDownloadWriter) OnProgress(bytesWritten, totalBytes int64) error {
	if w.onProgress != nil {
		w.onProgress(bytesWritten, totalBytes)
	}
	return nil
}

func (w *fileDownloadWriter) OnComplete() error {
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// Cleanup closes the file handle without marking the download as complete.
// Called when the download fails to ensure the handle is released before removing files.
func (w *fileDownloadWriter) Cleanup() {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
}

// formatBytes returns a human-readable byte size string.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
