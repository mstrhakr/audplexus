package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	plexProduct  = "Audible Plex Downloader"
	plexPlatform = "Web"
)

type plexPinResponse struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	AuthToken string `json:"authToken"`
}

// plexResourcesResponse wraps the JSON response from plex.tv/api/v2/resources.
// Note: The v2 resources endpoint returns an array of devices directly (not wrapped in MediaContainer).
type plexDevice struct {
	Name        string           `json:"name"`
	Product     string           `json:"product"`
	Provides    string           `json:"provides"`
	Owned       bool             `json:"owned"`
	Connections []plexConnection `json:"connections"`
}

type plexConnection struct {
	URI   string `json:"uri"`
	Local bool   `json:"local"`
}

// plexMediaContainer wraps all standard Plex server JSON responses.
type plexMediaContainer struct {
	Size      int `json:"size"`
	TotalSize int `json:"totalSize"`
}

// plexSearchResponse wraps search endpoint JSON response.
type plexSearchResponse struct {
	MediaContainer plexMediaContainer `json:"MediaContainer"`
}

// plexSectionItemsResponse wraps section items endpoint JSON response (e.g., /library/sections/{id}/albums).
type plexSectionItemsResponse struct {
	MediaContainer plexMediaContainer `json:"MediaContainer"`
}

type plexServerOption struct {
	Name  string
	URL   string
	Local bool
}

type plexLibrarySection struct {
	ID        string
	Title     string
	Type      string
	Locations []plexLocation
}

// plexSectionsResponse wraps /library/sections JSON response.
type plexSectionsResponse struct {
	MediaContainer plexSectionsContainer `json:"MediaContainer"`
}

type plexSectionsContainer struct {
	Size        int                    `json:"size"`
	Directories []plexSectionDirectory `json:"Directory"`
}

type plexSectionDirectory struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Type  string `json:"type"`
	// Some Plex servers include Location nodes on section list responses.
	Locations []plexLocation `json:"Location"`
}

// plexSectionDetailResponse wraps the detailed response from /library/sections/{id}
type plexSectionDetailResponse struct {
	MediaContainer plexSectionDetailContainer `json:"MediaContainer"`
}

type plexSectionDetailContainer struct {
	Size        int                          `json:"size"`
	Directories []plexSectionDetailDirectory `json:"Directory"`
}

type plexSectionDetailDirectory struct {
	Key       string         `json:"key"`
	Title     string         `json:"title"`
	Type      string         `json:"type"`
	Locations []plexLocation `json:"Location"`
}

type plexLocation struct {
	Path string `json:"path"`
}

// plexActivitiesResponse is the JSON response from /activities endpoint.
type plexActivitiesResponse struct {
	MediaContainer plexActivitiesContainer `json:"MediaContainer"`
}

type plexActivitiesContainer struct {
	Size       int            `json:"size"`
	Activities []plexActivity `json:"Activity"`
}

type plexActivity struct {
	UUID        string  `json:"uuid"`
	Type        string  `json:"type"`
	Cancellable bool    `json:"cancellable"`
	UserID      int     `json:"userID"`
	Title       string  `json:"title"`
	Subtitle    string  `json:"subtitle"`
	Progress    float64 `json:"progress"` // -1 means indeterminate
}

func (s *Server) plexClientID() string {
	hostname, _ := os.Hostname()
	hostname = strings.TrimSpace(strings.ToLower(hostname))
	if hostname == "" {
		hostname = "local"
	}
	return "apd-" + hostname
}

func (s *Server) plexAuthURL(pinCode string) string {
	clientID := url.QueryEscape(s.plexClientID())
	code := url.QueryEscape(pinCode)
	product := url.QueryEscape(plexProduct)
	device := url.QueryEscape("Audible Plex Downloader Web")
	return fmt.Sprintf("https://app.plex.tv/auth#?clientID=%s&code=%s&context%%5Bdevice%%5D%%5Bproduct%%5D=%s&context%%5Bdevice%%5D%%5BdeviceName%%5D=%s", clientID, code, product, device)
}

func (s *Server) handlePlexStart(c *gin.Context) {
	pin, err := s.plexCreatePin(c.Request.Context())
	if err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to start Plex auth: " + err.Error()})
		return
	}

	s.renderAuthPage(c, http.StatusOK, gin.H{
		"PlexPendingPinID":   pin.ID,
		"PlexPendingPinCode": pin.Code,
		"PlexAuthURL":        s.plexAuthURL(pin.Code),
		"Success":            "Plex sign-in started. Complete login in plex.tv, then click 'Check Plex Login'.",
	})
}

