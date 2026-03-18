package library

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mstrhakr/audible-plex-downloader/internal/database"
)

func TestCandidateLibraryPathsIncludesNewAndLegacyLayouts(t *testing.T) {
	root := filepath.Join("testdata", "library")
	book := &database.Book{
		Title:          "Harry Potter and the Order of the Phoenix",
		Author:         "J.K. Rowling",
		Series:         "Harry Potter",
		SeriesPosition: "5",
	}

	paths := candidateLibraryPaths(book, root)

	wantNew := filepath.Join(root, "J.K. Rowling", "Harry Potter and the Order of the Phoenix", "Harry Potter and the Order of the Phoenix - J.K. Rowling.m4b")
	wantLegacySeries := filepath.Join(root, "J.K. Rowling", "Harry Potter and the Order of the Phoenix - Harry Potter, Book 5", "Harry Potter and the Order of the Phoenix - Harry Potter, Book 5.m4b")
	wantLegacyTitleOnly := filepath.Join(root, "J.K. Rowling", "Harry Potter and the Order of the Phoenix", "Harry Potter and the Order of the Phoenix.m4b")

	if !containsPath(paths, wantNew) {
		t.Fatalf("candidateLibraryPaths missing new layout path: %s", wantNew)
	}
	if !containsPath(paths, wantLegacySeries) {
		t.Fatalf("candidateLibraryPaths missing legacy series path: %s", wantLegacySeries)
	}
	if !containsPath(paths, wantLegacyTitleOnly) {
		t.Fatalf("candidateLibraryPaths missing legacy title path: %s", wantLegacyTitleOnly)
	}
}

func TestReconcileBookFromLibraryMarksMissingCompleteAsNew(t *testing.T) {
	tmp := t.TempDir()
	book := &database.Book{
		ID:       42,
		ASIN:     "B000TEST01",
		Title:    "Missing File Book",
		Author:   "Test Author",
		Status:   database.BookStatusComplete,
		FilePath: filepath.Join(tmp, "missing.m4b"),
		FileSize: 123,
	}

	db := &reconcileMockDB{}
	changed, err := reconcileBookFromLibrary(context.Background(), db, book, tmp)
	if err != nil {
		t.Fatalf("reconcileBookFromLibrary() error = %v", err)
	}
	if !changed {
		t.Fatal("reconcileBookFromLibrary() changed = false, want true")
	}
	if db.upserted == nil {
		t.Fatal("expected book to be upserted")
	}
	if db.upserted.Status != database.BookStatusNew {
		t.Fatalf("book status = %q, want %q", db.upserted.Status, database.BookStatusNew)
	}
	if db.upserted.FilePath != "" {
		t.Fatalf("book file path = %q, want empty", db.upserted.FilePath)
	}
	if db.upserted.FileSize != 0 {
		t.Fatalf("book file size = %d, want 0", db.upserted.FileSize)
	}
}

func TestBuildASINFileIndexExtractsASINFromFilename(t *testing.T) {
	files := map[string]int64{
		filepath.Join("root", "pirateaba", "Hell's Wardens B0DCCZ5MG2 [us]", "Hell's Wardens The Wandering Inn, Book 14 - The Wandering Inn 14 B0DCCZ5MG2 [us].m4b"): 100,
		filepath.Join("root", "misc", "not-a-book.txt"): 10,
	}

	index := buildASINFileIndex(files)
	if got := index["B0DCCZ5MG2"]; got == "" {
		t.Fatalf("buildASINFileIndex() missing ASIN key %q", "B0DCCZ5MG2")
	}
}

func TestBuildASINFileIndexExtractsASINFromFolderOnly(t *testing.T) {
	// File has a plain name with no ASIN; the ASIN is only in the parent folder name.
	// This is the Audnexus.bundle pattern: "Title B0XXXXXXXXXX [us]/Title.m4b"
	folderASINPath := filepath.Join("root", "Author Name", "Some Title B0FOLDER01 [us]", "Some Title.m4b")
	files := map[string]int64{
		folderASINPath: 500,
	}

	index := buildASINFileIndex(files)
	if got := index["B0FOLDER01"]; got == "" {
		t.Fatalf("buildASINFileIndex() should find ASIN in folder name but got empty; path=%q", folderASINPath)
	}
	if index["B0FOLDER01"] != folderASINPath {
		t.Fatalf("buildASINFileIndex() path = %q, want %q", index["B0FOLDER01"], folderASINPath)
	}
}

