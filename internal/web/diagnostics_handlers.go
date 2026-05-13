package web

import (
	"context"
	"encoding/json"
	"html"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/library"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
)

type diagnosticsDestinationCard struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Matched      int    `json:"matched"`
	Missing      int    `json:"missing"`
	Unknown      int    `json:"unknown"`
	FetchHealthy bool   `json:"fetch_healthy"`
	FetchError   string `json:"fetch_error,omitempty"`
}

type diagnosticsBookDestinationStatus struct {
	DestinationID   string `json:"destination_id"`
	DestinationName string `json:"destination_name"`
	DestinationType string `json:"destination_type"`
	Status          string `json:"status"`
	MatchMethod     string `json:"match_method,omitempty"`
	Reason          string `json:"reason,omitempty"`
	ServerItemID    string `json:"server_item_id,omitempty"`
	ServerTitle     string `json:"server_title,omitempty"`
}

type diagnosticsIssueItem struct {
	ASIN                string                            `json:"asin"`
	Title               string                            `json:"title"`
	Author              string                            `json:"author"`
	FilePath            string                            `json:"file_path,omitempty"`
	OnDisk              bool                              `json:"on_disk"`
	IssueSummary        string                            `json:"issue_summary"`
	DestinationStatuses []diagnosticsBookDestinationStatus `json:"destination_statuses"`
	CanTargetedScan     bool                              `json:"can_targeted_scan"`
	CanRedownload       bool                              `json:"can_redownload"`
}

type diagnosticsCompareResponse struct {
	GeneratedAt       time.Time                  `json:"generated_at"`
	TotalBooks        int                        `json:"total_books"`
	CompleteBooks     int                        `json:"complete_books"`
	IssueBooks        int                        `json:"issue_books"`
	DiskMissing       int                        `json:"disk_missing"`
	Destinations      []diagnosticsDestinationCard `json:"destinations"`
	Items             []diagnosticsIssueItem     `json:"items"`
	UserMarketplace   string                     `json:"user_marketplace"`
}

type diagnosticsRemoteItem struct {
	ID       string
	Title    string
	Path     string
	Filename string
	ASIN     string
}

type diagnosticsDestinationInventory struct {
	Destination database.LibraryDestination
	Items       []diagnosticsRemoteItem
	ItemsByID   map[string]diagnosticsRemoteItem
	StoredIDs   map[int64]string
	FetchErr    error
}

func (s *Server) handleDiagnostics(c *gin.Context) {
	marketplace := "us"
	if creds := s.audible.GetCredentials(); creds != nil && creds.Marketplace != "" {
		marketplace = creds.Marketplace
	}
	c.HTML(http.StatusOK, "diagnostics.html", gin.H{
		"Page":            "diagnostics",
		"UserMarketplace": marketplace,
	})
}

