package mediaserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mstrhakr/audplexus/internal/audnexus"
	"github.com/mstrhakr/audplexus/internal/database"
)

// JellyfinBackend implements Backend against a Jellyfin server. It is a
// near-cousin of EmbyBackend (Jellyfin forked from Emby in 2018) but with
// material differences confirmed by API research:
//
//   - Auth: `Authorization: MediaBrowser Token="...", Client="...", ...`
//     header. Legacy `X-Emby-Token` works on 10.x but is deprecated and
//     dies in Jellyfin 12.0. New code uses the proper header.
//   - Audiobooks: Jellyfin has BaseItemKind.AudioBook as a first-class
//     item type (Emby returns each book as both Audio and MusicAlbum).
//     Item filter is IncludeItemTypes=AudioBook.
//   - Image upload: concrete Content-Type required (image/jpeg, NOT
//     image/jpg, NOT image/*). Jellyfin's ImageController rejects with
//     400 — Emby tolerates loose Content-Type.
//   - LockedFields wire-compatible (`["Tags"]`) but the server side is an
//     enum so unknown strings silently no-op.
//
// Most of the per-book post-organize flow mirrors Emby: scan trigger,
// item match, series collection, franchise tag.
type JellyfinBackend struct {
	db         database.Database
	audnexus   *audnexus.Client
	libraryDir string

	clientID string

	adminMu     sync.Mutex
	adminUserID string

	destination *database.LibraryDestination
}

// NewJellyfin constructs a Jellyfin backend. audnexusClient may be nil to
// disable audnexus-sourced enrichment.
func NewJellyfin(db database.Database, audnexusClient *audnexus.Client, libraryDir string) *JellyfinBackend {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "audplexus"
	}
	return &JellyfinBackend{
		db: db, audnexus: audnexusClient, libraryDir: libraryDir,
		clientID: "audplexus-" + strings.ToLower(strings.TrimSpace(hostname)),
	}
}

// WithDestination binds the backend to a specific library_destinations row.
func (j *JellyfinBackend) WithDestination(d *database.LibraryDestination) *JellyfinBackend {
	j.destination = d
	return j
}

func (j *JellyfinBackend) Name() string { return string(TypeJellyfin) }

// Capabilities — Jellyfin is essentially Emby's feature set: scan trigger,
// per-item refresh, series grouping (BoxSet collections), franchise tag,
// image upload, item count, author images, BoxSet covers.
func (j *JellyfinBackend) Capabilities() CapabilitySet {
	return NewCapabilitySet(
		CapTriggerScan,
		CapPerItemRefresh,
		CapSeriesGrouping,
		CapFranchiseTag,
		CapImageUpload,
		CapItemCount,
		CapAuthorImages,
		CapBoxSetCovers,
	)
}

func (j *JellyfinBackend) Configured(ctx context.Context) bool {
	u, k, l := j.settings(ctx)
	return u != "" && k != "" && l != ""
}

func (j *JellyfinBackend) settings(ctx context.Context) (string, string, string) {
	if j.destination != nil {
		return strings.TrimSpace(j.destination.URL),
			strings.TrimSpace(j.destination.APIKey),
			strings.TrimSpace(j.destination.LibraryID)
	}
	u, _ := j.db.GetSetting(ctx, "jellyfin_url")
	k, _ := j.db.GetSetting(ctx, "jellyfin_api_key")
	l, _ := j.db.GetSetting(ctx, "jellyfin_library_id")
	if strings.TrimSpace(u) == "" {
		u = strings.TrimSpace(os.Getenv("JELLYFIN_URL"))
	}
	if strings.TrimSpace(k) == "" {
		k = strings.TrimSpace(os.Getenv("JELLYFIN_API_KEY"))
	}
	if strings.TrimSpace(l) == "" {
		l = strings.TrimSpace(os.Getenv("JELLYFIN_LIBRARY_ID"))
	}
	return strings.TrimSpace(u), strings.TrimSpace(k), strings.TrimSpace(l)
}

