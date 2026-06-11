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
	"github.com/lib/pq"
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
	_, err := p.db.ExecContext(ctx, `TRUNCATE books, download_queue, sync_history, settings, devices, users, sessions RESTART IDENTITY CASCADE`)
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
		        drm_type, status, unavailable_reason, file_path, file_size,
		        created_at, updated_at
		 FROM books WHERE id = $1`, id))
}

func (p *PostgresDB) GetBookByASIN(ctx context.Context, asin string) (*Book, error) {
	return p.scanBook(p.db.QueryRowContext(ctx,
		`SELECT id, asin, title, author, author_asin, narrator, publisher, description,
		        duration, series, series_position, cover_url, purchase_date, release_date,
		        drm_type, status, unavailable_reason, file_path, file_size,
		        created_at, updated_at
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
	                 drm_type, status, unavailable_reason, file_path, file_size,
	                 created_at, updated_at
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

// CountBooksByStatus runs a single GROUP BY to return every status
// bucket's row count. The library page's filter tabs used to make 7
// independent LIMIT-1 counts which was 7x what we actually needed.
func (p *PostgresDB) CountBooksByStatus(ctx context.Context) (map[BookStatus]int, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM books GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count books by status: %w", err)
	}
	defer rows.Close()
	out := map[BookStatus]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("scan status count: %w", err)
		}
		out[BookStatus(status)] = n
	}
	return out, rows.Err()
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
		                    drm_type, status, unavailable_reason, file_path, file_size, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		 ON CONFLICT(asin) DO UPDATE SET
		    title=EXCLUDED.title, author=EXCLUDED.author, author_asin=EXCLUDED.author_asin,
		    narrator=EXCLUDED.narrator, publisher=EXCLUDED.publisher, description=EXCLUDED.description,
		    duration=EXCLUDED.duration, series=EXCLUDED.series, series_position=EXCLUDED.series_position,
		    cover_url=EXCLUDED.cover_url, purchase_date=EXCLUDED.purchase_date, release_date=EXCLUDED.release_date,
		    drm_type=EXCLUDED.drm_type, status=EXCLUDED.status, unavailable_reason=EXCLUDED.unavailable_reason,
		    file_path=EXCLUDED.file_path, file_size=EXCLUDED.file_size, updated_at=EXCLUDED.updated_at
		 RETURNING id`,
		book.ASIN, book.Title, book.Author, book.AuthorASIN, book.Narrator, book.Publisher,
		book.Description, book.Duration, book.Series, book.SeriesPosition, book.CoverURL,
		book.PurchaseDate, book.ReleaseDate, book.DRMType, book.Status, book.UnavailableReason,
		book.FilePath, book.FileSize, book.CreatedAt, book.UpdatedAt).Scan(&book.ID)
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
	if strings.TrimSpace(string(item.Status)) == "" {
		item.Status = DownloadStatusPending
	}
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO download_queue (book_id, asin, priority, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		item.BookID, item.ASIN, item.Priority, item.Status, item.CreatedAt, item.UpdatedAt).Scan(&item.ID)
	if err != nil {
		return fmt.Errorf("enqueue download: %w", err)
	}
	return nil
}

func (p *PostgresDB) GetNextPendingDownload(ctx context.Context) (*DownloadQueue, error) {
	// Atomically claim the next pending item to prevent duplicate processing.
	now := time.Now()
	row := p.db.QueryRowContext(ctx,
		`WITH next_item AS (
		   SELECT id, status
		   FROM download_queue
		   WHERE status IN ($1, $2)
		   ORDER BY priority DESC, created_at ASC
		   LIMIT 1
		   FOR UPDATE SKIP LOCKED
		 )
		 UPDATE download_queue dq
		 SET status = CASE
		     WHEN ni.status = $2 THEN $4
		     ELSE $3
		   END,
		   started_at = $5,
		   updated_at = $5
		 FROM next_item ni
		 WHERE dq.id = ni.id
		 RETURNING dq.id, dq.book_id, dq.asin, dq.priority, dq.status, dq.progress, dq.error, dq.started_at, dq.completed_at, dq.created_at, dq.updated_at`,
		DownloadStatusPending, DownloadStatusReorganize, DownloadStatusActive, DownloadStatusReorganizing, now)

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
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("retry download: begin tx: %w", err)
	}
	defer tx.Rollback()

	var asin string
	var bookID int64
	var priority int
	err = tx.QueryRowContext(ctx,
		`SELECT asin, book_id, priority FROM download_queue WHERE id = $1 AND status = $2`,
		id, DownloadStatusFailed).Scan(&asin, &bookID, &priority)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("retry download: lookup: %w", err)
	}

	now := time.Now()
	// Cancel ALL failed entries for this ASIN to prevent duplicate queuing.
	_, err = tx.ExecContext(ctx,
		`UPDATE download_queue SET status = $1, updated_at = $2 WHERE asin = $3 AND status = $4`,
		DownloadStatusCancelled, now, asin, DownloadStatusFailed)
	if err != nil {
		return fmt.Errorf("retry download: cancel duplicates: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO download_queue (book_id, asin, priority, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		bookID, asin, priority, DownloadStatusPending, now, now)
	if err != nil {
		return fmt.Errorf("retry download: enqueue: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE books SET status = $1, updated_at = $2 WHERE id = $3`,
		BookStatusQueued, now, bookID)
	if err != nil {
		return fmt.Errorf("retry download: reset book: %w", err)
	}

	return tx.Commit()
}

