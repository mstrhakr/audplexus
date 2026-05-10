package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/mstrhakr/audplexus/internal/database"
)

// destinationView is the per-card view-model rendered by settings.html.
// Sensitive fields (api keys, plex tokens) are intentionally omitted.
type destinationView struct {
	ID          string
	DisplayName string
	Type        string
	TypeLabel   string
	Enabled     bool
	URL         string
	LibraryID   string

	// Health is one of: "healthy" | "failed" | "never" | "not_configured".
	Health    string
	LastError string
}

// destinationsForView reads enabled+disabled destinations for the Settings
// page card list. Sensitive fields are stripped server-side so they never
// reach the template.
func (s *Server) destinationsForView(ctx context.Context) []destinationView {
	rows, err := s.db.ListLibraryDestinations(ctx)
	if err != nil {
		webLog.Warn().Err(err).Msg("destinations: list failed")
		return nil
	}
	out := make([]destinationView, 0, len(rows))
	for _, r := range rows {
		out = append(out, destinationView{
			ID:          r.ID,
			DisplayName: r.DisplayName,
			Type:        string(r.Type),
			TypeLabel:   destinationTypeLabel(r.Type),
			Enabled:     r.Enabled,
			URL:         r.URL,
			LibraryID:   r.LibraryID,
			Health:      summarizeHealth(&r),
			LastError:   r.LastHealthCheckErr,
		})
	}
	return out
}

func destinationTypeLabel(t database.LibraryDestinationType) string {
	switch t {
	case database.LibraryDestinationTypePlex:
		return "Plex"
	case database.LibraryDestinationTypeEmby:
		return "Emby"
	case database.LibraryDestinationTypeJellyfin:
		return "Jellyfin"
	case database.LibraryDestinationTypeABS:
		return "Audiobookshelf"
	default:
		return string(t)
	}
}

func summarizeHealth(d *database.LibraryDestination) string {
	if !destinationConfigured(d) {
		return "not_configured"
	}
	if d.LastHealthCheckAt == nil || d.LastHealthCheckOK == nil {
		return "never"
	}
	if *d.LastHealthCheckOK {
		return "healthy"
	}
	return "failed"
}

func destinationConfigured(d *database.LibraryDestination) bool {
	if strings.TrimSpace(d.URL) == "" {
		return false
	}
	switch d.Type {
	case database.LibraryDestinationTypePlex:
		return strings.TrimSpace(d.PlexToken) != "" && strings.TrimSpace(d.PlexSectionID) != ""
	case database.LibraryDestinationTypeEmby, database.LibraryDestinationTypeJellyfin, database.LibraryDestinationTypeABS:
		return strings.TrimSpace(d.APIKey) != "" && strings.TrimSpace(d.LibraryID) != ""
	}
	return false
}

// handleDestinationsNewPicker renders the type-picker page (step 1 of 2
// in the add flow). Two-page server flow rather than a JS-toggled single
// form — simpler, validation cleaner, no JS dependency.
func (s *Server) handleDestinationsNewPicker(c *gin.Context) {
	data := s.authBaseData(c.Request.Context())
	data["Page"] = "destinations_new_picker"
	c.HTML(http.StatusOK, "destinations_new.html", data)
}

// handleDestinationsNewForm renders the type-specific config form (step 2)
// when the user submits the type picker.
func (s *Server) handleDestinationsNewForm(c *gin.Context) {
	t := strings.ToLower(strings.TrimSpace(c.PostForm("type")))
	if !validDestinationType(t) {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Pick a destination type."})
		return
	}
	data := s.authBaseData(c.Request.Context())
	data["Page"] = "destinations_new_form"
	data["DestType"] = t
	data["DestTypeLabel"] = destinationTypeLabel(database.LibraryDestinationType(t))
	c.HTML(http.StatusOK, "destinations_form.html", data)
}

// handleDestinationsCreate persists a new destination after the form submit.
func (s *Server) handleDestinationsCreate(c *gin.Context) {
	d, err := s.destinationFromForm(c, "")
	if err != nil {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": err.Error()})
		return
	}
	d.ID = uuid.NewString()
	d.Enabled = true
	d.CreatedAt = time.Now().UTC()
	if err := s.db.CreateLibraryDestination(c.Request.Context(), d); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Could not create destination: " + err.Error()})
		return
	}
	c.Redirect(http.StatusSeeOther, "/settings#library-destinations")
}

// handleDestinationEditForm renders the per-destination edit form. Sensitive
// values (PlexToken, APIKey) are NOT prefilled into the template — leaving
// the field blank means "keep existing"; entering a new value rotates.
func (s *Server) handleDestinationEditForm(c *gin.Context) {
	id := c.Param("id")
	row, err := s.db.GetLibraryDestination(c.Request.Context(), id)
	if err != nil || row == nil {
		s.renderAuthPage(c, http.StatusNotFound, gin.H{"Error": "Destination not found."})
		return
	}
	data := s.authBaseData(c.Request.Context())
	data["Page"] = "destinations_edit"
	data["DestType"] = string(row.Type)
	data["DestTypeLabel"] = destinationTypeLabel(row.Type)
	data["Dest"] = row // template uses Dest.DisplayName, .URL, .LibraryID, .PlexSectionID
	c.HTML(http.StatusOK, "destinations_form.html", data)
}

