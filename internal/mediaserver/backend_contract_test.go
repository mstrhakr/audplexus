package mediaserver

import (
	"context"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

// TestBackendsImplementInterface is a compile-time assertion that all
// concrete backend types satisfy Backend. Guards against forgetting to
// implement OnBookOrganized on a new backend.
func TestBackendsImplementInterface(t *testing.T) {
	var _ Backend = (*PlexBackend)(nil)
	var _ Backend = (*EmbyBackend)(nil)
}

// TestNotConfiguredBackendReturnsTypedOutcome confirms the contract: a
// not-configured backend must return SkippedNotConfigured rather than
// silently no-op'ing. This is the central rule of the new operational
// contract — backends never lie to the caller about doing nothing.
func TestNotConfiguredBackendReturnsTypedOutcome(t *testing.T) {
	t.Setenv("PLEX_URL", "")
	t.Setenv("PLEX_TOKEN", "")
	t.Setenv("PLEX_SECTION_ID", "")
	t.Setenv("EMBY_URL", "")
	t.Setenv("EMBY_API_KEY", "")
	t.Setenv("EMBY_LIBRARY_ID", "")

	db := newSettingsOnlyStubDB()

	for _, tc := range []struct {
		name    string
		backend Backend
	}{
		{"plex", NewPlex(db, "/audiobooks")},
		{"emby", NewEmby(db, nil, "/audiobooks")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcomes := tc.backend.OnBookOrganized(context.Background(), OrganizedBook{
				BookID:    1,
				ASIN:      "B0TEST",
				Title:     "Test Book",
				LocalPath: "/audiobooks/Author/Test Book.m4b",
			})
			if len(outcomes) == 0 {
				t.Fatal("not-configured backend returned no outcomes; contract requires at least one SkippedNotConfigured outcome")
			}
			for _, o := range outcomes {
				if o.Status != OutcomeSkippedNotConfigured {
					t.Errorf("op=%s status=%q, want %q (not-configured backend must announce skip, not silently no-op)",
						o.Operation, o.Status, OutcomeSkippedNotConfigured)
				}
			}
		})
	}
}

// settingsOnlyStubDB satisfies database.Database with returning zero values
// from every method except GetSetting/SetSetting which act as a real
// in-memory map. Backend tests only exercise settings; everything else
// would represent unintended coupling and is left to fail at runtime.
type settingsOnlyStubDB struct {
	settings map[string]string
}

func newSettingsOnlyStubDB() *settingsOnlyStubDB {
	return &settingsOnlyStubDB{settings: map[string]string{}}
}

// Settings — the only methods backends call in this test path.
func (s *settingsOnlyStubDB) GetSetting(ctx context.Context, key string) (string, error) {
	return s.settings[key], nil
}
func (s *settingsOnlyStubDB) SetSetting(ctx context.Context, key, value string) error {
	s.settings[key] = value
	return nil
}

// Everything else: minimal zero-value stubs to satisfy the interface.
func (s *settingsOnlyStubDB) Close() error                                  { return nil }
func (s *settingsOnlyStubDB) Migrate() error                                { return nil }
func (s *settingsOnlyStubDB) Reset(ctx context.Context) error               { return nil }
func (s *settingsOnlyStubDB) GetBook(ctx context.Context, id int64) (*database.Book, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) GetBookByASIN(ctx context.Context, asin string) (*database.Book, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) ListBooks(ctx context.Context, f database.BookFilter) ([]database.Book, int, error) {
	return nil, 0, nil
}
func (s *settingsOnlyStubDB) UpsertBook(ctx context.Context, b *database.Book) error { return nil }
func (s *settingsOnlyStubDB) UpdateBookStatus(ctx context.Context, id int64, status database.BookStatus) error {
	return nil
}
func (s *settingsOnlyStubDB) DeleteBook(ctx context.Context, id int64) error { return nil }
func (s *settingsOnlyStubDB) EnqueueDownload(ctx context.Context, i *database.DownloadQueue) error {
	return nil
}
func (s *settingsOnlyStubDB) GetNextPendingDownload(ctx context.Context) (*database.DownloadQueue, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) UpdateDownload(ctx context.Context, i *database.DownloadQueue) error {
	return nil
}
func (s *settingsOnlyStubDB) ListDownloads(ctx context.Context, status *database.DownloadStatus) ([]database.DownloadQueue, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) CancelDownload(ctx context.Context, id int64) error  { return nil }
func (s *settingsOnlyStubDB) RetryDownload(ctx context.Context, id int64) error   { return nil }
func (s *settingsOnlyStubDB) RetryAllDownloads(ctx context.Context) (int64, error) { return 0, nil }
func (s *settingsOnlyStubDB) CreateSync(ctx context.Context, sync *database.SyncHistory) error {
	return nil
}
func (s *settingsOnlyStubDB) UpdateSync(ctx context.Context, sync *database.SyncHistory) error {
	return nil
}
func (s *settingsOnlyStubDB) GetLastSync(ctx context.Context) (*database.SyncHistory, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) GetActiveDevice(ctx context.Context) (*database.Device, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) SaveDevice(ctx context.Context, d *database.Device) error { return nil }
func (s *settingsOnlyStubDB) ListDevices(ctx context.Context) ([]database.Device, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) DeleteDevice(ctx context.Context, id int64) error { return nil }

// Library destinations + book destinations (PR-B). Stub returns zero values.
func (s *settingsOnlyStubDB) CreateLibraryDestination(ctx context.Context, d *database.LibraryDestination) error {
	return nil
}
func (s *settingsOnlyStubDB) GetLibraryDestination(ctx context.Context, id string) (*database.LibraryDestination, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) ListLibraryDestinations(ctx context.Context) ([]database.LibraryDestination, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) ListEnabledLibraryDestinations(ctx context.Context) ([]database.LibraryDestination, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) UpdateLibraryDestination(ctx context.Context, d *database.LibraryDestination) error {
	return nil
}
func (s *settingsOnlyStubDB) DeleteLibraryDestination(ctx context.Context, id string) error {
	return nil
}
func (s *settingsOnlyStubDB) UpsertBookDestination(ctx context.Context, bd *database.BookDestination) error {
	return nil
}
func (s *settingsOnlyStubDB) GetBookDestinations(ctx context.Context, bookID int64) ([]database.BookDestination, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) GetBookDestination(ctx context.Context, bookID int64, destID string) (*database.BookDestination, error) {
	return nil, nil
}
func (s *settingsOnlyStubDB) ListBookDestinationsBy(ctx context.Context, destID string, state *database.BookDestinationSyncState) ([]database.BookDestination, error) {
	return nil, nil
}