func (s *Server) handlePlexComplete(c *gin.Context) {
	pinID, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("pin_id")), 10, 64)
	if err != nil || pinID <= 0 {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Invalid Plex PIN ID. Start Plex auth again."})
		return
	}
	pinCode := strings.TrimSpace(c.PostForm("pin_code"))
	if pinCode == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Missing Plex PIN code. Start Plex auth again."})
		return
	}

	pin, err := s.plexGetPin(c.Request.Context(), pinID, pinCode)
	if err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to verify Plex login: " + err.Error()})
		return
	}

	if strings.TrimSpace(pin.AuthToken) == "" {
		s.renderAuthPage(c, http.StatusOK, gin.H{
			"Error":              "Plex login is not complete yet. Finish login in plex.tv, then click Check Plex Login again.",
			"PlexPendingPinID":   pinID,
			"PlexPendingPinCode": pinCode,
			"PlexAuthURL":        s.plexAuthURL(pinCode),
		})
		return
	}

	if err := s.db.SetSetting(c.Request.Context(), "plex_token", pin.AuthToken); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to save Plex token: " + err.Error()})
		return
	}

	servers, err := s.plexListServerOptions(c.Request.Context(), pin.AuthToken)
	if err != nil {
		s.renderAuthPage(c, http.StatusOK, gin.H{
			"Success": "Plex login complete and token saved. Could not list servers yet: " + err.Error(),
		})
		return
	}

	s.renderAuthPage(c, http.StatusOK, gin.H{
		"Success":     "Plex login complete. Select the server URL to use.",
		"PlexServers": servers,
	})
}

func (s *Server) handlePlexSelect(c *gin.Context) {
	plexURL := strings.TrimSpace(c.PostForm("plex_url"))
	if plexURL == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Please select a Plex server URL."})
		return
	}

	if err := s.db.SetSetting(c.Request.Context(), "plex_url", plexURL); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to save Plex URL: " + err.Error()})
		return
	}

	sections, err := s.plexListSections(c.Request.Context(), plexURL, s.mustPlexToken(c.Request.Context()))
	if err != nil {
		s.renderAuthPage(c, http.StatusOK, gin.H{"Success": "Plex server URL saved. Could not load libraries yet: " + err.Error()})
		return
	}

	s.renderAuthPage(c, http.StatusOK, gin.H{"Success": "Plex server URL saved. Select a Plex library section.", "PlexSections": sections})
}

func (s *Server) handlePlexSectionSelect(c *gin.Context) {
	sectionID := strings.TrimSpace(c.PostForm("plex_section_id"))
	sectionTitle := strings.TrimSpace(c.PostForm("plex_section_title"))
	if sectionID == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Please select a Plex library section."})
		return
	}

	ctx := c.Request.Context()
	plexURL, plexToken := s.getPlexSettings(ctx)

	if err := s.db.SetSetting(ctx, "plex_section_id", sectionID); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to save Plex section: " + err.Error()})
		return
	}
	if sectionTitle != "" {
		_ = s.db.SetSetting(ctx, "plex_section_title", sectionTitle)
	}

	// Fetch and save the actual library path from Plex
	if plexURL != "" && plexToken != "" {
		if sectionPath, err := s.plexSectionLocation(ctx, plexURL, plexToken, sectionID); err == nil && sectionPath != "" {
			_ = s.db.SetSetting(ctx, "plex_section_path", sectionPath)
		} else if err != nil {
			webLog.Debug().Err(err).Str("section_id", sectionID).Msg("plex section path not available from API")
		}
	}

	s.renderAuthPage(c, http.StatusOK, gin.H{"Success": "Plex library section saved."})
}

func (s *Server) handlePlexScan(c *gin.Context) {
	plexURL, plexToken := s.getPlexSettings(c.Request.Context())
	if plexURL == "" || plexToken == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Plex is not configured. Complete Plex login and server selection first."})
		return
	}
	sectionID, _ := s.db.GetSetting(c.Request.Context(), "plex_section_id")
	sectionID = strings.TrimSpace(sectionID)
	if sectionID == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Choose a Plex library section first."})
		return
	}

	if err := s.plexTriggerSectionScan(c.Request.Context(), plexURL, plexToken, sectionID, "", false); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to trigger Plex scan: " + err.Error()})
		return
	}

	s.renderAuthPage(c, http.StatusOK, gin.H{"Success": "Plex section scan triggered."})
}

