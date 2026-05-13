package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/logging"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
)

// downloadStallTimeout is how long the download stage may go without seeing
// any new bytes before its context is cancelled and the request is treated
// as a stalled CDN connection. Audible's CDN normally resets within seconds
// of an actual error, but we've seen connections sit silently for minutes.
const downloadStallTimeout = 2 * time.Minute

// Small UX delay so reorganize items remain visible briefly in the pipeline
// list before completion clears them from the active state.
const reorganizeCompleteVisibilityDelay = 200 * time.Millisecond

// copyFileSimple copies src to dst, overwriting any existing file. Used for
// best-effort sidecar writes (e.g. folder.jpg) where progress isn't needed.
func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// handleDownloadStage handles the download stage of the pipeline.
func (dm *DownloadManager) handleDownloadStage(ctx context.Context, item *database.DownloadQueue) {
	asinLog := dlLog.WithField("asin", item.ASIN)
	asinLog.Info().Int64("book_id", item.BookID).Msg("starting download stage")

	// Look up book title for SSE display
	bookTitle := item.ASIN
	book, err := dm.db.GetBook(ctx, item.BookID)
	if err != nil {
		dm.failItem(ctx, item, item.ASIN, fmt.Errorf("load book: %w", err))
		return
	}
	if book == nil {
		dm.failItem(ctx, item, item.ASIN, fmt.Errorf("load book: not found (id=%d)", item.BookID))
		return
	}
	if book.Title != "" {
		bookTitle = book.Title
	}

	pipeItem := &pipelineItem{
		BookID:       item.BookID,
		ASIN:         item.ASIN,
		Title:        bookTitle,
		DownloadItem: item,
		Book:         book,
	}

	// Skip actual download for reorganize jobs; jump straight to decrypt
	if item.Status.IsReorganize() {
		asinLog.Info().Msg("reorganize job: skipping download")
		dm.startMetadataPrefetch(ctx, pipeItem)
		now := time.Now()
		item.StartedAt = &now
		if err := dm.db.UpdateDownload(ctx, item); err != nil {
			asinLog.Warn().Err(err).Msg("failed to persist reorganize download status")
		}
		if err := dm.db.UpdateBookStatus(ctx, item.BookID, database.BookStatusProcessing); err != nil {
			asinLog.Warn().Err(err).Msg("failed to persist reorganize book status")
		}
		pipeItem.DecryptedPath = book.FilePath
		dm.emit(DownloadEvent{ASIN: item.ASIN, BookID: item.BookID, Title: bookTitle, Type: "stage", Stage: "waiting_moving", Progress: 1.0, QueueDepth: len(dm.processQueue) + 1, QueueItem: true})
		select {
		case dm.processQueue <- pipeItem:
			asinLog.Debug().Msg("reorganize job queued directly for move")
		case <-ctx.Done():
			asinLog.Warn().Msg("context cancelled while queuing for move")
		}
		return
	}

	// Per-item cancellable context: lets the user abort this download from
	// the UI and lets the stall watchdog kill a stuck connection without
	// taking down the whole worker pool.
	itemCtx, cancelItem := context.WithCancel(ctx)
	cancelTok := dm.registerActiveCancel(item.ASIN, cancelItem)
	defer func() {
		dm.unregisterActiveCancel(item.ASIN, cancelTok)
		cancelItem()
	}()

	// Start metadata lookup immediately. Use the parent worker ctx (not
	// itemCtx) so the in-flight audnexus calls survive a stall-watchdog
	// cancel of the download and remain available to decrypt/process,
	// which depend on chapter data to honor the mp3-split setting.
	dm.startMetadataPrefetch(ctx, pipeItem)

	// Mark download as active
	now := time.Now()
	item.Status = database.DownloadStatusActive
	item.StartedAt = &now
	if err := dm.db.UpdateDownload(itemCtx, item); err != nil {
		asinLog.Warn().Err(err).Msg("failed to persist download active status")
	}
	if err := dm.db.UpdateBookStatus(itemCtx, item.BookID, database.BookStatusDownloading); err != nil {
		asinLog.Warn().Err(err).Msg("failed to persist book downloading status")
	}

	dm.emit(DownloadEvent{ASIN: item.ASIN, BookID: item.BookID, Title: bookTitle, Type: "started", Stage: "downloading"})

	var lastEmit time.Time
	var lastDBWrite time.Time
	var lastLogPct int
	downloadStart := time.Now()
	var bytesSnapshot atomic.Int64
	var stalled atomic.Bool

	// Stall watchdog: if no new bytes arrive for downloadStallTimeout while
	// the download is in progress, cancel the item context. The CDN's TCP
	// keepalive can take 2h to fire, so without this a hung connection
	// would sit in io.Copy until then.
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		var lastSeen int64
		var lastSeenAt = time.Now()
		for {
			select {
			case <-itemCtx.Done():
				return
			case <-ticker.C:
				cur := bytesSnapshot.Load()
				if cur > lastSeen {
					lastSeen = cur
					lastSeenAt = time.Now()
					continue
				}
				// Fire whether or not any bytes arrived — a connection
				// that hangs before the first byte (slow TLS, server
				// silently dropping the request) is exactly the kind
				// of stall this watchdog is meant to recover from.
				if time.Since(lastSeenAt) >= downloadStallTimeout {
					stalled.Store(true)
					asinLog.Warn().
						Int64("bytes", cur).
						Dur("idle", time.Since(lastSeenAt).Round(time.Second)).
						Msg("download stalled with no progress; cancelling")
					cancelItem()
					return
				}
			}
		}
	}()

	writer := &fileDownloadWriter{
		asin:        item.ASIN,
		downloadDir: dm.downloadDir,
		onProgress: func(written, total int64) {
			bytesSnapshot.Store(written)
			progress := 0.0
			if total > 0 {
				progress = float64(written) / float64(total)
			}
			item.Progress = progress

			now := time.Now()
			elapsed := now.Sub(downloadStart).Seconds()
			speed := 0.0
			if elapsed > 0 {
				speed = float64(written) / elapsed
			}

			// Log at every 10% milestone
			pct := int(progress * 100)
			if pct/10 > lastLogPct/10 {
				asinLog.Info().
					Int("pct", pct).
					Str("written", formatBytes(written)).
					Str("total", formatBytes(total)).
					Str("speed", formatBytes(int64(speed))+"/s").
					Msg("download progress")
				lastLogPct = pct
			}

			// Persist to DB every 5 seconds
			if now.Sub(lastDBWrite) >= 5*time.Second {
				lastDBWrite = now
				if err := dm.db.UpdateDownload(itemCtx, item); err != nil {
					asinLog.Warn().Err(err).Msg("failed to persist download progress")
				}
			}

			// SSE to UI every 500ms for smooth progress
			if now.Sub(lastEmit) < 500*time.Millisecond {
				return
			}
			lastEmit = now

			dm.emit(DownloadEvent{
				ASIN:         item.ASIN,
				BookID:       item.BookID,
				Title:        bookTitle,
				Type:         "progress",
				Stage:        "downloading",
				Progress:     progress,
				BytesWritten: written,
				TotalBytes:   total,
				Speed:        speed,
			})
		},
	}

	bytesWritten, err := dm.client.DownloadBook(itemCtx, item.ASIN, writer)
	// Watchdog goroutine winds down once itemCtx is done; wait so it's
	// fully gone before we exit and cancelItem fires from the deferred
	// cleanup (avoids a brief leak window if the worker keeps churning).
	cancelItem()
	<-watchDone

	if err != nil {
		// Distinguish a watchdog-initiated stall cancel from a real network
		// error or a parent-context cancel (queue paused / shutdown) so the
		// user-facing message is accurate.
		switch {
		case stalled.Load():
			err = fmt.Errorf("download stalled (no progress for %s); cancelled", downloadStallTimeout)
		case ctx.Err() == nil && errors.Is(err, context.Canceled):
			err = fmt.Errorf("cancelled by user")
		}
		asinLog.Error().Err(err).Msg("download failed")
		writer.Cleanup()
		dm.cleanupDownloadFiles(item.ASIN)
		dm.failItem(ctx, item, bookTitle, err)
		return
	}
	elapsed := time.Since(downloadStart)
	asinLog.Info().
		Str("size", formatBytes(bytesWritten)).
		Str("elapsed", elapsed.Round(time.Second).String()).
		Msg("download complete")

	// Keep queue item active; full completion is only after decrypt + processing finish.
	now = time.Now()
	item.Progress = 1.0
	if err := dm.db.UpdateDownload(ctx, item); err != nil {
		asinLog.Warn().Err(err).Msg("failed to persist download-complete progress")
	}
	dm.emit(DownloadEvent{
		ASIN:         item.ASIN,
		BookID:       item.BookID,
		Title:        bookTitle,
		Type:         "progress",
		Stage:        "downloading",
		Progress:     1.0,
		BytesWritten: bytesWritten,
		TotalBytes:   bytesWritten,
	})

	// Push to decrypt queue
	queueDepth := len(dm.decryptQueue) + 1
	select {
	case dm.decryptQueue <- pipeItem:
		dm.emit(DownloadEvent{
			ASIN:       item.ASIN,
			BookID:     item.BookID,
			Title:      bookTitle,
			Type:       "stage",
			Stage:      "waiting_decrypt",
			Progress:   1.0,
			QueueDepth: queueDepth,
			QueueItem:  true,
		})
		asinLog.Debug().Msg("pushed to decrypt queue")
	case <-ctx.Done():
		asinLog.Warn().Msg("context cancelled while queuing for decrypt")
	}
}

