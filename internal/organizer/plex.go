package organizer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mstrhakr/audplexus/internal/audio"
	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/logging"
)

var orgLog = logging.Component("organizer")

// PlexOrganizer handles organizing audiobook files into Plex-compatible structure.
type PlexOrganizer struct {
	db            database.Database
	ffmpeg        *audio.FFmpeg
	libraryRoot   string
	embedCover    bool
	chapterFile   bool
	plexMatchFile bool
	mu            sync.RWMutex
}

// NewPlexOrganizer creates a new Plex file organizer.
func NewPlexOrganizer(db database.Database, ffmpeg *audio.FFmpeg, libraryRoot string, embedCover, chapterFile, plexMatchFile bool) *PlexOrganizer {
	return &PlexOrganizer{
		db:            db,
		ffmpeg:        ffmpeg,
		libraryRoot:   libraryRoot,
		embedCover:    embedCover,
		chapterFile:   chapterFile,
		plexMatchFile: plexMatchFile,
	}
}

// SetEmbedCover updates the embed cover setting at runtime.
func (o *PlexOrganizer) SetEmbedCover(v bool) {
	o.mu.Lock()
	o.embedCover = v
	o.mu.Unlock()
}

// SetChapterFile updates the chapter file setting at runtime.
func (o *PlexOrganizer) SetChapterFile(v bool) {
	o.mu.Lock()
	o.chapterFile = v
	o.mu.Unlock()
}

// SetPlexMatchFile updates the plexmatch file setting at runtime.
func (o *PlexOrganizer) SetPlexMatchFile(v bool) {
	o.mu.Lock()
	o.plexMatchFile = v
	o.mu.Unlock()
}

// Organize takes a decrypted audiobook file and moves it into the Plex library structure.
// Structure: {libraryRoot}/{Author}/{Title ASIN [region]}/{Title: Subtitle - Series Position ASIN [region]}.m4b
// Region, subtitle, and series are optional.
// Optionally embeds metadata, cover art, and generates a chapters file.
func (o *PlexOrganizer) Organize(ctx context.Context, book *database.Book, enriched *audnexus.EnrichedBook, inputPath string) (string, error) {
	return o.OrganizeWithProgress(ctx, book, enriched, inputPath, nil, nil)
}

