package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

// SQLiteDB implements Database using SQLite.
type SQLiteDB struct {
	db *sql.DB
}

// NewSQLite opens a SQLite database at the given path and returns a Database.
func NewSQLite(path string) (*SQLiteDB, error) {
	// modernc.org/sqlite reads PRAGMAs via the ?_pragma= URI param. The older
	// ?_foreign_keys=on syntax is silently ignored by this driver, so FK
	// CASCADE actions never fired before — preventing book_library_destinations
	// rows from being cleaned up on book delete (caught by PR-B's FK test).
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode%3DWAL&_pragma=foreign_keys%3DON")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite doesn't handle concurrent writes well
	return &SQLiteDB{db: db}, nil
}

func (s *SQLiteDB) Close() error {
	return s.db.Close()
}

func (s *SQLiteDB) Reset(ctx context.Context) error {
	tables := []string{"download_queue", "sync_history", "settings", "devices", "books"}
	for _, t := range tables {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return fmt.Errorf("reset table %s: %w", t, err)
		}
	}
	return nil
}

func (s *SQLiteDB) Migrate() error {
	sourceDriver, err := iofs.New(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}
	dbDriver, err := sqlite.WithInstance(s.db, &sqlite.Config{})
	if err != nil {
		return fmt.Errorf("create migration db driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", dbDriver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// --- Books ---

func (s *SQLiteDB) GetBook(ctx context.Context, id int64) (*Book, error) {
	return s.scanBook(s.db.QueryRowContext(ctx,
		`SELECT id, asin, title, author, author_asin, narrator, publisher, description,
		        duration, series, series_position, cover_url, purchase_date, release_date,
		        drm_type, status, file_path, file_size,
		        created_at, updated_at
		 FROM books WHERE id = ?`, id))
}

func (s *SQLiteDB) GetBookByASIN(ctx context.Context, asin string) (*Book, error) {
	return s.scanBook(s.db.QueryRowContext(ctx,
		`SELECT id, asin, title, author, author_asin, narrator, publisher, description,
		        duration, series, series_position, cover_url, purchase_date, release_date,
		        drm_type, status, file_path, file_size,
		        created_at, updated_at
		 FROM books WHERE asin = ?`, asin))
}

func (s *SQLiteDB) ListBooks(ctx context.Context, filter BookFilter) ([]Book, int, error) {
	where, args := buildBookWhere(filter)

	// Count
	var total int
	countQuery := "SELECT COUNT(*) FROM books" + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count books: %w", err)
	}

	// Query
	orderBy := " ORDER BY purchase_date DESC"
	if filter.SortBy != "" {
		col := sanitizeSortColumn(filter.SortBy)
		dir := "ASC"
		if strings.EqualFold(filter.SortDir, "desc") {
			dir = "DESC"
		}
		orderBy = fmt.Sprintf(" ORDER BY %s %s", col, dir)
	}

	limit := ""
	if filter.Limit > 0 {
		limit = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	offset := ""
	if filter.Offset > 0 {
		offset = fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	query := `SELECT id, asin, title, author, author_asin, narrator, publisher, description,
	                 duration, series, series_position, cover_url, purchase_date, release_date,
	                 drm_type, status, file_path, file_size,
	                 created_at, updated_at
	          FROM books` + where + orderBy + limit + offset

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list books: %w", err)
	}
	defer rows.Close()

	var books []Book
	for rows.Next() {
		b, err := s.scanBookRow(rows)
		if err != nil {
			return nil, 0, err
		}
		books = append(books, *b)
	}
	return books, total, rows.Err()
}

func (s *SQLiteDB) UpsertBook(ctx context.Context, book *Book) error {
	now := time.Now()
	book.UpdatedAt = now
	if book.CreatedAt.IsZero() {
		book.CreatedAt = now
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO books (asin, title, author, author_asin, narrator, publisher, description,
		                    duration, series, series_position, cover_url, purchase_date, release_date,
		                    drm_type, status, file_path, file_size, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(asin) DO UPDATE SET
		    title=excluded.title, author=excluded.author, author_asin=excluded.author_asin,
		    narrator=excluded.narrator, publisher=excluded.publisher, description=excluded.description,
		    duration=excluded.duration, series=excluded.series, series_position=excluded.series_position,
		    cover_url=excluded.cover_url, purchase_date=excluded.purchase_date, release_date=excluded.release_date,
		    drm_type=excluded.drm_type, status=excluded.status, file_path=excluded.file_path,
		    file_size=excluded.file_size, updated_at=excluded.updated_at`,
		book.ASIN, book.Title, book.Author, book.AuthorASIN, book.Narrator, book.Publisher,
		book.Description, book.Duration, book.Series, book.SeriesPosition, book.CoverURL,
		book.PurchaseDate, book.ReleaseDate, book.DRMType, book.Status, book.FilePath,
		book.FileSize, book.CreatedAt, book.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert book: %w", err)
	}

	if book.ID == 0 {
		id, _ := result.LastInsertId()
		book.ID = id
	}
	return nil
}

func (s *SQLiteDB) UpdateBookStatus(ctx context.Context, id int64, status BookStatus) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE books SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now(), id)
	return err
}

func (s *SQLiteDB) DeleteBook(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, id)
	return err
}

// --- Download Queue ---

func (s *SQLiteDB) EnqueueDownload(ctx context.Context, item *DownloadQueue) error {
	now := time.Now()
	item.CreatedAt = now
	item.UpdatedAt = now
	if strings.TrimSpace(string(item.Status)) == "" {
		item.Status = DownloadStatusPending
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO download_queue (book_id, asin, priority, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		item.BookID, item.ASIN, item.Priority, item.Status, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("enqueue download: %w", err)
	}
	item.ID, _ = result.LastInsertId()
	return nil
}

func (s *SQLiteDB) GetNextPendingDownload(ctx context.Context) (*DownloadQueue, error) {
	// Atomically claim the next pending item by updating its status to active
	// in the same transaction, preventing duplicate processing by concurrent workers.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx,
		`SELECT id, book_id, asin, priority, status, progress, error, started_at, completed_at, created_at, updated_at
		 FROM download_queue WHERE status IN (?, ?) ORDER BY priority DESC, created_at ASC LIMIT 1`,
		DownloadStatusPending, DownloadStatusReorganize)

	var d DownloadQueue
	if err := row.Scan(&d.ID, &d.BookID, &d.ASIN, &d.Priority, &d.Status, &d.Progress,
		&d.Error, &d.StartedAt, &d.CompletedAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan download: %w", err)
	}

	now := time.Now()
	claimStatus := DownloadStatusActive
	if d.Status == DownloadStatusReorganize {
		claimStatus = DownloadStatusReorganizing
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE download_queue SET status = ?, started_at = ?, updated_at = ? WHERE id = ?`,
		claimStatus, now, now, d.ID)
	if err != nil {
		return nil, fmt.Errorf("claim download: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	d.Status = claimStatus
	d.StartedAt = &now
	return &d, nil
}

func (s *SQLiteDB) UpdateDownload(ctx context.Context, item *DownloadQueue) error {
	item.UpdatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE download_queue SET status = ?, progress = ?, error = ?, started_at = ?, completed_at = ?, updated_at = ?
		 WHERE id = ?`,
		item.Status, item.Progress, item.Error, item.StartedAt, item.CompletedAt, item.UpdatedAt, item.ID)
	return err
}

func (s *SQLiteDB) ListDownloads(ctx context.Context, status *DownloadStatus) ([]DownloadQueue, error) {
	query := `SELECT id, book_id, asin, priority, status, progress, error, started_at, completed_at, created_at, updated_at
	          FROM download_queue`
	var args []interface{}
	if status != nil {
		query += " WHERE status = ?"
		args = append(args, *status)
	}
	query += " ORDER BY priority DESC, created_at ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list downloads: %w", err)
	}
	defer rows.Close()

	var items []DownloadQueue
	for rows.Next() {
		var d DownloadQueue
		if err := rows.Scan(&d.ID, &d.BookID, &d.ASIN, &d.Priority, &d.Status, &d.Progress,
			&d.Error, &d.StartedAt, &d.CompletedAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan download: %w", err)
		}
		items = append(items, d)
	}
	return items, rows.Err()
}

func (s *SQLiteDB) CancelDownload(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE download_queue SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		DownloadStatusCancelled, time.Now(), id, DownloadStatusPending)
	return err
}

func (s *SQLiteDB) RetryDownload(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("retry download: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Look up the queue entry to get ASIN and book_id.
	var asin string
	var bookID int64
	var priority int
	err = tx.QueryRowContext(ctx,
		`SELECT asin, book_id, priority FROM download_queue WHERE id = ? AND status = ?`,
		id, DownloadStatusFailed).Scan(&asin, &bookID, &priority)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil // already retried or not found
		}
		return fmt.Errorf("retry download: lookup: %w", err)
	}

	now := time.Now()
	// Cancel ALL failed entries for this ASIN to prevent duplicate queuing.
	_, err = tx.ExecContext(ctx,
		`UPDATE download_queue SET status = ?, updated_at = ? WHERE asin = ? AND status = ?`,
		DownloadStatusCancelled, now, asin, DownloadStatusFailed)
	if err != nil {
		return fmt.Errorf("retry download: cancel duplicates: %w", err)
	}

	// Insert a single fresh pending entry.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO download_queue (book_id, asin, priority, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		bookID, asin, priority, DownloadStatusPending, now, now)
	if err != nil {
		return fmt.Errorf("retry download: enqueue: %w", err)
	}

	// Reset book status to queued.
	_, err = tx.ExecContext(ctx,
		`UPDATE books SET status = ?, updated_at = ? WHERE id = ?`,
		BookStatusQueued, now, bookID)
	if err != nil {
		return fmt.Errorf("retry download: reset book: %w", err)
	}

	return tx.Commit()
}

func (s *SQLiteDB) RetryAllDownloads(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("retry all: begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()

	// Collect one queue entry per unique ASIN from failed rows (for book_id + priority).
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT asin, book_id, MAX(priority) as priority
		 FROM download_queue WHERE status = ?
		 GROUP BY asin, book_id`,
		DownloadStatusFailed)
	if err != nil {
		return 0, fmt.Errorf("retry all: list failed: %w", err)
	}
	type entry struct {
		asin     string
		bookID   int64
		priority int
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.asin, &e.bookID, &e.priority); err != nil {
			rows.Close()
			return 0, fmt.Errorf("retry all: scan: %w", err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Skip ASINs that already have a pending or active queue entry.
	var toRetry []entry
	for _, e := range entries {
		var count int
		_ = tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM download_queue WHERE asin = ? AND status IN (?, ?)`,
			e.asin, DownloadStatusPending, DownloadStatusActive).Scan(&count)
		if count == 0 {
			toRetry = append(toRetry, e)
		}
	}

	for _, e := range toRetry {
		// Cancel all failed entries for this ASIN.
		if _, err := tx.ExecContext(ctx,
			`UPDATE download_queue SET status = ?, updated_at = ? WHERE asin = ? AND status = ?`,
			DownloadStatusCancelled, now, e.asin, DownloadStatusFailed); err != nil {
			return 0, fmt.Errorf("retry all: cancel %s: %w", e.asin, err)
		}
		// Insert one fresh pending entry.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO download_queue (book_id, asin, priority, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			e.bookID, e.asin, e.priority, DownloadStatusPending, now, now); err != nil {
			return 0, fmt.Errorf("retry all: enqueue %s: %w", e.asin, err)
		}
		// Reset book status.
		if _, err := tx.ExecContext(ctx,
			`UPDATE books SET status = ?, updated_at = ? WHERE id = ?`,
			BookStatusQueued, now, e.bookID); err != nil {
			return 0, fmt.Errorf("retry all: reset book %s: %w", e.asin, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(toRetry)), nil
}

// --- Sync History ---

func (s *SQLiteDB) CreateSync(ctx context.Context, sync *SyncHistory) error {
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO sync_history (started_at, status) VALUES (?, ?)`,
		sync.StartedAt, sync.Status)
	if err != nil {
		return fmt.Errorf("create sync: %w", err)
	}
	sync.ID, _ = result.LastInsertId()
	return nil
}

func (s *SQLiteDB) UpdateSync(ctx context.Context, sync *SyncHistory) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sync_history SET completed_at = ?, books_found = ?, books_added = ?, status = ?, error = ?
		 WHERE id = ?`,
		sync.CompletedAt, sync.BooksFound, sync.BooksAdded, sync.Status, sync.Error, sync.ID)
	return err
}

func (s *SQLiteDB) GetLastSync(ctx context.Context) (*SyncHistory, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, started_at, completed_at, books_found, books_added, status, error
		 FROM sync_history ORDER BY id DESC LIMIT 1`)
	var sh SyncHistory
	err := row.Scan(&sh.ID, &sh.StartedAt, &sh.CompletedAt, &sh.BooksFound, &sh.BooksAdded, &sh.Status, &sh.Error)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get last sync: %w", err)
	}
	return &sh, nil
}

// --- Settings ---

func (s *SQLiteDB) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *SQLiteDB) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now())
	return err
}

// --- Devices ---

func (s *SQLiteDB) GetActiveDevice(ctx context.Context) (*Device, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, marketplace, credentials, is_active, created_at, updated_at
		 FROM devices WHERE is_active = 1 LIMIT 1`)
	return s.scanDevice(row)
}

func (s *SQLiteDB) SaveDevice(ctx context.Context, device *Device) error {
	now := time.Now()
	device.UpdatedAt = now
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}

	// Deactivate all if this one is active
	if device.IsActive {
		if _, err := s.db.ExecContext(ctx, `UPDATE devices SET is_active = 0`); err != nil {
			return fmt.Errorf("deactivate devices: %w", err)
		}
	}

	if device.ID == 0 {
		result, err := s.db.ExecContext(ctx,
			`INSERT INTO devices (name, marketplace, credentials, is_active, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			device.Name, device.Marketplace, device.Credentials, device.IsActive, device.CreatedAt, device.UpdatedAt)
		if err != nil {
			return fmt.Errorf("insert device: %w", err)
		}
		device.ID, _ = result.LastInsertId()
	} else {
		_, err := s.db.ExecContext(ctx,
			`UPDATE devices SET name = ?, marketplace = ?, credentials = ?, is_active = ?, updated_at = ?
			 WHERE id = ?`,
			device.Name, device.Marketplace, device.Credentials, device.IsActive, device.UpdatedAt, device.ID)
		if err != nil {
			return fmt.Errorf("update device: %w", err)
		}
	}
	return nil
}