func (p *PostgresDB) RetryAllDownloads(ctx context.Context) (int64, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("retry all: begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()

	rows, err := tx.QueryContext(ctx,
		`SELECT asin, book_id, MAX(priority) as priority
		 FROM download_queue WHERE status = $1
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

	var toRetry []entry
	for _, e := range entries {
		var count int
		_ = tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM download_queue WHERE asin = $1 AND status IN ($2, $3)`,
			e.asin, DownloadStatusPending, DownloadStatusActive).Scan(&count)
		if count == 0 {
			toRetry = append(toRetry, e)
		}
	}

	for _, e := range toRetry {
		if _, err := tx.ExecContext(ctx,
			`UPDATE download_queue SET status = $1, updated_at = $2 WHERE asin = $3 AND status = $4`,
			DownloadStatusCancelled, now, e.asin, DownloadStatusFailed); err != nil {
			return 0, fmt.Errorf("retry all: cancel %s: %w", e.asin, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO download_queue (book_id, asin, priority, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			e.bookID, e.asin, e.priority, DownloadStatusPending, now, now); err != nil {
			return 0, fmt.Errorf("retry all: enqueue %s: %w", e.asin, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE books SET status = $1, updated_at = $2 WHERE id = $3`,
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

// --- Audible accounts ---

func (p *PostgresDB) CreateAudibleAccount(ctx context.Context, a *AudibleAccount) error {
	now := time.Now()
	a.UpdatedAt = now
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO audible_accounts (id, display_name, marketplace, customer_id, credentials, enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		a.ID, a.DisplayName, a.Marketplace, a.CustomerID, a.Credentials, a.Enabled, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert audible account: %w", err)
	}
	return nil
}

func (p *PostgresDB) GetAudibleAccount(ctx context.Context, id string) (*AudibleAccount, error) {
	return p.scanAudibleAccount(p.db.QueryRowContext(ctx,
		`SELECT id, display_name, marketplace, customer_id, credentials, enabled, created_at, updated_at
		 FROM audible_accounts WHERE id = $1`, id))
}

func (p *PostgresDB) GetAudibleAccountByCustomerID(ctx context.Context, customerID string) (*AudibleAccount, error) {
	if strings.TrimSpace(customerID) == "" {
		return nil, nil
	}
	return p.scanAudibleAccount(p.db.QueryRowContext(ctx,
		`SELECT id, display_name, marketplace, customer_id, credentials, enabled, created_at, updated_at
		 FROM audible_accounts WHERE customer_id = $1 LIMIT 1`, customerID))
}

func (p *PostgresDB) ListAudibleAccounts(ctx context.Context) ([]AudibleAccount, error) {
	return p.queryAudibleAccounts(ctx,
		`SELECT id, display_name, marketplace, customer_id, credentials, enabled, created_at, updated_at
		 FROM audible_accounts ORDER BY created_at`)
}

func (p *PostgresDB) ListEnabledAudibleAccounts(ctx context.Context) ([]AudibleAccount, error) {
	return p.queryAudibleAccounts(ctx,
		`SELECT id, display_name, marketplace, customer_id, credentials, enabled, created_at, updated_at
		 FROM audible_accounts WHERE enabled = TRUE ORDER BY created_at`)
}

func (p *PostgresDB) UpdateAudibleAccount(ctx context.Context, a *AudibleAccount) error {
	a.UpdatedAt = time.Now()
	_, err := p.db.ExecContext(ctx,
		`UPDATE audible_accounts SET display_name = $1, marketplace = $2, customer_id = $3,
		    credentials = $4, enabled = $5, updated_at = $6 WHERE id = $7`,
		a.DisplayName, a.Marketplace, a.CustomerID, a.Credentials, a.Enabled, a.UpdatedAt, a.ID)
	if err != nil {
		return fmt.Errorf("update audible account: %w", err)
	}
	return nil
}

func (p *PostgresDB) DeleteAudibleAccount(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM audible_accounts WHERE id = $1`, id)
	return err
}

func (p *PostgresDB) SetBookAccount(ctx context.Context, asin, accountID string) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE books SET account_id = $1 WHERE asin = $2`, accountID, asin)
	return err
}

func (p *PostgresDB) GetBookAccount(ctx context.Context, asin string) (string, error) {
	var id string
	err := p.db.QueryRowContext(ctx, `SELECT account_id FROM books WHERE asin = $1`, asin).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (p *PostgresDB) ReplaceBookAccounts(ctx context.Context, asin string, accountIDs []string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM book_audible_accounts WHERE asin = $1`, asin); err != nil {
		return fmt.Errorf("clear book accounts: %w", err)
	}
	for _, id := range accountIDs {
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO book_audible_accounts (asin, account_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			asin, id); err != nil {
			return fmt.Errorf("insert book account: %w", err)
		}
	}
	return tx.Commit()
}

func (p *PostgresDB) ListASINsForAccount(ctx context.Context, accountID string) ([]string, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT asin FROM book_audible_accounts WHERE account_id = $1 ORDER BY asin`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list asins for account: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p *PostgresDB) GetBookAccountsForASINs(ctx context.Context, asins []string) (map[string][]string, error) {
	out := make(map[string][]string, len(asins))
	if len(asins) == 0 {
		return out, nil
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT asin, account_id FROM book_audible_accounts WHERE asin = ANY($1) ORDER BY account_id`,
		pq.Array(asins))
	if err != nil {
		return nil, fmt.Errorf("get book accounts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var asin, id string
		if err := rows.Scan(&asin, &id); err != nil {
			return nil, err
		}
		out[asin] = append(out[asin], id)
	}
	return out, rows.Err()
}

func (p *PostgresDB) queryAudibleAccounts(ctx context.Context, query string, args ...any) ([]AudibleAccount, error) {
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audible accounts: %w", err)
	}
	defer rows.Close()
	var out []AudibleAccount
	for rows.Next() {
		var a AudibleAccount
		if err := rows.Scan(&a.ID, &a.DisplayName, &a.Marketplace, &a.CustomerID,
			&a.Credentials, &a.Enabled, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan audible account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p *PostgresDB) scanAudibleAccount(row *sql.Row) (*AudibleAccount, error) {
	var a AudibleAccount
	err := row.Scan(&a.ID, &a.DisplayName, &a.Marketplace, &a.CustomerID,
		&a.Credentials, &a.Enabled, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan audible account: %w", err)
	}
	return &a, nil
}

// --- Helpers ---

func (p *PostgresDB) scanBook(row *sql.Row) (*Book, error) {
	var b Book
	err := row.Scan(&b.ID, &b.ASIN, &b.Title, &b.Author, &b.AuthorASIN, &b.Narrator,
		&b.Publisher, &b.Description, &b.Duration, &b.Series, &b.SeriesPosition,
		&b.CoverURL, &b.PurchaseDate, &b.ReleaseDate, &b.DRMType, &b.Status,
		&b.UnavailableReason, &b.FilePath, &b.FileSize,
		&b.CreatedAt, &b.UpdatedAt)
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
		&b.UnavailableReason, &b.FilePath, &b.FileSize,
		&b.CreatedAt, &b.UpdatedAt)
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
	if len(filter.ExcludeStatuses) > 0 {
		placeholders := make([]string, 0, len(filter.ExcludeStatuses))
		for _, status := range filter.ExcludeStatuses {
			placeholders = append(placeholders, fmt.Sprintf("$%d", paramIdx))
			args = append(args, status)
			paramIdx++
		}
		clauses = append(clauses, "status NOT IN ("+strings.Join(placeholders, ",")+")")
	}
	if filter.Search != "" {
		clauses = append(clauses, fmt.Sprintf("(title ILIKE $%d OR author ILIKE $%d OR series ILIKE $%d OR asin ILIKE $%d)", paramIdx, paramIdx+1, paramIdx+2, paramIdx+3))
		search := "%" + filter.Search + "%"
		args = append(args, search, search, search, search)
		paramIdx += 4
	}
	if filter.OnDisk != nil {
		if *filter.OnDisk {
			clauses = append(clauses, "file_path != ''")
		} else {
			clauses = append(clauses, "(file_path IS NULL OR file_path = '')")
		}
	}
	for _, destID := range filter.PresentInDestinations {
		clauses = append(clauses, fmt.Sprintf("EXISTS (SELECT 1 FROM book_library_destinations bld WHERE bld.book_id = books.id AND bld.destination_id = $%d AND bld.sync_state IN ('synced','syncing'))", paramIdx))
		args = append(args, destID)
		paramIdx++
	}
	for _, destID := range filter.MissingFromDestinations {
		clauses = append(clauses, fmt.Sprintf("NOT EXISTS (SELECT 1 FROM book_library_destinations bld WHERE bld.book_id = books.id AND bld.destination_id = $%d AND bld.sync_state IN ('synced','syncing'))", paramIdx))
		args = append(args, destID)
		paramIdx++
	}
	if filter.AccountID != "" {
		// books.account_id is the legacy single-owner fallback for rows
		// synced before the junction existed.
		clauses = append(clauses, fmt.Sprintf("(EXISTS (SELECT 1 FROM book_audible_accounts baa WHERE baa.asin = books.asin AND baa.account_id = $%d) OR books.account_id = $%d)", paramIdx, paramIdx+1))
		args = append(args, filter.AccountID, filter.AccountID)
		paramIdx += 2
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// --- Users ---

func (p *PostgresDB) scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Password, &u.Salt, &u.Iterations,
		&u.Identifier, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (p *PostgresDB) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return p.scanUser(p.db.QueryRowContext(ctx,
		`SELECT id, username, password, salt, iterations, identifier, created_at, updated_at
		 FROM users WHERE username = $1 LIMIT 1`, username))
}

func (p *PostgresDB) GetUserByID(ctx context.Context, id int64) (*User, error) {
	return p.scanUser(p.db.QueryRowContext(ctx,
		`SELECT id, username, password, salt, iterations, identifier, created_at, updated_at
		 FROM users WHERE id = $1`, id))
}

func (p *PostgresDB) GetFirstUser(ctx context.Context) (*User, error) {
	return p.scanUser(p.db.QueryRowContext(ctx,
		`SELECT id, username, password, salt, iterations, identifier, created_at, updated_at
		 FROM users ORDER BY id ASC LIMIT 1`))
}

func (p *PostgresDB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (p *PostgresDB) UpsertUser(ctx context.Context, user *User) error {
	now := time.Now()
	user.UpdatedAt = now
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	if user.ID == 0 {
		err := p.db.QueryRowContext(ctx,
			`INSERT INTO users (username, password, salt, iterations, identifier, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			user.Username, user.Password, user.Salt, user.Iterations, user.Identifier,
			user.CreatedAt, user.UpdatedAt).Scan(&user.ID)
		if err != nil {
			if isPostgresUniqueErr(err) {
				return ErrDuplicateUser
			}
			return fmt.Errorf("insert user: %w", err)
		}
		return nil
	}
	_, err := p.db.ExecContext(ctx,
		`UPDATE users SET username = $1, password = $2, salt = $3, iterations = $4,
		                  identifier = $5, updated_at = $6
		 WHERE id = $7`,
		user.Username, user.Password, user.Salt, user.Iterations, user.Identifier,
		user.UpdatedAt, user.ID)
	if err != nil {
		if isPostgresUniqueErr(err) {
			return ErrDuplicateUser
		}
		return fmt.Errorf("update user: %w", err)
	}
	return nil
}

// isPostgresUniqueErr reports whether err carries Postgres SQLSTATE 23505
// (unique_violation). We match on the error text rather than the driver's
// typed error so this stays driver-agnostic — every Go pq driver wraps the
// SQLSTATE into the error message.
func isPostgresUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "unique_violation") || strings.Contains(msg, "duplicate key value")
}

func (p *PostgresDB) RotateUserIdentifier(ctx context.Context, userID int64, newIdentifier string) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE users SET identifier = $1, updated_at = $2 WHERE id = $3`,
		newIdentifier, time.Now(), userID)
	return err
}

func (p *PostgresDB) DeleteUser(ctx context.Context, id int64) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

// --- Sessions ---

func (p *PostgresDB) CreateSession(ctx context.Context, sess *Session) error {
	now := time.Now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	if sess.LastSeen.IsZero() {
		sess.LastSeen = now
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, identifier, expires_at, last_seen, created_at, user_agent, ip)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sess.Token, sess.UserID, sess.Identifier, sess.ExpiresAt, sess.LastSeen,
		sess.CreatedAt, sess.UserAgent, sess.IP)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (p *PostgresDB) GetSession(ctx context.Context, token string) (*Session, error) {
	var sess Session
	err := p.db.QueryRowContext(ctx,
		`SELECT token, user_id, identifier, expires_at, last_seen, created_at, user_agent, ip
		 FROM sessions WHERE token = $1`, token).Scan(
		&sess.Token, &sess.UserID, &sess.Identifier, &sess.ExpiresAt, &sess.LastSeen,
		&sess.CreatedAt, &sess.UserAgent, &sess.IP)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (p *PostgresDB) TouchSession(ctx context.Context, token string, lastSeen time.Time) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen = $1 WHERE token = $2`, lastSeen, token)
	return err
}

func (p *PostgresDB) DeleteSession(ctx context.Context, token string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return err
}

func (p *PostgresDB) DeleteSessionsForUser(ctx context.Context, userID int64) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

func (p *PostgresDB) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := p.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < $1`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

