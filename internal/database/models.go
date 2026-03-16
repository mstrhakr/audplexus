package database

import "time"

// Book represents an audiobook in the local database.
type Book struct {
	ID             int64      `json:"id"`
	ASIN           string     `json:"asin"`
	Title          string     `json:"title"`
	Author         string     `json:"author"`
	AuthorASIN     string     `json:"author_asin"`
	Narrator       string     `json:"narrator"`
	Publisher      string     `json:"publisher"`
	Language       string     `json:"language"`
	Description    string     `json:"description"`
	Duration       int64      `json:"duration"` // seconds
	Series         string     `json:"series"`
	SeriesPosition string     `json:"series_position"`
	CoverURL       string     `json:"cover_url"`
	PurchaseDate   time.Time  `json:"purchase_date"`
	ReleaseDate    time.Time  `json:"release_date"`
	DRMType        string     `json:"drm_type"` // "Adrm" or "Mpeg"
	Status         BookStatus `json:"status"`
	FilePath       string     `json:"file_path"`
	FileSize       int64      `json:"file_size"`
	PlexRatingKey  string     `json:"plex_rating_key"`
	PlexTitle      string     `json:"plex_title"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// BookStatus represents the processing state of a book.
type BookStatus string

const (
	BookStatusNew         BookStatus = "new"
	BookStatusQueued      BookStatus = "queued"
	BookStatusDownloading BookStatus = "downloading"
	BookStatusDecrypting  BookStatus = "decrypting"
	BookStatusProcessing  BookStatus = "processing"
	BookStatusComplete    BookStatus = "complete"
	BookStatusFailed      BookStatus = "failed"
	BookStatusSkipped     BookStatus = "skipped"
)

// DownloadQueue represents a queued download job.
type DownloadQueue struct {
	ID          int64          `json:"id"`
	BookID      int64          `json:"book_id"`
	ASIN        string         `json:"asin"`
	Priority    int            `json:"priority"`
	Status      DownloadStatus `json:"status"`
	Progress    float64        `json:"progress"` // 0.0 - 1.0
	Error       string         `json:"error"`
	StartedAt   *time.Time     `json:"started_at"`
	CompletedAt *time.Time     `json:"completed_at"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// DownloadStatus represents the state of a download job.
type DownloadStatus string

const (
	DownloadStatusPending   DownloadStatus = "pending"
	DownloadStatusActive    DownloadStatus = "active"
	DownloadStatusComplete  DownloadStatus = "complete"
	DownloadStatusFailed    DownloadStatus = "failed"
	DownloadStatusCancelled DownloadStatus = "cancelled"
)

// SyncHistory records library sync operations.
type SyncHistory struct {
	ID          int64      `json:"id"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	BooksFound  int        `json:"books_found"`
	BooksAdded  int        `json:"books_added"`
	Status      string     `json:"status"`
	Error       string     `json:"error"`
}

// Setting is a key-value configuration entry.
type Setting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Device represents a registered Audible device.
type Device struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Marketplace string    `json:"marketplace"`
	Credentials []byte    `json:"credentials"` // encrypted JSON
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
