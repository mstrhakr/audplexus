package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mstrhakr/audplexus/internal/audio"
	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
)

// RepairBookMetadata scans every complete book and re-embeds metadata
// when the file's ASIN tag is missing or doesn't match the DB record.
// Forces TagProfileAudiobookRich so the asin/series/series-part iTunes
// atoms are written regardless of the user's tag profile setting —
// this phase exists specifically to make books matchable by
// ASIN-aware servers like Audiobookshelf.
//
// When an audnexus client is supplied, each repaired book is enriched
// before re-tagging so genre/copyright/description match the original
// download. With a nil audnexus client the repair falls back to the
// DB record alone — sufficient for the ASIN-match use case.
//
// Returns the number of files re-tagged.
func RepairBookMetadata(ctx context.Context, db database.Database, ff *audio.FFmpeg, an *audnexus.Client, onProgress func(processed, total int)) (int, error) {
	if ff == nil {
		return 0, fmt.Errorf("ffmpeg not available")
	}
	completeStatus := database.BookStatusComplete
	books, _, err := db.ListBooks(ctx, database.BookFilter{Status: &completeStatus})
	if err != nil {
		return 0, fmt.Errorf("list complete books: %w", err)
	}
	total := len(books)
	repaired := 0
	skipped := 0
	failed := 0
	for i := range books {
		select {
		case <-ctx.Done():
			return repaired, ctx.Err()
		default:
		}
		if onProgress != nil {
			onProgress(i, total)
		}
		book := &books[i]
		if book.FilePath == "" {
			skipped++
			continue
		}
		if _, statErr := os.Stat(book.FilePath); statErr != nil {
			skipped++
			continue
		}
		tags, err := ff.ProbeTags(ctx, book.FilePath)
		if err != nil {
			syncLog.Warn().Err(err).Str("path", book.FilePath).Msg("metadata_repair: probe failed")
			failed++
			continue
		}
		if !metadataNeedsRepair(book, tags) {
			continue
		}
		if err := retagBookFile(ctx, ff, an, book); err != nil {
			syncLog.Warn().Err(err).Str("path", book.FilePath).Str("asin", book.ASIN).Msg("metadata_repair: retag failed")
			failed++
			continue
		}
		repaired++
		syncLog.Info().Str("asin", book.ASIN).Str("title", book.Title).Msg("metadata_repair: re-tagged file")
	}
	if onProgress != nil {
		onProgress(total, total)
	}
	syncLog.Info().
		Int("books_complete", total).
		Int("repaired", repaired).
		Int("skipped_missing_file", skipped).
		Int("failed", failed).
		Msg("metadata_repair: pass complete")
	return repaired, nil
}

// metadataNeedsRepair returns true when the file's ASIN tag doesn't
// match the DB record. Triggers a rich re-tag rather than touching
// only the ASIN atom, so series/series-part land at the same time.
func metadataNeedsRepair(book *database.Book, tags map[string]string) bool {
	return strings.TrimSpace(tags["asin"]) != book.ASIN
}

// retagBookFile re-runs EmbedMetadata over the existing file with the
// AudiobookRich profile. Writes to a sibling temp file and atomically
// replaces the original on success so a crashed ffmpeg leaves the
// original file intact.
func retagBookFile(ctx context.Context, ff *audio.FFmpeg, an *audnexus.Client, book *database.Book) error {
	meta, err := buildRepairMetadata(ctx, an, book)
	if err != nil {
		return err
	}
	origInfo, err := os.Stat(book.FilePath)
	if err != nil {
		return err
	}

	dir := filepath.Dir(book.FilePath)
	ext := filepath.Ext(book.FilePath)
	tmp, err := os.CreateTemp(dir, ".audplexus-retag-*"+ext)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	// Remove the placeholder so ffmpeg creates a fresh output file
	// instead of truncating the 0600 CreateTemp file.
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	if err := ff.EmbedMetadata(book.FilePath, tmpPath, meta); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, origInfo.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, book.FilePath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// buildRepairMetadata constructs the AudiobookRich metadata set for a
// book. Uses the audnexus enrichment path when available so the
// repaired file ends up with the same rich tag set the initial
// download would have produced; falls back to the DB record alone
// when audnexus is unavailable or the network lookup fails.
func buildRepairMetadata(ctx context.Context, an *audnexus.Client, book *database.Book) (audio.Metadata, error) {
	if an != nil {
		enriched, err := an.EnrichMetadata(ctx, book)
		if err == nil && enriched != nil {
			meta := enriched.ToAudioMetadata()
			meta.Profile = audio.TagProfileAudiobookRich
			return meta, nil
		}
	}
	year := ""
	if !book.ReleaseDate.IsZero() {
		year = book.ReleaseDate.Format("2006")
	}
	return audio.Metadata{
		Title:       book.Title,
		Author:      book.Author,
		Narrator:    book.Narrator,
		Publisher:   book.Publisher,
		Language:    book.Language,
		Album:       book.Title,
		AlbumArtist: book.Author,
		Year:        year,
		Comment:     book.Description,
		Track:       book.SeriesPosition,
		Series:      book.Series,
		SeriesPart:  book.SeriesPosition,
		ASIN:        book.ASIN,
		Profile:     audio.TagProfileAudiobookRich,
	}, nil
}
