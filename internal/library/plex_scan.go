package library

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	plexProduct = "Audible Plex Downloader"
)

func buildPlexClientID() string {
	hostname, _ := os.Hostname()
	hostname = strings.TrimSpace(strings.ToLower(hostname))
	if hostname == "" {
		hostname = "local"
	}
	return "apd-" + hostname
}

func (dm *DownloadManager) triggerPlexScanForBook(finalPath string) {
	if strings.TrimSpace(finalPath) == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		plexURL, plexToken, sectionID := dm.getPlexScanSettings(ctx)
		if plexURL == "" || plexToken == "" || sectionID == "" {
			return
		}

		localScanPath := filepath.Dir(finalPath)

		// Resolve a Plex-visible path for a targeted scan.
		scanPath, ok := dm.resolvePlexScanPath(ctx, plexURL, plexToken, sectionID, localScanPath)
		if !ok {
			dlLog.Warn().
				Str("local_path", localScanPath).
				Str("section_id", sectionID).
				Msg("skipping per-book plex scan because section path is unavailable")
			return
		}

		if err := dm.triggerPlexSectionScan(ctx, plexURL, plexToken, sectionID, scanPath); err != nil {
			dlLog.Warn().Err(err).Str("scan_path", scanPath).Msg("plex scan trigger failed")
			return
		}

		dlLog.Info().Str("scan_path", scanPath).Str("section_id", sectionID).Msg("plex scan triggered for completed book")
	}()
}

type plexSectionDetailResponse struct {
	Directories []plexSectionDetailDirectory `xml:"Directory"`
}

type plexSectionDetailDirectory struct {
	Locations []plexLocation `xml:"Location"`
}

type plexLocation struct {
	Path string `xml:"path,attr"`
}

func (dm *DownloadManager) resolvePlexScanPath(ctx context.Context, plexURL, plexToken, sectionID, localScanPath string) (string, bool) {
	plexPath, _ := dm.db.GetSetting(ctx, "plex_section_path")
	plexPath = strings.TrimSpace(plexPath)

	if plexPath == "" {
		fetched, err := dm.fetchPlexSectionPath(ctx, plexURL, plexToken, sectionID)
		if err != nil {
			dlLog.Warn().Err(err).Str("section_id", sectionID).Msg("failed to fetch plex section path")
		} else if fetched != "" {
			plexPath = fetched
			if err := dm.db.SetSetting(ctx, "plex_section_path", fetched); err != nil {
				dlLog.Warn().Err(err).Str("plex_section_path", fetched).Msg("failed to cache plex section path")
			}
		}
	}

	if plexPath == "" {
		return "", false
	}

	libRoot := strings.TrimSpace(dm.libraryDir)
	scanPath, ok := translateScanPath(localScanPath, libRoot, plexPath)
	if !ok {
		dlLog.Warn().
			Str("local_path", localScanPath).
			Str("library_root", libRoot).
			Str("plex_section_path", plexPath).
			Msg("unable to translate scan path; skipping per-book plex scan")
		return "", false
	}

	if scanPath != localScanPath {
		dlLog.Debug().
			Str("local_path", localScanPath).
			Str("plex_path", scanPath).
			Msg("translated scan path to plex location")
	}

	return scanPath, true
}

func (dm *DownloadManager) fetchPlexSectionPath(ctx context.Context, plexURL, token, sectionID string) (string, error) {
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
	dm.addPlexHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("plex section detail endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Read and log the response for debugging
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read section detail response: %w", err)
	}

	dlLog.Debug().
		Str("section_id", sectionID).
		Str("response_body", string(body)).
		Msg("plex section detail response")

	var detailResp plexSectionDetailResponse
	if err := xml.Unmarshal(body, &detailResp); err != nil {
		return "", fmt.Errorf("failed to parse section details: %w", err)
	}

	dlLog.Debug().
		Str("section_id", sectionID).
		Int("directories_count", len(detailResp.Directories)).
		Msg("parsed plex section detail response")

	for i, dir := range detailResp.Directories {
		dlLog.Debug().
			Str("section_id", sectionID).
			Int("directory_index", i).
			Int("locations_count", len(dir.Locations)).
			Msg("checking directory for locations")
		for j, loc := range dir.Locations {
			dlLog.Debug().
				Str("section_id", sectionID).
				Int("directory_index", i).
				Int("location_index", j).
				Str("path", loc.Path).
				Msg("found location path")
			if p := strings.TrimSpace(loc.Path); p != "" {
				return p, nil
			}
		}
	}

	return "", fmt.Errorf("no location path found for section %s", sectionID)
}