func TestExtractASINFromPathPrefersFilenameOverFolder(t *testing.T) {
	// When both filename and folder contain ASINs, filename wins.
	path := filepath.Join("root", "Author", "Title B0FOLDER01 [us]", "Title B0FILENA01.m4b")
	got := extractASINFromPath(path)
	if got != "B0FILENA01" {
		t.Fatalf("extractASINFromPath() = %q, want %q (filename should take priority)", got, "B0FILENA01")
	}
}

func TestExtractASINFromPathMatchesISBN10(t *testing.T) {
	// The Audible API sometimes returns ISBN-10 (10 digits) instead of Audible ASIN.
	// The organizer writes whatever the API returns, so we need to match ISBN-10s too.
	path := filepath.Join("root", "Author Name", "Some Title 1774246864 [us]", "Some Title.m4b")
	got := extractASINFromPath(path)
	if got != "1774246864" {
		t.Fatalf("extractASINFromPath() = %q, want %q (should extract ISBN-10 from folder)", got, "1774246864")
	}
}

func TestExtractASINFromPathMatchesISBN10WithX(t *testing.T) {
	// Some ISBN-10 values end with 'X' (a checksum digit). We need to match those too.
	path := filepath.Join("root", "Author Name", "Some Title 103940474X [us]", "Some Title.m4b")
	got := extractASINFromPath(path)
	if got != "103940474X" {
		t.Fatalf("extractASINFromPath() = %q, want %q (should extract ISBN-10 with X)", got, "103940474X")
	}
}

func TestBuildASINIndexExtractsISBN10FromFolder(t *testing.T) {
	// ISBN-10 in folder name should be indexed like any ASIN.
	isbnPath := filepath.Join("root", "Author", "Book Title 0593393864 [us]", "Book Title.m4b")
	files := map[string]int64{
		isbnPath: 1000,
	}

	index := buildASINFileIndex(files)
	if got := index["0593393864"]; got == "" {
		t.Fatalf("buildASINFileIndex() should find ISBN-10 but got empty; path=%q", isbnPath)
	}
	if index["0593393864"] != isbnPath {
		t.Fatalf("buildASINFileIndex() path = %q, want %q", index["0593393864"], isbnPath)
	}
}

func TestFindBestFileForBookPrefersASINMatch(t *testing.T) {
	root := filepath.Join("root")
	asinFile := filepath.Join(root, "pirateaba", "Hell's Wardens B0DCCZ5MG2 [us]", "Hell's Wardens The Wandering Inn, Book 14 - The Wandering Inn 14 B0DCCZ5MG2 [us].m4b")
	otherFile := filepath.Join(root, "pirateaba", "Something Else", "Unrelated.m4b")

	discovered := map[string]int64{
		asinFile:  12345,
		otherFile: 111,
	}

	book := &database.Book{
		ASIN:   "B0DCCZ5MG2",
		Title:  "Hell's Wardens",
		Author: "pirateaba",
		Status: database.BookStatusNew,
	}

	matchedPath, matchedSize, matchMethod := findBestFileForBook(context.Background(), book, root, discovered, buildASINFileIndex(discovered))
	if matchedPath != asinFile {
		t.Fatalf("findBestFileForBook() path = %q, want %q", matchedPath, asinFile)
	}
	if matchedSize != 12345 {
		t.Fatalf("findBestFileForBook() size = %d, want %d", matchedSize, 12345)
	}
	if matchMethod != "asin_path" {
		t.Fatalf("findBestFileForBook() matchMethod = %q, want %q", matchMethod, "asin_path")
	}
}