// handleDestinationUpdate persists an edit. Sensitive fields (PlexToken,
// APIKey) are only updated when the form provides a non-empty value.
func (s *Server) handleDestinationUpdate(c *gin.Context) {
	id := c.Param("id")
	existing, err := s.db.GetLibraryDestination(c.Request.Context(), id)
	if err != nil || existing == nil {
		s.renderAuthPage(c, http.StatusNotFound, gin.H{"Error": "Destination not found."})
		return
	}
	updated, err := s.destinationFromForm(c, string(existing.Type))
	if err != nil {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": err.Error()})
		return
	}

	// Carry over secrets when not provided in the form.
	if strings.TrimSpace(updated.PlexToken) == "" {
		updated.PlexToken = existing.PlexToken
	}
	if strings.TrimSpace(updated.APIKey) == "" {
		updated.APIKey = existing.APIKey
	}
	updated.ID = existing.ID
	updated.Type = existing.Type
	updated.Enabled = existing.Enabled
	updated.CreatedAt = existing.CreatedAt
	updated.LastHealthCheckAt = existing.LastHealthCheckAt
	updated.LastHealthCheckOK = existing.LastHealthCheckOK
	updated.LastHealthCheckErr = existing.LastHealthCheckErr

	if err := s.db.UpdateLibraryDestination(c.Request.Context(), updated); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Could not save: " + err.Error()})
		return
	}
	c.Redirect(http.StatusSeeOther, "/settings#library-destinations")
}

// handleDestinationToggle flips the enabled flag.
func (s *Server) handleDestinationToggle(c *gin.Context) {
	id := c.Param("id")
	d, err := s.db.GetLibraryDestination(c.Request.Context(), id)
	if err != nil || d == nil {
		s.renderAuthPage(c, http.StatusNotFound, gin.H{"Error": "Destination not found."})
		return
	}
	d.Enabled = !d.Enabled
	if err := s.db.UpdateLibraryDestination(c.Request.Context(), d); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Could not toggle: " + err.Error()})
		return
	}
	c.Redirect(http.StatusSeeOther, "/settings#library-destinations")
}

// handleDestinationDelete is the only delete endpoint — POST-only, no
// safe GET counterpart (destructive actions must not be GETs per RFC 9110
// and WCAG semantics for destructive controls).
//
// Two-state behavior on the same path keeps the URL minimal:
//   - first POST (no `confirm` field) renders the confirmation page
//   - second POST (confirm=1, set by the confirmation page's submit) deletes
func (s *Server) handleDestinationDelete(c *gin.Context) {
	id := c.Param("id")
	d, err := s.db.GetLibraryDestination(c.Request.Context(), id)
	if err != nil || d == nil {
		s.renderAuthPage(c, http.StatusNotFound, gin.H{"Error": "Destination not found."})
		return
	}

	if c.PostForm("confirm") != "1" {
		// First POST: render the confirmation page.
		data := s.authBaseData(c.Request.Context())
		data["Page"] = "destinations_delete"
		data["Dest"] = destinationView{
			ID:          d.ID,
			DisplayName: d.DisplayName,
			Type:        string(d.Type),
			TypeLabel:   destinationTypeLabel(d.Type),
		}
		c.HTML(http.StatusOK, "destinations_delete.html", data)
		return
	}

	// Second POST with confirm=1: actually delete.
	if err := s.db.DeleteLibraryDestination(c.Request.Context(), id); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Could not delete: " + err.Error()})
		return
	}
	c.Redirect(http.StatusSeeOther, "/settings#library-destinations")
}

func (s *Server) destinationFromForm(c *gin.Context, existingType string) (*database.LibraryDestination, error) {
	t := existingType
	if t == "" {
		t = strings.ToLower(strings.TrimSpace(c.PostForm("type")))
	}
	if !validDestinationType(t) {
		return nil, errors.New("invalid destination type")
	}

	displayName := strings.TrimSpace(c.PostForm("display_name"))
	if displayName == "" {
		return nil, errors.New("display name is required")
	}

	d := &database.LibraryDestination{
		Type:            database.LibraryDestinationType(t),
		DisplayName:     displayName,
		URL:             strings.TrimSpace(c.PostForm("url")),
		AudiobookPath:   strings.TrimSpace(c.PostForm("audiobook_path")),
		DestinationPath: strings.TrimSpace(c.PostForm("destination_path")),
	}
	if d.URL == "" {
		return nil, errors.New("URL is required")
	}

	switch d.Type {
	case database.LibraryDestinationTypePlex:
		d.PlexToken = strings.TrimSpace(c.PostForm("plex_token"))
		d.PlexSectionID = strings.TrimSpace(c.PostForm("plex_section_id"))
		if d.PlexSectionID == "" {
			return nil, errors.New("Plex section ID is required")
		}
		// PlexToken may be empty on edit — caller carries it over.
	case database.LibraryDestinationTypeEmby, database.LibraryDestinationTypeJellyfin, database.LibraryDestinationTypeABS:
		d.APIKey = strings.TrimSpace(c.PostForm("api_key"))
		d.LibraryID = strings.TrimSpace(c.PostForm("library_id"))
		if d.LibraryID == "" {
			return nil, errors.New("Library ID is required")
		}
		// APIKey may be empty on edit — caller carries it over.
	}
	return d, nil
}

func validDestinationType(t string) bool {
	switch database.LibraryDestinationType(t) {
	case database.LibraryDestinationTypePlex,
		database.LibraryDestinationTypeEmby,
		database.LibraryDestinationTypeJellyfin,
		database.LibraryDestinationTypeABS:
		return true
	}
	return false
}
