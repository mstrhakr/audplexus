package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/errs"
	"github.com/mstrhakr/audplexus/internal/library"
	"github.com/mstrhakr/audplexus/internal/logging"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
	audible "github.com/mstrhakr/go-audible"
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
	ASIN                  string                             `json:"asin"`
	Title                 string                             `json:"title"`
	Author                string                             `json:"author"`
	FilePath              string                             `json:"file_path,omitempty"`
	OnDisk                bool                               `json:"on_disk"`
	IssueSummary          string                             `json:"issue_summary"`
	DestinationStatuses   []diagnosticsBookDestinationStatus `json:"destination_statuses"`
	MissingDestinationIDs []string                           `json:"missing_destination_ids,omitempty"`
	CanTargetedScan       bool                               `json:"can_targeted_scan"`
	CanRedownload         bool                               `json:"can_redownload"`
}

type diagnosticsCompareResponse struct {
	GeneratedAt     time.Time                    `json:"generated_at"`
	TotalBooks      int                          `json:"total_books"`
	CompleteBooks   int                          `json:"complete_books"`
	IssueBooks      int                          `json:"issue_books"`
	DiskMissing     int                          `json:"disk_missing"`
	Destinations    []diagnosticsDestinationCard `json:"destinations"`
	Items           []diagnosticsIssueItem       `json:"items"`
	UserMarketplace string                       `json:"user_marketplace"`
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
	c.HTML(http.StatusOK, "diagnostics.html", s.withSidebar(c, gin.H{
		"Page":            "diagnostics",
		"UserMarketplace": marketplace,
	}))
}

func (s *Server) handleDiagnosticsCompare(c *gin.Context) {
	ctx := c.Request.Context()
	marketplace := "us"
	if creds := s.audible.GetCredentials(); creds != nil && creds.Marketplace != "" {
		marketplace = creds.Marketplace
	}

	books, totalBooks, err := s.db.ListBooks(ctx, database.BookFilter{})
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
			// Strip embedded HTML / map HTTP codes to friendly hints
			// using the shared cleaner so the Drift Inbox card reads
			// the same way the destination card and Connection-test
			// list do for the same underlying failure.
			card.FetchError = cleanErrorForDisplay(inv.FetchErr.Error())
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
		missingIDs := make([]string, 0)
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
				destCards[cardIdx].Matched++
			} else {
				status.Status = reasonStatus(reason)
				status.Reason = reason
				if status.Status == "missing" {
					missingNames = append(missingNames, status.DestinationName)
					missingIDs = append(missingIDs, status.DestinationID)
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
			item.MissingDestinationIDs = missingIDs
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
		ASIN           string   `json:"asin"`
		DestinationID  string   `json:"destination_id"`
		DestinationIDs []string `json:"destination_ids"`
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
	requestedIDs := map[string]struct{}{}
	for _, id := range req.DestinationIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		requestedIDs[id] = struct{}{}
	}
	if len(requestedIDs) == 0 && strings.TrimSpace(req.DestinationID) != "" {
		requestedIDs[strings.TrimSpace(req.DestinationID)] = struct{}{}
	}
	if len(requestedIDs) > 0 {
		filtered := make([]database.LibraryDestination, 0, len(requestedIDs))
		for _, d := range dests {
			if _, ok := requestedIDs[d.ID]; ok {
				filtered = append(filtered, d)
			}
		}
		if len(filtered) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "requested destinations not found or not enabled"})
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

var diagnosticsKnownEnvVars = []string{
	"PORT",
	"DATABASE_TYPE",
	"DATABASE_PATH",
	"DATABASE_DSN",
	"AUDIOBOOKS_PATH",
	"DOWNLOADS_PATH",
	"CONFIG_PATH",
	"LOG_LEVEL",
	"LOG_JSON",
	"LOG_FILE_ENABLED",
	"LOG_FILE_PATH",
	"LOG_FILE_MAX_SIZE_MB",
	"LOG_FILE_MAX_BACKUPS",
	"LOG_FILE_MAX_AGE_DAYS",
	"LOG_FILE_COMPRESS",
	"OUTPUT_FORMAT",
	"DOWNLOAD_CONCURRENCY",
	"DOWNLOAD_DOWNLOAD_CONCURRENCY",
	"DECRYPT_CONCURRENCY",
	"DOWNLOAD_DECRYPT_CONCURRENCY",
	"PROCESS_CONCURRENCY",
	"DOWNLOAD_PROCESS_CONCURRENCY",
	"PLEX_URL",
	"PLEX_TOKEN",
	"SYNC_SCHEDULE",
	"SYNC_ENABLED",
	"SYNC_MODE",
	"SYNC_AUTO_QUEUE_NEW",
	"AUDPLEXUS_API_KEY",
	"AUDPLEXUS_ADMIN_USERNAME",
	"AUDPLEXUS_ADMIN_PASSWORD",
	"MEDIA_SERVER",
	"EMBY_URL",
	"EMBY_API_KEY",
	"EMBY_LIBRARY_ID",
	"EMBY_LIBRARY_PATH",
}