func TestFindBestFileForBookMatchesISBN10WithX(t *testing.T) {
	root := filepath.Join("root")
	isbnXFile := filepath.Join(root, "Author", "Great Book 103940474X [us]", "Great Book.m4b")
	discovered := map[string]int64{
		isbnXFile: 12345,
	}

	book := &database.Book{
		ASIN:   "103940474X",
		Title:  "Great Book",
		Author: "Author",
		Status: database.BookStatusNew,
	}

	matchedPath, matchedSize, matchMethod := findBestFileForBook(context.Background(), book, root, discovered, buildASINFileIndex(discovered))
	if matchedPath != isbnXFile {
		t.Fatalf("findBestFileForBook() path = %q, want %q", matchedPath, isbnXFile)
	}
	if matchedSize != 12345 {
		t.Fatalf("findBestFileForBook() size = %d, want %d", matchedSize, 12345)
	}
	if matchMethod != "asin_path" {
		t.Fatalf("findBestFileForBook() matchMethod = %q, want %q", matchMethod, "asin_path")
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

type reconcileMockDB struct {
	upserted *database.Book
}

func (m *reconcileMockDB) Close() error                    { return nil }
func (m *reconcileMockDB) Migrate() error                  { return nil }
func (m *reconcileMockDB) Reset(ctx context.Context) error { return nil }
func (m *reconcileMockDB) GetBook(ctx context.Context, id int64) (*database.Book, error) {
	return nil, nil
}
func (m *reconcileMockDB) GetBookByASIN(ctx context.Context, asin string) (*database.Book, error) {
	return nil, nil
}
func (m *reconcileMockDB) ListBooks(ctx context.Context, filter database.BookFilter) ([]database.Book, int, error) {
	return nil, 0, nil
}
func (m *reconcileMockDB) UpsertBook(ctx context.Context, book *database.Book) error {
	copyBook := *book
	m.upserted = &copyBook
	return nil
}
func (m *reconcileMockDB) UpdateBookStatus(ctx context.Context, id int64, status database.BookStatus) error {
	return nil
}
func (m *reconcileMockDB) UpdateBookPlexInfo(ctx context.Context, id int64, plexRatingKey, plexTitle string) error {
	return nil
}
func (m *reconcileMockDB) DeleteBook(ctx context.Context, id int64) error { return nil }
func (m *reconcileMockDB) EnqueueDownload(ctx context.Context, item *database.DownloadQueue) error {
	return nil
}
func (m *reconcileMockDB) GetNextPendingDownload(ctx context.Context) (*database.DownloadQueue, error) {
	return nil, nil
}
func (m *reconcileMockDB) UpdateDownload(ctx context.Context, item *database.DownloadQueue) error {
	return nil
}
func (m *reconcileMockDB) ListDownloads(ctx context.Context, status *database.DownloadStatus) ([]database.DownloadQueue, error) {
	return nil, nil
}
func (m *reconcileMockDB) CancelDownload(ctx context.Context, id int64) error   { return nil }
func (m *reconcileMockDB) RetryDownload(ctx context.Context, id int64) error    { return nil }
func (m *reconcileMockDB) RetryAllDownloads(ctx context.Context) (int64, error) { return 0, nil }
func (m *reconcileMockDB) CreateSync(ctx context.Context, sync *database.SyncHistory) error {
	return nil
}
func (m *reconcileMockDB) UpdateSync(ctx context.Context, sync *database.SyncHistory) error {
	return nil
}
func (m *reconcileMockDB) GetLastSync(ctx context.Context) (*database.SyncHistory, error) {
	return nil, nil
}
func (m *reconcileMockDB) GetSetting(ctx context.Context, key string) (string, error) { return "", nil }
func (m *reconcileMockDB) SetSetting(ctx context.Context, key, value string) error    { return nil }
func (m *reconcileMockDB) GetActiveDevice(ctx context.Context) (*database.Device, error) {
	return nil, nil
}
func (m *reconcileMockDB) SaveDevice(ctx context.Context, device *database.Device) error { return nil }
func (m *reconcileMockDB) ListDevices(ctx context.Context) ([]database.Device, error) {
	return nil, nil
}
func (m *reconcileMockDB) DeleteDevice(ctx context.Context, id int64) error { return nil }