// OnBookOrganized runs the per-book post-organize work: scan trigger,
// item match (filter=AudioBook), series grouping (BoxSet collection),
// franchise tag.
func (j *JellyfinBackend) OnBookOrganized(ctx context.Context, book OrganizedBook) []Outcome {
	baseURL, apiKey, libraryID := j.settings(ctx)
	if baseURL == "" || apiKey == "" || libraryID == "" {
		return []Outcome{SkippedConfigured(OpScanTrigger)}
	}

	outcomes := make([]Outcome, 0, 4)

	// 1. Scan trigger.
	scanCtx, scanCancel := context.WithTimeout(ctx, 30*time.Second)
	defer scanCancel()
	scanStart := time.Now()
	if strings.TrimSpace(book.LocalPath) == "" {
		outcomes = append(outcomes, Failed(OpScanTrigger, fmt.Errorf("empty local path"), "no path to scan"))
	} else if err := j.refreshLibrary(scanCtx, baseURL, apiKey); err != nil {
		outcomes = append(outcomes, Failed(OpScanTrigger, err, "library refresh failed"))
	} else {
		outcomes = append(outcomes, Succeeded(OpScanTrigger, "library refresh triggered", "", time.Since(scanStart)))
	}

	if strings.TrimSpace(book.Series) == "" {
		return outcomes
	}

	// 2. Item match — wait for Jellyfin to index the AudioBook.
	matchCtx, matchCancel := context.WithTimeout(ctx, 180*time.Second)
	defer matchCancel()
	matchStart := time.Now()
	itemID, err := j.waitForAudioBook(matchCtx, baseURL, apiKey, libraryID, book.Title)
	if err != nil {
		outcomes = append(outcomes,
			Failed(OpItemMatch, err, "audiobook not found in jellyfin within retry window"),
			Outcome{Operation: OpSeriesGrouping, Status: OutcomeDeferred, Detail: "skipped: depends on item_match", Err: err},
			Outcome{Operation: OpFranchiseTag, Status: OutcomeDeferred, Detail: "skipped: depends on item_match", Err: err})
		return outcomes
	}
	outcomes = append(outcomes, Succeeded(OpItemMatch, "matched jellyfin AudioBook by title", itemID, time.Since(matchStart)))

	// 3. Series grouping (BoxSet — same endpoints as Emby).
	groupStart := time.Now()
	collectionID, err := j.findOrCreateCollection(matchCtx, baseURL, apiKey, book.Series, itemID)
	if err != nil {
		outcomes = append(outcomes,
			Failed(OpSeriesGrouping, err, "find/create boxset failed"),
			Outcome{Operation: OpFranchiseTag, Status: OutcomeDeferred, Detail: "skipped: depends on series_grouping", Err: err})
		return outcomes
	}
	if err := j.addToCollection(matchCtx, baseURL, apiKey, collectionID, itemID); err != nil {
		outcomes = append(outcomes, Failed(OpSeriesGrouping, err, "add to boxset failed"))
	} else {
		outcomes = append(outcomes, Succeeded(OpSeriesGrouping, "book added to boxset \""+book.Series+"\"", collectionID, time.Since(groupStart)))
	}

	// 4. Franchise tag.
	tagStart := time.Now()
	tags := []string{book.Series}
	if f := franchiseFromSeries(book.Series); f != "" {
		tags = append(tags, f)
	}
	adminID, adminErr := j.resolveAdminUserID(matchCtx, baseURL, apiKey)
	switch {
	case adminErr != nil:
		outcomes = append(outcomes, Failed(OpFranchiseTag, adminErr, "no admin user resolved"))
	case adminID == "":
		outcomes = append(outcomes, Failed(OpFranchiseTag, fmt.Errorf("empty admin id"), "admin user resolved but empty"))
	default:
		if err := j.applyTags(matchCtx, baseURL, apiKey, adminID, itemID, tags); err != nil {
			outcomes = append(outcomes, Failed(OpFranchiseTag, err, "tag write failed"))
		} else {
			outcomes = append(outcomes, Succeeded(OpFranchiseTag, "tagged with series + franchise", itemID, time.Since(tagStart)))
		}
	}

	return outcomes
}

// ReconcileLibrary is not yet implemented for Jellyfin — returns a typed
// not-implemented error so callers can decide whether to retry. The
// per-book OnBookOrganized fan-out + book_library_destinations join table
// covers the normal case; a full reconcile pass requires a Jellyfin-shaped
// item walk that lives in a follow-up.
func (j *JellyfinBackend) ReconcileLibrary(ctx context.Context, progressFn func(current, total int)) error {
	return fmt.Errorf("jellyfin reconcile not yet implemented; per-book sync via OnBookOrganized works")
}

