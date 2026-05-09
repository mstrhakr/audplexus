package mediaserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
)

const plexProduct = "Audplexus"

// PlexBackend implements Backend against a Plex Media Server.
type PlexBackend struct {
	db         database.Database
	libraryDir string
	clientID   string
}

// NewPlex constructs a Plex backend. clientID is auto-derived from hostname.
func NewPlex(db database.Database, libraryDir string) *PlexBackend {
	return &PlexBackend{db: db, libraryDir: libraryDir, clientID: buildPlexClientID()}
}

func buildPlexClientID() string {
	hostname, _ := os.Hostname()
	hostname = strings.TrimSpace(strings.ToLower(hostname))
	if hostname == "" {
		hostname = "local"
	}
	return "audplexus-" + hostname
}

func (p *PlexBackend) Name() string { return string(TypePlex) }

func (p *PlexBackend) Configured(ctx context.Context) bool {
	u, t, s := p.settings(ctx)
	return u != "" && t != "" && s != ""
}

func (p *PlexBackend) settings(ctx context.Context) (string, string, string) {
	u, _ := p.db.GetSetting(ctx, "plex_url")
	t, _ := p.db.GetSetting(ctx, "plex_token")
	s, _ := p.db.GetSetting(ctx, "plex_section_id")
	if strings.TrimSpace(u) == "" {
		u = strings.TrimSpace(os.Getenv("PLEX_URL"))
	}
	if strings.TrimSpace(t) == "" {
		t = strings.TrimSpace(os.Getenv("PLEX_TOKEN"))
	}
	if strings.TrimSpace(s) == "" {
		s = strings.TrimSpace(os.Getenv("PLEX_SECTION_ID"))
	}
	return strings.TrimSpace(u), strings.TrimSpace(t), strings.TrimSpace(s)
}

// TriggerScanForBook asks Plex to scan the folder containing finalPath.
func (p *PlexBackend) TriggerScanForBook(finalPath string) {
	if strings.TrimSpace(finalPath) == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		plexURL, plexToken, sectionID := p.settings(ctx)
		if plexURL == "" || plexToken == "" || sectionID == "" {
			return
		}

		localScanPath := filepath.Dir(finalPath)
		scanPath, ok := p.resolveScanPath(ctx, plexURL, plexToken, sectionID, localScanPath)
		if !ok {
			msLog.Warn().
				Str("backend", "plex").
				Str("local_path", localScanPath).
				Str("section_id", sectionID).
				Msg("skipping per-book plex scan; section path unavailable")
			return
		}

		if err := p.triggerSectionScan(ctx, plexURL, plexToken, sectionID, scanPath); err != nil {
			msLog.Warn().Err(err).Str("scan_path", scanPath).Msg("plex scan trigger failed")
			return
		}
		msLog.Info().Str("scan_path", scanPath).Str("section_id", sectionID).Msg("plex scan triggered for completed book")
	}()
}

// EnsureBookInSeriesCollection adds the book to a series collection in Plex.
func (p *PlexBackend) EnsureBookInSeriesCollection(series, bookTitle string) {
	if strings.TrimSpace(series) == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		plexURL, plexToken, sectionID := p.settings(ctx)
		if plexURL == "" || plexToken == "" || sectionID == "" {
			return
		}

		albumKey, err := p.waitForAlbum(ctx, plexURL, plexToken, sectionID, bookTitle)
		if err != nil {
			msLog.Warn().Err(err).Str("series", series).Str("book", bookTitle).Msg("plex: failed to add book to series collection")
			return
		}

		if err := p.ensureBookInCollectionWithKey(ctx, plexURL, plexToken, sectionID, series, albumKey); err != nil {
			msLog.Warn().Err(err).Str("series", series).Str("book", bookTitle).Msg("plex: failed to add book to series collection")
		} else {
			msLog.Info().Str("series", series).Str("book", bookTitle).Msg("plex: book added to series collection")
		}
	}()
}