// OrganizeWithProgress performs the same work as Organize and reports file move progress.
// The callback receives bytes moved and total bytes.
// onStage is called when the move stage changes (e.g. to "finalizing" during flush);
// callers can use this to update the UI. It's optional (can be nil).
// Files are always saved to the configured libraryRoot. The Plex path (if configured)
// is only used when notifying Plex to scan the library.
func (o *PlexOrganizer) OrganizeWithProgress(ctx context.Context, book *database.Book, enriched *audnexus.EnrichedBook, inputPath string, onMoveProgress func(moved, total int64), onStage func(stage string)) (string, error) {
	_ = ctx
	author := strings.TrimSpace(enriched.Author())
	title := strings.TrimSpace(enriched.Title())
	subtitle := strings.TrimSpace(enriched.Subtitle())
	series := strings.TrimSpace(enriched.Series())
	seriesPosition := strings.TrimSpace(enriched.SeriesPosition())
	asin := strings.TrimSpace(book.ASIN)
	region := strings.TrimSpace(enriched.Region())

	if author == "" {
		author = "Unknown Author"
	}
	if title == "" {
		title = "Unknown Title"
	}
	filenameBase := buildFilenameBase(title, subtitle, series, seriesPosition, asin, region)
	bookDirName := buildBookDirectoryName(title, asin, region)

	// Always use the configured libraryRoot for file placement
	bookDir := filepath.Join(o.libraryRoot, sanitizePath(author), sanitizePath(bookDirName))
	if err := os.MkdirAll(bookDir, 0750); err != nil {
		return "", fmt.Errorf("create book directory: %w", err)
	}

	ext := filepath.Ext(inputPath)
	finalPath := filepath.Join(bookDir, sanitizePath(filenameBase)+ext)

	orgLog.Info().
		Str("asin", book.ASIN).
		Str("title", enriched.Title()).
		Str("author", enriched.Author()).
		Str("dest", finalPath).
		Msg("organizing audiobook")

	totalBytes := int64(0)
	if fi, err := os.Stat(inputPath); err == nil {
		totalBytes = fi.Size()
	}

	// File is already decrypted and tagged earlier in the pipeline.
	if err := os.Rename(inputPath, finalPath); err != nil {
		// Cross-device rename; fall back to copy+delete.
		if err := copyFile(inputPath, finalPath, totalBytes, onMoveProgress, onStage); err != nil {
			return "", fmt.Errorf("move file: %w", err)
		}
		_ = os.Remove(inputPath)
	} else if onMoveProgress != nil {
		onMoveProgress(totalBytes, totalBytes)
	}

	// Generate .plexmatch hint file for perfect Plex library scanning.
	o.mu.RLock()
	wantPlexMatch := o.plexMatchFile
	o.mu.RUnlock()
	if wantPlexMatch {
		if err := writePlexMatchFile(bookDir, enriched, book); err != nil {
			orgLog.Warn().Err(err).Msg("failed to write .plexmatch file")
		} else {
			orgLog.Debug().Str("path", filepath.Join(bookDir, ".plexmatch")).Msg(".plexmatch file written")
		}
	}

	// Generate chapters file
	o.mu.RLock()
	wantChapters := o.chapterFile
	o.mu.RUnlock()
	if wantChapters {
		chapters := enriched.ChapterMarks()
		if len(chapters) > 0 {
			chapterPath := filepath.Join(bookDir, sanitizePath(filenameBase)+".chapters.txt")
			if err := writeChaptersFile(chapterPath, chapters); err != nil {
				orgLog.Warn().Err(err).Msg("failed to write chapters file")
			} else {
				orgLog.Debug().Str("path", chapterPath).Int("chapters", len(chapters)).Msg("chapters file written")
			}
		}
	}

	// Update book in database
	book.FilePath = finalPath
	fi, _ := os.Stat(finalPath)
	if fi != nil {
		book.FileSize = fi.Size()
	}
	book.Status = database.BookStatusComplete
	if err := o.db.UpsertBook(ctx, book); err != nil {
		orgLog.Error().Err(err).Str("asin", book.ASIN).Msg("failed to update book record")
	}

	orgLog.Info().
		Str("asin", book.ASIN).
		Str("path", finalPath).
		Int64("size", book.FileSize).
		Msg("audiobook organized successfully")

	return finalPath, nil
}