func diagnosticsEnvPresence() map[string]bool {
	out := make(map[string]bool, len(diagnosticsKnownEnvVars))
	for _, key := range diagnosticsKnownEnvVars {
		out[key] = strings.TrimSpace(os.Getenv(key)) != ""
	}
	return out
}

func diagnosticsExportWindowFromInputs(rangeKeyRaw, sinceRaw, untilRaw string, now time.Time) (*time.Time, *time.Time, string, error) {
	end := now.UTC()
	rangeKey := strings.ToLower(strings.TrimSpace(rangeKeyRaw))
	if rangeKey == "" {
		rangeKey = "24h"
	}
	sinceRaw = strings.TrimSpace(sinceRaw)
	untilRaw = strings.TrimSpace(untilRaw)
	if sinceRaw != "" {
		since, err := time.Parse(time.RFC3339, sinceRaw)
		if err != nil {
			return nil, nil, "", fmt.Errorf("invalid since timestamp")
		}
		until := end
		if untilRaw != "" {
			parsed, err := time.Parse(time.RFC3339, untilRaw)
			if err != nil {
				return nil, nil, "", fmt.Errorf("invalid until timestamp")
			}
			until = parsed.UTC()
		}
		s := since.UTC()
		u := until.UTC()
		return &s, &u, "custom", nil
	}

	switch rangeKey {
	case "all":
		return nil, &end, "all", nil
	case "1h":
		s := end.Add(-1 * time.Hour)
		return &s, &end, "1h", nil
	case "6h":
		s := end.Add(-6 * time.Hour)
		return &s, &end, "6h", nil
	case "30d":
		s := end.Add(-30 * 24 * time.Hour)
		return &s, &end, "30d", nil
	case "7d":
		s := end.Add(-7 * 24 * time.Hour)
		return &s, &end, "7d", nil
	case "24h", "":
		s := end.Add(-24 * time.Hour)
		return &s, &end, "24h", nil
	default:
		return nil, nil, "", fmt.Errorf("invalid range; use 1h, 6h, 24h, 7d, 30d, all, or custom since/until")
	}
}

func diagnosticsExportWindow(c *gin.Context, now time.Time) (*time.Time, *time.Time, string, error) {
	return diagnosticsExportWindowFromInputs(c.DefaultQuery("range", "24h"), c.Query("since"), c.Query("until"), now)
}

func diagnosticsExportRanges(availability logging.LogAvailability, now time.Time) []gin.H {
	if availability.Earliest == nil || availability.Latest == nil {
		return []gin.H{{"value": "all", "label": "All available"}}
	}

	span := availability.Latest.Sub(*availability.Earliest)
	ranges := make([]gin.H, 0, 6)
	type option struct {
		value string
		label string
		dur   time.Duration
	}
	for _, o := range []option{
		{value: "1h", label: "Last 1 hour", dur: time.Hour},
		{value: "6h", label: "Last 6 hours", dur: 6 * time.Hour},
		{value: "24h", label: "Last 24 hours", dur: 24 * time.Hour},
		{value: "7d", label: "Last 7 days", dur: 7 * 24 * time.Hour},
		{value: "30d", label: "Last 30 days", dur: 30 * 24 * time.Hour},
	} {
		if span >= o.dur {
			ranges = append(ranges, gin.H{"value": o.value, "label": o.label})
		}
	}
	ranges = append(ranges, gin.H{"value": "all", "label": "All available"})
	_ = now // reserved for future dynamic labels
	return ranges
}

func writeZipJSON(zw *zip.Writer, name string, v any) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeZipLogLines(zw *zip.Writer, name string, entries []logging.ParsedEntry) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

func ndjsonFromLogEntries(entries []logging.ParsedEntry) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, e := range entries {
		_ = enc.Encode(e)
	}
	return b.String()
}

func prettyJSON(v any) string {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return strings.TrimRight(buf.String(), "\n")
}

