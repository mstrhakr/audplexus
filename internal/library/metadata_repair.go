package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mstrhakr/audplexus/internal/audio"
	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/logging"
)

var repairLog = logging.Component("metadata_repair")

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
	startTime := time.Now()
	repairLog.Info().Msg("metadata repair pass starting")

	completeStatus := database.BookStatusComplete
	books, _, err := db.ListBooks(ctx, database.BookFilter{Status: &completeStatus})
	if err != nil {
		return 0, fmt.Errorf("list complete books: %w", err)
	}
	total := len(books)
	repairLog.Info().Int("total_books", total).Msg("loaded complete books from database")

	repaired := 0
	skipped := 0
	failed := 0
	probeTimeMs := int64(0)
	retagTimeMs := int64(0)

	for i := range books {
		select {
		case <-ctx.Done():
			repairLog.Warn().Int("processed", i).Int("repaired", repaired).Msg("metadata repair cancelled")
			return repaired, ctx.Err()
		default:
		}
		if onProgress != nil {
			onProgress(i, total)
		}

		// Log progress every 50 books
		if i > 0 && i%50 == 0 {
			elapsed := time.Since(startTime)
			avgMs := elapsed.Milliseconds() / int64(i)
			eta := time.Duration((int64(total)-int64(i))*avgMs) * time.Millisecond
			repairLog.Info().
				Int("processed", i).
				Int("total", total).
				Int("repaired", repaired).
				Int("failed", failed).
				Int("elapsed_sec", int(elapsed.Seconds())).
				Int("eta_sec", int(eta.Seconds())).
				Msg("metadata repair batch progress")
		}

		book := &books[i]
		if book.FilePath == "" {
			skipped++
			continue
		}
		if _, statErr := os.Stat(book.FilePath); statErr != nil {
			skipped++
			repairLog.Debug().Str("asin", book.ASIN).Err(statErr).Msg("metadata repair: file not found")
			continue
		}

		// Probe tags
		probeStart := time.Now()
		tags, err := ff.ProbeTags(ctx, book.FilePath)
		probeMs := time.Since(probeStart).Milliseconds()
		probeTimeMs += probeMs

		if err != nil {
			repairLog.Warn().Err(err).Str("path", book.FilePath).Int("probe_ms", int(probeMs)).Msg("metadata repair: probe failed")
			failed++
			continue
		}

		if !metadataNeedsRepair(book, tags) {
			repairLog.Trace().Str("asin", book.ASIN).Int("probe_ms", int(probeMs)).Msg("metadata repair: file has correct asin tag, skipping")
			continue
		}

		// Retag file
		retagStart := time.Now()
		if err := retagBookFile(ctx, ff, an, book); err != nil {
			retagMs := time.Since(retagStart).Milliseconds()
			retagTimeMs += retagMs
			repairLog.Warn().Err(err).Str("path", book.FilePath).Str("asin", book.ASIN).Int("retag_ms", int(retagMs)).Msg("metadata repair: retag failed")
			failed++
			continue
		}
		retagMs := time.Since(retagStart).Milliseconds()
		retagTimeMs += retagMs

		repaired++
		repairLog.Info().Str("asin", book.ASIN).Str("title", book.Title).Int("probe_ms", int(probeMs)).Int("retag_ms", int(retagMs)).Msg("metadata repair: file re-tagged")
	}

	if onProgress != nil {
		onProgress(total, total)
	}

	totalElapsed := time.Since(startTime)
	repairLog.Info().
		Int("books_complete", total).
		Int("repaired", repaired).
		Int("skipped", skipped).
		Int("failed", failed).
		Int("probe_total_ms", int(probeTimeMs)).
		Int("retag_total_ms", int(retagTimeMs)).
		Int("elapsed_ms", int(totalElapsed.Milliseconds())).
		Int("elapsed_sec", int(totalElapsed.Seconds())).
		Msg("metadata repair: pass complete")

	if repaired > 0 || failed > 0 {
		avgProbeMs := probeTimeMs / int64(total)
		avgRetagMs := int64(0)
		if repaired > 0 {
			avgRetagMs = retagTimeMs / int64(repaired)
		}
		repairLog.Info().
			Int("avg_probe_ms", int(avgProbeMs)).
			Int("avg_retag_ms", int(avgRetagMs)).
			Msg("metadata repair: performance summary")
	}

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
	// Build metadata (may involve audnexus enrichment)
	buildStart := time.Now()
	meta, err := buildRepairMetadata(ctx, an, book)
	if err != nil {
		repairLog.Warn().Err(err).Str("asin", book.ASIN).Msg("metadata_repair: failed to build metadata")
		return err
	}
	buildMs := time.Since(buildStart).Milliseconds()

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

	// Embed metadata into temp file
	embedStart := time.Now()
	if err := ff.EmbedMetadata(book.FilePath, tmpPath, meta); err != nil {
		embedMs := time.Since(embedStart).Milliseconds()
		repairLog.Warn().Err(err).Str("asin", book.ASIN).Int("build_ms", int(buildMs)).Int("embed_ms", int(embedMs)).Msg("metadata_repair: embed failed")
		return err
	}
	embedMs := time.Since(embedStart).Milliseconds()

	if err := os.Chmod(tmpPath, origInfo.Mode().Perm()); err != nil {
		return err
	}

	replaceStart := time.Now()
	if err := os.Rename(tmpPath, book.FilePath); err != nil {
		replaceMs := time.Since(replaceStart).Milliseconds()
		repairLog.Warn().Err(err).Str("asin", book.ASIN).Int("replace_ms", int(replaceMs)).Msg("metadata_repair: replace failed")
		return err
	}
	replaceMs := time.Since(replaceStart).Milliseconds()

	cleanup = false

	repairLog.Debug().
		Str("asin", book.ASIN).
		Int("build_ms", int(buildMs)).
		Int("embed_ms", int(embedMs)).
		Int("replace_ms", int(replaceMs)).
		Int("total_ms", int(buildMs+embedMs+replaceMs)).
		Msg("metadata_repair: retag operation breakdown")

	return nil
}

// buildRepairMetadata constructs the AudiobookRich metadata set for a
// book. Uses the audnexus enrichment path when available so the
// repaired file ends up with the same rich tag set the initial
// download would have produced; falls back to the DB record alone
// when audnexus is unavailable or the network lookup fails.
func buildRepairMetadata(ctx context.Context, an *audnexus.Client, book *database.Book) (audio.Metadata, error) {
	if an != nil {
		enrichStart := time.Now()
		enriched, err := an.EnrichMetadata(ctx, book)
		enrichMs := time.Since(enrichStart).Milliseconds()

		if err != nil {
			repairLog.Debug().Err(err).Str("asin", book.ASIN).Int("enrichment_ms", int(enrichMs)).Msg("metadata_repair: audnexus enrichment failed, falling back to db record")
		} else if enriched != nil {
			repairLog.Debug().Str("asin", book.ASIN).Int("enrichment_ms", int(enrichMs)).Msg("metadata_repair: audnexus enrichment succeeded")
			meta := enriched.ToAudioMetadata()
			meta.Profile = audio.TagProfileAudiobookRich
			return meta, nil
		}
	}

	repairLog.Debug().Str("asin", book.ASIN).Msg("metadata_repair: using database record for metadata")

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
