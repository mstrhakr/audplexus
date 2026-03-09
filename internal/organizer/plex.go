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
	"time"

	"github.com/mstrhakr/audible-plex-downloader/internal/audio"
	"github.com/mstrhakr/audible-plex-downloader/internal/audnexus"
	"github.com/mstrhakr/audible-plex-downloader/internal/database"
	"github.com/mstrhakr/audible-plex-downloader/internal/logging"
)

var orgLog = logging.Component("organizer")

// PlexOrganizer handles organizing audiobook files into Plex-compatible structure.
type PlexOrganizer struct {
	db          database.Database
	ffmpeg      *audio.FFmpeg
	libraryRoot string
	embedCover  bool
	chapterFile bool
}

// NewPlexOrganizer creates a new Plex file organizer.
func NewPlexOrganizer(db database.Database, ffmpeg *audio.FFmpeg, libraryRoot string, embedCover, chapterFile bool) *PlexOrganizer {
	return &PlexOrganizer{
		db:          db,
		ffmpeg:      ffmpeg,
		libraryRoot: libraryRoot,
		embedCover:  embedCover,
		chapterFile: chapterFile,
	}
}

// Organize takes a decrypted audiobook file and moves it into the Plex library structure.
// Structure: {libraryRoot}/{Author}/{Title}/{Title - Author}.m4b
// Optionally embeds metadata, cover art, and generates a chapters file.
func (o *PlexOrganizer) Organize(ctx context.Context, book *database.Book, enriched *audnexus.EnrichedBook, inputPath string) (string, error) {
	return o.OrganizeWithProgress(ctx, book, enriched, inputPath, nil)
}

// OrganizeWithProgress performs the same work as Organize and reports file move progress.
// The callback receives bytes moved and total bytes.
func (o *PlexOrganizer) OrganizeWithProgress(ctx context.Context, book *database.Book, enriched *audnexus.EnrichedBook, inputPath string, onMoveProgress func(moved, total int64)) (string, error) {
	_ = ctx
	author := strings.TrimSpace(enriched.Author())
	title := strings.TrimSpace(enriched.Title())

	if author == "" {
		author = "Unknown Author"
	}
	if title == "" {
		title = "Unknown Title"
	}
	filenameBase := buildFilenameBase(title, author)

	bookDir := filepath.Join(o.libraryRoot, sanitizePath(author), sanitizePath(title))
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
		if err := copyFile(inputPath, finalPath, totalBytes, onMoveProgress); err != nil {
			return "", fmt.Errorf("move file: %w", err)
		}
		_ = os.Remove(inputPath)
	} else if onMoveProgress != nil {
		onMoveProgress(totalBytes, totalBytes)
	}

	// Generate chapters file
	if o.chapterFile {
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

// buildFilenameBase builds a Plex-friendly filename in "Title - Author" format.
func buildFilenameBase(title, author string) string {
	title = strings.TrimSpace(title)
	author = strings.TrimSpace(author)
	if title == "" {
		title = "Unknown Title"
	}
	if author == "" {
		return title
	}
	return fmt.Sprintf("%s - %s", title, author)
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
func copyFile(src, dst string, totalBytes int64, onProgress func(moved, total int64)) error {
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
			if onProgress != nil {
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
	return out.Close()
}

var unsafeChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// sanitizePath removes characters that are unsafe in filenames.
func sanitizePath(name string) string {
	s := unsafeChars.ReplaceAllString(name, "")
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	return s
}