func (s *Server) handlePlexCheck(c *gin.Context) {
	query := strings.TrimSpace(c.PostForm("query"))
	if query == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Enter a title to search in Plex."})
		return
	}

	plexURL, plexToken := s.getPlexSettings(c.Request.Context())
	if plexURL == "" || plexToken == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Plex is not configured. Complete Plex login and server selection first."})
		return
	}

	count, err := s.plexSearchCount(c.Request.Context(), plexURL, plexToken, query)
	if err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Plex search failed: " + err.Error(), "PlexCheckQuery": query})
		return
	}

	msg := fmt.Sprintf("Plex search found %d result(s) for %q.", count, query)
	if count == 0 {
		msg = fmt.Sprintf("Plex search found no matches for %q yet.", query)
	}
	s.renderAuthPage(c, http.StatusOK, gin.H{
		"Success":          msg,
		"PlexCheckQuery":   query,
		"PlexSearchResult": count,
	})
}

func (s *Server) getPlexSettings(ctx context.Context) (string, string) {
	plexURL, _ := s.db.GetSetting(ctx, "plex_url")
	plexToken, _ := s.db.GetSetting(ctx, "plex_token")
	if plexURL == "" {
		plexURL = strings.TrimSpace(os.Getenv("PLEX_URL"))
	}
	if plexToken == "" {
		plexToken = strings.TrimSpace(os.Getenv("PLEX_TOKEN"))
	}
	return plexURL, plexToken
}

func (s *Server) mustPlexToken(ctx context.Context) string {
	_, token := s.getPlexSettings(ctx)
	return token
}

func (s *Server) plexCreatePin(ctx context.Context) (*plexPinResponse, error) {
	u := "https://plex.tv/api/v2/pins?strong=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	s.addPlexHeaders(req, "")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("plex.tv returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var pin plexPinResponse
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return nil, err
	}
	if pin.ID == 0 || pin.Code == "" {
		return nil, fmt.Errorf("plex.tv returned an invalid PIN response")
	}
	return &pin, nil
}

func (s *Server) plexGetPin(ctx context.Context, pinID int64, pinCode string) (*plexPinResponse, error) {
	u := fmt.Sprintf("https://plex.tv/api/v2/pins/%d?code=%s", pinID, url.QueryEscape(pinCode))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	s.addPlexHeaders(req, "")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("plex.tv returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var pin plexPinResponse
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return nil, err
	}
	return &pin, nil
}

func (s *Server) plexListServerOptions(ctx context.Context, token string) ([]plexServerOption, error) {
	u := "https://plex.tv/api/v2/resources?includeHttps=1&includeRelay=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	s.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("plex resources returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var devices []plexDevice
	if err := json.NewDecoder(resp.Body).Decode(&devices); err != nil {
		return nil, err
	}

	options := make([]plexServerOption, 0)
	seen := make(map[string]struct{})
	for _, dev := range devices {
		if !strings.Contains(strings.ToLower(dev.Provides), "server") {
			continue
		}
		for _, conn := range dev.Connections {
			u := strings.TrimSpace(conn.URI)
			if u == "" {
				continue
			}
			if _, ok := seen[u]; ok {
				continue
			}
			seen[u] = struct{}{}
			options = append(options, plexServerOption{
				Name:  fmt.Sprintf("%s (%s)", dev.Name, dev.Product),
				URL:   u,
				Local: conn.Local,
			})
		}
	}

	sort.Slice(options, func(i, j int) bool {
		if options[i].Local != options[j].Local {
			return options[i].Local
		}
		if options[i].Name != options[j].Name {
			return options[i].Name < options[j].Name
		}
		return options[i].URL < options[j].URL
	})

	return options, nil
}

func (s *Server) plexListSections(ctx context.Context, plexURL, token string) ([]plexLibrarySection, error) {
	u, err := buildPlexURL(plexURL, "/library/sections", token, nil)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	s.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("plex sections returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var sectionsResp plexSectionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&sectionsResp); err != nil {
		return nil, err
	}

	sections := make([]plexLibrarySection, 0, len(sectionsResp.MediaContainer.Directories))
	for _, d := range sectionsResp.MediaContainer.Directories {
		id := extractSectionID(d.Key)
		if id == "" {
			continue
		}
		sections = append(sections, plexLibrarySection{ID: id, Title: d.Title, Type: d.Type, Locations: d.Locations})
	}

	sort.Slice(sections, func(i, j int) bool {
		if sections[i].Title != sections[j].Title {
			return sections[i].Title < sections[j].Title
		}
		return sections[i].ID < sections[j].ID
	})

	return sections, nil
}

