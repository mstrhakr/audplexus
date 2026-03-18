package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mstrhakr/audible-plex-downloader/internal/database"
)

var unsafePathChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// supportedAudioExtensions lists all audio file formats that can be discovered
var supportedAudioExtensions = map[string]bool{
	"m4b":  true, // Apple audiobook format (primary)
	"m4a":  true, // Apple audio format
	"mp3":  true, // MPEG audio
	"aax":  true, // Audible format v2/v3
	"aaxc": true, // Audible format v4
	"flac": true, // Free Lossless Audio Codec
	"ogg":  true, // Ogg Vorbis
	"wma":  true, // Windows Media Audio
	"aac":  true, // Advanced Audio Coding
	"opus": true, // Opus audio
}

// audioExtension returns the lowercase extension of a filename without the leading dot.
func audioExtension(filename string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
}

// isAudioFile checks if a filename has a supported audio extension
func isAudioFile(filename string) bool {
	return supportedAudioExtensions[audioExtension(filename)]
}

// reconcileExistingAudiobookFiles scans the expected library layout and reconciles
// each book's file state against disk. It marks books complete when a final
// audiobook file exists and marks previously complete books as new when the file
// is missing so they can be re-downloaded.
func reconcileExistingAudiobookFiles(ctx context.Context, db database.Database, libraryRoot string) (int, error) {
	reconciled, _, err := reconcileExistingAudiobookFilesWithProgress(ctx, db, libraryRoot, nil)
	return reconciled, err
}

