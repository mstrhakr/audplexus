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
	"sort"
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

	// destination, if set, overrides the settings-table lookup so multiple
	// Plex destinations can have independent config. Settings-table fallback
	// keeps destination-scoped config isolated.
	destination *database.LibraryDestination
}

// NewPlex constructs a Plex backend. clientID is auto-derived from hostname.
func NewPlex(db database.Database, libraryDir string) *PlexBackend {
	return &PlexBackend{db: db, libraryDir: libraryDir, clientID: buildPlexClientID()}
}

// WithDestination binds the backend to a specific library_destinations row.
// settings() reads URL/Token/SectionID from the row instead of the settings
// table, so two Plex destinations can have independent config. Returns the
// receiver so callers can chain.
func (p *PlexBackend) WithDestination(d *database.LibraryDestination) *PlexBackend {
	p.destination = d
	return p
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

// Capabilities — Plex supports library scan, per-item refresh (via section
// refresh with a path filter), and series grouping (via collections). It
// does NOT support per-item tagging, image upload, or franchise tags.
func (p *PlexBackend) Capabilities() CapabilitySet {
	return NewCapabilitySet(
		CapTriggerScan,
		CapPerItemRefresh,
		CapSeriesGrouping,
		CapItemCount,
	)
}

func (p *PlexBackend) Configured(ctx context.Context) bool {
	u, t, s := p.settings(ctx)
	return u != "" && t != "" && s != ""
}

func (p *PlexBackend) settings(ctx context.Context) (string, string, string) {
	// Destination-row binding wins. Lets multiple Plex destinations have
	// independent URL/token/section.
	if p.destination != nil {
		return strings.TrimSpace(p.destination.URL),
			strings.TrimSpace(p.destination.PlexToken),
			strings.TrimSpace(p.destination.PlexSectionID)
	}
	// Global settings/env fallback for unbound use.
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

// OnBookOrganized runs the per-book post-organize work synchronously and
// returns one Outcome per logical operation. Operations are idempotent:
// scan triggers tolerate repeats, collection-add is a no-op when the album
// already belongs to the collection.
func (p *PlexBackend) OnBookOrganized(ctx context.Context, book OrganizedBook) []Outcome {
	plexURL, plexToken, sectionID := p.settings(ctx)
	if plexURL == "" || plexToken == "" || sectionID == "" {
		return []Outcome{SkippedConfigured(OpScanTrigger)}
	}

	outcomes := make([]Outcome, 0, 3)

	// 1. Scan trigger
	scanCtx, scanCancel := context.WithTimeout(ctx, 30*time.Second)
	defer scanCancel()
	scanStart := time.Now()
	// Short-circuit on empty LocalPath. Calling resolveScanPath with
	// filepath.Dir("") would yield "." and trigger an unnecessary Plex
	// section-path fetch + cache write for a request that's going to fail
	// anyway. Copilot review caught this. Evaluating LocalPath up-front
	// also keeps the failure outcome's detail self-explanatory.
	if strings.TrimSpace(book.LocalPath) == "" {
		outcomes = append(outcomes, Failed(OpScanTrigger, fmt.Errorf("empty local path"), "no path to scan"))
	} else if scanPath, scanPathOK := p.resolveScanPath(scanCtx, plexURL, plexToken, sectionID, filepath.Dir(book.LocalPath)); !scanPathOK {
		// Path translation can fail when Plex does not expose section Location
		// details (or when only destination server-root is configured). Fall
		// back to a full section refresh so indexing still proceeds.
		if err := p.triggerSectionScan(scanCtx, plexURL, plexToken, sectionID, ""); err != nil {
			outcomes = append(outcomes, Failed(OpScanTrigger, fmt.Errorf("section path unavailable: %w", err), "plex section path could not be resolved; full section scan fallback failed"))
		} else {
			outcomes = append(outcomes, Succeeded(OpScanTrigger, "section scan triggered without path filter (section path unavailable)", "", time.Since(scanStart)))
		}
	} else if err := p.triggerSectionScan(scanCtx, plexURL, plexToken, sectionID, scanPath); err != nil {
		outcomes = append(outcomes, Failed(OpScanTrigger, err, "plex returned non-2xx on /refresh"))
	} else {
		outcomes = append(outcomes, Succeeded(OpScanTrigger, "section scan triggered for "+scanPath, "", time.Since(scanStart)))
	}

	// 2 + 3: only if the book has a series. Otherwise nothing further to do.
	if strings.TrimSpace(book.Series) == "" {
		return outcomes
	}

	// Item match. Plex needs the book indexed before we can add it to a
	// collection; this waits up to ~90s for the album to appear.
	matchCtx, matchCancel := context.WithTimeout(ctx, 120*time.Second)
	defer matchCancel()
	matchStart := time.Now()
	albumKey, err := p.waitForAlbum(matchCtx, plexURL, plexToken, sectionID, book.Title)
	if err != nil {
		outcomes = append(outcomes,
			Failed(OpItemMatch, err, "album not found in plex within retry window"),
			Outcome{Operation: OpSeriesGrouping, Status: OutcomeDeferred,
				Detail: "skipped: depends on item_match", Err: err})
		return outcomes
	}
	outcomes = append(outcomes, Succeeded(OpItemMatch, "matched plex album by title", albumKey, time.Since(matchStart)))

	// Series grouping (Plex collection).
	groupStart := time.Now()
	if err := p.ensureBookInCollectionWithKey(matchCtx, plexURL, plexToken, sectionID, book.Series, albumKey); err != nil {
		outcomes = append(outcomes, Failed(OpSeriesGrouping, err, "could not add album to series collection"))
		return outcomes
	}
	outcomes = append(outcomes, Succeeded(OpSeriesGrouping, "album added to series collection \""+book.Series+"\"", albumKey, time.Since(groupStart)))

	return outcomes
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
	validAlbumRatingKeys := make(map[string]struct{}, len(albums))
	for _, album := range albums {
		ratingKey := strings.TrimSpace(album.RatingKey)
		if ratingKey != "" {
			validAlbumRatingKeys[ratingKey] = struct{}{}
		}
	}

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
				if p.destination != nil {
					if err := upsertBookDestinationItem(ctx, p.db, book.ID, p.destination.ID, album.RatingKey, albumTitle); err != nil {
						msLog.Warn().Err(err).Int64("book_id", book.ID).Str("title", book.Title).Msg("plex: failed to update destination item id")
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

	bookDestinationIDs := map[int64]string{}
	if p.destination != nil {
		destRows, err := p.db.ListBookDestinationsBy(ctx, p.destination.ID, nil)
		if err != nil {
			return fmt.Errorf("list book destinations for Plex reconcile: %w", err)
		}
		bookDestinationIDs = make(map[int64]string, len(destRows))
		for _, bd := range destRows {
			if id := strings.TrimSpace(bd.ServerItemID); id != "" {
				bookDestinationIDs[bd.BookID] = id
			}
		}
	}

	seriesBooks := buildPlexSeriesBooksFromDestinations(books, bookDestinationIDs, validAlbumRatingKeys)

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

		collectionID, err := p.findOrCreateCollection(ctx, plexURL, plexToken, sectionID, series)
		if err != nil {
			msLog.Warn().Err(err).Str("series", series).Msg("plex: failed to find/create collection during reconciliation")
			seriesProcessed++
			continue
		}

		for _, book := range booksInSeries {
			itemURI := fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", machineID, book.RatingKey)
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

type plexSeriesBook struct {
	Title     string
	RatingKey string
}

func buildPlexSeriesBooksFromDestinations(books []database.Book, bookDestinationIDs map[int64]string, validAlbumRatingKeys map[string]struct{}) map[string][]plexSeriesBook {
	seriesBooks := make(map[string][]plexSeriesBook)
	for _, b := range books {
		series := strings.TrimSpace(b.Series)
		if series == "" {
			continue
		}

		ratingKey := strings.TrimSpace(bookDestinationIDs[b.ID])
		if ratingKey == "" {
			continue
		}
		if _, ok := validAlbumRatingKeys[ratingKey]; !ok {
			msLog.Debug().
				Int64("book_id", b.ID).
				Str("book", b.Title).
				Str("series", b.Series).
				Str("stored_destination_id", ratingKey).
				Msg("plex: stored destination id not found in current Plex album list")
			continue
		}

		seriesBooks[series] = append(seriesBooks[series], plexSeriesBook{
			Title:     b.Title,
			RatingKey: ratingKey,
		})
	}
	return seriesBooks
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
	// Per-destination path mapping (codex P2): when the backend is bound
	// to a destination row that carries explicit AudiobookPath /
	// DestinationPath, those win over the global libraryDir +
	// plex_section_path settings. Lets multi-dest installs route to
	// different mounts (household Plex on /audiobooks vs parents' Plex
	// on /mnt/exports/audiobooks) without colliding on a single global.
	if p.destination != nil {
		audiobookPath := strings.TrimSpace(p.destination.AudiobookPath)
		if audiobookPath == "" {
			// Legacy/synthesized destination rows persist only the server root
			// (DestinationPath). Use the configured local library root as source.
			audiobookPath = strings.TrimSpace(p.libraryDir)
		}
		destPath := strings.TrimSpace(p.destination.DestinationPath)
		if destPath != "" {
			scanPath, ok := translateScanPathWithFallback(localScanPath, audiobookPath, destPath)
			if !ok {
				msLog.Warn().Str("local_path", localScanPath).Str("audiobook_path", audiobookPath).Str("destination_path", destPath).Msg("plex: per-destination path translation failed")
			} else {
				return scanPath, true
			}
		}
	}

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

	scanPath, ok := translateScanPathWithFallback(localScanPath, strings.TrimSpace(p.libraryDir), plexPath)
	if !ok {
		msLog.Warn().Str("local_path", localScanPath).Str("library_root", p.libraryDir).Str("plex_section_path", plexPath).Msg("plex: unable to translate scan path")
		return "", false
	}
	return scanPath, true
}

func translateScanPathWithFallback(localScanPath, localLibraryRoot, serverLibraryRoot string) (string, bool) {
	if scanPath, ok := translateScanPath(localScanPath, localLibraryRoot, serverLibraryRoot); ok {
		return scanPath, true
	}
	// Common deployment shape: audplexus writes under /audiobooks while Plex
	// mounts the same content elsewhere. If configured local root is missing or
	// stale, retry with /audiobooks as source root.
	if scanPath, ok := translateScanPath(localScanPath, "/audiobooks", serverLibraryRoot); ok {
		return scanPath, true
	}
	return "", false
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

	// Fallback: some Plex setups omit Location on section detail but include
	// it on /library/sections.
	listPath, listErr := p.fetchSectionPathFromList(ctx, plexURL, token, sectionID)
	if listErr == nil {
		return listPath, nil
	}
	return "", fmt.Errorf("no location path found for section %s (detail endpoint had no Location; list fallback failed: %v)", sectionID, listErr)
}

func (p *PlexBackend) fetchSectionPathFromList(ctx context.Context, plexURL, token, sectionID string) (string, error) {
	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Plex URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/sections"
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
		return "", fmt.Errorf("plex sections list returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var sections struct {
		MediaContainer struct {
			Directories []struct {
				Key       string `json:"key"`
				Locations []struct {
					Path string `json:"path"`
				} `json:"Location"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sections); err != nil {
		return "", fmt.Errorf("parse sections list: %w", err)
	}

	for _, dir := range sections.MediaContainer.Directories {
		if strings.TrimSpace(dir.Key) != strings.TrimSpace(sectionID) {
			continue
		}
		for _, loc := range dir.Locations {
			if path := strings.TrimSpace(loc.Path); path != "" {
				return path, nil
			}
		}
		break
	}

	return "", fmt.Errorf("no location path found for section %s in sections list", sectionID)
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
	collectionID, err := p.findOrCreateCollection(ctx, plexURL, token, sectionID, series)
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

func (p *PlexBackend) findOrCreateCollection(ctx context.Context, plexURL, token, sectionID, seriesName string) (string, error) {
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

	methods := []string{http.MethodPost, http.MethodPut}
	var lastErr error
	for i, method := range methods {
		req, err := http.NewRequestWithContext(ctx, method, base.String(), nil)
		if err != nil {
			return err
		}
		p.addHeaders(req, token)
		msLog.Debug().
			Str("collection_id", collectionID).
			Str("plex_request", formatPlexRequestForLog(req)).
			Msg("plex: add to collection request")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		msLog.Debug().
			Str("collection_id", collectionID).
			Str("plex_response", formatPlexResponseForLog(resp, body)).
			Msg("plex: add to collection response")
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		lastErr = fmt.Errorf("plex add to collection via %s returned %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
		if i == len(methods)-1 || (resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed) {
			return lastErr
		}
	}
	return lastErr
}

func formatPlexRequestForLog(req *http.Request) string {
	if req == nil {
		return ""
	}
	redactedURL := redactPlexURL(req.URL)
	var b strings.Builder
	b.WriteString(req.Method)
	b.WriteString(" ")
	b.WriteString(redactedURL)
	b.WriteString(" ")
	b.WriteString(req.Proto)
	b.WriteString("\n")
	b.WriteString(formatPlexHeadersForLog(req.Header))
	return strings.TrimRight(b.String(), "\n")
}

func formatPlexResponseForLog(resp *http.Response, body []byte) string {
	if resp == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(resp.Proto)
	b.WriteString(" ")
	b.WriteString(resp.Status)
	b.WriteString("\n")
	b.WriteString(formatPlexHeadersForLog(resp.Header))
	if len(body) > 0 {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.Write(body)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatPlexHeadersForLog(header http.Header) string {
	if len(header) == 0 {
		return ""
	}
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, key := range keys {
		values := header.Values(key)
		for _, value := range values {
			b.WriteString(key)
			b.WriteString(": ")
			if strings.EqualFold(key, "X-Plex-Token") {
				b.WriteString("<redacted>")
			} else {
				b.WriteString(value)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func redactPlexURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	cloned := *u
	q := cloned.Query()
	if q.Has("X-Plex-Token") {
		q.Set("X-Plex-Token", "<redacted>")
	}
	cloned.RawQuery = q.Encode()
	return cloned.String()
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