func (s *SQLiteDB) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, marketplace, credentials, is_active, created_at, updated_at FROM devices`)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.Name, &d.Marketplace, &d.Credentials, &d.IsActive, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func (s *SQLiteDB) DeleteDevice(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	return err
}

// --- Helpers ---

func (s *SQLiteDB) scanBook(row *sql.Row) (*Book, error) {
	var b Book
	err := row.Scan(&b.ID, &b.ASIN, &b.Title, &b.Author, &b.AuthorASIN, &b.Narrator,
		&b.Publisher, &b.Description, &b.Duration, &b.Series, &b.SeriesPosition,
		&b.CoverURL, &b.PurchaseDate, &b.ReleaseDate, &b.DRMType, &b.Status,
		&b.FilePath, &b.FileSize,
		&b.CreatedAt, &b.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan book: %w", err)
	}
	return &b, nil
}

func (s *SQLiteDB) scanBookRow(rows *sql.Rows) (*Book, error) {
	var b Book
	err := rows.Scan(&b.ID, &b.ASIN, &b.Title, &b.Author, &b.AuthorASIN, &b.Narrator,
		&b.Publisher, &b.Description, &b.Duration, &b.Series, &b.SeriesPosition,
		&b.CoverURL, &b.PurchaseDate, &b.ReleaseDate, &b.DRMType, &b.Status,
		&b.FilePath, &b.FileSize,
		&b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("scan book row: %w", err)
	}
	return &b, nil
}

func (s *SQLiteDB) scanDownload(row *sql.Row) (*DownloadQueue, error) {
	var d DownloadQueue
	err := row.Scan(&d.ID, &d.BookID, &d.ASIN, &d.Priority, &d.Status, &d.Progress,
		&d.Error, &d.StartedAt, &d.CompletedAt, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan download: %w", err)
	}
	return &d, nil
}

func (s *SQLiteDB) scanDevice(row *sql.Row) (*Device, error) {
	var d Device
	err := row.Scan(&d.ID, &d.Name, &d.Marketplace, &d.Credentials, &d.IsActive, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan device: %w", err)
	}
	return &d, nil
}

var allowedSortColumns = map[string]string{
	"title":         "title",
	"author":        "author",
	"purchase_date": "purchase_date",
	"status":        "status",
	"created_at":    "created_at",
	"duration":      "duration",
}

func sanitizeSortColumn(col string) string {
	if mapped, ok := allowedSortColumns[col]; ok {
		return mapped
	}
	return "purchase_date"
}

func buildBookWhere(filter BookFilter) (string, []interface{}) {
	var clauses []string
	var args []interface{}

	if filter.Status != nil {
		clauses = append(clauses, "status = ?")
		args = append(args, *filter.Status)
	}
	if filter.Search != "" {
		clauses = append(clauses, "(title LIKE ? OR author LIKE ?)")
		search := "%" + filter.Search + "%"
		args = append(args, search, search)
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