func extractSectionID(key string) string {
	trimmed := strings.Trim(strings.TrimSpace(key), "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
}

func (s *Server) plexTriggerSectionScan(ctx context.Context, plexURL, token, sectionID, scanPath string, force bool) error {
	u, err := buildPlexURL(plexURL, "/library/sections/"+url.PathEscape(sectionID)+"/refresh", token, nil)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return err
	}
	q := parsed.Query()
	if strings.TrimSpace(scanPath) != "" {
		q.Set("path", scanPath)
	}
	if force {
		q.Set("force", "1") // Force metadata refresh for existing items
	}
	parsed.RawQuery = q.Encode()
	u = parsed.String()

	// Per OpenAPI spec: /library/sections/{sectionId}/refresh is a POST endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	s.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	webLog.Debug().
		Int("status_code", resp.StatusCode).
		Str("section_id", sectionID).
		Msg("plex scan trigger response")

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("scan endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *Server) plexSearchCount(ctx context.Context, plexURL, token, query string) (int, error) {
	u, err := buildPlexURL(plexURL, "/search", token, map[string]string{"query": query})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	s.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("search endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var searchResp plexSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return 0, err
	}
	return searchResp.MediaContainer.Size, nil
}

func (s *Server) plexSectionItemCount(ctx context.Context, plexURL, token, sectionID string) (int, error) {
	// Audiobooks in Plex music libraries are represented as albums, not artists.
	u, err := buildPlexURL(plexURL, "/library/sections/"+url.PathEscape(sectionID)+"/albums", token, map[string]string{
		"X-Plex-Container-Start": "0",
		"X-Plex-Container-Size":  "0",
	})
	if err != nil {
		return 0, err
	}

	webLog.Debug().Str("url", u).Str("section_id", sectionID).Msg("querying plex section item count")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	s.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		webLog.Debug().Err(err).Str("section_id", sectionID).Msg("plex request failed")
		return 0, err
	}
	defer resp.Body.Close()

	webLog.Debug().
		Int("status_code", resp.StatusCode).
		Str("content_type", resp.Header.Get("Content-Type")).
		Int64("content_length", resp.ContentLength).
		Str("section_id", sectionID).
		Msg("plex section items response received")

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("section items endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Read bytes first to log and inspect
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		webLog.Debug().Err(err).Str("section_id", sectionID).Msg("failed to read plex response body")
		return 0, fmt.Errorf("failed to read response body: %w", err)
	}

	webLog.Debug().
		Int("body_length", len(bodyBytes)).
		Str("body_preview", string(bodyBytes[:min(len(bodyBytes), 500)])).
		Str("section_id", sectionID).
		Msg("plex section items response body")

	var sectionResp plexSectionItemsResponse
	if err := json.Unmarshal(bodyBytes, &sectionResp); err != nil {
		webLog.Debug().Err(err).Str("section_id", sectionID).Str("body", string(bodyBytes)).Msg("failed to parse plex JSON response")
		return 0, fmt.Errorf("failed to parse JSON: %w", err)
	}

	webLog.Debug().
		Int("total_size", sectionResp.MediaContainer.TotalSize).
		Int("size", sectionResp.MediaContainer.Size).
		Str("section_id", sectionID).
		Msg("parsed plex section items")

	if sectionResp.MediaContainer.TotalSize > 0 {
		return sectionResp.MediaContainer.TotalSize, nil
	}
	return sectionResp.MediaContainer.Size, nil
}

