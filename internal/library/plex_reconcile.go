package library

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mstrhakr/audible-plex-downloader/internal/database"
)

// plexAllAlbumsResponse wraps the paginated /library/sections/{id}/albums response.
type plexAllAlbumsResponse struct {
	MediaContainer plexAllAlbumsContainer `json:"MediaContainer"`
}

type plexAllAlbumsContainer struct {
	TotalSize int              `json:"totalSize"`
	Size      int              `json:"size"`
	Offset    int              `json:"offset"`
	Metadata  []plexAlbumEntry `json:"Metadata"`
}

type plexAlbumEntry struct {
	RatingKey             string `json:"ratingKey"`
	Title                 string `json:"title"`
	ParentTitle           string `json:"parentTitle"` // artist/author name
	OriginallyAvailableAt string `json:"originallyAvailableAt,omitempty"`
}

// ReconcilePlexLibrary fetches all albums from Plex, matches them to local books,
// updates plex_rating_key/plex_title on matched books, and ensures series collections
// are set up correctly for all completed books with series info.
func (dm *DownloadManager) ReconcilePlexLibrary(ctx context.Context, progressFn func(current, total int)) error {
	plexURL, plexToken, sectionID := dm.getPlexScanSettings(ctx)
	if plexURL == "" || plexToken == "" || sectionID == "" {
		return fmt.Errorf("Plex not configured")
	}

	// Step 1: Fetch all albums from Plex.
	dlLog.Info().Msg("fetching all Plex albums for reconciliation")
	albums, err := dm.plexListAllAlbums(ctx, plexURL, plexToken, sectionID)
	if err != nil {
		return fmt.Errorf("list Plex albums: %w", err)
	}
	dlLog.Info().Int("plex_albums", len(albums)).Msg("fetched Plex album list")

	// Step 2: Load all complete books from local DB.
	completeStatus := database.BookStatusComplete
	books, _, err := dm.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 100000})
	if err != nil {
		return fmt.Errorf("list complete books: %w", err)
	}

	// Build a lookup map: lowercase title → []Book (multiple books can share a title).
	booksByTitle := make(map[string][]database.Book)
	for _, b := range books {
		key := strings.ToLower(strings.TrimSpace(b.Title))
		booksByTitle[key] = append(booksByTitle[key], b)
	}

	totalSteps := len(albums)
	// We'll also count collection steps after matching.
	if progressFn != nil {
		progressFn(0, totalSteps)
	}

	// Step 3: Match Plex albums to local books, update plex_rating_key and plex_title.
	matched := 0
	for i, album := range albums {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		albumTitle := strings.TrimSpace(album.Title)
		key := strings.ToLower(albumTitle)
		if candidates, ok := booksByTitle[key]; ok {
			for _, book := range candidates {
				if book.PlexRatingKey != album.RatingKey || book.PlexTitle != albumTitle {
					if err := dm.db.UpdateBookPlexInfo(ctx, book.ID, album.RatingKey, albumTitle); err != nil {
						dlLog.Warn().Err(err).Int64("book_id", book.ID).Str("title", book.Title).
							Msg("failed to update book plex info")
					} else {
						matched++
					}
				}
			}
		}
		if progressFn != nil && (i%25 == 0 || i == len(albums)-1) {
			progressFn(i+1, totalSteps)
		}
	}
	dlLog.Info().Int("matched", matched).Int("plex_albums", len(albums)).Int("local_books", len(books)).
		Msg("plex album matching complete")

	// Step 4: Reconcile series collections.
	// Reload books with updated plex info.
	books, _, err = dm.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 100000})
	if err != nil {
		return fmt.Errorf("reload books for collection reconciliation: %w", err)
	}

	// Group books by series.
	seriesBooks := make(map[string][]database.Book)
	for _, b := range books {
		series := strings.TrimSpace(b.Series)
		if series == "" || b.PlexRatingKey == "" {
			continue
		}
		seriesBooks[series] = append(seriesBooks[series], b)
	}

	if len(seriesBooks) == 0 {
		dlLog.Info().Msg("no series with Plex-matched books to reconcile")
		return nil
	}

	machineID, err := dm.plexMachineIdentifier(ctx, plexURL, plexToken)
	if err != nil {
		return fmt.Errorf("get machine identifier for collections: %w", err)
	}

	collectionsAdded := 0
	seriesProcessed := 0
	totalSeries := len(seriesBooks)
	for series, booksInSeries := range seriesBooks {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		collectionID, err := dm.plexFindOrCreateCollection(ctx, plexURL, plexToken, sectionID, series, machineID)
		if err != nil {
			dlLog.Warn().Err(err).Str("series", series).Msg("failed to find/create collection during reconciliation")
			seriesProcessed++
			continue
		}

		for _, book := range booksInSeries {
			itemURI := fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", machineID, book.PlexRatingKey)
			if err := dm.plexAddToCollection(ctx, plexURL, plexToken, collectionID, itemURI); err != nil {
				dlLog.Warn().Err(err).Str("series", series).Str("book", book.Title).
					Msg("failed to add book to series collection during reconciliation")
			} else {
				collectionsAdded++
			}
		}
		seriesProcessed++
		if progressFn != nil {
			// Report progress for the collection phase (offset by album matching)
			progressFn(totalSteps+seriesProcessed, totalSteps+totalSeries)
		}
	}

	dlLog.Info().
		Int("series_checked", totalSeries).
		Int("collection_adds", collectionsAdded).
		Msg("series collection reconciliation complete")

	return nil
}

// plexListAllAlbums fetches all albums from a Plex library section with pagination.
func (dm *DownloadManager) plexListAllAlbums(ctx context.Context, plexURL, token, sectionID string) ([]plexAlbumEntry, error) {
	const pageSize = 100
	var all []plexAlbumEntry
	offset := 0

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		base, err := url.Parse(strings.TrimRight(plexURL, "/"))
		if err != nil {
			return nil, fmt.Errorf("invalid Plex URL: %w", err)
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/library/sections/" + url.PathEscape(sectionID) + "/albums"
		q := base.Query()
		q.Set("X-Plex-Token", token)
		q.Set("X-Plex-Container-Start", fmt.Sprintf("%d", offset))
		q.Set("X-Plex-Container-Size", fmt.Sprintf("%d", pageSize))
		base.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
		if err != nil {
			return nil, err
		}
		dm.addPlexHeaders(req, token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return nil, fmt.Errorf("albums endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var albumsResp plexAllAlbumsResponse
		if err := json.NewDecoder(resp.Body).Decode(&albumsResp); err != nil {
			return nil, fmt.Errorf("parse albums page at offset %d: %w", offset, err)
		}

		all = append(all, albumsResp.MediaContainer.Metadata...)

		totalSize := albumsResp.MediaContainer.TotalSize
		if totalSize == 0 {
			totalSize = albumsResp.MediaContainer.Size
		}
		if offset+len(albumsResp.MediaContainer.Metadata) >= totalSize || len(albumsResp.MediaContainer.Metadata) == 0 {
			break
		}
		offset += len(albumsResp.MediaContainer.Metadata)
	}

	return all, nil
}
