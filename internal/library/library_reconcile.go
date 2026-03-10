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

// reconcileExistingAudiobookFiles scans the expected library layout and reconciles
// each book's file state against disk. It marks books complete when a final
// audiobook file exists and marks previously complete books as new when the file
// is missing so they can be re-downloaded.
func reconcileExistingAudiobookFiles(ctx context.Context, db database.Database, libraryRoot string) (int, error) {
	return reconcileExistingAudiobookFilesWithProgress(ctx, db, libraryRoot, nil)
}

// reconcileExistingAudiobookFilesWithProgress behaves like reconcileExistingAudiobookFiles
// and optionally reports scan progress as processed/total books.
func reconcileExistingAudiobookFilesWithProgress(ctx context.Context, db database.Database, libraryRoot string, onProgress func(processed, total int)) (int, error) {
	if strings.TrimSpace(libraryRoot) == "" {
		return 0, nil
	}

	updated := 0
	processed := 0
	totalBooks := 0
	totalKnown := false
	limit := 200
	offset := 0

	for {
		books, total, err := db.ListBooks(ctx, database.BookFilter{Limit: limit, Offset: offset})
		if err != nil {
			return updated, err
		}
		if !totalKnown {
			totalBooks = total
			totalKnown = true
			if onProgress != nil {
				onProgress(0, totalBooks)
			}
		}
		if len(books) == 0 {
			break
		}

		for i := range books {
			changed, err := reconcileBookFromLibrary(ctx, db, &books[i], libraryRoot)
			if err != nil {
				return updated, err
			}
			processed++
			if onProgress != nil {
				onProgress(processed, totalBooks)
			}
			if changed {
				updated++
			}
		}

		offset += len(books)
		if offset >= total {
			break
		}
	}

	if onProgress != nil {
		onProgress(processed, totalBooks)
	}

	return updated, nil
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
	exts := []string{"m4b", "mp3"}

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
