-- Library destinations (postgres). Mirror of sqlite migration 005 with
-- BOOLEAN/TIMESTAMPTZ/JSONB instead of INTEGER/TEXT-encoded equivalents.
CREATE TABLE library_destinations (
    id                    TEXT PRIMARY KEY,
    display_name          TEXT NOT NULL,
    type                  TEXT NOT NULL,
    enabled               BOOLEAN NOT NULL DEFAULT TRUE,
    created_at            TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL,

    url                   TEXT,
    api_key               TEXT,
    plex_token            TEXT,
    plex_section_id       TEXT,
    library_id            TEXT,
    audiobook_path        TEXT,
    destination_path      TEXT,

    last_health_check_at  TIMESTAMPTZ,
    last_health_check_ok  BOOLEAN,
    last_health_check_err TEXT,

    -- length(trim(coalesce(...,''))) > 0 enforces non-empty AND non-NULL.
    -- Bare length(trim(col))>0 evaluates to NULL when col is NULL, and
    -- SQL CHECK passes on NULL — so the coalesce is load-bearing.
    -- Copilot review flagged the original IS NOT NULL form as letting
    -- empty-string through.
    CHECK (
        (type = 'plex'     AND length(trim(coalesce(url,''))) > 0 AND length(trim(coalesce(plex_token,''))) > 0 AND length(trim(coalesce(plex_section_id,''))) > 0) OR
        (type = 'emby'     AND length(trim(coalesce(url,''))) > 0 AND length(trim(coalesce(api_key,'')))    > 0 AND length(trim(coalesce(library_id,'')))      > 0) OR
        (type = 'jellyfin' AND length(trim(coalesce(url,''))) > 0 AND length(trim(coalesce(api_key,'')))    > 0 AND length(trim(coalesce(library_id,'')))      > 0) OR
        (type = 'abs'      AND length(trim(coalesce(url,''))) > 0 AND length(trim(coalesce(api_key,'')))    > 0 AND length(trim(coalesce(library_id,'')))      > 0)
    )
);

CREATE INDEX library_destinations_enabled_idx ON library_destinations(enabled);
CREATE INDEX library_destinations_type_idx ON library_destinations(type);

CREATE TABLE book_library_destinations (
    book_id              BIGINT NOT NULL,
    destination_id       TEXT NOT NULL,

    server_item_id       TEXT,
    server_item_title    TEXT,

    sync_state           TEXT NOT NULL DEFAULT 'pending',
    last_attempted_at    TIMESTAMPTZ,
    last_succeeded_at    TIMESTAMPTZ,
    last_error           TEXT,
    attempt_count        INTEGER NOT NULL DEFAULT 0,
    disabled_reason      TEXT,
    per_op_outcomes      JSONB NOT NULL DEFAULT '{}'::jsonb,

    PRIMARY KEY (book_id, destination_id),
    FOREIGN KEY (book_id)        REFERENCES books(id)                  ON DELETE CASCADE,
    FOREIGN KEY (destination_id) REFERENCES library_destinations(id)   ON DELETE CASCADE
);

CREATE INDEX book_library_destinations_dest_idx ON book_library_destinations(destination_id, sync_state);
