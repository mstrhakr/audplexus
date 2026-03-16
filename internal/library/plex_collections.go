package library

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// plexIdentityResponse wraps the root "/" identity endpoint.
type plexIdentityResponse struct {
	MediaContainer plexIdentityContainer `json:"MediaContainer"`
}

type plexIdentityContainer struct {
	MachineIdentifier string `json:"machineIdentifier"`
}

// plexCollectionsResponse wraps /library/sections/{id}/collections.
type plexCollectionsResponse struct {
	MediaContainer plexCollectionsContainer `json:"MediaContainer"`
}

type plexCollectionsContainer struct {
	Size     int                `json:"size"`
	Metadata []plexCollectionMD `json:"Metadata"`
}

type plexCollectionMD struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
	Subtype   string `json:"subtype"`
}

// plexCreateCollectionResponse wraps POST /library/collections response.
type plexCreateCollectionResponse struct {
	MediaContainer plexCreateCollectionContainer `json:"MediaContainer"`
}

type plexCreateCollectionContainer struct {
	Size     int                `json:"size"`
	Metadata []plexCollectionMD `json:"Metadata"`
}

// plexSearchAlbumsResponse wraps the /library/sections/{id}/albums search.
type plexSearchAlbumsResponse struct {
	MediaContainer plexSearchAlbumsContainer `json:"MediaContainer"`
}

type plexSearchAlbumsContainer struct {
	Size     int               `json:"size"`
	Metadata []plexSearchAlbum `json:"Metadata"`
}

type plexSearchAlbum struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
}

// addBookToSeriesCollection finds or creates a Plex collection for the series
// and adds the book's album to it. This runs asynchronously after the Plex scan.
func (dm *DownloadManager) addBookToSeriesCollection(series, bookTitle string) {
	if strings.TrimSpace(series) == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		plexURL, plexToken, sectionID := dm.getPlexScanSettings(ctx)
		if plexURL == "" || plexToken == "" || sectionID == "" {
			return
		}

		// Poll Plex until the album appears (Plex needs time to index after scan).
		albumKey, err := dm.waitForPlexAlbum(ctx, plexURL, plexToken, sectionID, bookTitle)
		if err != nil {
			dlLog.Warn().Err(err).
				Str("series", series).
				Str("book", bookTitle).
				Msg("failed to add book to plex series collection")
			return
		}

		if err := dm.ensureBookInCollectionWithKey(ctx, plexURL, plexToken, sectionID, series, albumKey); err != nil {
			dlLog.Warn().Err(err).
				Str("series", series).
				Str("book", bookTitle).
				Msg("failed to add book to plex series collection")
		} else {
			dlLog.Info().
				Str("series", series).
				Str("book", bookTitle).
				Msg("book added to plex series collection")
		}
	}()
}

// waitForPlexAlbum polls Plex until the album appears in the library, returning its ratingKey.
func (dm *DownloadManager) waitForPlexAlbum(ctx context.Context, plexURL, plexToken, sectionID, bookTitle string) (string, error) {
	// Initial delay to give Plex a head start on indexing.
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Second):
	}

	intervals := []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second, 30 * time.Second}
	var lastErr error
	for _, wait := range intervals {
		albumKey, err := dm.plexFindAlbum(ctx, plexURL, plexToken, sectionID, bookTitle)
		if err == nil {
			return albumKey, nil
		}
		lastErr = err
		dlLog.Debug().Str("book", bookTitle).Dur("retry_in", wait).Msg("album not yet in Plex, retrying...")
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}

	// One final attempt.
	albumKey, err := dm.plexFindAlbum(ctx, plexURL, plexToken, sectionID, bookTitle)
	if err == nil {
		return albumKey, nil
	}
	return "", fmt.Errorf("album %q not found in Plex after retries: %w", bookTitle, lastErr)
}

func (dm *DownloadManager) ensureBookInCollection(ctx context.Context, plexURL, plexToken, sectionID, series, bookTitle string) error {
	// 1. Get the server's machine identifier for building source URIs.
	machineID, err := dm.plexMachineIdentifier(ctx, plexURL, plexToken)
	if err != nil {
		return fmt.Errorf("get machine identifier: %w", err)
	}

	// 2. Search for the book in Plex by title to get its ratingKey.
	albumKey, err := dm.plexFindAlbum(ctx, plexURL, plexToken, sectionID, bookTitle)
	if err != nil {
		return fmt.Errorf("find album %q: %w", bookTitle, err)
	}

	// 3. Find or create the series collection.
	collectionID, err := dm.plexFindOrCreateCollection(ctx, plexURL, plexToken, sectionID, series, machineID)
	if err != nil {
		return fmt.Errorf("find/create collection %q: %w", series, err)
	}

	// 4. Add the album to the collection.
	itemURI := fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", machineID, albumKey)
	if err := dm.plexAddToCollection(ctx, plexURL, plexToken, collectionID, itemURI); err != nil {
		return fmt.Errorf("add to collection: %w", err)
	}

	return nil
}

