package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// --- Library destinations (sqlite) ---

func (s *SQLiteDB) CreateLibraryDestination(ctx context.Context, d *LibraryDestination) error {
	if d == nil {
		return fmt.Errorf("nil destination")
	}
	if d.ID == "" {
		return fmt.Errorf("destination id required (caller should generate UUID)")
	}
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO library_destinations (
			id, display_name, type, enabled, created_at, updated_at,
			url, api_key, plex_token, plex_section_id, library_id,
			audiobook_path, destination_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		d.ID, d.DisplayName, string(d.Type), boolToInt(d.Enabled),
		d.CreatedAt.Format(time.RFC3339Nano), d.UpdatedAt.Format(time.RFC3339Nano),
		nullableStr(d.URL), nullableStr(d.APIKey), nullableStr(d.PlexToken),
		nullableStr(d.PlexSectionID), nullableStr(d.LibraryID),
		nullableStr(d.AudiobookPath), nullableStr(d.DestinationPath),
	)
	if err != nil {
		return fmt.Errorf("insert library_destination: %w", err)
	}
	return nil
}

func (s *SQLiteDB) GetLibraryDestination(ctx context.Context, id string) (*LibraryDestination, error) {
	row := s.db.QueryRowContext(ctx, libraryDestinationSelect+` WHERE id = ?`, id)
	d, err := scanLibraryDestination(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return d, err
}

func (s *SQLiteDB) ListLibraryDestinations(ctx context.Context) ([]LibraryDestination, error) {
	return s.queryLibraryDestinations(ctx, libraryDestinationSelect+` ORDER BY display_name`)
}

func (s *SQLiteDB) ListEnabledLibraryDestinations(ctx context.Context) ([]LibraryDestination, error) {
	return s.queryLibraryDestinations(ctx, libraryDestinationSelect+` WHERE enabled = 1 ORDER BY display_name`)
}

func (s *SQLiteDB) queryLibraryDestinations(ctx context.Context, q string, args ...any) ([]LibraryDestination, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query library_destinations: %w", err)
	}
	defer rows.Close()

	var out []LibraryDestination
	for rows.Next() {
		d, err := scanLibraryDestination(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *SQLiteDB) UpdateLibraryDestination(ctx context.Context, d *LibraryDestination) error {
	if d == nil || d.ID == "" {
		return fmt.Errorf("destination id required")
	}
	d.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_destinations SET
			display_name = ?, type = ?, enabled = ?, updated_at = ?,
			url = ?, api_key = ?, plex_token = ?, plex_section_id = ?, library_id = ?,
			audiobook_path = ?, destination_path = ?,
			last_health_check_at = ?, last_health_check_ok = ?, last_health_check_err = ?
		WHERE id = ?
	`,
		d.DisplayName, string(d.Type), boolToInt(d.Enabled), d.UpdatedAt.Format(time.RFC3339Nano),
		nullableStr(d.URL), nullableStr(d.APIKey), nullableStr(d.PlexToken),
		nullableStr(d.PlexSectionID), nullableStr(d.LibraryID),
		nullableStr(d.AudiobookPath), nullableStr(d.DestinationPath),
		nullableTime(d.LastHealthCheckAt), nullableBool(d.LastHealthCheckOK), nullableStr(d.LastHealthCheckErr),
		d.ID,
	)
	if err != nil {
		return fmt.Errorf("update library_destination: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("destination %q not found", d.ID)
	}
	return nil
}

func (s *SQLiteDB) DeleteLibraryDestination(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM library_destinations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete library_destination: %w", err)
	}
	return nil
}

// --- Book destinations (sqlite) ---

func (s *SQLiteDB) UpsertBookDestination(ctx context.Context, bd *BookDestination) error {
	if bd == nil {
		return fmt.Errorf("nil book_destination")
	}
	if bd.SyncState == "" {
		bd.SyncState = BookDestSyncPending
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO book_library_destinations (
			book_id, destination_id, server_item_id, server_item_title,
			sync_state, last_attempted_at, last_succeeded_at, last_error,
			attempt_count, disabled_reason, per_op_outcomes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(book_id, destination_id) DO UPDATE SET
			server_item_id = excluded.server_item_id,
			server_item_title = excluded.server_item_title,
			sync_state = excluded.sync_state,
			last_attempted_at = excluded.last_attempted_at,
			last_succeeded_at = excluded.last_succeeded_at,
			last_error = excluded.last_error,
			attempt_count = excluded.attempt_count,
			disabled_reason = excluded.disabled_reason,
			per_op_outcomes = excluded.per_op_outcomes
	`,
		bd.BookID, bd.DestinationID,
		nullableStr(bd.ServerItemID), nullableStr(bd.ServerItemTitle),
		string(bd.SyncState),
		nullableTime(bd.LastAttemptedAt), nullableTime(bd.LastSucceededAt),
		nullableStr(bd.LastError),
		bd.AttemptCount, nullableStr(bd.DisabledReason),
		bd.PerOpOutcomes,
	)
	if err != nil {
		return fmt.Errorf("upsert book_library_destination: %w", err)
	}
	return nil
}

func (s *SQLiteDB) GetBookDestinations(ctx context.Context, bookID int64) ([]BookDestination, error) {
	rows, err := s.db.QueryContext(ctx, bookDestinationSelect+` WHERE book_id = ?`, bookID)
	if err != nil {
		return nil, fmt.Errorf("query book_library_destinations: %w", err)
	}
	defer rows.Close()

	var out []BookDestination
	for rows.Next() {
		bd, err := scanBookDestination(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *bd)
	}
	return out, rows.Err()
}

func (s *SQLiteDB) GetBookDestination(ctx context.Context, bookID int64, destinationID string) (*BookDestination, error) {
	row := s.db.QueryRowContext(ctx, bookDestinationSelect+` WHERE book_id = ? AND destination_id = ?`, bookID, destinationID)
	bd, err := scanBookDestination(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return bd, err
}

func (s *SQLiteDB) ListBookDestinationsBy(ctx context.Context, destinationID string, state *BookDestinationSyncState) ([]BookDestination, error) {
	q := bookDestinationSelect + ` WHERE destination_id = ?`
	args := []any{destinationID}
	if state != nil {
		q += ` AND sync_state = ?`
		args = append(args, string(*state))
	}
	q += ` ORDER BY book_id`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query book_library_destinations by destination: %w", err)
	}
	defer rows.Close()

	var out []BookDestination
	for rows.Next() {
		bd, err := scanBookDestination(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *bd)
	}
	return out, rows.Err()
}

// --- shared scanners + helpers (sqlite-specific time format) ---

const libraryDestinationSelect = `
	SELECT id, display_name, type, enabled, created_at, updated_at,
	       url, api_key, plex_token, plex_section_id, library_id,
	       audiobook_path, destination_path,
	       last_health_check_at, last_health_check_ok, last_health_check_err
	FROM library_destinations
`

const bookDestinationSelect = `
	SELECT book_id, destination_id, server_item_id, server_item_title,
	       sync_state, last_attempted_at, last_succeeded_at, last_error,
	       attempt_count, disabled_reason, per_op_outcomes
	FROM book_library_destinations
`

func scanLibraryDestination(scan func(...any) error) (*LibraryDestination, error) {
	var d LibraryDestination
	var (
		typ                                                     string
		enabled                                                 int
		createdAt, updatedAt                                    string
		url, apiKey, plexToken                                  sql.NullString
		plexSectionID, libraryID                                sql.NullString
		audiobookPath, destinationPath                          sql.NullString
		lastHealthCheckAt                                       sql.NullString
		lastHealthCheckOK                                       sql.NullInt64
		lastHealthCheckErr                                      sql.NullString
	)
	err := scan(
		&d.ID, &d.DisplayName, &typ, &enabled, &createdAt, &updatedAt,
		&url, &apiKey, &plexToken, &plexSectionID, &libraryID,
		&audiobookPath, &destinationPath,
		&lastHealthCheckAt, &lastHealthCheckOK, &lastHealthCheckErr,
	)
	if err != nil {
		return nil, err
	}
	d.Type = LibraryDestinationType(typ)
	d.Enabled = enabled != 0
	d.CreatedAt = parseTimeRFC3339(createdAt)
	d.UpdatedAt = parseTimeRFC3339(updatedAt)
	d.URL = url.String
	d.APIKey = apiKey.String
	d.PlexToken = plexToken.String
	d.PlexSectionID = plexSectionID.String
	d.LibraryID = libraryID.String
	d.AudiobookPath = audiobookPath.String
	d.DestinationPath = destinationPath.String
	if lastHealthCheckAt.Valid {
		t := parseTimeRFC3339(lastHealthCheckAt.String)
		d.LastHealthCheckAt = &t
	}
	if lastHealthCheckOK.Valid {
		ok := lastHealthCheckOK.Int64 != 0
		d.LastHealthCheckOK = &ok
	}
	d.LastHealthCheckErr = lastHealthCheckErr.String
	return &d, nil
}

func scanBookDestination(scan func(...any) error) (*BookDestination, error) {
	var bd BookDestination
	var (
		serverItemID, serverItemTitle sql.NullString
		syncState                     string
		lastAttemptedAt, lastSucceededAt sql.NullString
		lastError, disabledReason     sql.NullString
		perOpOutcomes                 sql.NullString
	)
	err := scan(
		&bd.BookID, &bd.DestinationID,
		&serverItemID, &serverItemTitle,
		&syncState,
		&lastAttemptedAt, &lastSucceededAt, &lastError,
		&bd.AttemptCount, &disabledReason, &perOpOutcomes,
	)
	if err != nil {
		return nil, err
	}
	bd.ServerItemID = serverItemID.String
	bd.ServerItemTitle = serverItemTitle.String
	bd.SyncState = BookDestinationSyncState(syncState)
	if lastAttemptedAt.Valid {
		t := parseTimeRFC3339(lastAttemptedAt.String)
		bd.LastAttemptedAt = &t
	}
	if lastSucceededAt.Valid {
		t := parseTimeRFC3339(lastSucceededAt.String)
		bd.LastSucceededAt = &t
	}
	bd.LastError = lastError.String
	bd.DisabledReason = disabledReason.String
	bd.PerOpOutcomes = perOpOutcomes.String
	return &bd, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nullableBool(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

func parseTimeRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t
	}
	t, _ = time.Parse(time.RFC3339, s)
	return t
}
