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
	"strings"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
)

// ABSBackend implements Backend against an Audiobookshelf server. Smaller
// surface than Plex/Emby/Jellyfin: ABS has a built-in chokidar folder
// watcher (on by default) that picks up new files without an explicit
// scan call, and ABS's series support is metadata-driven rather than
// collection-driven. The Audiobook-rich tag profile (PR-A) writes
// `series` and `series-part` ID3-style atoms that ABS reads via ffprobe,
// which makes EnsureBookInSeriesCollection mostly a no-op for ABS.
//
// Auth: Authorization: Bearer <api_key>. Admin scope required for scan
// endpoints (ABS v2.26.0+ JWT rewrite, 2025-07).
type ABSBackend struct {
	db         database.Database
	libraryDir string

	destination *database.LibraryDestination
}

// NewABS constructs an ABS backend. audnexus client is not used by ABS
// (ABS does its own Audnexus enrichment internally via /api/items/{id}/match).
func NewABS(db database.Database, libraryDir string) *ABSBackend {
	return &ABSBackend{db: db, libraryDir: libraryDir}
}

// WithDestination binds the backend to a specific library_destinations row.
func (a *ABSBackend) WithDestination(d *database.LibraryDestination) *ABSBackend {
	a.destination = d
	return a
}

func (a *ABSBackend) Name() string { return string(TypeABS) }

// Capabilities — ABS has scan trigger, per-item refresh, item count, and
// implicit series grouping (via tag-driven metadata, not API calls). It
// does NOT have a franchise concept, BoxSet collections, or per-author
// images via this backend (ABS handles author images itself).
func (a *ABSBackend) Capabilities() CapabilitySet {
	return NewCapabilitySet(
		CapTriggerScan,
		CapPerItemRefresh,
		CapSeriesGrouping, // implicit via metadata.series, not collections
		CapItemCount,
	)
}

func (a *ABSBackend) Configured(ctx context.Context) bool {
	u, k, l := a.settings(ctx)
	return u != "" && k != "" && l != ""
}

func (a *ABSBackend) settings(ctx context.Context) (string, string, string) {
	if a.destination != nil {
		return strings.TrimSpace(a.destination.URL),
			strings.TrimSpace(a.destination.APIKey),
			strings.TrimSpace(a.destination.LibraryID)
	}
	u, _ := a.db.GetSetting(ctx, "abs_url")
	k, _ := a.db.GetSetting(ctx, "abs_api_key")
	l, _ := a.db.GetSetting(ctx, "abs_library_id")
	if strings.TrimSpace(u) == "" {
		u = strings.TrimSpace(os.Getenv("ABS_URL"))
	}
	if strings.TrimSpace(k) == "" {
		k = strings.TrimSpace(os.Getenv("ABS_API_KEY"))
	}
	if strings.TrimSpace(l) == "" {
		l = strings.TrimSpace(os.Getenv("ABS_LIBRARY_ID"))
	}
	return strings.TrimSpace(u), strings.TrimSpace(k), strings.TrimSpace(l)
}