// handleDecryptStage handles the decryption stage of the pipeline.
func (dm *DownloadManager) handleDecryptStage(parentCtx context.Context, item *pipelineItem) {
	asinLog := dlLog.WithField("asin", item.ASIN)
	asinLog.Info().Msg("starting decrypt stage")

	// Per-item cancel: lets the user abort this decrypt from the UI.
	// parentCtx is preserved separately for cleanup writes (failItem) so
	// they don't fail just because the user cancelled this item.
	ctx, cancelItem := context.WithCancel(parentCtx)
	cancelTok := dm.registerActiveCancel(item.ASIN, cancelItem)
	defer func() {
		dm.unregisterActiveCancel(item.ASIN, cancelTok)
		cancelItem()
	}()

	if err := dm.db.UpdateBookStatus(ctx, item.BookID, database.BookStatusDecrypting); err != nil {
		asinLog.Warn().Err(err).Msg("failed to persist book decrypting status")
	}
	dm.emit(DownloadEvent{ASIN: item.ASIN, BookID: item.BookID, Title: item.Title, Type: "stage", Stage: "decrypting"})

	// Skip actual decrypt for reorganize jobs; use existing file path
	if item.DownloadItem != nil && item.DownloadItem.Status.IsReorganize() {
		asinLog.Info().Str("path", item.Book.FilePath).Msg("reorganize job: using existing file")
		if strings.TrimSpace(item.Book.FilePath) == "" {
			dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("reorganize: book has no file_path"))
			return
		}
		if _, statErr := os.Stat(item.Book.FilePath); statErr != nil {
			dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("reorganize: file not found at %s: %w", item.Book.FilePath, statErr))
			return
		}
		item.DecryptedPath = item.Book.FilePath
		if item.Enriched == nil {
			item.Enriched = &audnexus.EnrichedBook{Book: item.Book}
		}
		asinLog.Info().Str("path", item.DecryptedPath).Msg("reorganize ready to move")
		dm.emit(DownloadEvent{
			ASIN:     item.ASIN,
			BookID:   item.BookID,
			Title:    item.Title,
			Type:     "progress",
			Stage:    "decrypting",
			Progress: 1.0,
		})
		queueDepth := len(dm.processQueue) + 1
		select {
		case dm.processQueue <- item:
			dm.emit(DownloadEvent{
				ASIN:       item.ASIN,
				BookID:     item.BookID,
				Title:      item.Title,
				Type:       "stage",
				Stage:      "waiting_moving",
				Progress:   1.0,
				QueueDepth: queueDepth,
				QueueItem:  true,
			})
			asinLog.Debug().Msg("pushed to process queue")
		case <-ctx.Done():
			asinLog.Warn().Msg("context cancelled while queuing for processing")
		}
		return
	}

	if item.EnrichDone != nil {
		select {
		case <-item.EnrichDone:
		case <-ctx.Done():
			asinLog.Warn().Msg("context cancelled while waiting for metadata prefetch")
			return
		}
	}

	if item.BookErr != nil {
		dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("load book: %w", item.BookErr))
		return
	}

	if item.Book == nil {
		book, err := dm.db.GetBook(ctx, item.BookID)
		if err != nil {
			dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("load book: %w", err))
			return
		}
		item.Book = book
	}

	enriched := item.Enriched
	if enriched == nil {
		enriched = &audnexus.EnrichedBook{Book: item.Book}
		if item.EnrichErr != nil {
			asinLog.Warn().Err(item.EnrichErr).Msg("metadata enrichment failed during prefetch, using Audible metadata fallback")
		}
	}

	decryptedPath, err := dm.decryptBook(ctx, item, enriched)
	if err != nil {
		// If the user cancelled this item, surface that in the failure
		// message rather than a noisy ffmpeg "context canceled" error.
		if parentCtx.Err() == nil && errors.Is(err, context.Canceled) {
			err = fmt.Errorf("cancelled by user")
		}
		asinLog.Error().Err(err).Msg("decryption failed")
		dm.cleanupDownloadFiles(item.ASIN)
		dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("decrypt: %w", err))
		return
	}
	item.DecryptedPath = decryptedPath
	item.Enriched = enriched
	asinLog.Info().Str("path", decryptedPath).Msg("decryption complete")
	dm.emit(DownloadEvent{
		ASIN:     item.ASIN,
		BookID:   item.BookID,
		Title:    item.Title,
		Type:     "progress",
		Stage:    "decrypting",
		Progress: 1.0,
	})

	// Push to process queue
	queueDepth := len(dm.processQueue) + 1
	select {
	case dm.processQueue <- item:
		dm.emit(DownloadEvent{
			ASIN:       item.ASIN,
			BookID:     item.BookID,
			Title:      item.Title,
			Type:       "stage",
			Stage:      "waiting_moving",
			Progress:   1.0,
			QueueDepth: queueDepth,
			QueueItem:  true,
		})
		asinLog.Debug().Msg("pushed to process queue")
	case <-ctx.Done():
		asinLog.Warn().Msg("context cancelled while queuing for processing")
	}
}

