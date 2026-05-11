package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mstrhakr/audplexus/internal/audio"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/logging"
)

var validatorLog = logging.Component("validator")

// FileValidator checks audio file integrity and detects corruption.
type FileValidator struct {
	ffmpeg *audio.FFmpeg
	db     database.Database
}

// NewFileValidator creates a new file validator.
func NewFileValidator(ffmpeg *audio.FFmpeg, db database.Database) *FileValidator {
	return &FileValidator{
		ffmpeg: ffmpeg,
		db:     db,
	}
}

// FileHealthReport contains validation results for a single file.
type FileHealthReport struct {
	ASIN             string
	FilePath         string
	IsValid          bool
	Duration         float64 // seconds
	Bitrate          int     // kbps
	Codec            string
	FileSize         int64
	Error            string
	IsSuspicious     bool // true if file has warning signs
	SuspiciousReason string
}

// ScanLibraryFiles scans a directory and validates all audio files.
// Returns a list of reports and any files that should be marked for re-download.
func (fv *FileValidator) ScanLibraryFiles(ctx context.Context, libraryDir string) ([]FileHealthReport, []int64, error) {
	var reports []FileHealthReport
	var booksToRedownload []int64

	validatorLog.Info().Str("path", libraryDir).Msg("scanning library for corrupt files")

	// Recursively walk the library directory for M4B files
	err := filepath.WalkDir(libraryDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		if !strings.HasSuffix(strings.ToLower(d.Name()), ".m4b") {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Get file size
		info, err := d.Info()
		if err != nil {
			validatorLog.Warn().Err(err).Str("file", path).Msg("failed to stat file")
			return nil
		}

		report := FileHealthReport{
			FilePath: path,
			FileSize: info.Size(),
		}

		// Validate with ffmpeg
		duration, bitrate, codec, ffErr := fv.probeFile(path)
		report.Duration = duration
		report.Bitrate = bitrate
		report.Codec = codec

		if ffErr != nil {
			report.IsValid = false
			report.Error = ffErr.Error()
			validatorLog.Warn().Err(ffErr).Str("file", path).Msg("file probe failed")
		} else {
			// Check for suspicious characteristics
			report.IsValid = true
			report.IsSuspicious, report.SuspiciousReason = fv.detectSuspicious(duration, bitrate, info.Size())
			if report.IsSuspicious {
				validatorLog.Warn().Str("file", path).Str("reason", report.SuspiciousReason).Msg("file looks corrupted")
			}
		}

		// Try to match file to a book in the database
		asin := fv.extractASIN(path)
		if asin != "" {
			report.ASIN = asin
			book, err := fv.db.GetBookByASIN(ctx, asin)
			if err == nil && book != nil && (report.IsSuspicious || !report.IsValid) {
				booksToRedownload = append(booksToRedownload, book.ID)
			}
		}

		reports = append(reports, report)
		return nil
	})

	if err != nil {
		return reports, booksToRedownload, err
	}

	validatorLog.Info().Int("total_files", len(reports)).Int("suspicious", len(booksToRedownload)).Msg("library scan complete")
	return reports, booksToRedownload, nil
}

// probeFile uses FFmpeg to check file integrity.
// Returns: duration (seconds), bitrate (kbps), codec, error
func (fv *FileValidator) probeFile(path string) (float64, int, string, error) {
	if fv.ffmpeg == nil {
		return 0, 0, "", fmt.Errorf("ffmpeg not available")
	}

	duration, err := fv.ffmpeg.Probe(path)
	if err != nil {
		return 0, 0, "", fmt.Errorf("probe failed: %w", err)
	}

	// For now we only get duration. In the future, we could extend audio.FFmpeg.Probe
	// to return bitrate and codec info as well.
	return duration, 0, "audio/mp4", nil
}

// detectSuspicious checks if file has warning signs of corruption.
// Returns: isSuspicious, reason
func (fv *FileValidator) detectSuspicious(duration float64, bitrateKbps int, fileSize int64) (bool, string) {
	// File too small for typical audiobook (minimum 1MB)
	if fileSize < 1024*1024 {
		return true, fmt.Sprintf("file too small (%d bytes)", fileSize)
	}

	// Duration suspiciously short (less than 10 minutes)
	if duration > 0 && duration < 600 {
		return true, fmt.Sprintf("duration very short (%.1f seconds)", duration)
	}

	// Bitrate detection (if available)
	// Typical M4B audiobooks: 64-320 kbps, usually 128-256 kbps
	// Very low bitrate is a red flag
	if bitrateKbps > 0 && bitrateKbps < 64 {
		return true, fmt.Sprintf("bitrate suspiciously low (%d kbps)", bitrateKbps)
	}

	// File size to duration ratio check
	// Typical: ~1MB per 10-15 seconds at standard bitrate
	// If ratio is way off, file might be truncated
	if duration > 0 && fileSize > 0 {
		// Convert kbps to bytes: kbps * 1000 bits/kilo / 8 bits/byte
		expectedMin := int64((duration * 64 * 1000) / 8)  // 64 kbps minimum
		expectedMax := int64((duration * 320 * 1000) / 8) // 320 kbps maximum

		if fileSize < expectedMin*900/1000 {
			// File is significantly smaller than expected even at minimum bitrate
			return true, fmt.Sprintf("file size smaller than expected for duration (%.1fs = %dB expected, got %dB)", duration, expectedMin, fileSize)
		}

		if fileSize > expectedMax*1100/1000 {
			// File is way larger than expected (could have metadata bloat, but unlikely)
			return true, fmt.Sprintf("file size larger than expected (%.1fs = %dB expected, got %dB)", duration, expectedMax, fileSize)
		}
	}

	return false, ""
}

// extractASIN tries to extract ASIN from the file path.
// Assumes path contains ASIN somewhere (either in filename or parent dirs).
func (fv *FileValidator) extractASIN(path string) string {
	// Common pattern: filename starts with ASIN (10 chars)
	// e.g., "B0BQ89DTNB.m4b" or "B0BQ89DTNB - Something.m4b"
	base := filepath.Base(path)

	if len(base) >= 10 {
		potential := base[:10]
		// Check if it looks like an ASIN (10 alphanumeric chars starting with B)
		if strings.HasPrefix(potential, "B") && isAlphanumeric(potential) {
			return potential
		}
	}

	// Also check parent directory names
	parts := strings.Split(path, string(filepath.Separator))
	for _, part := range parts {
		if len(part) >= 10 && strings.HasPrefix(part, "B") && isAlphanumeric(part[:10]) {
			return part[:10]
		}
	}

	return ""
}

func isAlphanumeric(s string) bool {
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// MarkBooksForRedownload marks books as needing re-download.
func (fv *FileValidator) MarkBooksForRedownload(ctx context.Context, bookIDs []int64, reason string) error {
	for _, bookID := range bookIDs {
		book, err := fv.db.GetBook(ctx, bookID)
		if err != nil {
			validatorLog.Warn().Err(err).Int64("book_id", bookID).Msg("failed to get book for redownload marking")
			continue
		}

		if book == nil {
			continue
		}

		// Mark as new and clear file path
		book.Status = database.BookStatusNew
		book.FilePath = ""
		book.FileSize = 0

		if err := fv.db.UpsertBook(ctx, book); err != nil {
			validatorLog.Error().Err(err).Int64("book_id", bookID).Str("asin", book.ASIN).Msg("failed to mark book for redownload")
		} else {
			validatorLog.Info().Str("asin", book.ASIN).Str("reason", reason).Msg("marked corrupted book for re-download")
		}
	}

	return nil
}

