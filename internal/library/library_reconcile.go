package library

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
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

// isAudioFile checks if a filename has a supported audio extension
func isAudioFile(filename string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	return supportedAudioExtensions[ext]
}

// reconcileExistingAudiobookFiles scans the expected library layout and reconciles
// each book's file state against disk. It marks books complete when a final
// audiobook file exists and marks previously complete books as new when the file
// is missing so they can be re-downloaded.
func reconcileExistingAudiobookFiles(ctx context.Context, db database.Database, libraryRoot string) (int, error) {
	return reconcileExistingAudiobookFilesWithProgress(ctx, db, libraryRoot, nil)
}

// reconcileExistingAudiobookFilesWithProgress scans the library directory for all audio files,
// attempts to match them to known books, and updates the database. This is a filesystem-driven
// approach that discovers both matched and unmatched audio files.
// Returns the count of books that were reconciled/updated.
func reconcileExistingAudiobookFilesWithProgress(ctx context.Context, db database.Database, libraryRoot string, onProgress func(processed, total int)) (int, error) {
	if strings.TrimSpace(libraryRoot) == "" {
		return 0, nil
	}

	// Phase 1: Discover all audio files in the library
	discoveredFiles := make(map[string]int64) // path -> size
	err := filepath.WalkDir(libraryRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible directories
		}
		if d.IsDir() {
			return nil
		}
		if !isAudioFile(d.Name()) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		info, err := d.Info()
		if err != nil {
			return nil // skip files we can't stat
		}
		discoveredFiles[path] = info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}

	// Build an index of ASIN -> file path for fast primary matching.
	asinFileIndex := buildASINFileIndex(discoveredFiles)

	// Phase 2: Load all books from the database
	books, _, err := db.ListBooks(ctx, database.BookFilter{Limit: 10000, Offset: 0})
	if err != nil {
		return 0, err
	}

	// Phase 3: Build a map of matched files per book and track unmatched files
	matchedFiles := make(map[string]struct{}) // files that were matched to a book
	updated := 0

	totalWork := len(books) // progress counters should represent books only
	processed := 0

	// For each book, find its best matching file on disk
	for i := range books {
		select {
		case <-ctx.Done():
			return updated, ctx.Err()
		default:
		}

		matchedFile, matchedSize := findBestFileForBook(ctx, &books[i], libraryRoot, discoveredFiles, asinFileIndex)
		if matchedFile != "" {
			matchedFiles[matchedFile] = struct{}{}

			// Update book if file status changed
			if books[i].FilePath != matchedFile || books[i].FileSize != matchedSize || books[i].Status != database.BookStatusComplete {
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

	// Phase 4: Log unmatched files for debugging (these could be from older versions or other sources)
	unmatchedCount := 0
	for filePath := range discoveredFiles {
		if _, matched := matchedFiles[filePath]; !matched {
			unmatchedCount++
		}
	}

	if unmatchedCount > 0 {
		// In future, we could add logic to:
		// 1. Read metadata from unmatched files
		// 2. Attempt fuzzy matching by comparing metadata to book info
		// 3. Create database entries for truly orphaned files
		// For now, just track that they exist
		syncLog.Info().Int("count", unmatchedCount).Msg("found unmatched audio files in library (not matched to any database book)")
	}

	return updated, nil
}

// findBestFileForBook searches for matching audio files for a book in discoveredFiles.
// It returns the best matching file path and its size, or empty string if no match found.
func findBestFileForBook(ctx context.Context, book *database.Book, libraryRoot string, discoveredFiles map[string]int64, asinFileIndex map[string]string) (string, int64) {
	if book == nil {
		return "", 0
	}

	_ = ctx

	// First choice: ASIN match from discovered filenames.
	asin := strings.ToUpper(strings.TrimSpace(book.ASIN))
	if asin != "" {
		if path, ok := asinFileIndex[asin]; ok {
			if size, found := discoveredFiles[path]; found {
				return path, size
			}
		}
	}

	paths := candidateLibraryPaths(book, libraryRoot)

	// Second choice: if we have a previously stored path and it exists in discoveredFiles
	if book.FilePath != "" {
		if size, found := discoveredFiles[book.FilePath]; found {
			return book.FilePath, size
		}
	}

	// Third choice: check all candidate paths against discovered files
	for _, path := range paths {
		if size, found := discoveredFiles[path]; found {
			return path, size
		}
	}

	return "", 0
}

func buildASINFileIndex(discoveredFiles map[string]int64) map[string]string {
	index := make(map[string]string)
	asinRe := regexp.MustCompile(`(?i)\bB[0-9A-Z]{9}\b`)

	for path := range discoveredFiles {
		name := filepath.Base(path)
		match := asinRe.FindString(name)
		if match == "" {
			continue
		}

		asin := strings.ToUpper(match)
		// Keep first match to avoid non-deterministic overwrites.
		if _, exists := index[asin]; !exists {
			index[asin] = path
		}
	}

	return index
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