// LibraryItemCount queries Jellyfin for the AudioBook count.
func (j *JellyfinBackend) LibraryItemCount(ctx context.Context) (int, error) {
	baseURL, apiKey, libraryID := j.settings(ctx)
	if baseURL == "" || apiKey == "" || libraryID == "" {
		return 0, nil
	}
	u, err := j.buildURL(baseURL, "/Items", map[string]string{
		"ParentId":         libraryID,
		"Recursive":        "true",
		"IncludeItemTypes": "AudioBook",
		"Limit":            "0",
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	j.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("jellyfin Items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		TotalRecordCount int `json:"TotalRecordCount"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, err
	}
	return r.TotalRecordCount, nil
}

// TriggerLibraryScan triggers a server-wide refresh and returns post-scan
// item count. Per Jellyfin API, /Library/Refresh has no per-library
// variant (PR can do per-folder refresh via /Items/{id}/Refresh, but the
// scheduled-sync flow wants the global trigger).
func (j *JellyfinBackend) TriggerLibraryScan(ctx context.Context) (int, error) {
	baseURL, apiKey, _ := j.settings(ctx)
	if baseURL == "" || apiKey == "" {
		return 0, fmt.Errorf("jellyfin not configured")
	}
	if err := j.refreshLibrary(ctx, baseURL, apiKey); err != nil {
		return 0, err
	}
	return j.LibraryItemCount(ctx)
}

// --- HTTP helpers ---

func (j *JellyfinBackend) buildURL(baseURL, pathSuffix string, query map[string]string) (string, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid jellyfin URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + pathSuffix
	q := base.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	base.RawQuery = q.Encode()
	return base.String(), nil
}

// addAuthHeader sets the Jellyfin-required Authorization header. Per
// research (PR #13306), Jellyfin 12.0 will drop the legacy X-Emby-Token
// fallback — using the canonical MediaBrowser scheme from day one.
func (j *JellyfinBackend) addAuthHeader(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", fmt.Sprintf(
		`MediaBrowser Token="%s", Client="Audplexus", Device="%s", DeviceId="%s", Version="1.0"`,
		apiKey, "audplexus", j.clientID,
	))
	req.Header.Set("Accept", "application/json")
}

