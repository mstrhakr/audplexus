package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"
)

// PostgresDB implements Database using PostgreSQL.
type PostgresDB struct {
	db *sql.DB
}

// NewPostgres opens a PostgreSQL connection and returns a Database.
func NewPostgres(dsn string) (*PostgresDB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	return &PostgresDB{db: db}, nil
}

func (p *PostgresDB) Close() error {
	return p.db.Close()
}

func (p *PostgresDB) Reset(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, `TRUNCATE books, download_queue, sync_history, settings, devices RESTART IDENTITY CASCADE`)
	if err != nil {
		return fmt.Errorf("reset postgres: %w", err)
	}
	return nil
}

func (p *PostgresDB) Migrate() error {
	sourceDriver, err := iofs.New(migrationsPostgres, "migrations_postgres")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}
	dbDriver, err := postgres.WithInstance(p.db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("create migration db driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", dbDriver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// --- Books ---

func (p *PostgresDB) GetBook(ctx context.Context, id int64) (*Book, error) {
	return p.scanBook(p.db.QueryRowContext(ctx,
		`SELECT id, asin, title, author, author_asin, narrator, publisher, description,
		        duration, series, series_position, cover_url, purchase_date, release_date,
		        drm_type, status, file_path, file_size, created_at, updated_at
		 FROM books WHERE id = $1`, id))
}

func (p *PostgresDB) GetBookByASIN(ctx context.Context, asin string) (*Book, error) {
	return p.scanBook(p.db.QueryRowContext(ctx,
		`SELECT id, asin, title, author, author_asin, narrator, publisher, description,
		        duration, series, series_position, cover_url, purchase_date, release_date,
		        drm_type, status, file_path, file_size, created_at, updated_at
		 FROM books WHERE asin = $1`, asin))
}

func (p *PostgresDB) ListBooks(ctx context.Context, filter BookFilter) ([]Book, int, error) {
	where, args := buildBookWherePostgres(filter)

	var total int
	countQuery := "SELECT COUNT(*) FROM books" + where
	if err := p.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count books: %w", err)
	}

	orderBy := " ORDER BY purchase_date DESC"
	if filter.SortBy != "" {
		col := sanitizeSortColumn(filter.SortBy)
		dir := "ASC"
		if strings.EqualFold(filter.SortDir, "desc") {
			dir = "DESC"
		}
		orderBy = fmt.Sprintf(" ORDER BY %s %s", col, dir)
	}

	paramIdx := len(args) + 1
	limit := ""
	if filter.Limit > 0 {
		limit = fmt.Sprintf(" LIMIT $%d", paramIdx)
		args = append(args, filter.Limit)
		paramIdx++
	}

	offset := ""
	if filter.Offset > 0 {
		offset = fmt.Sprintf(" OFFSET $%d", paramIdx)
		args = append(args, filter.Offset)
	}

	query := `SELECT id, asin, title, author, author_asin, narrator, publisher, description,
	                 duration, series, series_position, cover_url, purchase_date, release_date,
	                 drm_type, status, file_path, file_size, created_at, updated_at
	          FROM books` + where + orderBy + limit + offset

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list books: %w", err)
	}
	defer rows.Close()

	var books []Book
	for rows.Next() {
		b, err := p.scanBookRow(rows)
		if err != nil {
			return nil, 0, err
		}
		books = append(books, *b)
	}
	return books, total, rows.Err()
}

func (p *PostgresDB) UpsertBook(ctx context.Context, book *Book) error {
	now := time.Now()
	book.UpdatedAt = now
	if book.CreatedAt.IsZero() {
		book.CreatedAt = now
	}

	err := p.db.QueryRowContext(ctx,
		`INSERT INTO books (asin, title, author, author_asin, narrator, publisher, description,
		                    duration, series, series_position, cover_url, purchase_date, release_date,
		                    drm_type, status, file_path, file_size, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		 ON CONFLICT(asin) DO UPDATE SET
		    title=EXCLUDED.title, author=EXCLUDED.author, author_asin=EXCLUDED.author_asin,
		    narrator=EXCLUDED.narrator, publisher=EXCLUDED.publisher, description=EXCLUDED.description,
		    duration=EXCLUDED.duration, series=EXCLUDED.series, series_position=EXCLUDED.series_position,
		    cover_url=EXCLUDED.cover_url, purchase_date=EXCLUDED.purchase_date, release_date=EXCLUDED.release_date,
		    drm_type=EXCLUDED.drm_type, status=EXCLUDED.status, file_path=EXCLUDED.file_path,
		    file_size=EXCLUDED.file_size, updated_at=EXCLUDED.updated_at
		 RETURNING id`,
		book.ASIN, book.Title, book.Author, book.AuthorASIN, book.Narrator, book.Publisher,
		book.Description, book.Duration, book.Series, book.SeriesPosition, book.CoverURL,
		book.PurchaseDate, book.ReleaseDate, book.DRMType, book.Status, book.FilePath,
		book.FileSize, book.CreatedAt, book.UpdatedAt).Scan(&book.ID)
	if err != nil {
		return fmt.Errorf("upsert book: %w", err)
	}
	return nil
}