// ReconcileLibrary fetches all Plex albums, matches to local books, and
// ensures series collections are populated.
func (p *PlexBackend) ReconcileLibrary(ctx context.Context, progressFn func(current, total int)) error {
	plexURL, plexToken, sectionID := p.settings(ctx)
	if plexURL == "" || plexToken == "" || sectionID == "" {
		return fmt.Errorf("Plex not configured")
	}

	msLog.Info().Msg("fetching all Plex albums for reconciliation")
	albums, err := p.listAllAlbums(ctx, plexURL, plexToken, sectionID)
	if err != nil {
		return fmt.Errorf("list Plex albums: %w", err)
	}
	msLog.Info().Int("plex_albums", len(albums)).Msg("fetched Plex album list")

	completeStatus := database.BookStatusComplete
	books, _, err := p.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 100000})
	if err != nil {
		return fmt.Errorf("list complete books: %w", err)
	}

	booksByTitle := make(map[string][]database.Book)
	for _, b := range books {
		booksByTitle[normalizeTitle(b.Title)] = append(booksByTitle[normalizeTitle(b.Title)], b)
	}

	totalSteps := len(albums)
	if progressFn != nil {
		progressFn(0, totalSteps)
	}

	matched := 0
	for i, album := range albums {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		albumTitle := strings.TrimSpace(album.Title)
		key := normalizeTitle(albumTitle)
		if candidates, ok := booksByTitle[key]; ok {
			for _, book := range candidates {
				if book.MediaServerID != album.RatingKey || book.MediaServerTitle != albumTitle {
					if err := p.db.UpdateBookMediaServerInfo(ctx, book.ID, album.RatingKey, albumTitle); err != nil {
						msLog.Warn().Err(err).Int64("book_id", book.ID).Str("title", book.Title).Msg("plex: failed to update book media server info")
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
	msLog.Info().Int("matched", matched).Int("plex_albums", len(albums)).Int("local_books", len(books)).Msg("plex album matching complete")

	books, _, err = p.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 100000})
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
		msLog.Info().Msg("no series with Plex-matched books to reconcile")
		return nil
	}

	machineID, err := p.machineIdentifier(ctx, plexURL, plexToken)
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

		collectionID, err := p.findOrCreateCollection(ctx, plexURL, plexToken, sectionID, series, machineID)
		if err != nil {
			msLog.Warn().Err(err).Str("series", series).Msg("plex: failed to find/create collection during reconciliation")
			seriesProcessed++
			continue
		}

		for _, book := range booksInSeries {
			itemURI := fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", machineID, book.MediaServerID)
			if err := p.addToCollection(ctx, plexURL, plexToken, collectionID, itemURI); err != nil {
				msLog.Warn().Err(err).Str("series", series).Str("book", book.Title).Msg("plex: failed to add book to series collection during reconciliation")
			} else {
				collectionsAdded++
			}
		}
		seriesProcessed++
		if progressFn != nil {
			progressFn(totalSteps+seriesProcessed, totalSteps+totalSeries)
		}
	}

	msLog.Info().Int("series_checked", totalSeries).Int("collection_adds", collectionsAdded).Msg("plex series collection reconciliation complete")
	return nil
}

