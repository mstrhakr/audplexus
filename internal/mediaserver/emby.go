package mediaserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
)

// EmbyBackend implements Backend against an Emby Server.
//
// Emby uses an `api_key` query string (or `X-Emby-Token` header) for service
// auth. Audiobook items are returned with Type="Audio" inside an audiobook-
// type virtual folder. Collections are exposed as BoxSet items.
type EmbyBackend struct {
	db         database.Database
	libraryDir string
}

// NewEmby constructs an Emby backend.
func NewEmby(db database.Database, libraryDir string) *EmbyBackend {
	return &EmbyBackend{db: db, libraryDir: libraryDir}
}

func (e *EmbyBackend) Name() string { return string(TypeEmby) }

func (e *EmbyBackend) Configured(ctx context.Context) bool {
	u, k, l := e.settings(ctx)
	return u != "" && k != "" && l != ""
}

// settings returns (baseURL, apiKey, libraryID).
func (e *EmbyBackend) settings(ctx context.Context) (string, string, string) {
	u, _ := e.db.GetSetting(ctx, "emby_url")
	k, _ := e.db.GetSetting(ctx, "emby_api_key")
	l, _ := e.db.GetSetting(ctx, "emby_library_id")
	if strings.TrimSpace(u) == "" {
		u = strings.TrimSpace(os.Getenv("EMBY_URL"))
	}
	if strings.TrimSpace(k) == "" {
		k = strings.TrimSpace(os.Getenv("EMBY_API_KEY"))
	}
	if strings.TrimSpace(l) == "" {
		l = strings.TrimSpace(os.Getenv("EMBY_LIBRARY_ID"))
	}
	return strings.TrimSpace(u), strings.TrimSpace(k), strings.TrimSpace(l)
}

// libraryServerPath returns the path the Emby server uses to read the library
// (cached in DB; populated on first scan or via VirtualFolders lookup).
func (e *EmbyBackend) libraryServerPath(ctx context.Context, baseURL, apiKey, libraryID string) string {
	cached, _ := e.db.GetSetting(ctx, "emby_library_path")
	cached = strings.TrimSpace(cached)
	if cached != "" {
		return cached
	}
	if v := strings.TrimSpace(os.Getenv("EMBY_LIBRARY_PATH")); v != "" {
		return v
	}
	fetched, err := e.fetchVirtualFolderPath(ctx, baseURL, apiKey, libraryID)
	if err != nil {
		msLog.Warn().Err(err).Str("library_id", libraryID).Msg("emby: failed to fetch virtual folder path")
		return ""
	}
	if fetched != "" {
		_ = e.db.SetSetting(ctx, "emby_library_path", fetched)
	}
	return fetched
}

// TriggerScanForBook asks Emby to refresh the folder containing finalPath.
// Strategy: refresh the parent folder's BaseItem if we can resolve it by
// path, else fall back to a full library refresh.
func (e *EmbyBackend) TriggerScanForBook(finalPath string) {
	if strings.TrimSpace(finalPath) == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		baseURL, apiKey, libraryID := e.settings(ctx)
		if baseURL == "" || apiKey == "" || libraryID == "" {
			return
		}

		// Translate local path → server-visible path.
		localFolder := filepath.Dir(finalPath)
		serverPath := e.libraryServerPath(ctx, baseURL, apiKey, libraryID)
		scanPath, ok := translateScanPath(localFolder, strings.TrimSpace(e.libraryDir), serverPath)
		if !ok {
			msLog.Debug().Str("local_path", localFolder).Str("server_path", serverPath).Msg("emby: path translation failed; falling back to library refresh")
			if err := e.refreshLibrary(ctx, baseURL, apiKey, libraryID); err != nil {
				msLog.Warn().Err(err).Msg("emby: library refresh failed")
				return
			}
			msLog.Info().Str("library_id", libraryID).Msg("emby: full library refresh triggered")
			return
		}

		// Try to find the BaseItem for the parent folder and refresh it
		// specifically. Falling back to a library-wide refresh on lookup miss.
		itemID, err := e.findItemByPath(ctx, baseURL, apiKey, libraryID, scanPath)
		if err != nil || itemID == "" {
			msLog.Debug().Err(err).Str("server_path", scanPath).Msg("emby: no BaseItem for folder; refreshing whole library")
			if err := e.refreshLibrary(ctx, baseURL, apiKey, libraryID); err != nil {
				msLog.Warn().Err(err).Msg("emby: library refresh failed")
				return
			}
			msLog.Info().Str("server_path", scanPath).Msg("emby: full library refresh triggered (folder not yet indexed)")
			return
		}

		if err := e.refreshItem(ctx, baseURL, apiKey, itemID); err != nil {
			msLog.Warn().Err(err).Str("item_id", itemID).Msg("emby: item refresh failed")
			return
		}
		msLog.Info().Str("item_id", itemID).Str("server_path", scanPath).Msg("emby: targeted folder refresh triggered")
	}()
}