func diagnosticsSummaryMarkdown(issueType, expected, notes, gistURL, mode, detail, rangeLabel string, exported int, now time.Time) string {
	var b strings.Builder
	b.WriteString("# Audplexus Diagnostics Handover\n\n")
	b.WriteString("## Summary\n")
	b.WriteString("- Generated: " + now.Format(time.RFC3339) + "\n")
	b.WriteString("- Mode: `" + mode + "`\n")
	b.WriteString("- Detail: `" + detail + "`\n")
	b.WriteString("- Range: `" + rangeLabel + "`\n")
	b.WriteString("- Log entries exported: " + strconv.Itoa(exported) + "\n")
	if issueType != "" {
		b.WriteString("- Issue type: " + issueType + "\n")
	}
	if expected != "" {
		b.WriteString("- Expected: " + expected + "\n")
	}
	if notes != "" {
		b.WriteString("- Reporter notes: " + notes + "\n")
	}
	if gistURL != "" {
		b.WriteString("- Artifact: " + gistURL + "\n")
	}
	b.WriteString("\n## What To Check\n")
	b.WriteString("1. Inspect the gist artifact and compare destination health, runtime env, and logs around failure time.\n")
	b.WriteString("2. Correlate warning/error log lines with sync/download events in the same window.\n")
	b.WriteString("3. Validate destination auth/health and local path configuration from the report snapshot.\n")
	return b.String()
}

var diagnosticsURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)
var diagnosticsIPv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
var diagnosticsSecretQueryPattern = regexp.MustCompile(`(?i)(api[_-]?key|token|secret)=([^&\s"']+)`)

func redactDiagnosticsSensitive(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	out := diagnosticsURLPattern.ReplaceAllString(s, "[redacted-url]")
	out = diagnosticsIPv4Pattern.ReplaceAllString(out, "[redacted-ip]")
	out = diagnosticsSecretQueryPattern.ReplaceAllString(out, "$1=[redacted]")
	return out
}

func sanitizeRuntimeEnvForHandoff(env gin.H) gin.H {
	if env == nil {
		return gin.H{}
	}

	out := gin.H{
		"go_version":    env["go_version"],
		"os_arch":       env["os_arch"],
		"num_cpu":       env["num_cpu"],
		"log_level":     env["log_level"],
		"authenticated": env["authenticated"],
		"marketplace":   env["marketplace"],
		"last_sync":     env["last_sync"],
		"server_time":   env["server_time"],
	}

	if logFile, ok := env["log_file"].(gin.H); ok {
		out["log_file"] = gin.H{
			"enabled":      logFile["enabled"],
			"max_size_mb":  logFile["max_size_mb"],
			"max_backups":  logFile["max_backups"],
			"max_age_days": logFile["max_age_days"],
			"compress":     logFile["compress"],
		}
	} else if logFile, ok := env["log_file"].(map[string]interface{}); ok {
		out["log_file"] = gin.H{
			"enabled":      logFile["enabled"],
			"max_size_mb":  logFile["max_size_mb"],
			"max_backups":  logFile["max_backups"],
			"max_age_days": logFile["max_age_days"],
			"compress":     logFile["compress"],
		}
	}

	return out
}

func sanitizeDestinationsForHandoff(destinations []gin.H) []gin.H {
	out := make([]gin.H, 0, len(destinations))
	for _, d := range destinations {
		clean := gin.H{
			"id":              d["id"],
			"display_name":    d["display_name"],
			"type":            d["type"],
			"type_label":      d["type_label"],
			"enabled":         d["enabled"],
			"configured":      d["configured"],
			"health":          d["health"],
			"last_checked_at": d["last_checked_at"],
		}
		if detail, ok := d["health_detail"].(string); ok {
			clean["health_detail"] = redactDiagnosticsSensitive(detail)
		}
		if lastErr, ok := d["last_error"].(string); ok {
			clean["last_error"] = redactDiagnosticsSensitive(lastErr)
		}
		out = append(out, clean)
	}
	return out
}

type diagnosticsProxyRequest struct {
	Report gin.H `json:"report"`
}

type diagnosticsProxyResponse struct {
	Success  bool   `json:"success"`
	GistURL  string `json:"gist_url,omitempty"`
	IssueURL string `json:"issue_url,omitempty"`
	Error    string `json:"error,omitempty"`
}

const defaultDiagnosticsProxyEndpoint = "https://api.audplexus.dev/diagnostic"

func diagnosticsProxyEndpoint() string {
	if v := strings.TrimSpace(os.Getenv("AUDPLEXUS_DIAGNOSTIC_PROXY_URL")); v != "" {
		return v
	}
	return defaultDiagnosticsProxyEndpoint
}