func translateScanPath(localScanPath, localLibraryRoot, plexLibraryRoot string) (string, bool) {
	localScanPath = strings.TrimSpace(localScanPath)
	localLibraryRoot = strings.TrimSpace(localLibraryRoot)
	plexLibraryRoot = strings.TrimSpace(plexLibraryRoot)

	if localScanPath == "" || plexLibraryRoot == "" {
		return "", false
	}

	if localLibraryRoot == "" {
		if pathsEquivalent(localScanPath, plexLibraryRoot) {
			return plexLibraryRoot, true
		}
		return "", false
	}

	rel, err := filepath.Rel(localLibraryRoot, localScanPath)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}

	if rel == "." {
		return plexLibraryRoot, true
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if strings.Contains(plexLibraryRoot, `\`) && !strings.Contains(plexLibraryRoot, "/") {
		return strings.TrimRight(plexLibraryRoot, `\`) + `\` + strings.Join(parts, `\`), true
	}
	return strings.TrimRight(plexLibraryRoot, "/") + "/" + strings.Join(parts, "/"), true
}

func pathsEquivalent(a, b string) bool {
	a = strings.TrimRight(strings.TrimSpace(a), `/\`)
	b = strings.TrimRight(strings.TrimSpace(b), `/\`)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

func (dm *DownloadManager) getPlexScanSettings(ctx context.Context) (string, string, string) {
	plexURL, _ := dm.db.GetSetting(ctx, "plex_url")
	plexToken, _ := dm.db.GetSetting(ctx, "plex_token")
	sectionID, _ := dm.db.GetSetting(ctx, "plex_section_id")

	if strings.TrimSpace(plexURL) == "" {
		plexURL = strings.TrimSpace(os.Getenv("PLEX_URL"))
	}
	if strings.TrimSpace(plexToken) == "" {
		plexToken = strings.TrimSpace(os.Getenv("PLEX_TOKEN"))
	}
	if strings.TrimSpace(sectionID) == "" {
		sectionID = strings.TrimSpace(os.Getenv("PLEX_SECTION_ID"))
	}

	return strings.TrimSpace(plexURL), strings.TrimSpace(plexToken), strings.TrimSpace(sectionID)
}

func (dm *DownloadManager) triggerPlexSectionScan(ctx context.Context, plexURL, token, sectionID, scanPath string) error {
	u, err := buildPlexSectionScanURL(plexURL, token, sectionID, scanPath)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return fmt.Errorf("plex scan endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

func buildPlexSectionScanURL(plexURL, token, sectionID, scanPath string) (string, error) {
	base, err := url.Parse(strings.TrimRight(plexURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Plex URL: %w", err)
	}

	base.Path = strings.TrimRight(base.Path, "/") + "/library/sections/" + url.PathEscape(sectionID) + "/refresh"
	q := base.Query()
	q.Set("X-Plex-Token", token)
	if strings.TrimSpace(scanPath) != "" {
		q.Set("path", scanPath)
	}
	base.RawQuery = q.Encode()

	return base.String(), nil
}

func (dm *DownloadManager) addPlexHeaders(req *http.Request, token string) {
	req.Header.Set("X-Plex-Product", plexProduct)
	req.Header.Set("X-Plex-Client-Identifier", dm.plexClientID)
	req.Header.Set("X-Plex-Device-Name", "Audible Plex Downloader")
	req.Header.Set("X-Plex-Platform", "Go")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("X-Plex-Token", token)
	}
}