// ensureBookInCollectionWithKey is like ensureBookInCollection but uses a pre-resolved albumKey
// (used when the caller already waited for the album to appear in Plex).
func (dm *DownloadManager) ensureBookInCollectionWithKey(ctx context.Context, plexURL, plexToken, sectionID, series, albumKey string) error {
	machineID, err := dm.plexMachineIdentifier(ctx, plexURL, plexToken)
	if err != nil {
		return fmt.Errorf("get machine identifier: %w", err)
	}

	collectionID, err := dm.plexFindOrCreateCollection(ctx, plexURL, plexToken, sectionID, series, machineID)
	if err != nil {
		return fmt.Errorf("find/create collection %q: %w", series, err)
	}

	itemURI := fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", machineID, albumKey)
	if err := dm.plexAddToCollection(ctx, plexURL, plexToken, collectionID, itemURI); err != nil {
		return fmt.Errorf("add to collection: %w", err)
	}

	return nil
}

func (dm *DownloadManager) plexMachineIdentifier(ctx context.Context, plexURL, token string) (string, error) {
	u, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Plex URL: %w", err)
	}
	q := u.Query()
	q.Set("X-Plex-Token", token)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	dm.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("identity endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var identity plexIdentityResponse
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		return "", fmt.Errorf("parse identity: %w", err)
	}

	id := strings.TrimSpace(identity.MediaContainer.MachineIdentifier)
	if id == "" {
		return "", fmt.Errorf("empty machine identifier")
	}
	return id, nil
}

func (dm *DownloadManager) plexFindAlbum(ctx context.Context, plexURL, token, sectionID, title string) (string, error) {
	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Plex URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/sections/" + url.PathEscape(sectionID) + "/albums"
	q := base.Query()
	q.Set("X-Plex-Token", token)
	q.Set("title", title)
	q.Set("X-Plex-Container-Start", "0")
	q.Set("X-Plex-Container-Size", "50")
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return "", err
	}
	dm.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("albums search returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var albumsResp plexSearchAlbumsResponse
	if err := json.NewDecoder(resp.Body).Decode(&albumsResp); err != nil {
		return "", fmt.Errorf("parse albums: %w", err)
	}

	// Exact match preferred.
	for _, a := range albumsResp.MediaContainer.Metadata {
		if strings.EqualFold(strings.TrimSpace(a.Title), strings.TrimSpace(title)) {
			return a.RatingKey, nil
		}
	}
	// Partial fallback: first result whose title contains our title.
	titleLower := strings.ToLower(strings.TrimSpace(title))
	for _, a := range albumsResp.MediaContainer.Metadata {
		if strings.Contains(strings.ToLower(a.Title), titleLower) {
			return a.RatingKey, nil
		}
	}

	return "", fmt.Errorf("album %q not found in Plex (section %s)", title, sectionID)
}

func (dm *DownloadManager) plexListCollections(ctx context.Context, plexURL, token, sectionID string) ([]plexCollectionMD, error) {
	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid Plex URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/sections/" + url.PathEscape(sectionID) + "/collections"
	q := base.Query()
	q.Set("X-Plex-Token", token)
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
		return nil, fmt.Errorf("collections endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var collectionsResp plexCollectionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&collectionsResp); err != nil {
		return nil, fmt.Errorf("parse collections: %w", err)
	}

	return collectionsResp.MediaContainer.Metadata, nil
}

func (dm *DownloadManager) plexFindOrCreateCollection(ctx context.Context, plexURL, token, sectionID, seriesName, machineID string) (string, error) {
	collections, err := dm.plexListCollections(ctx, plexURL, token, sectionID)
	if err != nil {
		return "", fmt.Errorf("list collections: %w", err)
	}

	// Look for existing collection with matching title.
	for _, c := range collections {
		if strings.EqualFold(strings.TrimSpace(c.Title), strings.TrimSpace(seriesName)) {
			dlLog.Debug().Str("collection_id", c.RatingKey).Str("series", seriesName).Msg("found existing plex collection")
			return c.RatingKey, nil
		}
	}

	// Create a new collection. type=9 means album collection for music libraries.
	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Plex URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/collections"
	q := base.Query()
	q.Set("X-Plex-Token", token)
	q.Set("sectionId", sectionID)
	q.Set("title", seriesName)
	q.Set("type", "9") // 9 = album
	q.Set("smart", "0")
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), nil)
	if err != nil {
		return "", err
	}
	dm.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("create collection returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var createResp plexCreateCollectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		return "", fmt.Errorf("parse create response: %w", err)
	}

	if len(createResp.MediaContainer.Metadata) == 0 {
		return "", fmt.Errorf("collection created but no metadata returned")
	}

	collectionID := createResp.MediaContainer.Metadata[0].RatingKey
	dlLog.Info().Str("collection_id", collectionID).Str("series", seriesName).Msg("created new plex collection")
	return collectionID, nil
}

func (dm *DownloadManager) plexAddToCollection(ctx context.Context, plexURL, token, collectionID, itemURI string) error {
	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return fmt.Errorf("invalid Plex URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/collections/" + url.PathEscape(collectionID) + "/items"
	q := base.Query()
	q.Set("X-Plex-Token", token)
	q.Set("uri", itemURI)
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base.String(), nil)
	if err != nil {
		return err
	}
	dm.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("add to collection returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}
