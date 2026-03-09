package library

import (
	"context"
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

		// If Plex path is configured, translate local path to Plex path
		scanPath := localScanPath
		if plexPath, err := dm.db.GetSetting(ctx, "plex_section_path"); err == nil && strings.TrimSpace(plexPath) != "" {
			plexPath = strings.TrimSpace(plexPath)
			libRoot := strings.TrimSpace(dm.libraryDir)
			if libRoot != "" && plexPath != libRoot {
				// Replace the local library root with the Plex library root in the scan path
				if strings.HasPrefix(localScanPath, libRoot) {
					rel := strings.TrimPrefix(localScanPath, libRoot)
					rel = strings.TrimPrefix(rel, string(filepath.Separator))
					scanPath = filepath.Join(plexPath, rel)
					dlLog.Debug().
						Str("local_path", localScanPath).
						Str("plex_path", scanPath).
						Msg("translated scan path to plex location")
				}
			}
		}

		if err := dm.triggerPlexSectionScan(ctx, plexURL, plexToken, sectionID, scanPath); err != nil {
			dlLog.Warn().Err(err).Str("scan_path", scanPath).Msg("plex scan trigger failed")
			return
		}

		dlLog.Info().Str("scan_path", scanPath).Str("section_id", sectionID).Msg("plex scan triggered for completed book")
	}()
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