// OrganizeMultiFile takes a directory of per-chapter audio files and moves them
// into the Plex book folder, preserving each chapter as its own track. The
// chapter files in srcDir are expected to already be named in playback order
// (e.g. "01 - Prologue.mp3"); their names are kept verbatim inside the book
// folder so Plex orders tracks naturally.
//
// onMoveProgress reports cumulative bytes moved across all chapter files.
// onStage is called when the move stage changes (e.g. to "finalizing" during the last file's flush).
func (o *PlexOrganizer) OrganizeMultiFile(ctx context.Context, book *database.Book, enriched *audnexus.EnrichedBook, srcDir string, onMoveProgress func(moved, total int64), onStage func(stage string)) (string, error) {
	_ = ctx
	author := strings.TrimSpace(enriched.Author())
	title := strings.TrimSpace(enriched.Title())
	subtitle := strings.TrimSpace(enriched.Subtitle())
	series := strings.TrimSpace(enriched.Series())
	seriesPosition := strings.TrimSpace(enriched.SeriesPosition())
	asin := strings.TrimSpace(book.ASIN)
	region := strings.TrimSpace(enriched.Region())

	if author == "" {
		author = "Unknown Author"
	}
	if title == "" {
		title = "Unknown Title"
	}
	filenameBase := buildFilenameBase(title, subtitle, series, seriesPosition, asin, region)
	bookDirName := buildBookDirectoryName(title, asin, region)

	bookDir := filepath.Join(o.libraryRoot, sanitizePath(author), sanitizePath(bookDirName))
	if err := os.MkdirAll(bookDir, 0750); err != nil {
		return "", fmt.Errorf("create book directory: %w", err)
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return "", fmt.Errorf("read chapter source: %w", err)
	}

	// Pre-scan to compute total bytes for progress reporting.
	totalBytes := int64(0)
	chapterFiles := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		chapterFiles = append(chapterFiles, e)
		if fi, statErr := e.Info(); statErr == nil {
			totalBytes += fi.Size()
		}
	}

	orgLog.Info().
		Str("asin", book.ASIN).
		Str("title", enriched.Title()).
		Str("dest", bookDir).
		Int("chapters", len(chapterFiles)).
		Msg("organizing chapter-split audiobook")

	// firstFinal is returned as a representative path for the book record.
	firstFinal := ""
	movedTotal := int64(0)
	for _, e := range chapterFiles {
		src := filepath.Join(srcDir, e.Name())
		dst := filepath.Join(bookDir, e.Name())

		fileSize := int64(0)
		if fi, err := os.Stat(src); err == nil {
			fileSize = fi.Size()
		}

		if err := os.Rename(src, dst); err != nil {
			// Cross-device rename; fall back to copy+delete with sub-file progress.
			// Only pass onStage for the last file so it fires once at the end.
			var stg func(string)
			if e.Name() == chapterFiles[len(chapterFiles)-1].Name() {
				stg = onStage
			}
			subProgress := func(moved, _ int64) {
				if onMoveProgress != nil {
					onMoveProgress(movedTotal+moved, totalBytes)
				}
			}
			if err := copyFile(src, dst, fileSize, subProgress, stg); err != nil {
				return "", fmt.Errorf("move chapter file %q: %w", e.Name(), err)
			}
			_ = os.Remove(src)
		}

		movedTotal += fileSize
		if onMoveProgress != nil {
			onMoveProgress(movedTotal, totalBytes)
		}

		if firstFinal == "" {
			firstFinal = dst
		}
	}

	if firstFinal == "" {
		return "", fmt.Errorf("no chapter files found in %s", srcDir)
	}

	// Generate .plexmatch hint file for perfect Plex library scanning.
	o.mu.RLock()
	wantPlexMatch := o.plexMatchFile
	o.mu.RUnlock()
	if wantPlexMatch {
		if err := writePlexMatchFile(bookDir, enriched, book); err != nil {
			orgLog.Warn().Err(err).Msg("failed to write .plexmatch file")
		}
	}

	// Generate chapters file alongside the chapter tracks for tools that read it.
	o.mu.RLock()
	wantChapters := o.chapterFile
	o.mu.RUnlock()
	if wantChapters {
		chapters := enriched.ChapterMarks()
		if len(chapters) > 0 {
			chapterPath := filepath.Join(bookDir, sanitizePath(filenameBase)+".chapters.txt")
			if err := writeChaptersFile(chapterPath, chapters); err != nil {
				orgLog.Warn().Err(err).Msg("failed to write chapters file")
			}
		}
	}

	// Update book in database. For multi-file output we record the directory as
	// FilePath and the cumulative size of all chapter files.
	book.FilePath = bookDir
	book.FileSize = totalBytes
	book.Status = database.BookStatusComplete
	if err := o.db.UpsertBook(ctx, book); err != nil {
		orgLog.Error().Err(err).Str("asin", book.ASIN).Msg("failed to update book record")
		return "", fmt.Errorf("update book record: %w", err)
	}

	orgLog.Info().
		Str("asin", book.ASIN).
		Str("path", bookDir).
		Int64("size", totalBytes).
		Int("chapters", len(chapterFiles)).
		Msg("audiobook organized successfully (chapter-split)")

	return bookDir, nil
}

// buildFilenameBase builds a Plex-friendly filename with ASIN and optional region for easier scanning.
// Output: "Title: Subtitle - SeriesName SeriesPosition ASIN [regionCode]" (subtitle/series/region are optional).
func buildFilenameBase(title, subtitle, series, seriesPosition, asin, region string) string {
	title = strings.TrimSpace(title)
	subtitle = strings.TrimSpace(subtitle)
	series = strings.TrimSpace(series)
	seriesPosition = strings.TrimSpace(seriesPosition)
	asin = strings.TrimSpace(asin)
	region = strings.TrimSpace(region)

	if title == "" {
		title = "Unknown Title"
	}

	base := title
	if subtitle != "" {
		base = base + ": " + subtitle
	}
	if series != "" {
		base = base + " - " + series
		if seriesPosition != "" {
			base = base + " " + seriesPosition
		}
	}
	if asin != "" {
		base = base + " " + asin
	}
	if region != "" {
		base = base + " [" + region + "]"
	}

	return base
}

// buildBookDirectoryName builds the book folder name with ASIN and optional region.
// Output: "Title ASIN [regionCode]" or just "Title" when ASIN is missing.
func buildBookDirectoryName(title, asin, region string) string {
	title = strings.TrimSpace(title)
	asin = strings.TrimSpace(asin)
	region = strings.TrimSpace(region)

	if title == "" {
		title = "Unknown Title"
	}

	base := title
	if asin != "" {
		base = base + " " + asin
	}
	if region != "" {
		base = base + " [" + region + "]"
	}

	return base
}