func (p *PostgresDB) UpdateBookStatus(ctx context.Context, id int64, status BookStatus) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE books SET status = $1, updated_at = $2 WHERE id = $3`,
		status, time.Now(), id)
	return err
}

func (p *PostgresDB) DeleteBook(ctx context.Context, id int64) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM books WHERE id = $1`, id)
	return err
}

// --- Download Queue ---

func (p *PostgresDB) EnqueueDownload(ctx context.Context, item *DownloadQueue) error {
	now := time.Now()
	item.CreatedAt = now
	item.UpdatedAt = now
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO download_queue (book_id, asin, priority, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		item.BookID, item.ASIN, item.Priority, DownloadStatusPending, item.CreatedAt, item.UpdatedAt).Scan(&item.ID)
	if err != nil {
		return fmt.Errorf("enqueue download: %w", err)
	}
	return nil
}

func (p *PostgresDB) GetNextPendingDownload(ctx context.Context) (*DownloadQueue, error) {
	// Atomically claim the next pending item to prevent duplicate processing.
	now := time.Now()
	row := p.db.QueryRowContext(ctx,
		`UPDATE download_queue SET status = $1, started_at = $2, updated_at = $2
		 WHERE id = (
		   SELECT id FROM download_queue WHERE status = $3
		   ORDER BY priority DESC, created_at ASC LIMIT 1
		   FOR UPDATE SKIP LOCKED
		 )
		 RETURNING id, book_id, asin, priority, status, progress, error, started_at, completed_at, created_at, updated_at`,
		DownloadStatusActive, now, DownloadStatusPending)

	var d DownloadQueue
	if err := row.Scan(&d.ID, &d.BookID, &d.ASIN, &d.Priority, &d.Status, &d.Progress,
		&d.Error, &d.StartedAt, &d.CompletedAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("claim download: %w", err)
	}
	return &d, nil
}

func (p *PostgresDB) UpdateDownload(ctx context.Context, item *DownloadQueue) error {
	item.UpdatedAt = time.Now()
	_, err := p.db.ExecContext(ctx,
		`UPDATE download_queue SET status = $1, progress = $2, error = $3, started_at = $4, completed_at = $5, updated_at = $6
		 WHERE id = $7`,
		item.Status, item.Progress, item.Error, item.StartedAt, item.CompletedAt, item.UpdatedAt, item.ID)
	return err
}

func (p *PostgresDB) ListDownloads(ctx context.Context, status *DownloadStatus) ([]DownloadQueue, error) {
	query := `SELECT id, book_id, asin, priority, status, progress, error, started_at, completed_at, created_at, updated_at
	          FROM download_queue`
	var args []interface{}
	if status != nil {
		query += " WHERE status = $1"
		args = append(args, *status)
	}
	query += " ORDER BY priority DESC, created_at ASC"

	rows, err := p.db.QueryContext(ctx, query, args...)
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

func (p *PostgresDB) CancelDownload(ctx context.Context, id int64) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE download_queue SET status = $1, updated_at = $2 WHERE id = $3 AND status = $4`,
		DownloadStatusCancelled, time.Now(), id, DownloadStatusPending)
	return err
}

func (p *PostgresDB) RetryDownload(ctx context.Context, id int64) error {
	// Reset the download queue entry
	_, err := p.db.ExecContext(ctx,
		`UPDATE download_queue SET status = $1, error = '', progress = 0, started_at = NULL, completed_at = NULL, updated_at = $2 WHERE id = $3 AND status = $4`,
		DownloadStatusPending, time.Now(), id, DownloadStatusFailed)
	if err != nil {
		return err
	}
	// Also reset the book status back to queued
	_, err = p.db.ExecContext(ctx,
		`UPDATE books SET status = $1, updated_at = $2 WHERE id = (SELECT book_id FROM download_queue WHERE id = $3)`,
		BookStatusQueued, time.Now(), id)
	return err
}

func (p *PostgresDB) RetryAllDownloads(ctx context.Context) (int64, error) {
	now := time.Now()
	// Reset all failed book statuses back to queued
	_, err := p.db.ExecContext(ctx,
		`UPDATE books SET status = $1, updated_at = $2 WHERE id IN (SELECT book_id FROM download_queue WHERE status = $3)`,
		BookStatusQueued, now, DownloadStatusFailed)
	if err != nil {
		return 0, err
	}
	// Reset all failed download queue entries
	result, err := p.db.ExecContext(ctx,
		`UPDATE download_queue SET status = $1, error = '', progress = 0, started_at = NULL, completed_at = NULL, updated_at = $2 WHERE status = $3`,
		DownloadStatusPending, now, DownloadStatusFailed)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// --- Sync History ---

func (p *PostgresDB) CreateSync(ctx context.Context, sync *SyncHistory) error {
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO sync_history (started_at, status) VALUES ($1, $2) RETURNING id`,
		sync.StartedAt, sync.Status).Scan(&sync.ID)
	if err != nil {
		return fmt.Errorf("create sync: %w", err)
	}
	return nil
}

