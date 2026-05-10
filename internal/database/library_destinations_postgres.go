package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// --- Library destinations (postgres) ---

func (p *PostgresDB) CreateLibraryDestination(ctx context.Context, d *LibraryDestination) error {
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

	_, err := p.db.ExecContext(ctx, `
		INSERT INTO library_destinations (
			id, display_name, type, enabled, created_at, updated_at,
			url, api_key, plex_token, plex_section_id, library_id,
			audiobook_path, destination_path
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		d.ID, d.DisplayName, string(d.Type), d.Enabled,
		d.CreatedAt, d.UpdatedAt,
		nullableStr(d.URL), nullableStr(d.APIKey), nullableStr(d.PlexToken),
		nullableStr(d.PlexSectionID), nullableStr(d.LibraryID),
		nullableStr(d.AudiobookPath), nullableStr(d.DestinationPath),
	)
	if err != nil {
		return fmt.Errorf("insert library_destination: %w", err)
	}
	return nil
}

func (p *PostgresDB) GetLibraryDestination(ctx context.Context, id string) (*LibraryDestination, error) {
	row := p.db.QueryRowContext(ctx, libraryDestinationSelectPG+` WHERE id = $1`, id)
	d, err := scanLibraryDestinationPG(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return d, err
}

func (p *PostgresDB) ListLibraryDestinations(ctx context.Context) ([]LibraryDestination, error) {
	return p.queryLibraryDestinations(ctx, libraryDestinationSelectPG+` ORDER BY display_name`)
}

func (p *PostgresDB) ListEnabledLibraryDestinations(ctx context.Context) ([]LibraryDestination, error) {
	return p.queryLibraryDestinations(ctx, libraryDestinationSelectPG+` WHERE enabled = TRUE ORDER BY display_name`)
}

func (p *PostgresDB) queryLibraryDestinations(ctx context.Context, q string, args ...any) ([]LibraryDestination, error) {
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query library_destinations: %w", err)
	}
	defer rows.Close()

	var out []LibraryDestination
	for rows.Next() {
		d, err := scanLibraryDestinationPG(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (p *PostgresDB) UpdateLibraryDestination(ctx context.Context, d *LibraryDestination) error {
	if d == nil || d.ID == "" {
		return fmt.Errorf("destination id required")
	}
	d.UpdatedAt = time.Now().UTC()
	res, err := p.db.ExecContext(ctx, `
		UPDATE library_destinations SET
			display_name = $1, type = $2, enabled = $3, updated_at = $4,
			url = $5, api_key = $6, plex_token = $7, plex_section_id = $8, library_id = $9,
			audiobook_path = $10, destination_path = $11,
			last_health_check_at = $12, last_health_check_ok = $13, last_health_check_err = $14
		WHERE id = $15
	`,
		d.DisplayName, string(d.Type), d.Enabled, d.UpdatedAt,
		nullableStr(d.URL), nullableStr(d.APIKey), nullableStr(d.PlexToken),
		nullableStr(d.PlexSectionID), nullableStr(d.LibraryID),
		nullableStr(d.AudiobookPath), nullableStr(d.DestinationPath),
		nullableTimePG(d.LastHealthCheckAt), nullableBoolPG(d.LastHealthCheckOK), nullableStr(d.LastHealthCheckErr),
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

func (p *PostgresDB) DeleteLibraryDestination(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM library_destinations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete library_destination: %w", err)
	}
	return nil
}

// --- Book destinations (postgres) ---

func (p *PostgresDB) UpsertBookDestination(ctx context.Context, bd *BookDestination) error {
	if bd == nil {
		return fmt.Errorf("nil book_destination")
	}
	if bd.SyncState == "" {
		bd.SyncState = BookDestSyncPending
	}
	perOps := bd.PerOpOutcomes
	if perOps == "" {
		perOps = "{}"
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO book_library_destinations (
			book_id, destination_id, server_item_id, server_item_title,
			sync_state, last_attempted_at, last_succeeded_at, last_error,
			attempt_count, disabled_reason, per_op_outcomes
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb)
		ON CONFLICT (book_id, destination_id) DO UPDATE SET
			server_item_id = EXCLUDED.server_item_id,
			server_item_title = EXCLUDED.server_item_title,
			sync_state = EXCLUDED.sync_state,
			last_attempted_at = EXCLUDED.last_attempted_at,
			last_succeeded_at = EXCLUDED.last_succeeded_at,
			last_error = EXCLUDED.last_error,
			attempt_count = EXCLUDED.attempt_count,
			disabled_reason = EXCLUDED.disabled_reason,
			per_op_outcomes = EXCLUDED.per_op_outcomes
	`,
		bd.BookID, bd.DestinationID,
		nullableStr(bd.ServerItemID), nullableStr(bd.ServerItemTitle),
		string(bd.SyncState),
		nullableTimePG(bd.LastAttemptedAt), nullableTimePG(bd.LastSucceededAt),
		nullableStr(bd.LastError),
		bd.AttemptCount, nullableStr(bd.DisabledReason),
		perOps,
	)
	if err != nil {
		return fmt.Errorf("upsert book_library_destination: %w", err)
	}
	return nil
}