// writePlexMatchFile writes a .plexmatch YAML hint file in the book directory.
// Plex reads this file during library scans to match the audiobook directly to
// the correct Audible entry by ASIN, guaranteeing accurate metadata every time.
// Format reference: https://support.plex.tv/articles/plexmatch/
func writePlexMatchFile(bookDir string, enriched *audnexus.EnrichedBook, book *database.Book) error {
	asin := strings.TrimSpace(book.ASIN)
	if asin == "" {
		return nil // no ASIN, nothing useful to write
	}

	f, err := os.Create(filepath.Join(bookDir, ".plexmatch"))
	if err != nil {
		return err
	}
	defer f.Close()

	title := enriched.Title()
	fmt.Fprintf(f, "title: %s\n", plexMatchYAMLValue(title))

	if !book.ReleaseDate.IsZero() && book.ReleaseDate.Year() > 1 {
		fmt.Fprintf(f, "year: %d\n", book.ReleaseDate.Year())
	}

	// hint: audible://<ASIN> tells Plex exactly which Audible entry to use.
	fmt.Fprintf(f, "hint: audible://%s\n", asin)

	return nil
}

// plexMatchYAMLValue returns a safely quoted YAML scalar value.
// Double-quoting is used so that colons, special chars, and Unicode in titles
// are handled correctly without requiring an external YAML library.
func plexMatchYAMLValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// writeChaptersFile writes a Plex-compatible chapters.txt file.
// Format: HH:MM:SS.mmm Chapter Title
func writeChaptersFile(path string, chapters []audio.ChapterMark) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, ch := range chapters {
		ts := formatTimestamp(ch.StartMs)
		fmt.Fprintf(f, "%s %s\n", ts, ch.Title)
	}
	return nil
}

// formatTimestamp converts milliseconds to HH:MM:SS.mmm format.
func formatTimestamp(ms int) string {
	totalSec := ms / 1000
	millis := ms % 1000
	hours := totalSec / 3600
	minutes := (totalSec % 3600) / 60
	seconds := totalSec % 60
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
}

// downloadCover downloads cover art and saves as cover.jpg in the book directory.
func downloadCover(ctx context.Context, coverURL, bookDir, titleBase string) (string, error) {
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

	coverPath := filepath.Join(bookDir, "cover.jpg")
	out, err := os.Create(coverPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := io.Copy(out, io.LimitReader(resp.Body, 10*1024*1024)); err != nil {
		return "", err
	}

	return coverPath, nil
}

// copyFile copies a file from src to dst and optionally reports progress.
func copyFile(src, dst string, totalBytes int64, onProgress func(moved, total int64), onStage func(stage string)) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 1024*1024)
	moved := int64(0)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			w, writeErr := out.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}
			if w != n {
				return io.ErrShortWrite
			}
			moved += int64(w)
			// Only emit progress up to just-under 100% here; the caller emits
			// the true 100% after this function returns (i.e. after Close flushes).
			if onProgress != nil && moved < totalBytes {
				onProgress(moved, totalBytes)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if onStage != nil {
		onStage("finalizing")
	}
	return out.Close()
}

var unsafeChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// sanitizePath removes characters that are unsafe in filenames.
// On Windows, folder/file names cannot end with dots or spaces (the OS silently
// strips them, which can create 8.3 short-name collisions like "BLF4TR~H").
// We also replace common Unicode punctuation with ASCII equivalents so that
// names stay readable across all platforms.
func sanitizePath(name string) string {
	// Replace common Unicode punctuation with ASCII equivalents.
	r := strings.NewReplacer(
		"\u2018", "'", "\u2019", "'", // smart single quotes
		"\u201C", "", "\u201D", "", // smart double quotes (removed like regular quotes)
		"\u2013", "-", "\u2014", "-", // en-dash, em-dash
		"\u2026", "...", // ellipsis
		"\u00A0", " ", // non-breaking space
	)
	s := r.Replace(name)

	s = unsafeChars.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)

	// Strip trailing dots — Windows silently removes them from directory names,
	// which can cause 8.3 short-name fallbacks.
	s = strings.TrimRight(s, ".")
	s = strings.TrimSpace(s) // in case dots were after spaces

	if s == "" {
		return "_"
	}
	return s
}