// plexSectionLocation queries Plex for the filesystem path of a library section.
// Returns the first location path found for the section.
func (s *Server) plexSectionLocation(ctx context.Context, plexURL, token, sectionID string) (string, error) {
	u, err := buildPlexURL(plexURL, "/library/sections/"+url.PathEscape(sectionID), token, nil)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	s.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("section detail endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var detailResp plexSectionDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&detailResp); err != nil {
		return "", fmt.Errorf("failed to parse section details: %w", err)
	}

	// Return the first location path found
	for _, dir := range detailResp.MediaContainer.Directories {
		if len(dir.Locations) > 0 && strings.TrimSpace(dir.Locations[0].Path) != "" {
			return dir.Locations[0].Path, nil
		}
	}

	// Fallback: some Plex setups omit Location in section detail but include it
	// in the section list response.
	sections, listErr := s.plexListSections(ctx, plexURL, token)
	if listErr == nil {
		for _, sec := range sections {
			if sec.ID != sectionID {
				continue
			}
			for _, loc := range sec.Locations {
				if p := strings.TrimSpace(loc.Path); p != "" {
					return p, nil
				}
			}
			break
		}
	}

	if listErr != nil {
		return "", fmt.Errorf("no location path found for section %s (detail endpoint had no Location; list fallback failed: %v)", sectionID, listErr)
	}
	return "", fmt.Errorf("no location path found for section %s; Plex may not expose filesystem paths for this connection", sectionID)
}

func (s *Server) addPlexHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", plexProduct)
	req.Header.Set("X-Plex-Client-Identifier", s.plexClientID())
	req.Header.Set("X-Plex-Device-Name", "Audible Plex Downloader Web")
	req.Header.Set("X-Plex-Platform", plexPlatform)
	req.Header.Set("X-Plex-Version", "1.0")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("X-Plex-Token", token)
	}
}

func buildPlexURL(baseURL, path, token string, extraQuery map[string]string) (string, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Plex URL: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	q := u.Query()
	q.Set("X-Plex-Token", token)
	for k, v := range extraQuery {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// plexGetActivities queries the Plex /activities endpoint to get active operations.
func (s *Server) plexGetActivities(ctx context.Context, plexURL, token string) ([]plexActivity, error) {
	u, err := buildPlexURL(plexURL, "/activities", token, nil)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	s.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("activities endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var activitiesResp plexActivitiesResponse
	if err := json.NewDecoder(resp.Body).Decode(&activitiesResp); err != nil {
		return nil, fmt.Errorf("failed to parse activities response: %w", err)
	}

	return activitiesResp.MediaContainer.Activities, nil
}

// PlexItem represents an item in the Plex library.
type PlexItem struct {
	RatingKey   string `json:"rating_key"`
	Title       string `json:"title"`
	ParentTitle string `json:"parent_title,omitempty"` // Artist/Author name
	Year        int    `json:"year,omitempty"`
	AddedAt     int64  `json:"added_at,omitempty"`
	GUID        string `json:"guid,omitempty"`
}

// plexAlbumsResponse wraps Plex /library/sections/{id}/albums response with details.
type plexAlbumsResponse struct {
	MediaContainer plexAlbumsContainer `json:"MediaContainer"`
}

type plexAlbumsContainer struct {
	Size      int           `json:"size"`
	TotalSize int           `json:"totalSize"`
	Metadata  []plexAlbumMD `json:"Metadata"`
}

type plexAlbumMD struct {
	RatingKey   string `json:"ratingKey"`
	Title       string `json:"title"`
	ParentTitle string `json:"parentTitle"` // Artist/Author
	Year        int    `json:"year"`
	AddedAt     int64  `json:"addedAt"`
	GUID        string `json:"guid"`
}

// plexListSectionItems fetches all items (albums) in a Plex section with title details.
func (s *Server) plexListSectionItems(ctx context.Context, plexURL, token, sectionID string, limit int) ([]PlexItem, error) {
	if limit <= 0 {
		limit = 10000 // reasonable max for audiobook libraries
	}

	u, err := buildPlexURL(plexURL, "/library/sections/"+url.PathEscape(sectionID)+"/albums", token, map[string]string{
		"X-Plex-Container-Start": "0",
		"X-Plex-Container-Size":  strconv.Itoa(limit),
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	s.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("albums endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var albumsResp plexAlbumsResponse
	if err := json.NewDecoder(resp.Body).Decode(&albumsResp); err != nil {
		return nil, fmt.Errorf("failed to parse albums response: %w", err)
	}

	items := make([]PlexItem, 0, len(albumsResp.MediaContainer.Metadata))
	for _, album := range albumsResp.MediaContainer.Metadata {
		items = append(items, PlexItem{
			RatingKey:   album.RatingKey,
			Title:       album.Title,
			ParentTitle: album.ParentTitle,
			Year:        album.Year,
			AddedAt:     album.AddedAt,
			GUID:        album.GUID,
		})
	}

	return items, nil
}