// handleProcessStage handles final organization/chapter generation for already-tagged audio.
func (dm *DownloadManager) handleProcessStage(parentCtx context.Context, item *pipelineItem) {
	asinLog := dlLog.WithField("asin", item.ASIN)
	asinLog.Info().Msg("starting move stage")

	// Per-item cancel: lets the user abort this process step from the UI.
	// parentCtx stays valid for failItem cleanup writes even if the user
	// cancels this item mid-flight.
	ctx, cancelItem := context.WithCancel(parentCtx)
	cancelTok := dm.registerActiveCancel(item.ASIN, cancelItem)
	defer func() {
		dm.unregisterActiveCancel(item.ASIN, cancelTok)
		cancelItem()
	}()

	if err := dm.db.UpdateBookStatus(ctx, item.BookID, database.BookStatusProcessing); err != nil {
		asinLog.Warn().Err(err).Msg("failed to persist book processing status")
	}
	dm.emit(DownloadEvent{ASIN: item.ASIN, BookID: item.BookID, Title: item.Title, Type: "stage", Stage: "moving"})

	// Ensure canonical book/metadata are available before move.
	book := item.Book
	if book == nil {
		var err error
		book, err = dm.db.GetBook(ctx, item.BookID)
		if err != nil {
			asinLog.Error().Err(err).Msg("failed to load book record")
			dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("load book: %w", err))
			return
		}
	}

	// Use metadata prepared earlier in the pipeline.
	enriched := item.Enriched
	if enriched == nil {
		enriched = &audnexus.EnrichedBook{Book: book}
	}

	// Move already-tagged media into final Plex folder structure with real progress.
	decryptedPath := item.DecryptedPath
	if decryptedPath == "" {
		decryptedPath = filepath.Join(dm.downloadDir, item.ASIN+".m4b")
	}

	// Reorganize jobs should not transcode/decrypt/download again; they should
	// atomically rename existing on-disk paths into the current naming scheme.
	if item.DownloadItem != nil && item.DownloadItem.Status.IsReorganize() {
		totalBytes := int64(0)
		if fi, statErr := os.Stat(decryptedPath); statErr == nil {
			if fi.IsDir() {
				totalBytes = book.FileSize
			} else {
				totalBytes = fi.Size()
			}
		}

		moveStart := time.Now()
		var lastEmit time.Time
		onMoveProgress := func(moved, total int64) {
			now := time.Now()
			if now.Sub(lastEmit) < 300*time.Millisecond && moved < total {
				return
			}
			lastEmit = now

			if total <= 0 {
				total = totalBytes
			}
			progress := 0.0
			if total > 0 {
				progress = float64(moved) / float64(total)
				if progress > 1 {
					progress = 1
				}
			}
			speed := 0.0
			if elapsed := now.Sub(moveStart).Seconds(); elapsed > 0 {
				speed = float64(moved) / elapsed
			}
			dm.emit(DownloadEvent{
				ASIN:         item.ASIN,
				BookID:       item.BookID,
				Title:        item.Title,
				Type:         "progress",
				Stage:        "moving",
				Progress:     progress,
				BytesWritten: moved,
				TotalBytes:   total,
				Speed:        speed,
			})
		}

		onMoveProgress(0, totalBytes)
		onStage := func(stage string) {
			if stage == "finalizing" {
				dm.emit(DownloadEvent{
					ASIN:   item.ASIN,
					BookID: item.BookID,
					Title:  item.Title,
					Type:   "stage",
					Stage:  "finalizing",
				})
			}
		}

		finalPath, err := dm.organizer.ReorganizeInPlaceWithProgress(ctx, book, enriched, onMoveProgress, onStage)
		if err != nil {
			if parentCtx.Err() == nil && errors.Is(err, context.Canceled) {
				err = fmt.Errorf("cancelled by user")
			}
			dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("reorganize: %w", err))
			return
		}

		now := time.Now()
		if item.DownloadItem != nil {
			item.DownloadItem.Status = database.DownloadStatusComplete
			item.DownloadItem.Progress = 1.0
			item.DownloadItem.Error = ""
			item.DownloadItem.CompletedAt = &now
			if err := dm.db.UpdateDownload(context.WithoutCancel(parentCtx), item.DownloadItem); err != nil {
				asinLog.Warn().Err(err).Msg("failed to persist reorganize complete status")
			}
		}

		if reorganizeCompleteVisibilityDelay > 0 {
			t := time.NewTimer(reorganizeCompleteVisibilityDelay)
			select {
			case <-t.C:
			case <-ctx.Done():
			}
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}

		dm.emit(DownloadEvent{
			ASIN:     item.ASIN,
			BookID:   item.BookID,
			Title:    item.Title,
			Type:     "complete",
			Stage:    "complete",
			Progress: 1.0,
		})
		dm.goFanOutPostOrganize(asinLog, book, enriched, finalPath)
		return
	}

	// If the user has selected MP3 (chapter-split) output, transcode the
	// decrypted m4b into per-chapter mp3 files in a temp staging directory and
	// then organize that directory into the Plex book folder.
	if dm.OutputFormat() == "mp3" {
		chapters := enriched.ChapterMarks()
		meta, coverPath := dm.metadataWithOptionalCover(ctx, item.ASIN, enriched)
		if coverPath != "" {
			defer os.Remove(coverPath)
		}
		if len(chapters) == 0 {
			asinLog.Warn().Msg("mp3 chapter-split requested but no chapter data available; falling back to single-file output")
		} else {
			stageDir := filepath.Join(dm.downloadDir, item.ASIN+".chapters")
			// Clear any leftovers from a previous run; chapter counts can
			// change across re-runs and stale files would otherwise leak
			// into the final book folder.
			if err := os.RemoveAll(stageDir); err != nil && !os.IsNotExist(err) {
				dm.cleanupDownloadFiles(item.ASIN)
				dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("clean chapter staging dir: %w", err))
				return
			}
			if err := os.MkdirAll(stageDir, 0750); err != nil {
				dm.cleanupDownloadFiles(item.ASIN)
				dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("create chapter staging dir: %w", err))
				return
			}

			asinLog.Info().Int("chapters", len(chapters)).Str("stage_dir", stageDir).Msg("splitting into mp3 chapters")
			// Switch the badge to "transcoding" so the UI reflects what we're
			// actually doing (re-encoding audio, not moving files).
			dm.emit(DownloadEvent{ASIN: item.ASIN, BookID: item.BookID, Title: item.Title, Type: "stage", Stage: "transcoding"})
			onChapter := func(done, total int) {
				progress := 0.0
				if total > 0 {
					progress = float64(done) / float64(total)
				}
				dm.emit(DownloadEvent{
					ASIN:     item.ASIN,
					BookID:   item.BookID,
					Title:    item.Title,
					Type:     "progress",
					Stage:    "transcoding",
					Progress: progress,
				})
			}
			if err := dm.ffmpeg.SplitChapters(decryptedPath, stageDir, chapters, "mp3", meta, onChapter); err != nil {
				_ = os.RemoveAll(stageDir)
				dm.cleanupDownloadFiles(item.ASIN)
				if parentCtx.Err() == nil && errors.Is(err, context.Canceled) {
					err = fmt.Errorf("cancelled by user")
				}
				dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("split chapters: %w", err))
				return
			}

			// Rough total for progress: sum the staged file sizes.
			var totalBytes int64
			if entries, err := os.ReadDir(stageDir); err == nil {
				for _, e := range entries {
					if e.IsDir() {
						continue
					}
					if fi, err := e.Info(); err == nil {
						totalBytes += fi.Size()
					}
				}
			}

			moveStart := time.Now()
			var lastEmit time.Time
			onMoveProgress := func(moved, total int64) {
				now := time.Now()
				if now.Sub(lastEmit) < 300*time.Millisecond && moved < total {
					return
				}
				lastEmit = now

				if total <= 0 {
					total = totalBytes
				}
				progress := 0.0
				if total > 0 {
					progress = float64(moved) / float64(total)
					if progress > 1 {
						progress = 1
					}
				}
				speed := 0.0
				if elapsed := now.Sub(moveStart).Seconds(); elapsed > 0 {
					speed = float64(moved) / elapsed
				}
				dm.emit(DownloadEvent{
					ASIN:         item.ASIN,
					BookID:       item.BookID,
					Title:        item.Title,
					Type:         "progress",
					Stage:        "moving",
					Progress:     progress,
					BytesWritten: moved,
					TotalBytes:   total,
					Speed:        speed,
				})
			}

			onMoveProgress(0, totalBytes)
			onStage := func(stage string) {
				if stage == "finalizing" {
					dm.emit(DownloadEvent{
						ASIN:   item.ASIN,
						BookID: item.BookID,
						Title:  item.Title,
						Type:   "stage",
						Stage:  "finalizing",
					})
				}
			}
			finalPath, err := dm.organizer.OrganizeMultiFile(ctx, book, enriched, stageDir, onMoveProgress, onStage)
			if err != nil {
				_ = os.RemoveAll(stageDir)
				dm.cleanupDownloadFiles(item.ASIN)
				if parentCtx.Err() == nil && errors.Is(err, context.Canceled) {
					err = fmt.Errorf("cancelled by user")
				}
				dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("organize: %w", err))
				return
			}

			// Best-effort: drop the staging dir and the now-orphan decrypted m4b.
			_ = os.RemoveAll(stageDir)
			dm.cleanupDownloadFiles(item.ASIN)

			asinLog.Info().Str("path", finalPath).Msg("pipeline complete (chapter-split)")
			now := time.Now()
			if item.DownloadItem != nil {
				item.DownloadItem.Status = database.DownloadStatusComplete
				item.DownloadItem.Progress = 1.0
				item.DownloadItem.Error = ""
				item.DownloadItem.CompletedAt = &now
				if err := dm.db.UpdateDownload(context.WithoutCancel(parentCtx), item.DownloadItem); err != nil {
					asinLog.Warn().Err(err).Msg("failed to persist chapter-split complete status")
				}
			}
			dm.emit(DownloadEvent{
				ASIN:     item.ASIN,
				BookID:   item.BookID,
				Title:    item.Title,
				Type:     "complete",
				Stage:    "complete",
				Progress: 1.0,
			})
			dm.goFanOutPostOrganize(asinLog, book, enriched, finalPath)
			return
		}
	}

	totalBytes := int64(0)
	if st, err := os.Stat(decryptedPath); err == nil {
		totalBytes = st.Size()
	}

	moveStart := time.Now()
	var lastEmit time.Time
	onMoveProgress := func(moved, total int64) {
		now := time.Now()
		if now.Sub(lastEmit) < 300*time.Millisecond && moved < total {
			return
		}
		lastEmit = now

		if total <= 0 {
			total = totalBytes
		}

		progress := 0.0
		if total > 0 {
			progress = float64(moved) / float64(total)
			if progress > 1 {
				progress = 1
			}
		}

		speed := 0.0
		elapsed := now.Sub(moveStart).Seconds()
		if elapsed > 0 {
			speed = float64(moved) / elapsed
		}

		dm.emit(DownloadEvent{
			ASIN:         item.ASIN,
			BookID:       item.BookID,
			Title:        item.Title,
			Type:         "progress",
			Stage:        "moving",
			Progress:     progress,
			BytesWritten: moved,
			TotalBytes:   total,
			Speed:        speed,
		})
	}

	onMoveProgress(0, totalBytes)
	onStage := func(stage string) {
		if stage == "finalizing" {
			dm.emit(DownloadEvent{
				ASIN:   item.ASIN,
				BookID: item.BookID,
				Title:  item.Title,
				Type:   "stage",
				Stage:  "finalizing",
			})
		}
	}
	finalPath, err := dm.organizer.OrganizeWithProgress(ctx, book, enriched, decryptedPath, onMoveProgress, onStage)
	if err != nil {
		asinLog.Error().Err(err).Msg("organization failed")
		dm.cleanupDownloadFiles(item.ASIN)
		if parentCtx.Err() == nil && errors.Is(err, context.Canceled) {
			err = fmt.Errorf("cancelled by user")
		}
		dm.failItem(parentCtx, item.DownloadItem, item.Title, fmt.Errorf("organize: %w", err))
		return
	}

	// Drop a folder-level `folder.jpg` next to the audiobook so media servers
	// that prefer a sidecar cover (Emby looks for folder.jpg before falling
	// back to embedded artwork; Plex also accepts it) don't have to extract
	// from the m4b. Best-effort: failure here doesn't fail the pipeline.
	tempCover := filepath.Join(dm.downloadDir, item.ASIN+".cover.jpg")
	if _, statErr := os.Stat(tempCover); statErr == nil {
		folderCover := filepath.Join(filepath.Dir(finalPath), "folder.jpg")
		if err := copyFileSimple(tempCover, folderCover); err != nil {
			asinLog.Debug().Err(err).Str("dest", folderCover).Msg("folder cover write skipped")
		}
	}

	// Clean up intermediate files
	dm.cleanupDownloadFiles(item.ASIN)

	// Mark queue item complete before destination fan-out so the UI leaves
	// finalizing immediately even if a destination scan is slow.
	now := time.Now()
	if item.DownloadItem != nil {
		item.DownloadItem.Status = database.DownloadStatusComplete
		item.DownloadItem.Progress = 1.0
		item.DownloadItem.Error = ""
		item.DownloadItem.CompletedAt = &now
		// Detach: the work is done, the row must commit even if the
		// worker context is being torn down at shutdown.
		if err := dm.db.UpdateDownload(context.WithoutCancel(parentCtx), item.DownloadItem); err != nil {
			asinLog.Warn().Err(err).Msg("failed to persist pipeline complete status")
		}
	}

	asinLog.Info().Str("path", finalPath).Msg("pipeline complete")

	dm.emit(DownloadEvent{
		ASIN:     item.ASIN,
		BookID:   item.BookID,
		Title:    item.Title,
		Type:     "complete",
		Stage:    "complete",
		Progress: 1.0,
	})
	dm.goFanOutPostOrganize(asinLog, book, enriched, finalPath)
}