// OnBookOrganized for ABS:
//
//   - scan_trigger: POST /api/libraries/{id}/scan. Folder watcher is on by
//     default in ABS, so this is belt-and-suspenders — but the scan call
//     is idempotent so calling both is safe.
//   - item_match: search by ASIN to confirm the book got picked up.
//   - series_grouping: emit StatusSkippedExisting when the book already
//     has its series populated server-side (PR-A's Audiobook-rich profile
//     writes series tags that ABS auto-detects). When series tags are
//     missing, PATCH /api/items/{id}/media to set series explicitly.
func (a *ABSBackend) OnBookOrganized(ctx context.Context, book OrganizedBook) []Outcome {
	baseURL, apiKey, libraryID := a.settings(ctx)
	if baseURL == "" || apiKey == "" || libraryID == "" {
		return []Outcome{SkippedConfigured(OpScanTrigger)}
	}

	outcomes := make([]Outcome, 0, 3)

	// 1. Scan trigger. Force=1 so ABS rescans even if it thinks the folder
	// is already known.
	scanCtx, scanCancel := context.WithTimeout(ctx, 30*time.Second)
	defer scanCancel()
	scanStart := time.Now()
	if err := a.triggerLibraryScan(scanCtx, baseURL, apiKey, libraryID); err != nil {
		outcomes = append(outcomes, Failed(OpScanTrigger, err, "library scan trigger failed"))
	} else {
		outcomes = append(outcomes, Succeeded(OpScanTrigger, "library scan triggered", "", time.Since(scanStart)))
	}

	// 2. Item match by ASIN — wait for the watcher (or scan) to pick up
	// the file. The wait is shorter than Plex/Emby because ABS folder
	// watcher tends to be near-instant.
	if strings.TrimSpace(book.ASIN) == "" {
		// Without ASIN we can't reliably match. Skip silently — book is on
		// disk, ABS will pick it up.
		outcomes = append(outcomes, Outcome{
			Operation: OpItemMatch, Status: OutcomeDeferred,
			Detail: "no ASIN to match by; ABS folder watcher will index by path",
		})
		return outcomes
	}
	matchCtx, matchCancel := context.WithTimeout(ctx, 90*time.Second)
	defer matchCancel()
	matchStart := time.Now()
	itemID, err := a.waitForItemByASIN(matchCtx, baseURL, apiKey, libraryID, book.ASIN)
	if err != nil {
		outcomes = append(outcomes,
			Outcome{Operation: OpItemMatch, Status: OutcomeDeferred,
				Detail: "ABS hasn't indexed yet; will surface in next reconcile",
				Err:    err},
			Outcome{Operation: OpSeriesGrouping, Status: OutcomeDeferred,
				Detail: "skipped: depends on item_match", Err: err},
		)
		return outcomes
	}
	outcomes = append(outcomes, Succeeded(OpItemMatch, "matched ABS item by ASIN", itemID, time.Since(matchStart)))

	// 3. Series grouping — only if the book has a series.
	if strings.TrimSpace(book.Series) == "" {
		return outcomes
	}
	groupStart := time.Now()
	if a.itemHasSeries(matchCtx, baseURL, apiKey, itemID, book.Series) {
		outcomes = append(outcomes, SkippedExisting(OpSeriesGrouping, "ABS already has series tag (autodetected from m4b)"))
	} else if err := a.patchItemSeries(matchCtx, baseURL, apiKey, itemID, book.Series, book.SeriesPosition); err != nil {
		outcomes = append(outcomes, Failed(OpSeriesGrouping, err, "PATCH /api/items/{id}/media failed"))
	} else {
		outcomes = append(outcomes, Succeeded(OpSeriesGrouping, "series patched onto ABS item", itemID, time.Since(groupStart)))
	}

	return outcomes
}

// ReconcileLibrary walks the ABS library, matches each item back to a
// local book by ASIN, and records the server item ID in the bound
// destination row so future operations can address items directly.
// Pages through /api/libraries/{id}/items in batches.
//
// ABS auto-detects series from embedded metadata when the Audiobook-rich
// tag profile is enabled, so this reconcile pass intentionally does NOT
// patch series back onto items — that lives in OnBookOrganized for
// freshly-organized books and as an opt-in repair for older books.
func (a *ABSBackend) ReconcileLibrary(ctx context.Context, progressFn func(current, total int)) error {
	baseURL, apiKey, libraryID := a.settings(ctx)
	if baseURL == "" || apiKey == "" || libraryID == "" {
		return fmt.Errorf("abs not configured")
	}

	items, err := a.listAllItems(ctx, baseURL, apiKey, libraryID)
	if err != nil {
		return fmt.Errorf("list abs items: %w", err)
	}
	msLog.Info().Int("abs_items", len(items)).Msg("abs: fetched library item list for reconcile")

	completeStatus := database.BookStatusComplete
	books, _, err := a.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 100000})
	if err != nil {
		return fmt.Errorf("list complete books: %w", err)
	}
	booksByASIN := make(map[string]database.Book, len(books))
	for _, b := range books {
		if b.ASIN != "" {
			booksByASIN[strings.ToUpper(strings.TrimSpace(b.ASIN))] = b
		}
	}

	if progressFn != nil {
		progressFn(0, len(items))
	}
	matched := 0
	for i, it := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		asin := strings.ToUpper(strings.TrimSpace(it.ASIN))
		if asin == "" {
			continue
		}
		book, ok := booksByASIN[asin]
		if !ok {
			continue
		}
		if a.destination != nil {
			if err := upsertBookDestinationItem(ctx, a.db, book.ID, a.destination.ID, it.ID, it.Title); err != nil {
				msLog.Warn().Err(err).Int64("book_id", book.ID).Str("asin", asin).Msg("abs: failed to update destination item id")
			} else {
				matched++
			}
		}
		if progressFn != nil && (i%25 == 0 || i == len(items)-1) {
			progressFn(i+1, len(items))
		}
	}
	msLog.Info().Int("matched", matched).Int("abs_items", len(items)).Int("local_books", len(books)).Msg("abs: reconcile complete")
	return nil
}