func (s *Server) handleDiagnosticsCompare(c *gin.Context) {
	ctx := c.Request.Context()
	marketplace := "us"
	if creds := s.audible.GetCredentials(); creds != nil && creds.Marketplace != "" {
		marketplace = creds.Marketplace
	}

	books, totalBooks, err := s.db.ListBooks(ctx, database.BookFilter{Limit: 10000, Offset: 0})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load books: " + err.Error()})
		return
	}
	localASINPaths := scanLocalASINAudioPaths(s.audiobooksPath)
	completeStatus := database.BookStatusComplete
	_, completeBooks, _ := s.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 1})
	dests, _ := s.db.ListEnabledLibraryDestinations(ctx)
	webLog.Info().Int("enabled_destinations", len(dests)).Int("complete_books", completeBooks).Msg("diagnostics: compare start")

	inventories := make([]diagnosticsDestinationInventory, 0, len(dests))
	for _, d := range dests {
		items, fetchErr := s.fetchDiagnosticsInventory(ctx, d)
		itemsByID := make(map[string]diagnosticsRemoteItem, len(items))
		for _, item := range items {
			if id := strings.TrimSpace(item.ID); id != "" {
				itemsByID[id] = item
			}
		}
		storedIDs := map[int64]string{}
		rows, rowsErr := s.db.ListBookDestinationsBy(ctx, d.ID, nil)
		if rowsErr != nil {
			if fetchErr == nil {
				fetchErr = fmt.Errorf("list destination rows: %w", rowsErr)
			}
			webLog.Warn().Err(rowsErr).Str("destination_id", d.ID).Str("destination_type", string(d.Type)).Msg("diagnostics: failed to load stored destination ids")
		} else {
			for _, row := range rows {
				if id := strings.TrimSpace(row.ServerItemID); id != "" {
					storedIDs[row.BookID] = id
				}
			}
		}
		webLog.Debug().Str("destination_id", d.ID).Str("destination_type", string(d.Type)).Int("remote_items", len(items)).Int("stored_ids", len(storedIDs)).Bool("fetch_ok", fetchErr == nil).Msg("diagnostics: destination inventory loaded")
		inventories = append(inventories, diagnosticsDestinationInventory{Destination: d, Items: items, ItemsByID: itemsByID, StoredIDs: storedIDs, FetchErr: fetchErr})
	}

	destCards := make([]diagnosticsDestinationCard, 0, len(inventories))
	destCardIndex := map[string]int{}
	for i, inv := range inventories {
		destCardIndex[inv.Destination.ID] = i
		card := diagnosticsDestinationCard{
			ID:           inv.Destination.ID,
			Name:         destinationDisplayName(inv.Destination),
			Type:         string(inv.Destination.Type),
			FetchHealthy: inv.FetchErr == nil,
		}
		if inv.FetchErr != nil {
			card.FetchError = inv.FetchErr.Error()
		}
		destCards = append(destCards, card)
	}

	issues := make([]diagnosticsIssueItem, 0)
	diskMissing := 0
	for _, book := range books {
		if book.Status != database.BookStatusComplete {
			continue
		}
		onDisk := false
		if strings.TrimSpace(book.FilePath) != "" {
			if _, err := os.Stat(book.FilePath); err == nil {
				onDisk = true
			}
		}
		if !onDisk {
			diskMissing++
		}

		item := diagnosticsIssueItem{
			ASIN:                book.ASIN,
			Title:               book.Title,
			Author:              book.Author,
			FilePath:            book.FilePath,
			OnDisk:              onDisk,
			DestinationStatuses: make([]diagnosticsBookDestinationStatus, 0, len(inventories)),
			CanTargetedScan:     onDisk && strings.TrimSpace(book.FilePath) != "",
			CanRedownload:       true,
		}

		missingNames := make([]string, 0)
		unknownNames := make([]string, 0)
		for _, inv := range inventories {
			status := diagnosticsBookDestinationStatus{
				DestinationID:   inv.Destination.ID,
				DestinationName: destinationDisplayName(inv.Destination),
				DestinationType: string(inv.Destination.Type),
			}
			cardIdx := destCardIndex[inv.Destination.ID]

			if inv.FetchErr != nil {
				status.Status = "unknown"
				status.Reason = "destination fetch failed"
				unknownNames = append(unknownNames, status.DestinationName)
				destCards[cardIdx].Unknown++
				item.DestinationStatuses = append(item.DestinationStatuses, status)
				continue
			}

			matched, method, reason, remote := s.matchBookAgainstInventory(book, onDisk, inv)
			if matched {
				status.Status = "matched"
				status.MatchMethod = method
				status.ServerItemID = remote.ID
				status.ServerTitle = remote.Title
				webLog.Debug().Str("destination_id", inv.Destination.ID).Str("destination_type", string(inv.Destination.Type)).Str("asin", book.ASIN).Str("match_method", method).Str("server_item_id", remote.ID).Msg("diagnostics: book matched")
				destCards[cardIdx].Matched++
			} else {
				status.Status = reasonStatus(reason)
				status.Reason = reason
				if status.Status == "missing" {
					missingNames = append(missingNames, status.DestinationName)
					destCards[cardIdx].Missing++
				} else {
					unknownNames = append(unknownNames, status.DestinationName)
					destCards[cardIdx].Unknown++
				}
			}
			item.DestinationStatuses = append(item.DestinationStatuses, status)
		}

		sort.Slice(item.DestinationStatuses, func(i, j int) bool {
			return item.DestinationStatuses[i].DestinationName < item.DestinationStatuses[j].DestinationName
		})

		if !onDisk {
			item.IssueSummary = "File missing from disk"
		} else if len(missingNames) > 0 {
			item.IssueSummary = "Missing in destinations: " + strings.Join(missingNames, ", ")
		} else if len(unknownNames) > 0 {
			item.IssueSummary = "Unknown in destinations: " + strings.Join(unknownNames, ", ")
		}

		if item.IssueSummary != "" {
			issues = append(issues, item)
		}
	}

	for _, book := range books {
		if book.Status == database.BookStatusComplete {
			continue
		}
		asin := strings.ToUpper(strings.TrimSpace(book.ASIN))
		if asin == "" {
			continue
		}
		path, ok := localASINPaths[asin]
		if !ok {
			continue
		}
		issues = append(issues, diagnosticsIssueItem{
			ASIN:                book.ASIN,
			Title:               book.Title,
			Author:              book.Author,
			FilePath:            path,
			OnDisk:              true,
			IssueSummary:        "Scanner mismatch: audio exists on disk but book is not marked complete",
			DestinationStatuses: []diagnosticsBookDestinationStatus{},
			CanTargetedScan:     true,
			CanRedownload:       false,
		})
	}

	sort.Slice(issues, func(i, j int) bool {
		if issues[i].IssueSummary == issues[j].IssueSummary {
			return strings.ToLower(issues[i].Title) < strings.ToLower(issues[j].Title)
		}
		return issues[i].IssueSummary < issues[j].IssueSummary
	})

	response := diagnosticsCompareResponse{
		GeneratedAt:     time.Now().UTC(),
		TotalBooks:      totalBooks,
		CompleteBooks:   completeBooks,
		IssueBooks:      len(issues),
		DiskMissing:     diskMissing,
		Destinations:    destCards,
		Items:           issues,
		UserMarketplace: marketplace,
	}
	for _, card := range destCards {
		webLog.Info().Str("destination_id", card.ID).Str("destination_name", card.Name).Str("destination_type", card.Type).Int("matched", card.Matched).Int("missing", card.Missing).Int("unknown", card.Unknown).Bool("fetch_healthy", card.FetchHealthy).Msg("diagnostics: compare destination summary")
	}
	webLog.Info().Int("issue_books", len(issues)).Int("disk_missing", diskMissing).Int("complete_books", completeBooks).Msg("diagnostics: compare complete")
	c.JSON(http.StatusOK, response)
}

