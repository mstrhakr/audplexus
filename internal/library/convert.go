package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/logging"
)

// ErrConvertInProgress is returned when a convert is already running for
// the same book; the caller should retry once the in-flight conversion
// finishes rather than racing on shared staging directories.
var ErrConvertInProgress = errors.New("convert already in progress for this book")

// ConvertBook converts an already-organized book between single-file m4b and
// chapter-split mp3 layouts. The book's on-disk files are replaced in place
// and its database record is updated to point at the new layout.
//
// If the book is already in targetFormat the call is a no-op (success).
// Concurrent ConvertBook calls for the same book return ErrConvertInProgress.
func (dm *DownloadManager) ConvertBook(ctx context.Context, bookID int64, targetFormat string) error {
	targetFormat = strings.ToLower(strings.TrimSpace(targetFormat))
	if targetFormat != "m4b" && targetFormat != "mp3" {
		return fmt.Errorf("invalid target format %q (must be m4b or mp3)", targetFormat)
	}

	book, err := dm.db.GetBook(ctx, bookID)
	if err != nil {
		return fmt.Errorf("load book: %w", err)
	}
	if book == nil {
		return fmt.Errorf("book not found")
	}
	if book.FilePath == "" {
		return fmt.Errorf("book has no file on disk")
	}

	// Per-ASIN lock: reject (don't queue) duplicate requests so two clicks
	// can't race on the same staging directories.
	dm.convertMu.Lock()
	if _, busy := dm.convertingASINs[book.ASIN]; busy {
		dm.convertMu.Unlock()
		return ErrConvertInProgress
	}
	dm.convertingASINs[book.ASIN] = struct{}{}
	dm.convertMu.Unlock()
	defer func() {
		dm.convertMu.Lock()
		delete(dm.convertingASINs, book.ASIN)
		dm.convertMu.Unlock()
	}()

	asinLog := dlLog.WithField("asin", book.ASIN)

	// Detect current layout: if FilePath is a directory we treat it as a
	// chapter-split layout; if it's a file we use its extension.
	fi, err := os.Stat(book.FilePath)
	if err != nil {
		return fmt.Errorf("stat book path: %w", err)
	}

	currentFormat := "m4b"
	bookDir := book.FilePath
	if fi.IsDir() {
		currentFormat = "mp3"
	} else {
		bookDir = filepath.Dir(book.FilePath)
		if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(book.FilePath), ".")); ext == "mp3" {
			currentFormat = "mp3"
		}
	}

	if currentFormat == targetFormat {
		asinLog.Info().Str("format", targetFormat).Msg("convert no-op: book is already in target format")
		return nil
	}

	// Refresh metadata so we have chapter marks (m4b → mp3) or canonical
	// titles (mp3 → m4b) without trusting the cached DB row alone.
	enriched, enrichErr := dm.audnexus.EnrichMetadata(ctx, book)
	if enrichErr != nil {
		asinLog.Warn().Err(enrichErr).Msg("audnexus enrichment failed during convert; using db metadata only")
		enriched = &audnexus.EnrichedBook{Book: book}
	}

	dm.emit(DownloadEvent{ASIN: book.ASIN, BookID: book.ID, Title: book.Title, Type: "stage", Stage: "converting"})

	switch targetFormat {
	case "mp3":
		return dm.convertM4BToMP3(ctx, book, enriched, bookDir, asinLog)
	case "m4b":
		return dm.convertMP3ToM4B(ctx, book, enriched, bookDir, asinLog)
	}
	return nil
}

// moveFileCrossFS moves src to dst, falling back to copy+delete if Rename
// returns an EXDEV (cross-device) error. PlexOrganizer uses the same pattern
// for its primary move; we replicate it here so convert works when downloads
// and library are on different filesystems.
func moveFileCrossFS(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDeviceErr(err) {
		return err
	}

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
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// isCrossDeviceErr reports whether err is the "invalid cross-device link"
// (EXDEV) error returned by os.Rename when src and dst live on different
// filesystems. We string-match because the syscall errno isn't exposed
// portably across Linux/Windows.
func isCrossDeviceErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "cross-device") || strings.Contains(msg, "different drive") || strings.Contains(msg, "EXDEV")
}