func (j *JellyfinBackend) refreshLibrary(ctx context.Context, baseURL, apiKey string) error {
	u, err := j.buildURL(baseURL, "/Library/Refresh", nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	j.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("jellyfin /Library/Refresh returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (j *JellyfinBackend) waitForAudioBook(ctx context.Context, baseURL, apiKey, libraryID, title string) (string, error) {
	// Initial wait so Jellyfin's library scan has a head start.
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Second):
	}

	intervals := []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second, 30 * time.Second}
	var lastErr error
	for _, wait := range intervals {
		id, err := j.findAudioBookByTitle(ctx, baseURL, apiKey, libraryID, title)
		if err == nil {
			return id, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
	id, err := j.findAudioBookByTitle(ctx, baseURL, apiKey, libraryID, title)
	if err == nil {
		return id, nil
	}
	return "", fmt.Errorf("audiobook %q not found in jellyfin after retries: %w", title, lastErr)
}

func (j *JellyfinBackend) findAudioBookByTitle(ctx context.Context, baseURL, apiKey, libraryID, title string) (string, error) {
	u, err := j.buildURL(baseURL, "/Items", map[string]string{
		"ParentId":         libraryID,
		"Recursive":        "true",
		"IncludeItemTypes": "AudioBook", // Jellyfin's first-class audiobook kind
		"SearchTerm":       title,
		"Limit":            "50",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	j.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("jellyfin Items search returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		Items []struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	wantNorm := normalizeTitle(title)
	for _, it := range r.Items {
		if normalizeTitle(it.Name) == wantNorm {
			return it.Id, nil
		}
	}
	for _, it := range r.Items {
		if strings.Contains(normalizeTitle(it.Name), wantNorm) {
			return it.Id, nil
		}
	}
	return "", fmt.Errorf("audiobook %q not found in jellyfin (library %s)", title, libraryID)
}

func (j *JellyfinBackend) findOrCreateCollection(ctx context.Context, baseURL, apiKey, name, seedItemID string) (string, error) {
	if id, err := j.findCollectionByName(ctx, baseURL, apiKey, name); err == nil && id != "" {
		return id, nil
	}
	u, err := j.buildURL(baseURL, "/Collections", map[string]string{
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
	j.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("jellyfin /Collections returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		Id string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.Id == "" {
		return "", fmt.Errorf("collection created but no Id returned")
	}
	return r.Id, nil
}

func (j *JellyfinBackend) findCollectionByName(ctx context.Context, baseURL, apiKey, name string) (string, error) {
	u, err := j.buildURL(baseURL, "/Items", map[string]string{
		"Recursive":        "true",
		"IncludeItemTypes": "BoxSet",
		"SearchTerm":       name,
		"Limit":            "50",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	j.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("jellyfin BoxSet search returned %d", resp.StatusCode)
	}
	var r struct {
		Items []struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	wantNorm := normalizeTitle(name)
	for _, it := range r.Items {
		if normalizeTitle(it.Name) == wantNorm {
			return it.Id, nil
		}
	}
	return "", fmt.Errorf("boxset %q not found", name)
}

func (j *JellyfinBackend) addToCollection(ctx context.Context, baseURL, apiKey, collectionID, itemID string) error {
	u, err := j.buildURL(baseURL, "/Collections/"+url.PathEscape(collectionID)+"/Items", map[string]string{
		"Ids": itemID,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	j.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("jellyfin add to collection returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (j *JellyfinBackend) resolveAdminUserID(ctx context.Context, baseURL, apiKey string) (string, error) {
	j.adminMu.Lock()
	cached := j.adminUserID
	j.adminMu.Unlock()
	if cached != "" {
		return cached, nil
	}

	u, err := j.buildURL(baseURL, "/Users", nil)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	j.addAuthHeader(req, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("jellyfin /Users returned %d", resp.StatusCode)
	}
	var users []struct {
		Id     string `json:"Id"`
		Policy struct {
			IsAdministrator bool `json:"IsAdministrator"`
		} `json:"Policy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Policy.IsAdministrator && u.Id != "" {
			j.adminMu.Lock()
			j.adminUserID = u.Id
			j.adminMu.Unlock()
			return u.Id, nil
		}
	}
	return "", fmt.Errorf("no administrator user found")
}

func (j *JellyfinBackend) applyTags(ctx context.Context, baseURL, apiKey, adminID, itemID string, tags []string) error {
	// Fetch current item DTO via the admin user.
	getURL, err := j.buildURL(baseURL, "/Users/"+url.PathEscape(adminID)+"/Items/"+url.PathEscape(itemID), nil)
	if err != nil {
		return err
	}
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return err
	}
	j.addAuthHeader(getReq, apiKey)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return err
	}
	defer getResp.Body.Close()
	if getResp.StatusCode < 200 || getResp.StatusCode >= 300 {
		return fmt.Errorf("jellyfin GET item returned %d", getResp.StatusCode)
	}
	var dto map[string]any
	if err := json.NewDecoder(getResp.Body).Decode(&dto); err != nil {
		return fmt.Errorf("decode item dto: %w", err)
	}

	dto["Tags"] = tags
	dto["TagItems"] = tagItemsFromTags(tags)
	dto["LockedFields"] = ensureLockedFieldTags(dto["LockedFields"])

	// POST modified DTO. Jellyfin accepts the standard /Items/{id} update.
	postURL, err := j.buildURL(baseURL, "/Items/"+url.PathEscape(itemID), nil)
	if err != nil {
		return err
	}
	body, err := json.Marshal(dto)
	if err != nil {
		return fmt.Errorf("encode item dto: %w", err)
	}
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	j.addAuthHeader(postReq, apiKey)
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		return err
	}
	defer postResp.Body.Close()
	if postResp.StatusCode < 200 || postResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(postResp.Body, 1024))
		return fmt.Errorf("jellyfin tag update returned %d: %s", postResp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// Shared helpers used by both Emby and Jellyfin tag-update flows.
func tagItemsFromTags(tags []string) []map[string]string {
	out := make([]map[string]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, map[string]string{"Name": t})
	}
	return out
}

func ensureLockedFieldTags(existing any) []string {
	out := []string{"Tags"}
	if existing == nil {
		return out
	}
	if arr, ok := existing.([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok && s != "Tags" {
				out = append(out, s)
			}
		}
	}
	return out
}
