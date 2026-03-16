package database

import "context"

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
	UpsertBook(ctx context.Context, book *Book) error
	UpdateBookStatus(ctx context.Context, id int64, status BookStatus) error
	UpdateBookPlexInfo(ctx context.Context, id int64, plexRatingKey, plexTitle string) error
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
}

// BookFilter defines parameters for listing books.
type BookFilter struct {
	Status  *BookStatus
	Search  string
	SortBy  string // "title", "author", "purchase_date", "status"
	SortDir string // "asc", "desc"
	Limit   int
	Offset  int
}