func submitDiagnosticsProxy(ctx context.Context, endpoint string, report gin.H) (diagnosticsProxyResponse, error) {
	out := diagnosticsProxyResponse{}
	body, err := json.Marshal(diagnosticsProxyRequest{Report: report})
	if err != nil {
		return out, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "audplexus-diagnostics/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("decode proxy response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !out.Success {
		if strings.TrimSpace(out.Error) == "" {
			out.Error = fmt.Sprintf("proxy request failed (%d)", resp.StatusCode)
		}
		return out, errors.New(out.Error)
	}
	return out, nil
}

type diagnosticsHandoffRequest struct {
	Range         string `json:"range"`
	Mode          string `json:"mode"`        // logs | package
	Detail        string `json:"detail"`      // standard | full
	UploadMode    string `json:"upload_mode"` // none | gist_secret | gist_public
	IssueType     string `json:"issue_type"`
	ExpectedValue string `json:"expected_value"`
	ReproSteps    string `json:"repro_steps"`
	UserNotes     string `json:"user_notes"`
	IssueTitle    string `json:"issue_title"`
}

type diagnosticsHandoffResponse struct {
	Success    bool   `json:"success"`
	Message    string `json:"message,omitempty"`
	GistURL    string `json:"gist_url,omitempty"`
	IssueURL   string `json:"issue_url,omitempty"`
	IssueBody  string `json:"issue_body,omitempty"`
	IssueTitle string `json:"issue_title,omitempty"`
}

// buildDeploymentMode is set at compile time with:
//
//	-ldflags "-X github.com/mstrhakr/audplexus/internal/web.buildDeploymentMode=docker"
//
// Defaults to native for local/dev builds.
var buildDeploymentMode = "native"

// Build metadata fields are optional ldflags values used by serverVersion().
//
//	-ldflags "-X github.com/mstrhakr/audplexus/internal/web.buildReleaseVersion=0.6.1"
//	-ldflags "-X github.com/mstrhakr/audplexus/internal/web.buildCommitRef=abc123def456"
//	-ldflags "-X github.com/mstrhakr/audplexus/internal/web.buildTimestamp=20260831153045"
//	-ldflags "-X github.com/mstrhakr/audplexus/internal/web.buildChannel=release"
//
// buildChannel expects: "release" for tagged builds, "dev" otherwise.
var buildReleaseVersion = ""
var buildCommitRef = ""
var buildTimestamp = ""
var buildChannel = "dev"

// handleDiagnosticsEnv returns a JSON snapshot of runtime + path
// info for the DS-style "Logs & Environment" diagnostics tab. Read-only.
func (s *Server) handleDiagnosticsEnv(c *gin.Context) {
	c.JSON(http.StatusOK, s.diagnosticsEnvSnapshot(c.Request.Context()))
}

func (s *Server) diagnosticsEnvSnapshot(ctx context.Context) gin.H {
	marketplace := "us"
	if stored, _ := s.db.GetSetting(ctx, "audible_marketplace"); strings.TrimSpace(stored) != "" {
		marketplace = strings.TrimSpace(stored)
	} else if creds := s.audible.GetCredentials(); creds != nil && creds.Marketplace != "" {
		marketplace = creds.Marketplace
	}

	var lastSyncOut gin.H
	if last, err := s.db.GetLastSync(ctx); err == nil && last != nil {
		lastSyncOut = gin.H{
			"started_at":   last.StartedAt,
			"completed_at": last.CompletedAt,
			"status":       last.Status,
			"books_found":  last.BooksFound,
			"books_added":  last.BooksAdded,
		}
	}
	logFileCfg := logging.GetFileConfig()

	return gin.H{
		"app_version":     serverVersion(),
		"deployment_mode": diagnosticsDeploymentMode(),
		"go_version":      runtime.Version(),
		"os_arch":         runtime.GOOS + "/" + runtime.GOARCH,
		"num_cpu":         runtime.NumCPU(),
		"audiobooks_path": s.audiobooksPath,
		"downloads_path":  s.downloadsPath,
		"config_path":     s.configPath,
		"log_level":       logging.GetLevel(),
		"authenticated":   s.audible.IsAuthenticated(),
		"marketplace":     marketplace,
		"last_sync":       lastSyncOut,
		"server_time":     time.Now(),
		"log_file": gin.H{
			"enabled":      logFileCfg.Enabled,
			"path":         logFileCfg.Path,
			"max_size_mb":  logFileCfg.MaxSizeMB,
			"max_backups":  logFileCfg.MaxBackups,
			"max_age_days": logFileCfg.MaxAgeDays,
			"compress":     logFileCfg.Compress,
		},
	}
}

func diagnosticsDeploymentMode() string {
	mode := strings.ToLower(strings.TrimSpace(buildDeploymentMode))
	if mode == "" {
		return "native"
	}
	return mode
}

// handleDiagnosticsDestinations returns a JSON snapshot of every
// configured destination's connection state for the Connection-tests
// list on the Logs & Environment diagnostics tab. Read-only; per-row
// Test buttons hit the existing /destinations/:id/test endpoint.
//
// We reuse destinationSummaries (the same view-model the dashboard
// reads) so disabled / never-checked / failed states render identically
// across the app. Sensitive fields (api_key, plex_token) are not in
// the summary struct, so this can't leak credentials.
func (s *Server) handleDiagnosticsDestinations(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"destinations": s.diagnosticsDestinationsSnapshot(c.Request.Context())})
}