func (p *PostgresDB) GetBookDestinations(ctx context.Context, bookID int64) ([]BookDestination, error) {
	rows, err := p.db.QueryContext(ctx, bookDestinationSelectPG+` WHERE book_id = $1`, bookID)
	if err != nil {
		return nil, fmt.Errorf("query book_library_destinations: %w", err)
	}
	defer rows.Close()

	var out []BookDestination
	for rows.Next() {
		bd, err := scanBookDestinationPG(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *bd)
	}
	return out, rows.Err()
}

func (p *PostgresDB) GetBookDestination(ctx context.Context, bookID int64, destinationID string) (*BookDestination, error) {
	row := p.db.QueryRowContext(ctx, bookDestinationSelectPG+` WHERE book_id = $1 AND destination_id = $2`, bookID, destinationID)
	bd, err := scanBookDestinationPG(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return bd, err
}

func (p *PostgresDB) ListBookDestinationsBy(ctx context.Context, destinationID string, state *BookDestinationSyncState) ([]BookDestination, error) {
	q := bookDestinationSelectPG + ` WHERE destination_id = $1`
	args := []any{destinationID}
	if state != nil {
		q += ` AND sync_state = $2`
		args = append(args, string(*state))
	}
	q += ` ORDER BY book_id`

	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query book_library_destinations by destination: %w", err)
	}
	defer rows.Close()

	var out []BookDestination
	for rows.Next() {
		bd, err := scanBookDestinationPG(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *bd)
	}
	return out, rows.Err()
}

const libraryDestinationSelectPG = `
	SELECT id, display_name, type, enabled, created_at, updated_at,
	       url, api_key, plex_token, plex_section_id, library_id,
	       audiobook_path, destination_path,
	       last_health_check_at, last_health_check_ok, last_health_check_err
	FROM library_destinations
`

const bookDestinationSelectPG = `
	SELECT book_id, destination_id, server_item_id, server_item_title,
	       sync_state, last_attempted_at, last_succeeded_at, last_error,
	       attempt_count, disabled_reason, per_op_outcomes::text
	FROM book_library_destinations
`

func scanLibraryDestinationPG(scan func(...any) error) (*LibraryDestination, error) {
	var d LibraryDestination
	var (
		typ                                       string
		url, apiKey, plexToken                    sql.NullString
		plexSectionID, libraryID                  sql.NullString
		audiobookPath, destinationPath            sql.NullString
		lastHealthCheckAt                         sql.NullTime
		lastHealthCheckOK                         sql.NullBool
		lastHealthCheckErr                        sql.NullString
	)
	err := scan(
		&d.ID, &d.DisplayName, &typ, &d.Enabled, &d.CreatedAt, &d.UpdatedAt,
		&url, &apiKey, &plexToken, &plexSectionID, &libraryID,
		&audiobookPath, &destinationPath,
		&lastHealthCheckAt, &lastHealthCheckOK, &lastHealthCheckErr,
	)
	if err != nil {
		return nil, err
	}
	d.Type = LibraryDestinationType(typ)
	d.URL = url.String
	d.APIKey = apiKey.String
	d.PlexToken = plexToken.String
	d.PlexSectionID = plexSectionID.String
	d.LibraryID = libraryID.String
	d.AudiobookPath = audiobookPath.String
	d.DestinationPath = destinationPath.String
	if lastHealthCheckAt.Valid {
		t := lastHealthCheckAt.Time
		d.LastHealthCheckAt = &t
	}
	if lastHealthCheckOK.Valid {
		v := lastHealthCheckOK.Bool
		d.LastHealthCheckOK = &v
	}
	d.LastHealthCheckErr = lastHealthCheckErr.String
	return &d, nil
}

func scanBookDestinationPG(scan func(...any) error) (*BookDestination, error) {
	var bd BookDestination
	var (
		serverItemID, serverItemTitle    sql.NullString
		syncState                        string
		lastAttemptedAt, lastSucceededAt sql.NullTime
		lastError, disabledReason        sql.NullString
		perOpOutcomes                    sql.NullString
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
		t := lastAttemptedAt.Time
		bd.LastAttemptedAt = &t
	}
	if lastSucceededAt.Valid {
		t := lastSucceededAt.Time
		bd.LastSucceededAt = &t
	}
	bd.LastError = lastError.String
	bd.DisabledReason = disabledReason.String
	bd.PerOpOutcomes = perOpOutcomes.String
	return &bd, nil
}

func nullableTimePG(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return *t
}

func nullableBoolPG(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}