// reconcileExistingAudiobookFilesWithProgress scans the library directory for all audio files,
// attempts to match them to known books, and updates the database. This is a filesystem-driven
// approach that discovers both matched and unmatched audio files.
// Returns the count of books that were reconciled/updated.
func reconcileExistingAudiobookFilesWithProgress(ctx context.Context, db database.Database, libraryRoot string, onProgress func(processed, total int)) (int, error) {
	if strings.TrimSpace(libraryRoot) == "" {
		return 0, nil
	}

	// Phase 1: Discover all audio files in the library.
	// Mirrors Audnexus.bundle: the full path (folder names + filename) is searched
	// for an ASIN so files under folders like "Title B0XXXXXXXXXX [us]/" are found.
	discoveredFiles := make(map[string]int64) // path -> size
	filesVisited := 0
	nonAudioSkipped := 0
	walkErrors := 0
	statErrors := 0
	discoveredByExt := make(map[string]int)
	skippedByExt := make(map[string]int)
	err := filepath.WalkDir(libraryRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			walkErrors++
			syncLog.Debug().Err(err).Str("path", path).Msg("fs_scan: skipping entry (walk error)")
			return nil // skip inaccessible directories
		}
		if d.IsDir() {
			return nil
		}

		filesVisited++
		ext := audioExtension(d.Name())
		if ext == "" {
			ext = "no_ext"
		}
		if !isAudioFile(d.Name()) {
			nonAudioSkipped++
			skippedByExt[ext]++
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		info, err := d.Info()
		if err != nil {
			statErrors++
			syncLog.Debug().Err(err).Str("path", path).Msg("fs_scan: skipping audio file (stat error)")
			return nil // skip files we can't stat
		}
		discoveredFiles[path] = info.Size()
		discoveredByExt[ext]++

		// Log each discovered file with its extracted ASIN for debug visibility
		asin := extractASINFromPath(path)
		if asin == "" {
			syncLog.Debug().Str("path", path).Str("asin", "none").Msg("fs_scan: discovered audio file")
		} else {
			syncLog.Debug().Str("path", path).Str("asin", asin).Msg("fs_scan: discovered audio file")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	syncLog.Info().
		Str("library_root", libraryRoot).
		Int("files_visited", filesVisited).
		Int("audio_files_discovered", len(discoveredFiles)).
		Int("non_audio_skipped", nonAudioSkipped).
		Int("walk_errors", walkErrors).
		Int("stat_errors", statErrors).
		Msg("fs_scan: audio discovery complete")
	syncLog.Debug().
		Str("audio_by_ext", formatCountMap(discoveredByExt)).
		Str("skipped_by_ext", formatCountMap(skippedByExt)).
		Msg("fs_scan: extension breakdown")

	// Build an index of ASIN -> file path. Searches filename AND all parent directory
	// components, matching the Audnexus.bundle behaviour.
	asinFileIndex := buildASINFileIndex(discoveredFiles)

	// Log files without ASINs (these will likely not be matched)
	filesWithoutASIN := make([]string, 0)
	for path := range discoveredFiles {
		if extractASINFromPath(path) == "" {
			filesWithoutASIN = append(filesWithoutASIN, path)
		}
	}
	if len(filesWithoutASIN) > 0 {
		syncLog.Warn().
			Int("count", len(filesWithoutASIN)).
			Strs("files", filesWithoutASIN).
			Msg("fs_scan: audio files discovered with no ASIN in path (will not be hard-matched)")
	}

	syncLog.Debug().
		Int("asin_index_entries", len(asinFileIndex)).
		Int("files_without_asin", len(filesWithoutASIN)).
		Msg("fs_scan: ASIN index built")

	// Phase 2: Load all books from the database
	books, _, err := db.ListBooks(ctx, database.BookFilter{Limit: 10000, Offset: 0})
	if err != nil {
		return 0, err
	}

	// Phase 3: Build a map of matched files per book and track unmatched files
	matchedFiles := make(map[string]struct{}) // files that were matched to a book
	updated := 0
	missingCompleteBooks := 0
	matchMethodCounts := map[string]int{
		"asin_path":      0,
		"stored_path":    0,
		"candidate_path": 0,
		"no_match":       0,
	}

	totalWork := len(books) // progress counters should represent books only
	processed := 0

	// For each book, find its best matching file on disk
	for i := range books {
		select {
		case <-ctx.Done():
			return updated, ctx.Err()
		default:
		}

		matchedFile, matchedSize, matchMethod := findBestFileForBook(ctx, &books[i], libraryRoot, discoveredFiles, asinFileIndex)
		matchMethodCounts[matchMethod]++
		if matchedFile != "" {
			matchedFiles[matchedFile] = struct{}{}

			// Update book if file status changed
			if books[i].FilePath != matchedFile || books[i].FileSize != matchedSize || books[i].Status != database.BookStatusComplete {
				syncLog.Debug().
					Str("asin", books[i].ASIN).
					Str("file", matchedFile).
					Str("match_method", matchMethod).
					Msg("fs_scan: reconciling book to file")
				books[i].FilePath = matchedFile
				books[i].FileSize = matchedSize
				books[i].Status = database.BookStatusComplete
				if err := db.UpsertBook(ctx, &books[i]); err != nil {
					return updated, err
				}
				updated++
			}
		} else if books[i].Status == database.BookStatusComplete {
			// Book was marked complete but file is missing
			missingCompleteBooks++
			syncLog.Debug().
				Str("asin", books[i].ASIN).
				Str("title", books[i].Title).
				Str("prev_path", books[i].FilePath).
				Msg("fs_scan: complete book has no file on disk, marking new")
			books[i].FilePath = ""
			books[i].FileSize = 0
			books[i].Status = database.BookStatusNew
			if err := db.UpsertBook(ctx, &books[i]); err != nil {
				return updated, err
			}
			updated++
		}

		processed++
		if onProgress != nil {
			onProgress(processed, totalWork)
		}
	}

	// Phase 4: Report on unmatched files (files on disk with no matching database book)
	unmatchedFiles := make([]string, 0)
	for filePath := range discoveredFiles {
		if _, matched := matchedFiles[filePath]; !matched {
			unmatchedFiles = append(unmatchedFiles, filePath)
		}
	}

	syncLog.Info().
		Int("books_in_db", len(books)).
		Int("audio_files_on_disk", len(discoveredFiles)).
		Int("files_matched_to_book", len(matchedFiles)).
		Int("files_unmatched", len(unmatchedFiles)).
		Int("complete_books_missing_file", missingCompleteBooks).
		Int("books_updated", updated).
		Msg("fs_scan: reconciliation complete")
	syncLog.Debug().
		Str("match_methods", formatCountMap(matchMethodCounts)).
		Msg("fs_scan: match method breakdown")
	if len(unmatchedFiles) > 0 {
		syncLog.Warn().
			Int("count", len(unmatchedFiles)).
			Strs("files", unmatchedFiles).
			Msg("fs_scan: unmatched audio files on disk (no matching database book found)")
	}

	return updated, nil
}

// findBestFileForBook searches for a matching audio file for a book among discoveredFiles.
// Returns the best matching file path, its size, and the method used to find it.
// Match methods (in priority order):
//   - "asin_path"      hard match: ASIN found anywhere in the path (filename or folder), mirroring Audnexus.bundle
//   - "stored_path"    previously stored FilePath is still present on disk
//   - "candidate_path" a generated candidate path (author/title/filename layout) exists on disk
//   - "no_match"       no file found
func findBestFileForBook(ctx context.Context, book *database.Book, libraryRoot string, discoveredFiles map[string]int64, asinFileIndex map[string]string) (string, int64, string) {
	if book == nil {
		return "", 0, "no_match"
	}

	_ = ctx

	// First choice: hard match — file path (folder or filename) contains the book's ASIN.
	// Audible stores the identifier (ASIN/ISBN) in the ASIN field of the database.
	asin := strings.ToUpper(strings.TrimSpace(book.ASIN))
	if asin != "" {
		if path, ok := asinFileIndex[asin]; ok {
			if size, found := discoveredFiles[path]; found {
				return path, size, "asin_path"
			}
		}
	}

	// Second choice: previously stored file path still exists on disk
	if book.FilePath != "" {
		if size, found := discoveredFiles[book.FilePath]; found {
			return book.FilePath, size, "stored_path"
		}
	}

	// Third choice: check generated candidate paths (author/title/filename layout)
	for _, path := range candidateLibraryPaths(book, libraryRoot) {
		if size, found := discoveredFiles[path]; found {
			return path, size, "candidate_path"
		}
	}

	return "", 0, "no_match"
}

// asinPathRe matches both:
//   - Audible ASINs: B + 9 uppercase alphanumeric chars (e.g. B0DCCZ5MG2)
//   - ISBN-10 fallback: 10 digits (e.g. 1774246864, 0525588035)
//
// The Audible API sometimes returns ISBN-10 in place of ASINs, so we match both.
// Accepts Audible ASINs (BXXXXXXXXX) and ISBN-10 (including trailing X checksum).
var asinPathRe = regexp.MustCompile(`(?i)\bB[0-9A-Z]{9}\b|\b[0-9]{9}[0-9X]\b`)

// extractASINFromPath searches for an Audible ASIN (or ISBN-10 fallback) anywhere in a file path.
// Audible ASINs start with 'B' + 9 alphanumerics, but the Audible API sometimes returns ISBN-10s
// (10 digits) in place of ASINs. Since the organizer writes whatever identifier came from the API,
// we match both patterns. The filename is checked first (most specific), then parent directories.
func extractASINFromPath(path string) string {
	// Check filename first
	if match := asinPathRe.FindString(filepath.Base(path)); match != "" {
		return strings.ToUpper(match)
	}
	// Walk up directory components
	dir := filepath.Dir(path)
	for {
		if match := asinPathRe.FindString(filepath.Base(dir)); match != "" {
			return strings.ToUpper(match)
		}
		next := filepath.Dir(dir)
		if next == dir { // reached filesystem root
			break
		}
		dir = next
	}
	return ""
}

func buildASINFileIndex(discoveredFiles map[string]int64) map[string]string {
	index := make(map[string]string)
	for path := range discoveredFiles {
		asin := extractASINFromPath(path)
		if asin == "" {
			continue
		}
		// Keep first match to avoid non-deterministic overwrites.
		if _, exists := index[asin]; !exists {
			index[asin] = path
		}
	}
	return index
}

// formatCountMap formats a map[string]int as a sorted "key=val,key=val" string for logging.
func formatCountMap(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k, v := range counts {
		if v > 0 {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "none"
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, ",")
}

func reconcileBookFromLibrary(ctx context.Context, db database.Database, book *database.Book, libraryRoot string) (bool, error) {
	if book == nil {
		return false, nil
	}

	paths := candidateLibraryPaths(book, libraryRoot)
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}

		if book.FilePath == p && book.FileSize == fi.Size() && book.Status == database.BookStatusComplete {
			return false, nil
		}

		book.FilePath = p
		book.FileSize = fi.Size()
		book.Status = database.BookStatusComplete
		if err := db.UpsertBook(ctx, book); err != nil {
			return false, err
		}
		return true, nil
	}

	if book.Status == database.BookStatusComplete {
		book.FilePath = ""
		book.FileSize = 0
		book.Status = database.BookStatusNew
		if err := db.UpsertBook(ctx, book); err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

func candidateLibraryPaths(book *database.Book, libraryRoot string) []string {
	if book == nil {
		return nil
	}

	authors := authorCandidates(book.Author)
	titles := titleCandidates(book)
	filenameBases := filenameBaseCandidates(book)

	// Support all audio extensions, not just m4b and mp3
	exts := []string{"m4b", "m4a", "mp3", "aax", "aaxc", "flac", "ogg", "wma", "aac", "opus"}

	seen := make(map[string]struct{})
	paths := make([]string, 0, len(authors)*len(titles)*len(filenameBases)*len(exts)+1)

	if strings.TrimSpace(book.FilePath) != "" {
		seen[book.FilePath] = struct{}{}
		paths = append(paths, book.FilePath)
	}

	for _, author := range authors {
		authorDir := sanitizeLibraryPath(author)
		for _, title := range titles {
			titleDir := sanitizeLibraryPath(title)
			base := filepath.Join(libraryRoot, authorDir, titleDir)
			for _, filenameBase := range filenameBases {
				fileBase := sanitizeLibraryPath(filenameBase)
				for _, ext := range exts {
					p := filepath.Join(base, fileBase+"."+ext)
					if _, ok := seen[p]; ok {
						continue
					}
					seen[p] = struct{}{}
					paths = append(paths, p)
				}
			}
		}
	}

	return paths
}

func authorCandidates(author string) []string {
	author = strings.TrimSpace(author)
	if author == "" {
		return []string{"Unknown Author"}
	}

	parts := strings.Split(author, ",")
	first := strings.TrimSpace(parts[0])
	if first != "" && first != author {
		return []string{author, first}
	}
	return []string{author}
}

func titleCandidates(book *database.Book) []string {
	title := strings.TrimSpace(book.Title)
	if title == "" {
		title = "Unknown Title"
	}

	withSeries := title
	series := strings.TrimSpace(book.Series)
	seriesPos := strings.TrimSpace(book.SeriesPosition)
	if series != "" && seriesPos != "" {
		withSeries = title + " - " + series + ", Book " + seriesPos
	}

	if withSeries == title {
		return []string{title}
	}
	return []string{withSeries, title}
}

func filenameBaseCandidates(book *database.Book) []string {
	if book == nil {
		return []string{"Unknown Title"}
	}

	titles := titleCandidates(book)
	authors := authorCandidates(book.Author)

	seen := make(map[string]struct{})
	bases := make([]string, 0, len(titles)*(len(authors)+1))

	for _, title := range titles {
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		if _, ok := seen[title]; !ok {
			seen[title] = struct{}{}
			bases = append(bases, title)
		}
		for _, author := range authors {
			author = strings.TrimSpace(author)
			if author == "" {
				continue
			}
			candidate := title + " - " + author
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			bases = append(bases, candidate)
		}
	}

	if len(bases) == 0 {
		return []string{"Unknown Title"}
	}
	return bases
}

func sanitizeLibraryPath(name string) string {
	s := unsafePathChars.ReplaceAllString(name, "")
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	return s
}