func (dm *DownloadManager) convertM4BToMP3(ctx context.Context, book *database.Book, enriched *audnexus.EnrichedBook, bookDir string, asinLog *logging.Logger) error {
	chapters := enriched.ChapterMarks()
	if len(chapters) == 0 {
		return fmt.Errorf("no chapter data available for ASIN %s; cannot split into mp3", book.ASIN)
	}

	srcM4B := book.FilePath

	// Stage chapter mp3s alongside the source so a failure leaves the original
	// m4b untouched until we've fully built the replacement set.
	stageDir := filepath.Join(dm.downloadDir, book.ASIN+".convert-mp3")
	if err := os.RemoveAll(stageDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clean stage dir: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0750); err != nil {
		return fmt.Errorf("create stage dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	asinLog.Info().Int("chapters", len(chapters)).Str("stage_dir", stageDir).Msg("convert: splitting m4b into mp3 chapters")
	if err := dm.ffmpeg.SplitChapters(srcM4B, stageDir, chapters, "mp3"); err != nil {
		return fmt.Errorf("split chapters: %w", err)
	}

	// Move staged mp3 files into the existing book directory; on success
	// remove the source m4b and any sibling .chapters.txt that named it.
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return fmt.Errorf("read stage dir: %w", err)
	}

	moved := make([]string, 0, len(entries))
	var totalBytes int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(stageDir, e.Name())
		dst := filepath.Join(bookDir, e.Name())
		if err := moveFileCrossFS(src, dst); err != nil {
			// Roll back any files already moved into the book dir.
			for _, m := range moved {
				_ = os.Remove(m)
			}
			return fmt.Errorf("move chapter %q: %w", e.Name(), err)
		}
		moved = append(moved, dst)
		if fi, err := os.Stat(dst); err == nil {
			totalBytes += fi.Size()
		}
	}

	if err := os.Remove(srcM4B); err != nil && !os.IsNotExist(err) {
		asinLog.Warn().Err(err).Str("path", srcM4B).Msg("failed to remove original m4b after convert")
	}

	book.FilePath = bookDir
	book.FileSize = totalBytes
	book.Status = database.BookStatusComplete
	if err := dm.db.UpsertBook(ctx, book); err != nil {
		return fmt.Errorf("update book record: %w", err)
	}

	asinLog.Info().Str("path", bookDir).Int("chapters", len(moved)).Msg("convert m4b→mp3 complete")
	if dm.mediaServer != nil {
		dm.mediaServer.TriggerScanForBook(bookDir)
		dm.mediaServer.EnsureBookInSeriesCollection(enriched.Series(), enriched.Title())
	}
	dm.emit(DownloadEvent{
		ASIN:     book.ASIN,
		BookID:   book.ID,
		Title:    book.Title,
		Type:     "complete",
		Stage:    "complete",
		Progress: 1.0,
	})
	return nil
}

func (dm *DownloadManager) convertMP3ToM4B(ctx context.Context, book *database.Book, enriched *audnexus.EnrichedBook, bookDir string, asinLog *logging.Logger) error {
	entries, err := os.ReadDir(bookDir)
	if err != nil {
		return fmt.Errorf("read book dir: %w", err)
	}

	mp3Files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			mp3Files = append(mp3Files, filepath.Join(bookDir, e.Name()))
		}
	}
	if len(mp3Files) == 0 {
		return fmt.Errorf("no mp3 files found in %s", bookDir)
	}
	// Filenames are zero-padded by the splitter so a lexical sort matches
	// playback order (e.g. "01 - ...", "02 - ...").
	sort.Strings(mp3Files)

	// Stage the concat output in the download dir so a failure leaves the
	// existing mp3 layout intact.
	stagePath := filepath.Join(dm.downloadDir, book.ASIN+".convert.m4b")
	defer os.Remove(stagePath)

	asinLog.Info().Int("inputs", len(mp3Files)).Str("output", stagePath).Msg("convert: concatenating mp3 chapters into m4b")
	if err := dm.ffmpeg.ConcatToM4B(mp3Files, stagePath, "128k"); err != nil {
		return fmt.Errorf("concat to m4b: %w", err)
	}

	// Reuse the organizer's filename builder via Organize so the resulting
	// file lands with the canonical Plex naming. We hand it the staged m4b
	// and an empty-but-correct book record; it will move/rename in place.
	finalPath, err := dm.organizer.Organize(ctx, book, enriched, stagePath)
	if err != nil {
		return fmt.Errorf("organize converted m4b: %w", err)
	}

	// Remove the per-chapter mp3 files now that the m4b is in place.
	for _, p := range mp3Files {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			asinLog.Warn().Err(err).Str("path", p).Msg("failed to remove chapter mp3 after convert")
		}
	}

	asinLog.Info().Str("path", finalPath).Msg("convert mp3→m4b complete")
	if dm.mediaServer != nil {
		dm.mediaServer.TriggerScanForBook(finalPath)
		dm.mediaServer.EnsureBookInSeriesCollection(enriched.Series(), enriched.Title())
	}
	dm.emit(DownloadEvent{
		ASIN:     book.ASIN,
		BookID:   book.ID,
		Title:    book.Title,
		Type:     "complete",
		Stage:    "complete",
		Progress: 1.0,
	})

	return nil
}
