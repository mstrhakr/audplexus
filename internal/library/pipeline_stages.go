package library

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/logging"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
)

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

	// Start metadata lookup immediately so download starts with metadata fetch.
	dm.startMetadataPrefetch(ctx, pipeItem)

	// Mark download as active
	now := time.Now()
	item.Status = database.DownloadStatusActive
	item.StartedAt = &now
	_ = dm.db.UpdateDownload(ctx, item)
	_ = dm.db.UpdateBookStatus(ctx, item.BookID, database.BookStatusDownloading)

	dm.emit(DownloadEvent{ASIN: item.ASIN, BookID: item.BookID, Title: bookTitle, Type: "started", Stage: "downloading"})

	var lastEmit time.Time
	var lastDBWrite time.Time
	var lastLogPct int
	downloadStart := time.Now()

	writer := &fileDownloadWriter{
		asin:        item.ASIN,
		downloadDir: dm.downloadDir,
		onProgress: func(written, total int64) {
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
				_ = dm.db.UpdateDownload(ctx, item)
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

	bytesWritten, err := dm.client.DownloadBook(ctx, item.ASIN, writer)
	if err != nil {
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
	_ = dm.db.UpdateDownload(ctx, item)
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
func (dm *DownloadManager) handleDecryptStage(ctx context.Context, item *pipelineItem) {
	asinLog := dlLog.WithField("asin", item.ASIN)
	asinLog.Info().Msg("starting decrypt stage")

	_ = dm.db.UpdateBookStatus(ctx, item.BookID, database.BookStatusDecrypting)
	dm.emit(DownloadEvent{ASIN: item.ASIN, BookID: item.BookID, Title: item.Title, Type: "stage", Stage: "decrypting"})

	if item.EnrichDone != nil {
		select {
		case <-item.EnrichDone:
		case <-ctx.Done():
			asinLog.Warn().Msg("context cancelled while waiting for metadata prefetch")
			return
		}
	}

	if item.BookErr != nil {
		dm.failItem(ctx, item.DownloadItem, item.Title, fmt.Errorf("load book: %w", item.BookErr))
		return
	}

	if item.Book == nil {
		book, err := dm.db.GetBook(ctx, item.BookID)
		if err != nil {
			dm.failItem(ctx, item.DownloadItem, item.Title, fmt.Errorf("load book: %w", err))
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
		asinLog.Error().Err(err).Msg("decryption failed")
		dm.cleanupDownloadFiles(item.ASIN)
		dm.failItem(ctx, item.DownloadItem, item.Title, fmt.Errorf("decrypt: %w", err))
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
func (dm *DownloadManager) handleProcessStage(ctx context.Context, item *pipelineItem) {
	asinLog := dlLog.WithField("asin", item.ASIN)
	asinLog.Info().Msg("starting move stage")

	_ = dm.db.UpdateBookStatus(ctx, item.BookID, database.BookStatusProcessing)
	dm.emit(DownloadEvent{ASIN: item.ASIN, BookID: item.BookID, Title: item.Title, Type: "stage", Stage: "moving"})

	// Ensure canonical book/metadata are available before move.
	book := item.Book
	if book == nil {
		var err error
		book, err = dm.db.GetBook(ctx, item.BookID)
		if err != nil {
			asinLog.Error().Err(err).Msg("failed to load book record")
			dm.failItem(ctx, item.DownloadItem, item.Title, fmt.Errorf("load book: %w", err))
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
	finalPath, err := dm.organizer.OrganizeWithProgress(ctx, book, enriched, decryptedPath, onMoveProgress)
	if err != nil {
		asinLog.Error().Err(err).Msg("organization failed")
		dm.cleanupDownloadFiles(item.ASIN)
		dm.failItem(ctx, item.DownloadItem, item.Title, fmt.Errorf("organize: %w", err))
		return
	}
	onMoveProgress(totalBytes, totalBytes)

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

	// Run media-server post-organize work BEFORE marking the download complete.
	// Multi-destination fan-out: every enabled library_destinations row has its
	// backend's OnBookOrganized invoked concurrently (bounded). Per-destination
	// outcomes are recorded in book_library_destinations by the manager.
	// Falls back to single-backend mediaServer for installs that haven't run
	// the first-boot synthesis yet (no destinations enabled).
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
	// Cover sidecar path for backends that prefer a sidecar (Emby/Jellyfin).
	coverCandidate := filepath.Join(filepath.Dir(finalPath), "folder.jpg")
	if _, statErr := os.Stat(coverCandidate); statErr == nil {
		organizedBook.CoverPath = coverCandidate
	}

	// Multi-destination fan-out is the modern path. Legacy single-backend
	// path only fires when EITHER destinations isn't wired (very early
	// boot) OR fan-out returned zero results (no enabled rows AND legacy
	// config is present — codex flagged this gap; without the fallback,
	// existing Plex installs that never set media_server_type lose their
	// post-download scan/collection work after upgrade).
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

	// Mark queue item complete only after the entire pipeline (including
	// media-server post-organize work) has run.
	now := time.Now()
	if item.DownloadItem != nil {
		item.DownloadItem.Status = database.DownloadStatusComplete
		item.DownloadItem.Progress = 1.0
		item.DownloadItem.Error = ""
		item.DownloadItem.CompletedAt = &now
		_ = dm.db.UpdateDownload(ctx, item.DownloadItem)
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

