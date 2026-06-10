package database

import (
	"context"
	"time"
)

// Database defines the storage interface for the application.
// Implementations exist for both SQLite and PostgreSQL.
type Database interface {
	// Lifecycle
	Close() error
	Migrate() error
	Reset(ctx context.Context) error

	// Books
	GetBook(ctx context.Context, id int64) (*Book, error)
	GetBookByASIN(ctx context.Context, asin string) (*Book, error)
	ListBooks(ctx context.Context, filter BookFilter) ([]Book, int, error)
	// CountBooksByStatus returns the row count of each book status in
	// one query. Keys are the BookStatus values present in the table;
	// statuses with zero books are omitted. Used by the library page's
	// filter tabs so we don't fire 7 LIMIT-1 counts per page render.
	CountBooksByStatus(ctx context.Context) (map[BookStatus]int, error)
	UpsertBook(ctx context.Context, book *Book) error
	UpdateBookStatus(ctx context.Context, id int64, status BookStatus) error
	DeleteBook(ctx context.Context, id int64) error

	// Download Queue
	EnqueueDownload(ctx context.Context, item *DownloadQueue) error
	GetNextPendingDownload(ctx context.Context) (*DownloadQueue, error)
	UpdateDownload(ctx context.Context, item *DownloadQueue) error
	ListDownloads(ctx context.Context, status *DownloadStatus) ([]DownloadQueue, error)
	CancelDownload(ctx context.Context, id int64) error
	RetryDownload(ctx context.Context, id int64) error
	RetryAllDownloads(ctx context.Context) (int64, error)

	// Sync History
	CreateSync(ctx context.Context, sync *SyncHistory) error
	UpdateSync(ctx context.Context, sync *SyncHistory) error
	GetLastSync(ctx context.Context) (*SyncHistory, error)

	// Settings
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error

	// Devices
	GetActiveDevice(ctx context.Context) (*Device, error)
	SaveDevice(ctx context.Context, device *Device) error
	ListDevices(ctx context.Context) ([]Device, error)
	DeleteDevice(ctx context.Context, id int64) error

	// Audible accounts (multi-account model). Most installs have one; the
	// schema + UI support several, each with its own credentials/marketplace.
	CreateAudibleAccount(ctx context.Context, a *AudibleAccount) error
	GetAudibleAccount(ctx context.Context, id string) (*AudibleAccount, error)
	GetAudibleAccountByCustomerID(ctx context.Context, customerID string) (*AudibleAccount, error)
	ListAudibleAccounts(ctx context.Context) ([]AudibleAccount, error)
	ListEnabledAudibleAccounts(ctx context.Context) ([]AudibleAccount, error)
	UpdateAudibleAccount(ctx context.Context, a *AudibleAccount) error
	DeleteAudibleAccount(ctx context.Context, id string) error

	// Per-book owning account. Kept off the hot book-scan paths; resolve on
	// demand. SetBookAccount stamps the owning account during sync;
	// GetBookAccount resolves it for download routing.
	SetBookAccount(ctx context.Context, asin, accountID string) error
	GetBookAccount(ctx context.Context, asin string) (string, error)

	// Library destinations (multi-destination model — replaces single-active
	// MEDIA_SERVER selector). Multiple destinations of the same type are
	// allowed.
	CreateLibraryDestination(ctx context.Context, d *LibraryDestination) error
	GetLibraryDestination(ctx context.Context, id string) (*LibraryDestination, error)
	ListLibraryDestinations(ctx context.Context) ([]LibraryDestination, error)
	ListEnabledLibraryDestinations(ctx context.Context) ([]LibraryDestination, error)
	UpdateLibraryDestination(ctx context.Context, d *LibraryDestination) error
	DeleteLibraryDestination(ctx context.Context, id string) error

	// Auth: single-admin Forms login + cookie sessions. The schema supports
	// multiple users but the UI exposes one. Identifier rotation on the
	// users row invalidates every session whose snapshot no longer matches.
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByID(ctx context.Context, id int64) (*User, error)
	// GetFirstUser returns the oldest user row (lowest id), or nil when no
	// users exist. Used by the single-admin UI to surface the admin without
	// hardcoding id=1 (which breaks if the row was ever rotated).
	GetFirstUser(ctx context.Context) (*User, error)
	CountUsers(ctx context.Context) (int, error)
	UpsertUser(ctx context.Context, user *User) error
	RotateUserIdentifier(ctx context.Context, userID int64, newIdentifier string) error
	DeleteUser(ctx context.Context, id int64) error

	CreateSession(ctx context.Context, session *Session) error
	GetSession(ctx context.Context, token string) (*Session, error)
	TouchSession(ctx context.Context, token string, lastSeen time.Time) error
	DeleteSession(ctx context.Context, token string) error
	DeleteSessionsForUser(ctx context.Context, userID int64) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)

	// Per-(book, destination) state. Used by the post-organize fan-out
	// (recording outcomes) and reconcile (matching server-side items back
	// to local books).
	UpsertBookDestination(ctx context.Context, bd *BookDestination) error
	GetBookDestinations(ctx context.Context, bookID int64) ([]BookDestination, error)
	GetBookDestination(ctx context.Context, bookID int64, destinationID string) (*BookDestination, error)
	ListBookDestinationsBy(ctx context.Context, destinationID string, state *BookDestinationSyncState) ([]BookDestination, error)
}

// BookFilter defines parameters for listing books.
type BookFilter struct {
	Status *BookStatus
	// ExcludeStatuses filters out rows whose status matches any listed value.
	// Used by derived UI buckets such as "available" = all minus unavailable.
	ExcludeStatuses []BookStatus
	Search          string
	SortBy          string // "title", "author", "purchase_date", "status"
	SortDir         string // "asc", "desc"
	Limit           int
	Offset          int

	// Presence filters. Each is independent and stacks with the others;
	// an empty/nil value means "no filter on this dimension".
	//
	// OnDisk filters on whether books.file_path is non-empty. nil means
	// either; true keeps only on-disk rows; false keeps only rows whose
	// file is missing locally.
	//
	// PresentInDestinations keeps books that ARE currently synced to
	// every listed destination (sync_state in synced|syncing).
	// MissingFromDestinations keeps books NOT currently synced to any
	// of the listed destinations. The two slices stack — a book is
	// included only when it satisfies every per-destination predicate.
	OnDisk                  *bool
	PresentInDestinations   []string
	MissingFromDestinations []string
}