// EnsureBookInSeriesCollection waits for Emby to index the book, then ensures
// a BoxSet collection named `series` exists and contains the book's item.
func (e *EmbyBackend) EnsureBookInSeriesCollection(series, bookTitle string) {
	if strings.TrimSpace(series) == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()

		baseURL, apiKey, libraryID := e.settings(ctx)
		if baseURL == "" || apiKey == "" || libraryID == "" {
			return
		}

		itemID, err := e.waitForItem(ctx, baseURL, apiKey, libraryID, bookTitle)
		if err != nil {
			msLog.Warn().Err(err).Str("series", series).Str("book", bookTitle).Msg("emby: failed to add book to series collection")
			return
		}

		collectionID, err := e.findOrCreateCollection(ctx, baseURL, apiKey, series, itemID)
		if err != nil {
			msLog.Warn().Err(err).Str("series", series).Msg("emby: failed to find/create collection")
			return
		}

		if err := e.addToCollection(ctx, baseURL, apiKey, collectionID, itemID); err != nil {
			msLog.Warn().Err(err).Str("series", series).Str("book", bookTitle).Msg("emby: failed to add book to collection")
			return
		}
		msLog.Info().Str("series", series).Str("book", bookTitle).Msg("emby: book added to series collection")
	}()
}

// ReconcileLibrary walks the Emby library, recording each indexed item's
// server ID against matching local books, then ensures every series with
// matched books has a populated BoxSet collection.
func (e *EmbyBackend) ReconcileLibrary(ctx context.Context, progressFn func(current, total int)) error {
	baseURL, apiKey, libraryID := e.settings(ctx)
	if baseURL == "" || apiKey == "" || libraryID == "" {
		return fmt.Errorf("Emby not configured")
	}

	msLog.Info().Msg("emby: fetching all library items for reconciliation")
	items, err := e.listAllItems(ctx, baseURL, apiKey, libraryID)
	if err != nil {
		return fmt.Errorf("list Emby items: %w", err)
	}
	msLog.Info().Int("emby_items", len(items)).Msg("emby: fetched library item list")

	completeStatus := database.BookStatusComplete
	books, _, err := e.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 100000})
	if err != nil {
		return fmt.Errorf("list complete books: %w", err)
	}

	booksByTitle := make(map[string][]database.Book)
	for _, b := range books {
		key := strings.ToLower(strings.TrimSpace(b.Title))
		booksByTitle[key] = append(booksByTitle[key], b)
	}

	totalSteps := len(items)
	if progressFn != nil {
		progressFn(0, totalSteps)
	}

	matched := 0
	for i, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		title := strings.TrimSpace(item.Name)
		key := strings.ToLower(title)
		if candidates, ok := booksByTitle[key]; ok {
			for _, book := range candidates {
				if book.MediaServerID != item.ID || book.MediaServerTitle != title {
					if err := e.db.UpdateBookMediaServerInfo(ctx, book.ID, item.ID, title); err != nil {
						msLog.Warn().Err(err).Int64("book_id", book.ID).Str("title", book.Title).Msg("emby: failed to update book media server info")
					} else {
						matched++
					}
				}
			}
		}
		if progressFn != nil && (i%25 == 0 || i == len(items)-1) {
			progressFn(i+1, totalSteps)
		}
	}
	msLog.Info().Int("matched", matched).Int("emby_items", len(items)).Int("local_books", len(books)).Msg("emby: item matching complete")

	books, _, err = e.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 100000})
	if err != nil {
		return fmt.Errorf("reload books for collection reconciliation: %w", err)
	}

	seriesBooks := make(map[string][]database.Book)
	for _, b := range books {
		series := strings.TrimSpace(b.Series)
		if series == "" || b.MediaServerID == "" {
			continue
		}
		seriesBooks[series] = append(seriesBooks[series], b)
	}

	if len(seriesBooks) == 0 {
		msLog.Info().Msg("emby: no series with matched books to reconcile")
		return nil
	}

	collectionsAdded := 0
	seriesProcessed := 0
	totalSeries := len(seriesBooks)
	for series, booksInSeries := range seriesBooks {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var seedID string
		if len(booksInSeries) > 0 {
			seedID = booksInSeries[0].MediaServerID
		}
		collectionID, err := e.findOrCreateCollection(ctx, baseURL, apiKey, series, seedID)
		if err != nil {
			msLog.Warn().Err(err).Str("series", series).Msg("emby: failed to find/create collection during reconciliation")
			seriesProcessed++
			continue
		}

		for _, book := range booksInSeries {
			if err := e.addToCollection(ctx, baseURL, apiKey, collectionID, book.MediaServerID); err != nil {
				msLog.Warn().Err(err).Str("series", series).Str("book", book.Title).Msg("emby: failed to add book to collection during reconciliation")
			} else {
				collectionsAdded++
			}
		}
		seriesProcessed++
		if progressFn != nil {
			progressFn(totalSteps+seriesProcessed, totalSteps+totalSeries)
		}
	}

	msLog.Info().Int("series_checked", totalSeries).Int("collection_adds", collectionsAdded).Msg("emby: series collection reconciliation complete")
	return nil
}

