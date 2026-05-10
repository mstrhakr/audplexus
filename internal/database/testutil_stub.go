package database

import "context"

// StubDB is the minimal Database implementation used by:
//   - mediaserver tests that only need GetSetting/SetSetting
//   - cmd/abstest and cmd/jellytest live verification programs
//
// Only the methods backend code actually exercises return useful values.
// Everything else returns zero values so the type satisfies the
// Database interface without forcing every caller to stub 30+ methods.
//
// Production code MUST NOT use StubDB. Real *SQLiteDB or *PostgresDB
// only.
type StubDB struct {
	settings map[string]string
}

// NewStubDB returns a StubDB ready to accept GetSetting/SetSetting.
func NewStubDB() *StubDB {
	return &StubDB{settings: map[string]string{}}
}

// SeedSettings prepopulates the settings map. Useful when constructing
// a backend with a known config from a test or live-probe binary.
func (s *StubDB) SeedSettings(kv map[string]string) {
	for k, v := range kv {
		s.settings[k] = v
	}
}

func (s *StubDB) GetSetting(ctx context.Context, key string) (string, error) {
	return s.settings[key], nil
}
func (s *StubDB) SetSetting(ctx context.Context, key, value string) error {
	s.settings[key] = value
	return nil
}

// --- Lifecycle ---
func (s *StubDB) Close() error                  { return nil }
func (s *StubDB) Migrate() error                { return nil }
func (s *StubDB) Reset(ctx context.Context) error { return nil }

// --- Books ---
func (s *StubDB) GetBook(ctx context.Context, id int64) (*Book, error)         { return nil, nil }
func (s *StubDB) GetBookByASIN(ctx context.Context, asin string) (*Book, error) { return nil, nil }
func (s *StubDB) ListBooks(ctx context.Context, f BookFilter) ([]Book, int, error) {
	return nil, 0, nil
}
func (s *StubDB) UpsertBook(ctx context.Context, b *Book) error                       { return nil }
func (s *StubDB) UpdateBookStatus(ctx context.Context, id int64, st BookStatus) error { return nil }
func (s *StubDB) UpdateBookPlexInfo(ctx context.Context, id int64, k, t string) error {
	return nil
}
func (s *StubDB) UpdateBookMediaServerInfo(ctx context.Context, id int64, k, t string) error {
	return nil
}
func (s *StubDB) DeleteBook(ctx context.Context, id int64) error { return nil }

// --- Download Queue ---
func (s *StubDB) EnqueueDownload(ctx context.Context, i *DownloadQueue) error { return nil }
func (s *StubDB) GetNextPendingDownload(ctx context.Context) (*DownloadQueue, error) {
	return nil, nil
}
func (s *StubDB) UpdateDownload(ctx context.Context, i *DownloadQueue) error { return nil }
func (s *StubDB) ListDownloads(ctx context.Context, st *DownloadStatus) ([]DownloadQueue, error) {
	return nil, nil
}
func (s *StubDB) CancelDownload(ctx context.Context, id int64) error  { return nil }
func (s *StubDB) RetryDownload(ctx context.Context, id int64) error   { return nil }
func (s *StubDB) RetryAllDownloads(ctx context.Context) (int64, error) { return 0, nil }

// --- Sync ---
func (s *StubDB) CreateSync(ctx context.Context, sh *SyncHistory) error  { return nil }
func (s *StubDB) UpdateSync(ctx context.Context, sh *SyncHistory) error  { return nil }
func (s *StubDB) GetLastSync(ctx context.Context) (*SyncHistory, error)  { return nil, nil }

// --- Devices ---
func (s *StubDB) GetActiveDevice(ctx context.Context) (*Device, error)   { return nil, nil }
func (s *StubDB) SaveDevice(ctx context.Context, d *Device) error        { return nil }
func (s *StubDB) ListDevices(ctx context.Context) ([]Device, error)      { return nil, nil }
func (s *StubDB) DeleteDevice(ctx context.Context, id int64) error       { return nil }

// --- Library destinations ---
func (s *StubDB) CreateLibraryDestination(ctx context.Context, d *LibraryDestination) error {
	return nil
}
func (s *StubDB) GetLibraryDestination(ctx context.Context, id string) (*LibraryDestination, error) {
	return nil, nil
}
func (s *StubDB) ListLibraryDestinations(ctx context.Context) ([]LibraryDestination, error) {
	return nil, nil
}
func (s *StubDB) ListEnabledLibraryDestinations(ctx context.Context) ([]LibraryDestination, error) {
	return nil, nil
}
func (s *StubDB) UpdateLibraryDestination(ctx context.Context, d *LibraryDestination) error {
	return nil
}
func (s *StubDB) DeleteLibraryDestination(ctx context.Context, id string) error { return nil }
func (s *StubDB) UpsertBookDestination(ctx context.Context, bd *BookDestination) error {
	return nil
}
func (s *StubDB) GetBookDestinations(ctx context.Context, bookID int64) ([]BookDestination, error) {
	return nil, nil
}
func (s *StubDB) GetBookDestination(ctx context.Context, bookID int64, destID string) (*BookDestination, error) {
	return nil, nil
}
func (s *StubDB) ListBookDestinationsBy(ctx context.Context, destID string, st *BookDestinationSyncState) ([]BookDestination, error) {
	return nil, nil
}