// goFanOutPostOrganize launches fanOutPostOrganize as a tracked goroutine so
// StopAndWait can join it before the process exits. Use this instead of a bare
// "go fanOutPostOrganize(...)" at all goroutine call sites. Synchronous callers
// in convert.go call fanOutPostOrganize directly and must NOT use this wrapper.
func (dm *DownloadManager) goFanOutPostOrganize(asinLog *logging.Logger, book *database.Book, enriched *audnexus.EnrichedBook, finalPath string) {
	dm.fanOutWg.Add(1)
	go func() {
		defer dm.fanOutWg.Done()
		dm.fanOutPostOrganize(context.Background(), asinLog, book, enriched, finalPath)
	}()
}

// fanOutPostOrganize runs OnBookOrganized across every enabled destination
// (multi-dest path) or falls back to the legacy single mediaServer when
// destinations isn't wired or returned zero results. Shared by the standard
// download path, the chapter-split path, and the convert m4b<->mp3 paths.
// When launched as a goroutine, callers should use goFanOutPostOrganize
// so that StopAndWait can track and join it.
func (dm *DownloadManager) fanOutPostOrganize(ctx context.Context, asinLog *logging.Logger, book *database.Book, enriched *audnexus.EnrichedBook, finalPath string) {
	organizedBook := mediaserver.OrganizedBook{
		BookID:      book.ID,
		ASIN:        book.ASIN,
		Title:       book.Title,
		Author:      book.Author,
		LocalPath:   finalPath,
		OrganizedAt: time.Now(),
	}
	if enriched != nil {
		organizedBook.Title = enriched.Title()
		organizedBook.Author = enriched.Author()
		organizedBook.Series = enriched.Series()
		organizedBook.SeriesPosition = enriched.SeriesPosition()
	}
	coverCandidate := filepath.Join(filepath.Dir(finalPath), "folder.jpg")
	if _, statErr := os.Stat(coverCandidate); statErr == nil {
		organizedBook.CoverPath = coverCandidate
	}

	ranFanOut := false
	if dm.destinations != nil {
		results := dm.destinations.FanOut(ctx, organizedBook)
		for _, r := range results {
			logBookOutcomes(asinLog, "destination:"+r.Destination.DisplayName, book.ID, r.Outcomes)
		}
		ranFanOut = len(results) > 0
	}
	if !ranFanOut && dm.mediaServer != nil {
		outcomes := dm.mediaServer.OnBookOrganized(ctx, organizedBook)
		logBookOutcomes(asinLog, "legacy:"+dm.mediaServer.Name(), book.ID, outcomes)
	}
}