func (s *Server) diagnosticsDestinationsSnapshot(ctx context.Context) []gin.H {
	// Coverage stat needs a books-complete count, but the diagnostics
	// list doesn't render coverage — pass 0 so the helper skips that
	// branch and we avoid a per-call books table scan.
	summaries := s.destinationSummaries(ctx, 0)

	out := make([]gin.H, 0, len(summaries))
	for _, v := range summaries {
		// HealthDetail is written live from err.Error() in
		// buildDestinationSummary, so it can still carry raw HTML
		// response bodies. LastError gets cleaned at write time in
		// recordDestinationHealth, but rows persisted before that
		// fix may still hold raw text — clean both at the JSON
		// boundary so the Connection-tests list always renders the
		// same friendly wording as the rest of the app.
		out = append(out, gin.H{
			"id":              v.ID,
			"display_name":    v.DisplayName,
			"type":            v.Type,
			"type_label":      v.TypeLabel,
			"enabled":         v.Enabled,
			"configured":      v.Configured,
			"url":             v.URL,
			"health":          v.Health,
			"health_detail":   errs.CleanForDisplay(v.HealthDetail),
			"last_error":      errs.CleanForDisplay(v.LastError),
			"last_checked_at": v.LastCheckedAt,
		})
	}
	return out
}

// handleDiagnosticsLogAvailability returns available log time window + valid ranges.
func (s *Server) handleDiagnosticsLogAvailability(c *gin.Context) {
	now := time.Now().UTC()
	availability, err := logging.GetLogAvailability()
	if err != nil {
		webLog.Warn().Err(err).Msg("diagnostics availability: failed to inspect file logs")
	}

	ranges := diagnosticsExportRanges(availability, now)
	defaultRange := "all"
	for _, candidate := range []string{"24h", "7d", "30d", "6h", "1h", "all"} {
		for _, r := range ranges {
			if r["value"] == candidate {
				defaultRange = candidate
				goto done
			}
		}
	}
done:

	c.JSON(http.StatusOK, gin.H{
		"source":        availability.Source,
		"earliest":      availability.Earliest,
		"latest":        availability.Latest,
		"ranges":        ranges,
		"default_range": defaultRange,
	})
}

// handleDiagnosticsExport bundles logs + sanitized diagnostics metadata.
func (s *Server) handleDiagnosticsExport(c *gin.Context) {
	now := time.Now().UTC()
	since, until, rangeLabel, err := diagnosticsExportWindow(c, now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	mode := strings.ToLower(strings.TrimSpace(c.DefaultQuery("mode", "package")))
	if mode != "package" && mode != "logs" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mode; use package or logs"})
		return
	}
	detail := strings.ToLower(strings.TrimSpace(c.DefaultQuery("detail", "standard")))
	if detail != "standard" && detail != "full" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid detail; use standard or full"})
		return
	}

	maxLines := 5000
	if mode == "logs" {
		maxLines = 20000
	} else if detail == "full" {
		maxLines = 20000
	}
	if raw := strings.TrimSpace(c.Query("max_lines")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "max_lines must be a positive integer"})
			return
		}
		if parsed > 20000 {
			parsed = 20000
		}
		maxLines = parsed
	}

	entries, source, logErr := logging.ExportLogs(since, until, maxLines)
	if logErr != nil {
		webLog.Warn().Err(logErr).Msg("diagnostics export: log file read failed; using available logs")
	}

	ctx := c.Request.Context()
	envSnapshot := s.diagnosticsEnvSnapshot(ctx)
	destinationSnapshot := s.diagnosticsDestinationsSnapshot(ctx)

	filePrefix := "audplexus-diagnostics"
	if mode == "logs" {
		filePrefix = "audplexus-logs"
	}
	fileName := fmt.Sprintf("%s-%s.zip", filePrefix, now.Format("20060102-150405"))
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))

	zw := zip.NewWriter(c.Writer)
	defer func() {
		_ = zw.Close()
	}()

	summary := gin.H{
		"generated_at":       now,
		"logs_source":        source,
		"logs_exported":      len(entries),
		"range":              rangeLabel,
		"mode":               mode,
		"detail":             detail,
		"max_lines":          maxLines,
		"has_log_read_error": logErr != nil,
	}
	if err := writeZipJSON(zw, "summary.json", summary); err != nil {
		webLog.Warn().Err(err).Msg("diagnostics export: failed to write summary.json")
	}
	if err := writeZipLogLines(zw, "logs.ndjson", entries); err != nil {
		webLog.Warn().Err(err).Msg("diagnostics export: failed to write logs.ndjson")
	}
	if err := writeZipJSON(zw, "env_presence.json", diagnosticsEnvPresence()); err != nil {
		webLog.Warn().Err(err).Msg("diagnostics export: failed to write env_presence.json")
	}
	if mode == "package" {
		if err := writeZipJSON(zw, "runtime_env.json", envSnapshot); err != nil {
			webLog.Warn().Err(err).Msg("diagnostics export: failed to write runtime_env.json")
		}
		if detail == "full" {
			if err := writeZipJSON(zw, "destinations.json", gin.H{"destinations": destinationSnapshot}); err != nil {
				webLog.Warn().Err(err).Msg("diagnostics export: failed to write destinations.json")
			}
		}
	}
	if logErr != nil {
		if w, err := zw.Create("warnings.txt"); err == nil {
			_, _ = w.Write([]byte("Log export warning: " + logErr.Error() + "\n"))
		}
	}
}