// LibraryItemCount queries Plex for the album count in the configured section.
func (p *PlexBackend) LibraryItemCount(ctx context.Context) (int, error) {
	plexURL, plexToken, sectionID := p.settings(ctx)
	if plexURL == "" || plexToken == "" || sectionID == "" {
		return 0, nil
	}

	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return 0, fmt.Errorf("invalid Plex URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/sections/" + url.PathEscape(sectionID) + "/albums"
	q := base.Query()
	q.Set("X-Plex-Token", plexToken)
	q.Set("X-Plex-Container-Start", "0")
	q.Set("X-Plex-Container-Size", "0")
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return 0, err
	}
	p.addHeaders(req, plexToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("plex section items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		MediaContainer struct {
			Size      int `json:"size"`
			TotalSize int `json:"totalSize"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, err
	}
	if r.MediaContainer.TotalSize > 0 {
		return r.MediaContainer.TotalSize, nil
	}
	return r.MediaContainer.Size, nil
}

// TriggerLibraryScan triggers a scan of the whole configured section and
// returns the post-scan item count.
func (p *PlexBackend) TriggerLibraryScan(ctx context.Context) (int, error) {
	plexURL, plexToken, sectionID := p.settings(ctx)
	if plexURL == "" || plexToken == "" || sectionID == "" {
		return 0, fmt.Errorf("Plex not configured")
	}
	if err := p.triggerSectionScan(ctx, plexURL, plexToken, sectionID, ""); err != nil {
		return 0, err
	}
	return p.LibraryItemCount(ctx)
}

// --- internal helpers (verbatim port of internal/library/plex_*.go) ---

func (p *PlexBackend) resolveScanPath(ctx context.Context, plexURL, plexToken, sectionID, localScanPath string) (string, bool) {
	plexPath, _ := p.db.GetSetting(ctx, "plex_section_path")
	plexPath = strings.TrimSpace(plexPath)

	if plexPath == "" {
		fetched, err := p.fetchSectionPath(ctx, plexURL, plexToken, sectionID)
		if err != nil {
			msLog.Warn().Err(err).Str("section_id", sectionID).Msg("plex: failed to fetch section path")
		} else if fetched != "" {
			plexPath = fetched
			if err := p.db.SetSetting(ctx, "plex_section_path", fetched); err != nil {
				msLog.Warn().Err(err).Str("plex_section_path", fetched).Msg("plex: failed to cache section path")
			}
		}
	}

	if plexPath == "" {
		return "", false
	}

	scanPath, ok := translateScanPath(localScanPath, strings.TrimSpace(p.libraryDir), plexPath)
	if !ok {
		msLog.Warn().Str("local_path", localScanPath).Str("library_root", p.libraryDir).Str("plex_section_path", plexPath).Msg("plex: unable to translate scan path")
		return "", false
	}
	return scanPath, true
}

func (p *PlexBackend) fetchSectionPath(ctx context.Context, plexURL, token, sectionID string) (string, error) {
	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Plex URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/sections/" + url.PathEscape(sectionID)
	q := base.Query()
	q.Set("X-Plex-Token", token)
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return "", err
	}
	p.addHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("plex section detail returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var detail struct {
		MediaContainer struct {
			Directories []struct {
				Locations []struct {
					Path string `json:"path"`
				} `json:"Location"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return "", fmt.Errorf("parse section details: %w", err)
	}

	for _, dir := range detail.MediaContainer.Directories {
		for _, loc := range dir.Locations {
			if path := strings.TrimSpace(loc.Path); path != "" {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("no location path found for section %s", sectionID)
}

func (p *PlexBackend) triggerSectionScan(ctx context.Context, plexURL, token, sectionID, scanPath string) error {
	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return fmt.Errorf("invalid Plex URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/sections/" + url.PathEscape(sectionID) + "/refresh"
	q := base.Query()
	q.Set("X-Plex-Token", token)
	if strings.TrimSpace(scanPath) != "" {
		q.Set("path", scanPath)
	}
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), nil)
	if err != nil {
		return err
	}
	p.addHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("plex scan returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (p *PlexBackend) addHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", plexProduct)
	req.Header.Set("X-Plex-Client-Identifier", p.clientID)
	req.Header.Set("X-Plex-Device-Name", "Audplexus")
	req.Header.Set("X-Plex-Platform", "Go")
	req.Header.Set("X-Plex-Version", "1.0")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("X-Plex-Token", token)
	}
}

func (p *PlexBackend) waitForAlbum(ctx context.Context, plexURL, token, sectionID, bookTitle string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Second):
	}

	intervals := []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second, 30 * time.Second}
	var lastErr error
	for _, wait := range intervals {
		key, err := p.findAlbum(ctx, plexURL, token, sectionID, bookTitle)
		if err == nil {
			return key, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
	key, err := p.findAlbum(ctx, plexURL, token, sectionID, bookTitle)
	if err == nil {
		return key, nil
	}
	return "", fmt.Errorf("album %q not found in Plex after retries: %w", bookTitle, lastErr)
}

func (p *PlexBackend) findAlbum(ctx context.Context, plexURL, token, sectionID, title string) (string, error) {
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
	p.addHeaders(req, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("plex albums search returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Title     string `json:"title"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	wantNorm := normalizeTitle(title)
	for _, a := range r.MediaContainer.Metadata {
		if normalizeTitle(a.Title) == wantNorm {
			return a.RatingKey, nil
		}
	}
	for _, a := range r.MediaContainer.Metadata {
		if strings.Contains(normalizeTitle(a.Title), wantNorm) {
			return a.RatingKey, nil
		}
	}
	return "", fmt.Errorf("album %q not found in Plex (section %s)", title, sectionID)
}

func (p *PlexBackend) ensureBookInCollectionWithKey(ctx context.Context, plexURL, token, sectionID, series, albumKey string) error {
	machineID, err := p.machineIdentifier(ctx, plexURL, token)
	if err != nil {
		return fmt.Errorf("get machine identifier: %w", err)
	}
	collectionID, err := p.findOrCreateCollection(ctx, plexURL, token, sectionID, series, machineID)
	if err != nil {
		return fmt.Errorf("find/create collection %q: %w", series, err)
	}
	itemURI := fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", machineID, albumKey)
	if err := p.addToCollection(ctx, plexURL, token, collectionID, itemURI); err != nil {
		return fmt.Errorf("add to collection: %w", err)
	}
	return nil
}

func (p *PlexBackend) machineIdentifier(ctx context.Context, plexURL, token string) (string, error) {
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
	p.addHeaders(req, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("plex identity returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("parse identity: %w", err)
	}
	id := strings.TrimSpace(r.MediaContainer.MachineIdentifier)
	if id == "" {
		return "", fmt.Errorf("empty machine identifier")
	}
	return id, nil
}

func (p *PlexBackend) findOrCreateCollection(ctx context.Context, plexURL, token, sectionID, seriesName, machineID string) (string, error) {
	collections, err := p.listCollections(ctx, plexURL, token, sectionID)
	if err != nil {
		return "", fmt.Errorf("list collections: %w", err)
	}
	wantSeries := normalizeTitle(seriesName)
	for _, c := range collections {
		if normalizeTitle(c.Title) == wantSeries {
			return c.RatingKey, nil
		}
	}

	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Plex URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/collections"
	q := base.Query()
	q.Set("X-Plex-Token", token)
	q.Set("sectionId", sectionID)
	q.Set("title", seriesName)
	q.Set("type", "9")
	q.Set("smart", "0")
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), nil)
	if err != nil {
		return "", err
	}
	p.addHeaders(req, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("plex create collection returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("parse create response: %w", err)
	}
	if len(r.MediaContainer.Metadata) == 0 {
		return "", fmt.Errorf("collection created but no metadata returned")
	}
	return r.MediaContainer.Metadata[0].RatingKey, nil
}

type plexCollection struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
}

func (p *PlexBackend) listCollections(ctx context.Context, plexURL, token, sectionID string) ([]plexCollection, error) {
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
	p.addHeaders(req, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("plex collections returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		MediaContainer struct {
			Metadata []plexCollection `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("parse collections: %w", err)
	}
	return r.MediaContainer.Metadata, nil
}

func (p *PlexBackend) addToCollection(ctx context.Context, plexURL, token, collectionID, itemURI string) error {
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
	p.addHeaders(req, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("plex add to collection returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

type plexAlbumEntry struct {
	RatingKey   string `json:"ratingKey"`
	Title       string `json:"title"`
	ParentTitle string `json:"parentTitle"`
}

func (p *PlexBackend) listAllAlbums(ctx context.Context, plexURL, token, sectionID string) ([]plexAlbumEntry, error) {
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
		p.addHeaders(req, token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			return nil, fmt.Errorf("plex albums returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var r struct {
			MediaContainer struct {
				TotalSize int              `json:"totalSize"`
				Size      int              `json:"size"`
				Metadata  []plexAlbumEntry `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("parse albums page at offset %d: %w", offset, err)
		}
		resp.Body.Close()

		all = append(all, r.MediaContainer.Metadata...)
		totalSize := r.MediaContainer.TotalSize
		if totalSize == 0 {
			totalSize = r.MediaContainer.Size
		}
		if offset+len(r.MediaContainer.Metadata) >= totalSize || len(r.MediaContainer.Metadata) == 0 {
			break
		}
		offset += len(r.MediaContainer.Metadata)
	}
	return all, nil
}