// logBookOutcomes emits a structured log line per backend outcome. Counts
// per-status give an at-a-glance picture; per-failure detail aids debugging.
func logBookOutcomes(asinLog *logging.Logger, backend string, bookID int64, outcomes []mediaserver.Outcome) {
	if len(outcomes) == 0 {
		return
	}
	var ok, failed, skipped, deferred, unsupported int
	for _, o := range outcomes {
		switch o.Status {
		case mediaserver.OutcomeSucceeded:
			ok++
		case mediaserver.OutcomeFailed:
			failed++
			asinLog.Warn().
				Str("backend", backend).
				Int64("book_id", bookID).
				Str("op", o.Operation).
				Err(o.Err).
				Str("detail", o.Detail).
				Int64("duration_ms", o.DurationMs).
				Msg("media-server op failed")
		case mediaserver.OutcomeSkippedExisting, mediaserver.OutcomeSkippedNotConfigured:
			skipped++
		case mediaserver.OutcomeDeferred:
			deferred++
		case mediaserver.OutcomeUnsupported:
			unsupported++
		}
	}
	asinLog.Info().
		Str("backend", backend).
		Int64("book_id", bookID).
		Int("ok", ok).
		Int("failed", failed).
		Int("skipped", skipped).
		Int("deferred", deferred).
		Int("unsupported", unsupported).
		Msg("media-server post-organize outcomes")
}


func (dm *DownloadManager) startMetadataPrefetch(ctx context.Context, item *pipelineItem) {
	if item.EnrichDone != nil {
		return
	}

	item.EnrichDone = make(chan struct{})
	go func() {
		defer close(item.EnrichDone)

		book := item.Book
		if book == nil {
			var err error
			book, err = dm.db.GetBook(ctx, item.BookID)
			if err != nil {
				item.BookErr = err
				return
			}
			item.Book = book
		}

		enriched, err := dm.audnexus.EnrichMetadata(ctx, book)
		if err != nil {
			item.EnrichErr = err
			item.Enriched = &audnexus.EnrichedBook{Book: book}
			return
		}
		item.Enriched = enriched
	}()
}