// absLibraryItem is a thin DTO of the fields ReconcileLibrary needs from
// /api/libraries/{id}/items — keeps the parser tolerant to ABS's larger
// response shape without forcing us to model every field.
type absLibraryItem struct {
	ID    string
	Title string
	ASIN  string
}

func (a *ABSBackend) listAllItems(ctx context.Context, baseURL, apiKey, libraryID string) ([]absLibraryItem, error) {
	const pageSize = 200
	var all []absLibraryItem
	page := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		u, err := a.buildURL(baseURL, "/api/libraries/"+url.PathEscape(libraryID)+"/items", map[string]string{
			"limit":    fmt.Sprintf("%d", pageSize),
			"page":     fmt.Sprintf("%d", page),
			"minified": "1",
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		a.addAuthHeader(req, apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return nil, fmt.Errorf("abs /items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var r struct {
			Results []struct {
				ID    string `json:"id"`
				Media struct {
					Metadata struct {
						Title string `json:"title"`
						ASIN  string `json:"asin"`
					} `json:"metadata"`
				} `json:"media"`
			} `json:"results"`
			Total int `json:"total"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("parse abs items page %d: %w", page, err)
		}
		resp.Body.Close()

		for _, hit := range r.Results {
			all = append(all, absLibraryItem{
				ID:    hit.ID,
				Title: hit.Media.Metadata.Title,
				ASIN:  hit.Media.Metadata.ASIN,
			})
		}
		if len(r.Results) < pageSize {
			break
		}
		page++
	}
	return all, nil
}

// LibraryItemCount queries the ABS library stats endpoint.
func (a *ABSBackend) LibraryItemCount(ctx context.Context) (int, error) {
	baseURL, apiKey, libraryID := a.settings(ctx)
	if baseURL == "" || apiKey == "" || libraryID == "" {
		return 0, nil
	}
	u, err := a.buildURL(baseURL, "/api/libraries/"+url.PathEscape(libraryID)+"/stats", nil)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	a.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("abs /stats returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// ABS stats response shape varies by version. Common fields:
	//   totalItems, numItems, totalAuthors, items, etc.
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return 0, err
	}
	for _, key := range []string{"totalItems", "numItems", "items", "totalLibraryItems"} {
		if v, ok := raw[key]; ok {
			if n, ok := toInt(v); ok {
				return n, nil
			}
		}
	}
	return 0, nil
}

// TriggerLibraryScan triggers an ABS library scan and returns post-scan
// item count. Note: the scan is async on the server side, so the count
// reflects the state at the time of the next call rather than the
// post-scan steady state.
func (a *ABSBackend) TriggerLibraryScan(ctx context.Context) (int, error) {
	baseURL, apiKey, libraryID := a.settings(ctx)
	if baseURL == "" || apiKey == "" || libraryID == "" {
		return 0, fmt.Errorf("abs not configured")
	}
	if err := a.triggerLibraryScan(ctx, baseURL, apiKey, libraryID); err != nil {
		return 0, err
	}
	return a.LibraryItemCount(ctx)
}

// ABSLibrary is a minimal projection of a library row from GET /api/libraries,
// exported so the destinations UI can populate a picker without going through
// a backend instance (no destination row exists yet at form-render time).
type ABSLibrary struct {
	ID        string
	Name      string
	MediaType string // "book", "podcast", ...
	Path      string // first folder.fullPath, when present
}

// ListLibraries fetches all libraries the given (baseURL, apiKey) credentials
// can see. Used by the destination form's "Discover libraries" affordance to
// turn library-ID-paste UX into a dropdown picker. Errors surface inline in
// the UI; the function itself does not log.
func ListLibraries(ctx context.Context, baseURL, apiKey string) ([]ABSLibrary, error) {
	// Reuse a backend's URL/header helpers — the empty receiver is fine since
	// those helpers don't touch any state.
	a := &ABSBackend{}
	u, err := a.buildURL(baseURL, "/api/libraries", nil)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("abs /api/libraries returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimLeft(bodyBytes, " \t\r\n")
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("abs /api/libraries returned an empty body")
	}

	type rawLib struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		MediaType string `json:"mediaType"`
		Folders   []struct {
			FullPath string `json:"fullPath"`
		} `json:"folders"`
	}

	// ABS returns either {libraries:[...]} (older) or a bare array, depending
	// on version. Sniff the first non-whitespace byte rather than try-then-
	// fall-back so an empty paged response doesn't get misclassified.
	var raw []rawLib
	if trimmed[0] == '[' {
		if err := json.Unmarshal(bodyBytes, &raw); err != nil {
			return nil, fmt.Errorf("parse abs libraries: %w", err)
		}
	} else {
		var wrapped struct {
			Libraries []rawLib `json:"libraries"`
		}
		if err := json.Unmarshal(bodyBytes, &wrapped); err != nil {
			return nil, fmt.Errorf("parse abs libraries: %w", err)
		}
		raw = wrapped.Libraries
	}

	out := make([]ABSLibrary, 0, len(raw))
	for _, r := range raw {
		path := ""
		for _, f := range r.Folders {
			if p := strings.TrimSpace(f.FullPath); p != "" {
				path = p
				break
			}
		}
		out = append(out, ABSLibrary{ID: r.ID, Name: r.Name, MediaType: r.MediaType, Path: path})
	}
	return out, nil
}

// --- HTTP helpers ---

func (a *ABSBackend) buildURL(baseURL, pathSuffix string, query map[string]string) (string, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid abs URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + pathSuffix
	q := base.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	base.RawQuery = q.Encode()
	return base.String(), nil
}

func (a *ABSBackend) addAuthHeader(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
}

func (a *ABSBackend) triggerLibraryScan(ctx context.Context, baseURL, apiKey, libraryID string) error {
	u, err := a.buildURL(baseURL, "/api/libraries/"+url.PathEscape(libraryID)+"/scan", nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	a.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("abs scan returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (a *ABSBackend) waitForItemByASIN(ctx context.Context, baseURL, apiKey, libraryID, asin string) (string, error) {
	intervals := []time.Duration{2 * time.Second, 3 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}
	var lastErr error
	for _, wait := range intervals {
		id, err := a.findItemByASIN(ctx, baseURL, apiKey, libraryID, asin)
		if err == nil && id != "" {
			return id, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
	id, err := a.findItemByASIN(ctx, baseURL, apiKey, libraryID, asin)
	if err == nil && id != "" {
		return id, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("item with ASIN %q not found in ABS library", asin)
	}
	return "", lastErr
}

func (a *ABSBackend) findItemByASIN(ctx context.Context, baseURL, apiKey, libraryID, asin string) (string, error) {
	// ABS doesn't have a native ASIN filter on /items — use /search?q=ASIN
	// then verify metadata.asin client-side.
	u, err := a.buildURL(baseURL, "/api/libraries/"+url.PathEscape(libraryID)+"/search", map[string]string{
		"q":     asin,
		"limit": "20",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	a.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("abs search returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		Book []struct {
			LibraryItem struct {
				ID    string `json:"id"`
				Media struct {
					Metadata struct {
						ASIN string `json:"asin"`
					} `json:"metadata"`
				} `json:"media"`
			} `json:"libraryItem"`
		} `json:"book"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	wantASIN := strings.ToUpper(strings.TrimSpace(asin))
	for _, hit := range r.Book {
		if strings.ToUpper(strings.TrimSpace(hit.LibraryItem.Media.Metadata.ASIN)) == wantASIN {
			return hit.LibraryItem.ID, nil
		}
	}
	return "", fmt.Errorf("item with ASIN %q not found", asin)
}

func (a *ABSBackend) itemHasSeries(ctx context.Context, baseURL, apiKey, itemID, expected string) bool {
	u, err := a.buildURL(baseURL, "/api/items/"+url.PathEscape(itemID), map[string]string{
		"expanded": "1",
	})
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	a.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var r struct {
		Media struct {
			Metadata struct {
				Series []struct {
					Name string `json:"name"`
				} `json:"series"`
			} `json:"metadata"`
		} `json:"media"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return false
	}
	want := normalizeTitle(expected)
	for _, s := range r.Media.Metadata.Series {
		if normalizeTitle(s.Name) == want {
			return true
		}
	}
	return false
}

func (a *ABSBackend) patchItemSeries(ctx context.Context, baseURL, apiKey, itemID, series, sequence string) error {
	u, err := a.buildURL(baseURL, "/api/items/"+url.PathEscape(itemID)+"/media", nil)
	if err != nil {
		return err
	}
	body := map[string]any{
		"metadata": map[string]any{
			"series": []map[string]any{
				{"name": series, "sequence": sequence},
			},
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, u, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return err
	}
	a.addAuthHeader(req, apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("abs PATCH /media returned %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return nil
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