func (s *Server) handleDiagnosticsReportHandoff(c *gin.Context) {
	var req diagnosticsHandoffRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	now := time.Now().UTC()

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "package"
	}
	detail := strings.ToLower(strings.TrimSpace(req.Detail))
	if detail == "" {
		detail = "standard"
	}
	rangeLabel := strings.TrimSpace(req.Range)
	if rangeLabel == "" {
		rangeLabel = "24h"
	}

	since, until, _, err := diagnosticsExportWindowFromInputs(strings.TrimSpace(req.Range), "", "", now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	maxLines := 5000
	if mode == "logs" || detail == "full" {
		maxLines = 20000
	}
	entries, source, logErr := logging.ExportLogs(since, until, maxLines)
	if logErr != nil {
		webLog.Warn().Err(logErr).Msg("handoff: log export degraded")
	}

	ctx := c.Request.Context()
	envSnapshot := sanitizeRuntimeEnvForHandoff(s.diagnosticsEnvSnapshot(ctx))
	destinationSnapshot := sanitizeDestinationsForHandoff(s.diagnosticsDestinationsSnapshot(ctx))
	envPresence := diagnosticsEnvPresence()
	uploadMode := strings.ToLower(strings.TrimSpace(req.UploadMode))
	if uploadMode == "" {
		uploadMode = "gist_secret"
	}
	cleanExpected := strings.TrimSpace(redactDiagnosticsSensitive(req.ExpectedValue))
	cleanSteps := strings.TrimSpace(redactDiagnosticsSensitive(req.ReproSteps))
	cleanNotes := strings.TrimSpace(redactDiagnosticsSensitive(req.UserNotes))
	cleanVersion := strings.TrimSpace(redactDiagnosticsSensitive(serverVersion()))
	cleanDeploy := strings.TrimSpace(redactDiagnosticsSensitive(diagnosticsDeploymentMode()))

	issueTitle := strings.TrimSpace(redactDiagnosticsSensitive(req.IssueTitle))
	if issueTitle == "" {
		issueTitle = fmt.Sprintf("diagnostics: %s (%s)", strings.TrimSpace(req.IssueType), now.Format("2006-01-02"))
		if strings.TrimSpace(req.IssueType) == "" {
			issueTitle = fmt.Sprintf("diagnostics handoff (%s)", now.Format("2006-01-02"))
		}
	}
	issueBody := diagnosticsSummaryMarkdown(req.IssueType, cleanExpected, cleanNotes, "", mode, detail, rangeLabel, len(entries), now)
	if logErr != nil {
		issueBody += "\n\n> Note: file log inspection reported an error; export may rely on partial in-memory logs.\n"
	}
	if source != "" {
		issueBody += "\nLog source: `" + source + "`\n"
	}

	endpoint := diagnosticsProxyEndpoint()

	logLines := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.RawLine) != "" {
			logLines = append(logLines, redactDiagnosticsSensitive(e.RawLine))
			continue
		}
		if strings.TrimSpace(e.Message) != "" {
			logLines = append(logLines, redactDiagnosticsSensitive(e.Message))
		}
	}

	proxyReport := gin.H{
		"report_id":         fmt.Sprintf("AUD-%s", now.Format("20060102-150405")),
		"timestamp":         now.Format(time.RFC3339),
		"issue_type":        req.IssueType,
		"expected_value":    cleanExpected,
		"repro_steps":       cleanSteps,
		"user_message":      cleanNotes,
		"issue_title":       issueTitle,
		"app_version":       cleanVersion,
		"deployment_mode":   cleanDeploy,
		"issue_body":        issueBody,
		"range":             rangeLabel,
		"mode":              mode,
		"detail":            detail,
		"upload_mode":       uploadMode,
		"log_source":        source,
		"logs_exported":     len(logLines),
		"runtime_env":       envSnapshot,
		"env_presence":      envPresence,
		"destinations":      destinationSnapshot,
		"recent_logs":       logLines,
		"generated_summary": diagnosticsSummaryMarkdown(req.IssueType, cleanExpected, cleanNotes, "", mode, detail, rangeLabel, len(logLines), now),
	}

	proxyResp, err := submitDiagnosticsProxy(ctx, endpoint, proxyReport)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, diagnosticsHandoffResponse{
		Success:    true,
		Message:    "Submitted via diagnostics proxy",
		GistURL:    strings.TrimSpace(proxyResp.GistURL),
		IssueURL:   strings.TrimSpace(proxyResp.IssueURL),
		IssueBody:  issueBody,
		IssueTitle: issueTitle,
	})
}

