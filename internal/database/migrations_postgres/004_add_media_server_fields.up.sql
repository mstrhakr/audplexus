ALTER TABLE books ADD COLUMN media_server_id TEXT NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN media_server_title TEXT NOT NULL DEFAULT '';
UPDATE books SET media_server_id = plex_rating_key WHERE media_server_id = '' AND plex_rating_key <> '';
UPDATE books SET media_server_title = plex_title WHERE media_server_title = '' AND plex_title <> '';