// LibraryItemCount returns how many items Emby has indexed in the configured
// library (Audio + AudioBook types).
func (e *EmbyBackend) LibraryItemCount(ctx context.Context) (int, error) {
	baseURL, apiKey, libraryID := e.settings(ctx)
	if baseURL == "" || apiKey == "" || libraryID == "" {
		return 0, nil
	}

	u, err := e.buildURL(baseURL, "/emby/Items", apiKey, map[string]string{
		"ParentId":         libraryID,
		"Recursive":        "true",
		"IncludeItemTypes": "Audio,AudioBook,MusicAlbum",
		"Limit":            "0",
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("emby items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		TotalRecordCount int `json:"TotalRecordCount"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, err
	}
	return r.TotalRecordCount, nil
}

// TriggerLibraryScan triggers a whole-library refresh and returns the count.
func (e *EmbyBackend) TriggerLibraryScan(ctx context.Context) (int, error) {
	baseURL, apiKey, libraryID := e.settings(ctx)
	if baseURL == "" || apiKey == "" || libraryID == "" {
		return 0, fmt.Errorf("Emby not configured")
	}
	if err := e.refreshLibrary(ctx, baseURL, apiKey, libraryID); err != nil {
		return 0, err
	}
	return e.LibraryItemCount(ctx)
}

// --- internal helpers ---

// embyItem is a minimal projection of the Emby BaseItem JSON.
type embyItem struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
	Type string `json:"Type"`
	Path string `json:"Path,omitempty"`
}

// buildURL composes an Emby request URL. apiKey goes in the query string for
// broadest compatibility (some reverse proxies strip custom headers).
func (e *EmbyBackend) buildURL(baseURL, path, apiKey string, extra map[string]string) (string, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Emby URL: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	q := u.Query()
	q.Set("api_key", apiKey)
	for k, v := range extra {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (e *EmbyBackend) refreshLibrary(ctx context.Context, baseURL, apiKey, libraryID string) error {
	// Refreshing the library folder item triggers a scan of just that library;
	// /Library/Refresh would scan everything.
	return e.refreshItem(ctx, baseURL, apiKey, libraryID)
}

func (e *EmbyBackend) refreshItem(ctx context.Context, baseURL, apiKey, itemID string) error {
	u, err := e.buildURL(baseURL, "/emby/Items/"+url.PathEscape(itemID)+"/Refresh", apiKey, map[string]string{
		"Recursive":           "true",
		"ImageRefreshMode":    "Default",
		"MetadataRefreshMode": "Default",
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("emby refresh returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// fetchVirtualFolderPath looks up the on-disk path Emby uses for libraryID.
func (e *EmbyBackend) fetchVirtualFolderPath(ctx context.Context, baseURL, apiKey, libraryID string) (string, error) {
	u, err := e.buildURL(baseURL, "/emby/Library/VirtualFolders", apiKey, nil)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("emby VirtualFolders returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var folders []struct {
		ItemID    string   `json:"ItemId"`
		Locations []string `json:"Locations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		return "", fmt.Errorf("parse VirtualFolders: %w", err)
	}
	for _, f := range folders {
		if f.ItemID == libraryID && len(f.Locations) > 0 {
			return strings.TrimSpace(f.Locations[0]), nil
		}
	}
	return "", fmt.Errorf("no virtual folder found for library %s", libraryID)
}

// findItemByPath asks Emby for the BaseItem with a given on-disk path, scoped
// to the configured library. Returns "" when no match.
func (e *EmbyBackend) findItemByPath(ctx context.Context, baseURL, apiKey, libraryID, serverPath string) (string, error) {
	u, err := e.buildURL(baseURL, "/emby/Items", apiKey, map[string]string{
		"ParentId":  libraryID,
		"Recursive": "true",
		"Path":      serverPath,
		"Fields":    "Path",
		"Limit":     "5",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("emby items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		Items []embyItem `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	for _, it := range r.Items {
		if strings.EqualFold(strings.TrimRight(it.Path, "/"), strings.TrimRight(serverPath, "/")) {
			return it.ID, nil
		}
	}
	return "", nil
}

func (e *EmbyBackend) waitForItem(ctx context.Context, baseURL, apiKey, libraryID, title string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Second):
	}

	intervals := []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second, 30 * time.Second}
	var lastErr error
	for _, wait := range intervals {
		id, err := e.findItemByTitle(ctx, baseURL, apiKey, libraryID, title)
		if err == nil && id != "" {
			return id, nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
	id, err := e.findItemByTitle(ctx, baseURL, apiKey, libraryID, title)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil {
		return "", fmt.Errorf("item %q not found in Emby after retries: %w", title, err)
	}
	if lastErr != nil {
		return "", fmt.Errorf("item %q not found in Emby after retries: %w", title, lastErr)
	}
	return "", fmt.Errorf("item %q not found in Emby after retries", title)
}

func (e *EmbyBackend) findItemByTitle(ctx context.Context, baseURL, apiKey, libraryID, title string) (string, error) {
	u, err := e.buildURL(baseURL, "/emby/Items", apiKey, map[string]string{
		"ParentId":         libraryID,
		"Recursive":        "true",
		"IncludeItemTypes": "Audio,AudioBook,MusicAlbum",
		"SearchTerm":       title,
		"Limit":            "20",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("emby items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		Items []embyItem `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	titleTrim := strings.TrimSpace(title)
	for _, it := range r.Items {
		if strings.EqualFold(strings.TrimSpace(it.Name), titleTrim) {
			return it.ID, nil
		}
	}
	titleLower := strings.ToLower(titleTrim)
	for _, it := range r.Items {
		if strings.Contains(strings.ToLower(it.Name), titleLower) {
			return it.ID, nil
		}
	}
	return "", nil
}

// findOrCreateCollection looks up a BoxSet by exact name. If missing, creates
// one seeded with seedItemID (Emby requires at least one item to create a
// collection in a single call).
func (e *EmbyBackend) findOrCreateCollection(ctx context.Context, baseURL, apiKey, name, seedItemID string) (string, error) {
	if id, err := e.findCollectionByName(ctx, baseURL, apiKey, name); err == nil && id != "" {
		return id, nil
	} else if err != nil {
		return "", fmt.Errorf("look up collection: %w", err)
	}

	if seedItemID == "" {
		return "", fmt.Errorf("cannot create empty Emby collection (no seed item)")
	}

	u, err := e.buildURL(baseURL, "/emby/Collections", apiKey, map[string]string{
		"Name": name,
		"Ids":  seedItemID,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("emby create collection returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("parse create collection response: %w", err)
	}
	if r.ID == "" {
		// Fall back to a name lookup if Emby didn't echo the new ID.
		return e.findCollectionByName(ctx, baseURL, apiKey, name)
	}
	return r.ID, nil
}

func (e *EmbyBackend) findCollectionByName(ctx context.Context, baseURL, apiKey, name string) (string, error) {
	u, err := e.buildURL(baseURL, "/emby/Items", apiKey, map[string]string{
		"IncludeItemTypes": "BoxSet",
		"Recursive":        "true",
		"SearchTerm":       name,
		"Limit":            "20",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("emby collections lookup returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		Items []embyItem `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for _, it := range r.Items {
		if strings.EqualFold(strings.TrimSpace(it.Name), strings.TrimSpace(name)) {
			return it.ID, nil
		}
		if strings.ToLower(strings.TrimSpace(it.Name)) == want {
			return it.ID, nil
		}
	}
	return "", nil
}

func (e *EmbyBackend) addToCollection(ctx context.Context, baseURL, apiKey, collectionID, itemID string) error {
	u, err := e.buildURL(baseURL, "/emby/Collections/"+url.PathEscape(collectionID)+"/Items", apiKey, map[string]string{
		"Ids": itemID,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(nil))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("emby add to collection returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (e *EmbyBackend) listAllItems(ctx context.Context, baseURL, apiKey, libraryID string) ([]embyItem, error) {
	const pageSize = 200
	var all []embyItem
	startIndex := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		u, err := e.buildURL(baseURL, "/emby/Items", apiKey, map[string]string{
			"ParentId":         libraryID,
			"Recursive":        "true",
			"IncludeItemTypes": "Audio,AudioBook,MusicAlbum",
			"Limit":            strconv.Itoa(pageSize),
			"StartIndex":       strconv.Itoa(startIndex),
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			return nil, fmt.Errorf("emby items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var r struct {
			Items            []embyItem `json:"Items"`
			TotalRecordCount int        `json:"TotalRecordCount"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("parse Emby items at %d: %w", startIndex, err)
		}
		resp.Body.Close()

		all = append(all, r.Items...)
		if len(r.Items) == 0 || len(all) >= r.TotalRecordCount {
			break
		}
		startIndex += len(r.Items)
	}
	return all, nil
}