// handleDiagnosticsLogsSSE streams diagnostics log entries via SSE.
// It emits a bootstrap event with recent history, then incremental
// "log" events for each new line captured by the in-memory ring buffer.
func (s *Server) handleDiagnosticsLogsSSE(c *gin.Context) {
	n := 300
	if v := c.Query("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 1024 {
			n = parsed
		}
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	ctx := c.Request.Context()
	id, ch := logging.SubscribeLogs()
	defer logging.UnsubscribeLogs(id)

	initialRaw := logging.TailLogs(n)
	initial := make([]logging.ParsedEntry, len(initialRaw))
	for i, raw := range initialRaw {
		entry := logging.ParseLogLine(raw.Line)
		if entry.Time.IsZero() {
			entry.Time = raw.Time
		}
		initial[i] = entry
	}
	c.SSEvent("logs_bootstrap", gin.H{"entries": initial})

	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()

	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case <-keepAlive.C:
			c.SSEvent("ping", gin.H{"ts": time.Now().UTC()})
			return true
		case raw, ok := <-ch:
			if !ok {
				return false
			}
			entry := logging.ParseLogLine(raw.Line)
			if entry.Time.IsZero() {
				entry.Time = raw.Time
			}
			c.SSEvent("log", entry)
			return true
		}
	})
}

