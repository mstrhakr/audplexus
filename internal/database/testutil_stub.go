package database

import (
	"context"
	"time"
)

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
func (s *StubDB) CountBooksByStatus(ctx context.Context) (map[BookStatus]int, error) { return nil, nil }
func (s *StubDB) UpsertBook(ctx context.Context, b *Book) error                       { return nil }
func (s *StubDB) UpdateBookStatus(ctx context.Context, id int64, st BookStatus) error { return nil }
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

// --- Audible accounts ---
func (s *StubDB) CreateAudibleAccount(ctx context.Context, a *AudibleAccount) error { return nil }
func (s *StubDB) GetAudibleAccount(ctx context.Context, id string) (*AudibleAccount, error) {
	return nil, nil
}
func (s *StubDB) GetAudibleAccountByCustomerID(ctx context.Context, customerID string) (*AudibleAccount, error) {
	return nil, nil
}
func (s *StubDB) ListAudibleAccounts(ctx context.Context) ([]AudibleAccount, error) { return nil, nil }
func (s *StubDB) ListEnabledAudibleAccounts(ctx context.Context) ([]AudibleAccount, error) {
	return nil, nil
}
func (s *StubDB) UpdateAudibleAccount(ctx context.Context, a *AudibleAccount) error { return nil }
func (s *StubDB) DeleteAudibleAccount(ctx context.Context, id string) error         { return nil }
func (s *StubDB) SetBookAccount(ctx context.Context, asin, accountID string) error  { return nil }
func (s *StubDB) GetBookAccount(ctx context.Context, asin string) (string, error)   { return "", nil }

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

// Auth stubs — empty defaults so middleware sees "no admin yet" and (
// because auth_method falls back to "none" via settings) is permissive.
func (s *StubDB) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return nil, nil
}
func (s *StubDB) GetUserByID(ctx context.Context, id int64) (*User, error)   { return nil, nil }
func (s *StubDB) GetFirstUser(ctx context.Context) (*User, error)            { return nil, nil }
func (s *StubDB) CountUsers(ctx context.Context) (int, error)                { return 0, nil }
func (s *StubDB) UpsertUser(ctx context.Context, user *User) error           { return nil }
func (s *StubDB) RotateUserIdentifier(ctx context.Context, id int64, ident string) error {
	return nil
}
func (s *StubDB) DeleteUser(ctx context.Context, id int64) error             { return nil }
func (s *StubDB) CreateSession(ctx context.Context, sess *Session) error     { return nil }
func (s *StubDB) GetSession(ctx context.Context, token string) (*Session, error) { return nil, nil }
func (s *StubDB) TouchSession(ctx context.Context, token string, t time.Time) error {
	return nil
}
func (s *StubDB) DeleteSession(ctx context.Context, token string) error        { return nil }
func (s *StubDB) DeleteSessionsForUser(ctx context.Context, userID int64) error { return nil }
func (s *StubDB) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	return 0, nil
}