func scanLocalASINAudioPaths(root string) map[string]string {
	index := map[string]string{}
	if strings.TrimSpace(root) == "" {
		return index
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(d.Name()), "."))
		switch ext {
		case "m4b", "m4a", "mp3", "aax", "aaxc", "flac", "ogg", "wma", "aac", "opus":
		default:
			return nil
		}
		asin := library.ExtractASINFromPath(path)
		if asin == "" {
			return nil
		}
		if _, exists := index[asin]; !exists {
			index[asin] = path
		}
		return nil
	})
	return index
}

func (s *Server) handleDiagnosticsTargetedScan(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		ASIN          string `json:"asin"`
		DestinationID string `json:"destination_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.ASIN) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing asin"})
		return
	}
	book, err := s.db.GetBookByASIN(ctx, strings.TrimSpace(req.ASIN))
	if err != nil || book == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "book not found"})
		return
	}
	if strings.TrimSpace(book.FilePath) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "book has no local file path"})
		return
	}
	webLog.Info().Str("asin", strings.TrimSpace(req.ASIN)).Str("file_path", book.FilePath).Str("destination_id", strings.TrimSpace(req.DestinationID)).Msg("diagnostics: targeted scan start")

	dests, _ := s.db.ListEnabledLibraryDestinations(ctx)
	if len(dests) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no enabled destinations configured"})
		return
	}
	if strings.TrimSpace(req.DestinationID) != "" {
		filtered := make([]database.LibraryDestination, 0, 1)
		for _, d := range dests {
			if d.ID == strings.TrimSpace(req.DestinationID) {
				filtered = append(filtered, d)
				break
			}
		}
		if len(filtered) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "destination not found or not enabled"})
			return
		}
		dests = filtered
	}

	results := make([]gin.H, 0, len(dests))
	okCount := 0
	for _, d := range dests {
		backend, err := s.buildDestinationBackend(&d)
		if err != nil {
			results = append(results, gin.H{"destination_id": d.ID, "destination_name": destinationDisplayName(d), "ok": false, "error": err.Error()})
			continue
		}
		perCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		outcomes := backend.OnBookOrganized(perCtx, mediaserver.OrganizedBook{
			BookID:         book.ID,
			ASIN:           book.ASIN,
			Title:          book.Title,
			Author:         book.Author,
			Series:         "",
			SeriesPosition: "",
			LocalPath:      book.FilePath,
			OrganizedAt:    time.Now().UTC(),
		})
		cancel()

		scanOK := false
		scanDetail := ""
		scanErr := ""
		for _, o := range outcomes {
			if o.Operation != mediaserver.OpScanTrigger {
				continue
			}
			if o.Status == mediaserver.OutcomeSucceeded || o.Status == mediaserver.OutcomeSkippedExisting {
				scanOK = true
				scanDetail = o.Detail
			} else {
				if o.Err != nil {
					scanErr = o.Err.Error()
				} else {
					scanErr = o.Detail
				}
			}
		}
		if scanOK {
			okCount++
			webLog.Info().Str("asin", book.ASIN).Str("destination_id", d.ID).Str("destination_name", destinationDisplayName(d)).Str("detail", scanDetail).Msg("diagnostics: targeted scan triggered")
			results = append(results, gin.H{"destination_id": d.ID, "destination_name": destinationDisplayName(d), "ok": true, "detail": scanDetail})
		} else {
			if scanErr == "" {
				scanErr = "scan trigger was not reported by backend"
			}
			webLog.Warn().Str("asin", book.ASIN).Str("destination_id", d.ID).Str("destination_name", destinationDisplayName(d)).Str("error", scanErr).Msg("diagnostics: targeted scan failed")
			results = append(results, gin.H{"destination_id": d.ID, "destination_name": destinationDisplayName(d), "ok": false, "error": scanErr})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": fmt.Sprintf("Triggered targeted scan on %d/%d destinations", okCount, len(dests)),
		"results": results,
	})
}

func reasonStatus(reason string) string {
	if strings.Contains(strings.ToLower(reason), "missing") {
		return "missing"
	}
	return "unknown"
}

func (s *Server) matchBookAgainstInventory(book database.Book, onDisk bool, inv diagnosticsDestinationInventory) (bool, string, string, diagnosticsRemoteItem) {
	empty := diagnosticsRemoteItem{}
	if storedID := strings.TrimSpace(inv.StoredIDs[book.ID]); storedID != "" {
		if remote, ok := inv.ItemsByID[storedID]; ok {
			return true, "server_item_id", "", remote
		}
		return false, "", "missing: stored server item id not found in destination", empty
	}

	if inv.Destination.Type == database.LibraryDestinationTypePlex {
		want := normalizeDiagnosticsPathKey(book.FilePath)
		if want == "" {
			want = normalizeDiagnosticsTitle(book.Title)
		}
		if want == "" {
			return false, "", "unknown: no path or title to compare", empty
		}
		pathMatches := make([]diagnosticsRemoteItem, 0, 1)
		for _, it := range inv.Items {
			remoteKey := normalizeDiagnosticsPathKey(it.Path)
			if remoteKey != "" && remoteKey == want {
				pathMatches = append(pathMatches, it)
			}
		}
		if len(pathMatches) == 1 {
			return true, "path_suffix", "", pathMatches[0]
		}
		if len(pathMatches) > 1 {
			return false, "", "unknown: multiple Plex items share artist/album path", empty
		}
		for _, it := range inv.Items {
			remoteTitle := normalizeDiagnosticsTitle(it.Title)
			if remoteTitle != "" && (strings.Contains(remoteTitle, want) || strings.Contains(want, remoteTitle)) {
				return true, "title_contains", "", it
			}
		}
	}

	if inv.Destination.Type == database.LibraryDestinationTypeABS {
		asin := strings.ToUpper(strings.TrimSpace(book.ASIN))
		if asin == "" {
			return false, "", "unknown: no ASIN on local book", empty
		}
		match := make([]diagnosticsRemoteItem, 0, 1)
		for _, it := range inv.Items {
			if strings.EqualFold(strings.TrimSpace(it.ASIN), asin) {
				match = append(match, it)
			}
		}
		if len(match) == 1 {
			return true, "asin_exact", "", match[0]
		}
		if len(match) > 1 {
			return false, "", "unknown: multiple ABS items share ASIN", empty
		}

		// metadata.asin missed. ABS may have auto-matched the book to a different
		// edition during library scan, leaving metadata.asin pointing at an
		// alternate ISBN/ASIN even though the book folder itself was named after
		// the Audible ASIN by the organizer. Fall back to the folder/file path
		// token, which is the source of truth Audplexus controls.
		pathMatches := make([]diagnosticsRemoteItem, 0, 1)
		for _, it := range inv.Items {
			if remoteASIN := library.ExtractASINFromPath(it.Path); remoteASIN != "" && strings.EqualFold(remoteASIN, asin) {
				pathMatches = append(pathMatches, it)
			}
		}
		if len(pathMatches) == 1 {
			return true, "asin_path", "", pathMatches[0]
		}
		if len(pathMatches) > 1 {
			return false, "", "unknown: multiple ABS items share ASIN in path", empty
		}
		return false, "", "missing: ASIN not found in destination", empty
	}

	if !onDisk || strings.TrimSpace(book.FilePath) == "" {
		return false, "", "unknown: no local file path to compare", empty
	}

	pathMatches := make([]diagnosticsRemoteItem, 0, 1)
	filenameMatches := make([]diagnosticsRemoteItem, 0, 1)
	targetPath := normalizeMatchPath(s.mapBookPathForDestination(book.FilePath, inv.Destination))
	targetName := strings.ToLower(strings.TrimSpace(filepath.Base(book.FilePath)))

	for _, it := range inv.Items {
		if targetPath != "" && normalizeMatchPath(it.Path) != "" && normalizeMatchPath(it.Path) == targetPath {
			pathMatches = append(pathMatches, it)
		}
		if targetName != "" && strings.EqualFold(strings.TrimSpace(it.Filename), targetName) {
			filenameMatches = append(filenameMatches, it)
		}
	}

	if len(pathMatches) == 1 {
		return true, "path_exact", "", pathMatches[0]
	}
	if len(pathMatches) > 1 {
		return false, "", "unknown: multiple destination items match path", empty
	}
	if len(filenameMatches) == 1 {
		return true, "filename_exact", "", filenameMatches[0]
	}
	if len(filenameMatches) > 1 {
		return false, "", "unknown: multiple destination items match filename", empty
	}
	return false, "", "missing: no path/filename match in destination", empty
}

func normalizeMatchPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimRight(p, "/")
	return strings.ToLower(p)
}

func (s *Server) mapBookPathForDestination(localPath string, d database.LibraryDestination) string {
	localPath = filepath.Clean(strings.TrimSpace(localPath))
	if localPath == "." || localPath == "" {
		return ""
	}
	localRoot := strings.TrimSpace(d.AudiobookPath)
	if localRoot == "" {
		localRoot = strings.TrimSpace(s.audiobooksPath)
	}
	destRoot := strings.TrimSpace(d.DestinationPath)
	if localRoot == "" || destRoot == "" {
		return localPath
	}
	localNorm := normalizeMatchPath(localPath)
	rootNorm := normalizeMatchPath(localRoot)
	if !strings.HasPrefix(localNorm+"/", rootNorm+"/") {
		return localPath
	}
	rel := strings.TrimPrefix(localNorm, rootNorm)
	rel = strings.TrimPrefix(rel, "/")
	mapped := normalizeMatchPath(destRoot)
	if rel != "" {
		mapped = strings.TrimRight(mapped, "/") + "/" + rel
	}
	return mapped
}

func (s *Server) fetchDiagnosticsInventory(ctx context.Context, d database.LibraryDestination) ([]diagnosticsRemoteItem, error) {
	switch d.Type {
	case database.LibraryDestinationTypeABS:
		return fetchABSDiagnosticsItems(ctx, d)
	case database.LibraryDestinationTypeEmby:
		return fetchEmbyDiagnosticsItems(ctx, d)
	case database.LibraryDestinationTypeJellyfin:
		return fetchJellyfinDiagnosticsItems(ctx, d)
	case database.LibraryDestinationTypePlex:
		return fetchPlexDiagnosticsItems(ctx, d)
	default:
		return nil, fmt.Errorf("unsupported destination type %q", d.Type)
	}
}

func fetchABSDiagnosticsItems(ctx context.Context, d database.LibraryDestination) ([]diagnosticsRemoteItem, error) {
	base := strings.TrimRight(strings.TrimSpace(d.URL), "/")
	if base == "" || strings.TrimSpace(d.APIKey) == "" || strings.TrimSpace(d.LibraryID) == "" {
		return nil, fmt.Errorf("destination not fully configured")
	}
	items := make([]diagnosticsRemoteItem, 0)
	page := 0
	for {
		u := fmt.Sprintf("%s/api/libraries/%s/items?limit=200&page=%d&minified=1", base, url.PathEscape(strings.TrimSpace(d.LibraryID)), page)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(d.APIKey))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return nil, fmt.Errorf("abs items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var parsed struct {
			Results []struct {
				ID      string `json:"id"`
				Title   string `json:"title"`
				Path    string `json:"path"`
				RelPath string `json:"relPath"`
				Media   struct {
					Metadata struct {
						ASIN string `json:"asin"`
					} `json:"metadata"`
				} `json:"media"`
			} `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		if len(parsed.Results) == 0 {
			break
		}
		for _, r := range parsed.Results {
			path := strings.TrimSpace(r.Path)
			if path == "" {
				path = strings.TrimSpace(r.RelPath)
			}
			items = append(items, diagnosticsRemoteItem{
				ID:    r.ID,
				Title: r.Title,
				Path:  path,
				ASIN:  strings.ToUpper(strings.TrimSpace(r.Media.Metadata.ASIN)),
			})
		}
		if len(parsed.Results) < 200 {
			break
		}
		page++
	}
	return items, nil
}