// diagnosticsAccountProbe is one account's view of a single ASIN, fetched live
// from the per-item library endpoint (/1.0/library/{asin}). Exists to answer
// "account X owns this title, why doesn't it sync?" — the LIST endpoint
// applies server-side filters the item endpoint doesn't, so a title can probe
// fine here yet never appear in a sync.
type diagnosticsAccountProbe struct {
	AccountID    string `json:"account_id"`
	AccountName  string `json:"account_name"`
	Found        bool   `json:"found"`
	Error        string `json:"error,omitempty"`
	Title        string `json:"title,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	FormatType   string `json:"format_type,omitempty"`
	DeliveryType string `json:"delivery_type,omitempty"`
	Downloadable bool   `json:"downloadable"`
	Entitled     bool   `json:"entitled"`
	EntitleError string `json:"entitle_error,omitempty"`
}

// handleDiagnosticsASINProbe checks one ASIN against every connected account
// plus the local DB. GET /api/diagnostics/asin/:asin
func (s *Server) handleDiagnosticsASINProbe(c *gin.Context) {
	ctx := c.Request.Context()
	asin := strings.TrimSpace(c.Param("asin"))
	if asin == "" || len(asin) > 20 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid ASIN"})
		return
	}

	var probes []diagnosticsAccountProbe
	if s.accounts != nil {
		for _, acct := range s.accounts.EnabledAccounts() {
			p := diagnosticsAccountProbe{AccountID: acct.ID, AccountName: acct.Name}
			if acct.Client == nil || !acct.Client.IsAuthenticated() {
				p.Error = "not authenticated"
				probes = append(probes, p)
				continue
			}
			probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			item, err := acct.Client.GetBook(probeCtx, asin)
			if err != nil {
				p.Error = errs.CleanForDisplay(err.Error())
			} else if item == nil || item.ASIN == "" {
				p.Error = "not in this account's library"
			} else {
				p.Found = true
				p.Title = item.Title
				p.ContentType = item.ContentType
				p.FormatType = item.FormatType
				p.DeliveryType = item.ContentDeliveryType
				p.Downloadable = item.Downloadable()
				if ok, cdErr := acct.Client.CanDownload(probeCtx, *item); cdErr != nil {
					p.EntitleError = errs.CleanEntitlementReason(cdErr.Error())
				} else {
					p.Entitled = ok
				}
			}
			cancel()
			probes = append(probes, p)
		}
	}

	// Local DB view: does a row exist, and which accounts are stamped on it.
	localStatus := ""
	localReason := ""
	var localOwners []string
	if book, err := s.db.GetBookByASIN(ctx, asin); err == nil && book != nil {
		localStatus = string(book.Status)
		localReason = book.UnavailableReason
		if owners, err := s.db.GetBookAccountsForASINs(ctx, []string{asin}); err == nil {
			localOwners = owners[asin]
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"asin":         asin,
		"accounts":     probes,
		"local_status": localStatus,
		"local_reason": localReason,
		"local_owners": localOwners,
	})
}

// diagnosticsAccountInventory reconciles one account's live Audible library
// against what the local DB has stamped for it. Answers "the account owns N
// titles but the Library filter shows M — which ones, and why."
type diagnosticsAccountInventory struct {
	AccountID     string   `json:"account_id"`
	AccountName   string   `json:"account_name"`
	Error         string   `json:"error,omitempty"`
	APICount      int      `json:"api_count"`       // distinct ASINs the library API returned (full response_groups)
	MinimalCount  int      `json:"minimal_count"`   // same fetch with minimal response_groups — if higher, groups are filtering
	TotalResults  int      `json:"total_results"`   // what the API itself reports as the library size
	StampedCount  int      `json:"stamped_count"`   // rows in book_audible_accounts for this account
	InAPINotDB    []string `json:"in_api_not_db"`   // API returned it, but it's not stamped (the bug surface)
	InDBNotAPI    []string `json:"in_db_not_api"`   // stamped, but the API no longer returns it
	OnlyInMinimal []string `json:"only_in_minimal"` // ASINs the minimal fetch surfaced that the full one dropped
	Downloadable  int      `json:"downloadable"`    // of APICount, how many pass Downloadable()
	NonDownload   int      `json:"non_download"`    // of APICount, how many fail it
}

// handleDiagnosticsAccountInventory fetches every connected account's full
// library live and reconciles it against the DB ownership junction. This is
// the ground-truth tool for "owned vs shown" gaps. GET
// /api/diagnostics/account-inventory
func (s *Server) handleDiagnosticsAccountInventory(c *gin.Context) {
	ctx := c.Request.Context()
	if s.accounts == nil {
		c.JSON(http.StatusOK, gin.H{"accounts": []diagnosticsAccountInventory{}})
		return
	}

	libraryGroups := append([]string{}, audible.DefaultResponseGroups...)
	libraryGroups = append(libraryGroups, "relationships")

	out := make([]diagnosticsAccountInventory, 0)
	for _, acct := range s.accounts.EnabledAccounts() {
		inv := diagnosticsAccountInventory{AccountID: acct.ID, AccountName: acct.Name}
		if acct.Client == nil || !acct.Client.IsAuthenticated() {
			inv.Error = "not authenticated"
			out = append(out, inv)
			continue
		}

		fetchCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		books, total, err := library.FetchEntireLibraryWithTotal(fetchCtx, acct.Client, libraryGroups)
		cancel()
		if err != nil {
			inv.Error = errs.CleanForDisplay(err.Error())
			out = append(out, inv)
			continue
		}
		inv.TotalResults = total

		apiASINs := make(map[string]struct{}, len(books))
		for _, b := range books {
			if b.ASIN == "" {
				continue
			}
			if _, dup := apiASINs[b.ASIN]; dup {
				continue
			}
			apiASINs[b.ASIN] = struct{}{}
			if b.Downloadable() {
				inv.Downloadable++
			} else {
				inv.NonDownload++
			}
		}
		inv.APICount = len(apiASINs)

		// Second fetch with MINIMAL response_groups. The Audible library
		// endpoint can omit items whose product data doesn't satisfy the
		// requested groups (delisted/rights-changed titles often lack
		// product_plans/price). If the bare fetch surfaces ASINs the full
		// one dropped, response_groups is the filter and the fix is to
		// trim them. This is a diagnostic comparison only.
		minCtx, minCancel := context.WithTimeout(ctx, 120*time.Second)
		minBooks, _, minErr := library.FetchEntireLibraryWithTotal(minCtx, acct.Client, []string{"product_desc", "product_attrs"})
		minCancel()
		if minErr == nil {
			minSet := make(map[string]struct{}, len(minBooks))
			for _, b := range minBooks {
				if b.ASIN != "" {
					minSet[b.ASIN] = struct{}{}
				}
			}
			inv.MinimalCount = len(minSet)
			for a := range minSet {
				if _, ok := apiASINs[a]; !ok {
					inv.OnlyInMinimal = append(inv.OnlyInMinimal, a)
				}
			}
			sort.Strings(inv.OnlyInMinimal)
		}

		// What the DB has stamped for this account.
		stamped, err := s.db.ListASINsForAccount(ctx, acct.ID)
		if err != nil {
			inv.Error = "stamped-lookup failed: " + errs.CleanForDisplay(err.Error())
			out = append(out, inv)
			continue
		}
		stampedSet := make(map[string]struct{}, len(stamped))
		for _, a := range stamped {
			stampedSet[a] = struct{}{}
		}
		inv.StampedCount = len(stampedSet)

		for a := range apiASINs {
			if _, ok := stampedSet[a]; !ok {
				inv.InAPINotDB = append(inv.InAPINotDB, a)
			}
		}
		for a := range stampedSet {
			if _, ok := apiASINs[a]; !ok {
				inv.InDBNotAPI = append(inv.InDBNotAPI, a)
			}
		}
		sort.Strings(inv.InAPINotDB)
		sort.Strings(inv.InDBNotAPI)
		out = append(out, inv)
	}

	c.JSON(http.StatusOK, gin.H{"accounts": out})
}