func (p *PostgresDB) UpdateSync(ctx context.Context, sync *SyncHistory) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE sync_history SET completed_at = $1, books_found = $2, books_added = $3, status = $4, error = $5
		 WHERE id = $6`,
		sync.CompletedAt, sync.BooksFound, sync.BooksAdded, sync.Status, sync.Error, sync.ID)
	return err
}

func (p *PostgresDB) GetLastSync(ctx context.Context) (*SyncHistory, error) {
	row := p.db.QueryRowContext(ctx,
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

func (p *PostgresDB) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := p.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (p *PostgresDB) SetSetting(ctx context.Context, key, value string) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, $3)
		 ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
		key, value, time.Now())
	return err
}

// --- Devices ---

func (p *PostgresDB) GetActiveDevice(ctx context.Context) (*Device, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT id, name, marketplace, credentials, is_active, created_at, updated_at
		 FROM devices WHERE is_active = true LIMIT 1`)
	return p.scanDevice(row)
}

func (p *PostgresDB) SaveDevice(ctx context.Context, device *Device) error {
	now := time.Now()
	device.UpdatedAt = now
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}

	if device.IsActive {
		if _, err := p.db.ExecContext(ctx, `UPDATE devices SET is_active = false`); err != nil {
			return fmt.Errorf("deactivate devices: %w", err)
		}
	}

	if device.ID == 0 {
		err := p.db.QueryRowContext(ctx,
			`INSERT INTO devices (name, marketplace, credentials, is_active, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			device.Name, device.Marketplace, device.Credentials, device.IsActive, device.CreatedAt, device.UpdatedAt).Scan(&device.ID)
		if err != nil {
			return fmt.Errorf("insert device: %w", err)
		}
	} else {
		_, err := p.db.ExecContext(ctx,
			`UPDATE devices SET name = $1, marketplace = $2, credentials = $3, is_active = $4, updated_at = $5
			 WHERE id = $6`,
			device.Name, device.Marketplace, device.Credentials, device.IsActive, device.UpdatedAt, device.ID)
		if err != nil {
			return fmt.Errorf("update device: %w", err)
		}
	}
	return nil
}

func (p *PostgresDB) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := p.db.QueryContext(ctx,
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

func (p *PostgresDB) DeleteDevice(ctx context.Context, id int64) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM devices WHERE id = $1`, id)
	return err
}

// --- Helpers ---

func (p *PostgresDB) scanBook(row *sql.Row) (*Book, error) {
	var b Book
	err := row.Scan(&b.ID, &b.ASIN, &b.Title, &b.Author, &b.AuthorASIN, &b.Narrator,
		&b.Publisher, &b.Description, &b.Duration, &b.Series, &b.SeriesPosition,
		&b.CoverURL, &b.PurchaseDate, &b.ReleaseDate, &b.DRMType, &b.Status,
		&b.FilePath, &b.FileSize, &b.CreatedAt, &b.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan book: %w", err)
	}
	return &b, nil
}

func (p *PostgresDB) scanBookRow(rows *sql.Rows) (*Book, error) {
	var b Book
	err := rows.Scan(&b.ID, &b.ASIN, &b.Title, &b.Author, &b.AuthorASIN, &b.Narrator,
		&b.Publisher, &b.Description, &b.Duration, &b.Series, &b.SeriesPosition,
		&b.CoverURL, &b.PurchaseDate, &b.ReleaseDate, &b.DRMType, &b.Status,
		&b.FilePath, &b.FileSize, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("scan book row: %w", err)
	}
	return &b, nil
}

func (p *PostgresDB) scanDownload(row *sql.Row) (*DownloadQueue, error) {
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

func (p *PostgresDB) scanDevice(row *sql.Row) (*Device, error) {
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

func buildBookWherePostgres(filter BookFilter) (string, []interface{}) {
	var clauses []string
	var args []interface{}
	paramIdx := 1

	if filter.Status != nil {
		clauses = append(clauses, fmt.Sprintf("status = $%d", paramIdx))
		args = append(args, *filter.Status)
		paramIdx++
	}
	if filter.Search != "" {
		clauses = append(clauses, fmt.Sprintf("(title ILIKE $%d OR author ILIKE $%d)", paramIdx, paramIdx+1))
		search := "%" + filter.Search + "%"
		args = append(args, search, search)
		paramIdx += 2
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}
