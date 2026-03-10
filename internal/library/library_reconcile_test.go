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

func (m *reconcileMockDB) Close() error                          { return nil }
func (m *reconcileMockDB) Migrate() error                        { return nil }
func (m *reconcileMockDB) Reset(ctx context.Context) error       { return nil }
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
