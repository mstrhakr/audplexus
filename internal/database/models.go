package database

import (
	"fmt"
	"time"
)

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

// LibraryDestinationType identifies a destination kind. Matches the `type`
// column on library_destinations and the Go-side Backend.Type() return.
type LibraryDestinationType string

const (
	LibraryDestinationTypePlex     LibraryDestinationType = "plex"
	LibraryDestinationTypeEmby     LibraryDestinationType = "emby"
	LibraryDestinationTypeJellyfin LibraryDestinationType = "jellyfin"
	LibraryDestinationTypeABS      LibraryDestinationType = "abs"
)

// LibraryDestination is one configured library destination row. Sensitive
// fields (PlexToken, APIKey) MUST be redacted by callers when serializing
// for logs or API responses.
type LibraryDestination struct {
	ID          string                 `json:"id"`
	DisplayName string                 `json:"display_name"`
	Type        LibraryDestinationType `json:"type"`
	Enabled     bool                   `json:"enabled"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`

	// Per-type config. NULL/empty for non-matching types.
	URL             string `json:"url,omitempty"`
	APIKey          string `json:"-"` // sensitive; never marshal
	PlexToken       string `json:"-"` // sensitive; never marshal
	PlexSectionID   string `json:"plex_section_id,omitempty"`
	LibraryID       string `json:"library_id,omitempty"`
	AudiobookPath   string `json:"audiobook_path,omitempty"`
	DestinationPath string `json:"destination_path,omitempty"`

	LastHealthCheckAt  *time.Time `json:"last_health_check_at,omitempty"`
	LastHealthCheckOK  *bool      `json:"last_health_check_ok,omitempty"`
	LastHealthCheckErr string     `json:"last_health_check_err,omitempty"`
}

// HasAPIKey reports whether the destination has an API key set without
// exposing the value. Used by Settings UI to show "configured" badges
// without leaking the secret.
func (d *LibraryDestination) HasAPIKey() bool { return d.APIKey != "" }

// HasPlexToken reports whether the destination has a Plex token set.
func (d *LibraryDestination) HasPlexToken() bool { return d.PlexToken != "" }

// String redacts secrets in default fmt formatting. `json:"-"` covers
// json.Marshal but Printf("%+v", dest) would still expose APIKey and
// PlexToken via reflection. This Stringer wins for %v/%s; %+v falls
// back to the GoStringer below for completeness.
func (d LibraryDestination) String() string {
	return fmt.Sprintf("LibraryDestination{ID:%s Type:%s DisplayName:%q Enabled:%t URL:%s LibraryID:%s APIKey:%s PlexToken:%s}",
		d.ID, d.Type, d.DisplayName, d.Enabled, d.URL, d.LibraryID,
		redactToken(d.APIKey), redactToken(d.PlexToken))
}

// GoString covers the %#v verb used by some loggers. Same redaction.
func (d LibraryDestination) GoString() string { return d.String() }

func redactToken(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<redacted>"
}

// BookDestinationSyncState is the per-(book, destination) state machine.
type BookDestinationSyncState string

const (
	BookDestSyncPending                BookDestinationSyncState = "pending"
	BookDestSyncSyncing                BookDestinationSyncState = "syncing"
	BookDestSyncSynced                 BookDestinationSyncState = "synced"
	BookDestSyncFailed                 BookDestinationSyncState = "failed"
	BookDestSyncOrphaned               BookDestinationSyncState = "orphaned"
	BookDestSyncRemovedFromDestination BookDestinationSyncState = "removed_from_destination"
)

// BookDestination is one (book, library destination) row tracking per-pair
// state. Replaces the legacy 1:1 books.media_server_id columns.
type BookDestination struct {
	BookID          int64                    `json:"book_id"`
	DestinationID   string                   `json:"destination_id"`
	ServerItemID    string                   `json:"server_item_id,omitempty"`
	ServerItemTitle string                   `json:"server_item_title,omitempty"`
	SyncState       BookDestinationSyncState `json:"sync_state"`
	LastAttemptedAt *time.Time               `json:"last_attempted_at,omitempty"`
	LastSucceededAt *time.Time               `json:"last_succeeded_at,omitempty"`
	LastError       string                   `json:"last_error,omitempty"`
	AttemptCount    int                      `json:"attempt_count"`
	DisabledReason  string                   `json:"disabled_reason,omitempty"`
	// PerOpOutcomes is JSON-encoded {operation: {status, at, detail}, ...}.
	// Empty string when no outcomes recorded yet.
	PerOpOutcomes string `json:"per_op_outcomes,omitempty"`
}

