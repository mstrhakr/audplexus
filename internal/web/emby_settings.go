package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
)

// embySettings reads the Emby connection settings (DB first, env fallback).
func (s *Server) embySettings(ctx context.Context) (url, apiKey, libraryID string) {
	url, _ = s.db.GetSetting(ctx, "emby_url")
	apiKey, _ = s.db.GetSetting(ctx, "emby_api_key")
	libraryID, _ = s.db.GetSetting(ctx, "emby_library_id")
	return strings.TrimSpace(url), strings.TrimSpace(apiKey), strings.TrimSpace(libraryID)
}

// handleEmbyConfigure persists the user-submitted Emby connection settings
// and switches the active media server backend to Emby.
func (s *Server) handleEmbyConfigure(c *gin.Context) {
	ctx := c.Request.Context()

	url := strings.TrimSpace(c.PostForm("emby_url"))
	apiKey := strings.TrimSpace(c.PostForm("emby_api_key"))
	libraryID := strings.TrimSpace(c.PostForm("emby_library_id"))

	if url == "" || apiKey == "" || libraryID == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Emby URL, API key, and library ID are all required."})
		return
	}

	if err := s.db.SetSetting(ctx, "emby_url", url); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to save Emby URL: " + err.Error()})
		return
	}
	if err := s.db.SetSetting(ctx, "emby_api_key", apiKey); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to save Emby API key: " + err.Error()})
		return
	}
	if err := s.db.SetSetting(ctx, "emby_library_id", libraryID); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to save Emby library ID: " + err.Error()})
		return
	}
	// Clear any cached library path so it gets refetched against the new connection.
	_ = s.db.SetSetting(ctx, "emby_library_path", "")

	if err := s.db.SetSetting(ctx, mediaserver.SettingKeyType, string(mediaserver.TypeEmby)); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to switch media server type to Emby: " + err.Error()})
		return
	}

	s.renderAuthPage(c, http.StatusOK, gin.H{
		"Success": "Emby settings saved. Restart Audplexus to switch the active media server backend to Emby.",
	})
}

// handleEmbyScan triggers a one-shot library refresh against Emby. Useful
// for verifying connection settings from the UI.
func (s *Server) handleEmbyScan(c *gin.Context) {
	ctx := c.Request.Context()

	backend := s.downloads.MediaServer()
	if backend == nil || backend.Name() != string(mediaserver.TypeEmby) {
		// Construct a one-shot Emby backend just for the test scan so we can
		// verify settings even before the active backend is switched.
		backend = mediaserver.NewEmby(s.db, s.audnexus, s.audiobooksPath)
	}

	if !backend.Configured(ctx) {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Emby is not fully configured. Set URL, API key, and library ID first."})
		return
	}

	count, err := backend.TriggerLibraryScan(ctx)
	if err != nil {
		s.renderAuthPage(c, http.StatusBadGateway, gin.H{"Error": "Emby scan failed: " + err.Error()})
		return
	}
	s.renderAuthPage(c, http.StatusOK, gin.H{
		"Success": fmt.Sprintf("Emby library refresh triggered. Library currently reports %d items.", count),
	})
}

// handleMediaServerSelect switches the active backend type.
func (s *Server) handleMediaServerSelect(c *gin.Context) {
	t := strings.ToLower(strings.TrimSpace(c.PostForm("media_server_type")))
	switch mediaserver.Type(t) {
	case mediaserver.TypePlex, mediaserver.TypeEmby:
	default:
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Unknown media server type. Choose 'plex' or 'emby'."})
		return
	}
	if err := s.db.SetSetting(c.Request.Context(), mediaserver.SettingKeyType, t); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to set media server type: " + err.Error()})
		return
	}
	s.renderAuthPage(c, http.StatusOK, gin.H{
		"Success": "Media server type set to " + t + ". Restart Audplexus for the change to take effect.",
	})
}