func fetchEmbyDiagnosticsItems(ctx context.Context, d database.LibraryDestination) ([]diagnosticsRemoteItem, error) {
	base := strings.TrimRight(strings.TrimSpace(d.URL), "/")
	apiKey := strings.TrimSpace(d.APIKey)
	libraryID := strings.TrimSpace(d.LibraryID)
	if base == "" || apiKey == "" || libraryID == "" {
		return nil, fmt.Errorf("destination not fully configured")
	}
	items := make([]diagnosticsRemoteItem, 0)
	start := 0
	for {
		u, _ := url.Parse(base + "/emby/Items")
		q := u.Query()
		q.Set("api_key", apiKey)
		q.Set("ParentId", libraryID)
		q.Set("Recursive", "true")
		q.Set("IncludeItemTypes", "MusicAlbum")
		q.Set("Fields", "Path")
		q.Set("StartIndex", fmt.Sprintf("%d", start))
		q.Set("Limit", "200")
		u.RawQuery = q.Encode()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return nil, fmt.Errorf("emby items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var parsed struct {
			Items []struct {
				ID   string `json:"Id"`
				Name string `json:"Name"`
				Path string `json:"Path"`
			} `json:"Items"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		if len(parsed.Items) == 0 {
			break
		}
		for _, it := range parsed.Items {
			fn := strings.ToLower(strings.TrimSpace(filepath.Base(it.Path)))
			items = append(items, diagnosticsRemoteItem{ID: it.ID, Title: it.Name, Path: it.Path, Filename: fn})
		}
		if len(parsed.Items) < 200 {
			break
		}
		start += len(parsed.Items)
	}
	return items, nil
}

func fetchJellyfinDiagnosticsItems(ctx context.Context, d database.LibraryDestination) ([]diagnosticsRemoteItem, error) {
	base := strings.TrimRight(strings.TrimSpace(d.URL), "/")
	apiKey := strings.TrimSpace(d.APIKey)
	libraryID := strings.TrimSpace(d.LibraryID)
	if base == "" || apiKey == "" || libraryID == "" {
		return nil, fmt.Errorf("destination not fully configured")
	}
	items := make([]diagnosticsRemoteItem, 0)
	start := 0
	for {
		u, _ := url.Parse(base + "/Items")
		q := u.Query()
		q.Set("ParentId", libraryID)
		q.Set("Recursive", "true")
		q.Set("IncludeItemTypes", "AudioBook")
		q.Set("Fields", "Path")
		q.Set("StartIndex", fmt.Sprintf("%d", start))
		q.Set("Limit", "200")
		u.RawQuery = q.Encode()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		req.Header.Set("Authorization", fmt.Sprintf("MediaBrowser Token=\"%s\", Client=\"Audplexus\", Device=\"Audplexus\", DeviceId=\"audplexus-diagnostics\", Version=\"1.0\"", apiKey))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return nil, fmt.Errorf("jellyfin items returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var parsed struct {
			Items []struct {
				ID   string `json:"Id"`
				Name string `json:"Name"`
				Path string `json:"Path"`
			} `json:"Items"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		if len(parsed.Items) == 0 {
			break
		}
		for _, it := range parsed.Items {
			fn := strings.ToLower(strings.TrimSpace(filepath.Base(it.Path)))
			items = append(items, diagnosticsRemoteItem{ID: it.ID, Title: it.Name, Path: it.Path, Filename: fn})
		}
		if len(parsed.Items) < 200 {
			break
		}
		start += len(parsed.Items)
	}
	return items, nil
}

func fetchPlexDiagnosticsItems(ctx context.Context, d database.LibraryDestination) ([]diagnosticsRemoteItem, error) {
	base := strings.TrimRight(strings.TrimSpace(d.URL), "/")
	token := strings.TrimSpace(d.PlexToken)
	sectionID := strings.TrimSpace(d.PlexSectionID)
	if base == "" || token == "" || sectionID == "" {
		return nil, fmt.Errorf("destination not fully configured")
	}
	albums := make([]struct {
		RatingKey string `json:"ratingKey"`
		Title     string `json:"title"`
	}, 0)
	start := 0
	for {
		u, _ := url.Parse(fmt.Sprintf("%s/library/sections/%s/albums", base, url.PathEscape(sectionID)))
		q := u.Query()
		q.Set("X-Plex-Token", token)
		q.Set("X-Plex-Container-Start", fmt.Sprintf("%d", start))
		q.Set("X-Plex-Container-Size", "100")
		u.RawQuery = q.Encode()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return nil, fmt.Errorf("plex albums returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var parsed struct {
			MediaContainer struct {
				Metadata []struct {
					RatingKey string `json:"ratingKey"`
					Title     string `json:"title"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		if len(parsed.MediaContainer.Metadata) == 0 {
			break
		}
		albums = append(albums, parsed.MediaContainer.Metadata...)
		if len(parsed.MediaContainer.Metadata) < 100 {
			break
		}
		start += len(parsed.MediaContainer.Metadata)
	}

	items := make([]diagnosticsRemoteItem, 0, len(albums))
	for _, album := range albums {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		itemPath, err := fetchPlexAlbumTrackPath(ctx, base, token, strings.TrimSpace(album.RatingKey))
		if err != nil {
			webLog.Debug().Err(err).Str("rating_key", album.RatingKey).Str("title", album.Title).Msg("diagnostics: plex album track path fetch failed")
		}
		fn := strings.ToLower(strings.TrimSpace(filepath.Base(itemPath)))
		items = append(items, diagnosticsRemoteItem{ID: album.RatingKey, Title: album.Title, Path: itemPath, Filename: fn})
	}
	return items, nil
}

func fetchPlexAlbumTrackPath(ctx context.Context, base, token, ratingKey string) (string, error) {
	u, _ := url.Parse(fmt.Sprintf("%s/library/metadata/%s/children", base, url.PathEscape(ratingKey)))
	q := u.Query()
	q.Set("X-Plex-Token", token)
	q.Set("X-Plex-Container-Start", "0")
	q.Set("X-Plex-Container-Size", "1")
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("plex children returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		MediaContainer struct {
			Metadata []struct {
				Media []struct {
					Part []struct {
						File string `json:"file"`
					} `json:"Part"`
				} `json:"Media"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	for _, md := range parsed.MediaContainer.Metadata {
		for _, m := range md.Media {
			for _, p := range m.Part {
				if path := strings.TrimSpace(p.File); path != "" {
					return path, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no track path found for Plex album %s", ratingKey)
}

func destinationDisplayName(d database.LibraryDestination) string {
	if strings.TrimSpace(d.DisplayName) != "" {
		return d.DisplayName
	}
	return d.ID
}

func normalizeDiagnosticsTitle(s string) string {
	s = html.UnescapeString(s)
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.NewReplacer(
		"&", " and ",
		"‘", "'",
		"’", "'",
		"“", "\"",
		"”", "\"",
		"–", "-",
		"—", "-",
	).Replace(s)
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			break
		}
	}
	s = strings.NewReplacer(
		":", "",
		"-", " ",
		"_", " ",
		".", "",
		",", "",
		"'", "",
		"\"", "",
		"(", "",
		")", "",
		"[", "",
		"]", "",
		"!", "",
		"?", "",
	).Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func normalizeDiagnosticsPathKey(p string) string {
	p = html.UnescapeString(strings.TrimSpace(p))
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimRight(filepath.Clean(p), "/")
	if p == "" || p == "." {
		return ""
	}
	dir := filepath.Dir(p)
	if dir == "." || dir == "/" {
		return ""
	}
	dir = strings.ReplaceAll(dir, "\\", "/")
	parts := strings.FieldsFunc(dir, func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.ToLower(strings.Join(parts, "/"))
}

