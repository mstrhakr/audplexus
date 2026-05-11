package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
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

// handleDestinationTest performs a live health check against the
// configured server using the form-submitted (or stored) credentials.
// Returns an HTML fragment for HTMX swap into #test-result with
// role="status" aria-live="polite" so SR users hear the outcome.
//
// Two routes hit this handler:
//   POST /destinations/test         — form values, no row persisted
//   POST /destinations/:id/test     — saved row, secrets carried over;
//                                     on test outcome the row's
//                                     last_health_check_* columns are
//                                     updated so the dashboard's
//                                     "Healthy/Failed" badge reflects
//                                     the most recent test.
func (s *Server) handleDestinationTest(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	d, err := s.destinationForTest(c)
	if err != nil {
		renderTestResult(c, false, "", err.Error())
		return
	}

	backend, err := s.buildDestinationBackend(d)
	if err != nil {
		s.recordDestinationHealth(c.Request.Context(), c.Param("id"), false, err.Error())
		renderTestResult(c, false, "", "Could not construct backend: "+err.Error())
		return
	}

	count, err := backend.LibraryItemCount(ctx)
	if err != nil {
		s.recordDestinationHealth(c.Request.Context(), c.Param("id"), false, err.Error())
		renderTestResult(c, false, "", "Connection or auth failed: "+err.Error())
		return
	}
	s.recordDestinationHealth(c.Request.Context(), c.Param("id"), true, "")
	renderTestResult(c, true, fmt.Sprintf("Library reports %d item(s).", count), "")
}

// recordDestinationHealth updates a destination row's
// last_health_check_* columns. No-op when destID is empty (the
// "test before save" path on /destinations/test has no row yet).
//
// Called from:
//   - handleDestinationTest after a Test Connection click
//   - DestinationManager.ReconcileAll (per-destination outcome)
//   - DestinationManager.TriggerScanAll (per-destination outcome)
func (s *Server) recordDestinationHealth(ctx context.Context, destID string, ok bool, errMsg string) {
	if destID == "" {
		return
	}
	row, err := s.db.GetLibraryDestination(ctx, destID)
	if err != nil || row == nil {
		return
	}
	now := time.Now().UTC()
	row.LastHealthCheckAt = &now
	row.LastHealthCheckOK = &ok
	row.LastHealthCheckErr = errMsg
	if err := s.db.UpdateLibraryDestination(ctx, row); err != nil {
		webLog.Debug().Err(err).Str("destination_id", destID).Msg("recordDestinationHealth: update failed")
	}
}

// destinationForTest builds a *LibraryDestination from form values for
// the new-destination test path, OR loads the saved row for the existing-
// destination test path. Sensitive empty fields on the saved-row path are
// carried over so the user doesn't have to retype the API key just to test.
func (s *Server) destinationForTest(c *gin.Context) (*database.LibraryDestination, error) {
	id := c.Param("id")
	if id == "" {
		// New-destination test: build from form values directly.
		return s.destinationFromForm(c, "")
	}
	existing, err := s.db.GetLibraryDestination(c.Request.Context(), id)
	if err != nil || existing == nil {
		return nil, fmt.Errorf("destination not found")
	}
	formed, err := s.destinationFromForm(c, string(existing.Type))
	if err != nil {
		// Form values absent or invalid — test the saved row as-is.
		return existing, nil
	}
	if strings.TrimSpace(formed.PlexToken) == "" {
		formed.PlexToken = existing.PlexToken
	}
	if strings.TrimSpace(formed.APIKey) == "" {
		formed.APIKey = existing.APIKey
	}
	return formed, nil
}

// handleDestinationsDiscoverABS calls GET /api/libraries against the
// posted URL+token, then renders an HTML fragment containing a <select>
// of libraries. The destination form's JS swaps the picked option's
// value into the library_id input — turning "paste this UUID from a
// curl command" UX into a dropdown picker.
//
// Same fragment shape as handleDestinationTest: HTMX-targeted, no JSON.
// Errors render an inline failure box so screen-reader users still
// hear the outcome via aria-live="polite".
func (s *Server) handleDestinationsDiscoverABS(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	urlStr := strings.TrimSpace(c.PostForm("url"))
	apiKey, err := s.resolveAPIKeyFromForm(c)
	if err != nil {
		renderDiscoverResult(c, nil, err.Error())
		return
	}
	if urlStr == "" {
		renderDiscoverResult(c, nil, "Enter the URL before discovering libraries.")
		return
	}
	if err := validateRemoteURL(urlStr); err != nil {
		renderDiscoverResult(c, nil, err.Error())
		return
	}

	libs, listErr := mediaserver.ListLibraries(ctx, urlStr, apiKey)
	if listErr != nil {
		renderDiscoverResult(c, nil, "Could not list libraries: "+listErr.Error())
		return
	}

	renderDiscoverResult(c, libs, "")
}

// handleDestinationsDiscoverEmby calls /emby/Library/MediaFolders and
// renders a <select> picker for audiobook libraries. Same shape as the
// ABS discover handler; filter is CollectionType="audiobooks".
//
// Two routes hit this handler:
//   POST /destinations/discover/emby           — form values
//   POST /destinations/:id/discover/emby       — saved row, api_key carried
func (s *Server) handleDestinationsDiscoverEmby(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	urlStr := strings.TrimSpace(c.PostForm("url"))
	apiKey, err := s.resolveAPIKeyFromForm(c)
	if err != nil {
		renderEmbyLikeDiscover(c, nil, "audiobooks", "Emby", "emby_discover_picker", err.Error())
		return
	}
	if urlStr == "" {
		renderEmbyLikeDiscover(c, nil, "audiobooks", "Emby", "emby_discover_picker", "Enter the Emby URL first.")
		return
	}
	if err := validateRemoteURL(urlStr); err != nil {
		renderEmbyLikeDiscover(c, nil, "audiobooks", "Emby", "emby_discover_picker", err.Error())
		return
	}

	libs, err := mediaserver.EmbyListLibraries(ctx, urlStr, apiKey)
	if err != nil {
		renderEmbyLikeDiscover(c, nil, "audiobooks", "Emby", "emby_discover_picker", "Could not list libraries: "+err.Error())
		return
	}
	// Adapt EmbyLibrary -> generic shape.
	rows := make([]mediaServerLibrary, 0, len(libs))
	for _, l := range libs {
		rows = append(rows, mediaServerLibrary{ID: l.ID, Name: l.Name, Kind: l.CollectionType, Path: l.Path})
	}
	renderEmbyLikeDiscover(c, rows, "audiobooks", "Emby", "emby_discover_picker", "")
}

// handleDestinationsDiscoverJellyfin calls /Library/VirtualFolders and
// renders a <select> picker for audiobook libraries. Mirror of the Emby
// discover handler; filter is CollectionType="books".
//
// Two routes hit this handler:
//   POST /destinations/discover/jellyfin           — form values
//   POST /destinations/:id/discover/jellyfin       — saved row, api_key carried
func (s *Server) handleDestinationsDiscoverJellyfin(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	urlStr := strings.TrimSpace(c.PostForm("url"))
	apiKey, err := s.resolveAPIKeyFromForm(c)
	if err != nil {
		renderEmbyLikeDiscover(c, nil, "books", "Jellyfin", "jellyfin_discover_picker", err.Error())
		return
	}
	if urlStr == "" {
		renderEmbyLikeDiscover(c, nil, "books", "Jellyfin", "jellyfin_discover_picker", "Enter the Jellyfin URL first.")
		return
	}
	if err := validateRemoteURL(urlStr); err != nil {
		renderEmbyLikeDiscover(c, nil, "books", "Jellyfin", "jellyfin_discover_picker", err.Error())
		return
	}

	libs, err := mediaserver.JellyfinListLibraries(ctx, urlStr, apiKey)
	if err != nil {
		renderEmbyLikeDiscover(c, nil, "books", "Jellyfin", "jellyfin_discover_picker", "Could not list libraries: "+err.Error())
		return
	}
	rows := make([]mediaServerLibrary, 0, len(libs))
	for _, l := range libs {
		rows = append(rows, mediaServerLibrary{ID: l.ID, Name: l.Name, Kind: l.CollectionType, Path: l.Path})
	}
	renderEmbyLikeDiscover(c, rows, "books", "Jellyfin", "jellyfin_discover_picker", "")
}

// mediaServerLibrary is the generic row shape consumed by renderEmbyLikeDiscover.
// Used to share rendering between Emby and Jellyfin (and any future
// backend whose libraries have an id, a name, a kind, and an optional
// on-disk path).
type mediaServerLibrary struct {
	ID   string
	Name string
	Kind string // CollectionType — "audiobooks" for Emby, "books" for Jellyfin
	Path string
}

// renderEmbyLikeDiscover emits the picker fragment for any backend whose
// libraries map to mediaServerLibrary. wantKind names the CollectionType
// value that means "audiobook library" for this backend ("audiobooks"
// for Emby, "books" for Jellyfin). When no library matches wantKind the
// picker still renders all libraries so a misconfigured server doesn't
// strand the user. errMsg is rendered as an inline failure box when
// non-empty.
func renderEmbyLikeDiscover(c *gin.Context, libs []mediaServerLibrary, wantKind, backendLabel, pickerID, errMsg string) {
	if errMsg != "" {
		writeSensitiveHTML(c,
			`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" role="status" aria-live="polite">`+
				`<strong>Failed.</strong> `+htmlEscape(errMsg)+`</div>`)
		return
	}
	if len(libs) == 0 {
		writeSensitiveHTML(c,
			`<div class="info-box" style="border-color:var(--warning);margin:.5rem 0" role="status" aria-live="polite">`+
				`<strong>No libraries visible to this API key.</strong> `+
				`Check that the key has access to at least one library on this `+htmlEscape(backendLabel)+` server.</div>`)
		return
	}

	matching := make([]mediaServerLibrary, 0, len(libs))
	for _, l := range libs {
		if strings.EqualFold(l.Kind, wantKind) {
			matching = append(matching, l)
		}
	}

	// If no library has the expected CollectionType, show everything so
	// the user can still pick a misconfigured-but-real audiobook library.
	// Surface the mismatch as a warning so it's not silent.
	showLibs := matching
	warning := ""
	if len(matching) == 0 {
		showLibs = libs
		warning = "No libraries on this server have CollectionType=" + wantKind + "; showing all libraries so you can still pick one."
	}

	var sb strings.Builder
	banner := `<div class="info-box" style="border-color:var(--success);margin:.5rem 0" role="status" aria-live="polite">`
	if warning != "" {
		banner = `<div class="info-box" style="border-color:var(--warning);margin:.5rem 0" role="status" aria-live="polite">`
	}
	sb.WriteString(banner)
	sb.WriteString(`<strong>Connected.</strong> Found `)
	sb.WriteString(fmt.Sprintf("%d ", len(showLibs)))
	if len(showLibs) == 1 {
		sb.WriteString(`library`)
	} else {
		sb.WriteString(`libraries`)
	}
	if warning != "" {
		sb.WriteString(`. `)
		sb.WriteString(htmlEscape(warning))
	} else {
		sb.WriteString(`. Pick one — its ID will fill the Library ID field.`)
	}
	sb.WriteString(`</div>`)
	sb.WriteString(`<label for="`)
	sb.WriteString(htmlEscape(pickerID))
	sb.WriteString(`" class="visually-hidden">Discovered libraries</label>`)
	sb.WriteString(`<select id="`)
	sb.WriteString(htmlEscape(pickerID))
	sb.WriteString(`" class="form-control" style="margin:.25rem 0 .5rem 0" `)
	sb.WriteString(`onchange="document.getElementById('library_id').value=this.value">`)
	sb.WriteString(`<option value="">— pick a library —</option>`)
	for _, l := range showLibs {
		label := l.Name
		if l.Kind != "" {
			label += " (" + l.Kind + ")"
		}
		if l.Path != "" {
			label += " — " + l.Path
		}
		sb.WriteString(`<option value="`)
		sb.WriteString(htmlEscape(l.ID))
		sb.WriteString(`">`)
		sb.WriteString(htmlEscape(label))
		sb.WriteString(`</option>`)
	}
	sb.WriteString(`</select>`)
	writeSensitiveHTML(c, sb.String())
}

// resolveAPIKeyFromForm reads the API key from the form, OR for :id-bound
// routes, from the saved destination row when the form left it blank.
// Mirrors resolvePlexTokenFromForm.
func (s *Server) resolveAPIKeyFromForm(c *gin.Context) (string, error) {
	key := strings.TrimSpace(c.PostForm("api_key"))
	if key != "" {
		return key, nil
	}
	if id := c.Param("id"); id != "" {
		row, err := s.db.GetLibraryDestination(c.Request.Context(), id)
		if err == nil && row != nil && strings.TrimSpace(row.APIKey) != "" {
			return row.APIKey, nil
		}
	}
	return "", errors.New("API key is required — paste one or use the existing saved key.")
}

// handleDestinationsPlexPinStart kicks off a plex.tv PIN sign-in flow on
// behalf of the user. Returns an HTML fragment containing:
//   - a "Sign in with Plex" button that opens the plex.tv auth URL in
//     a popup (target="_blank")
//   - a hidden poller div that re-POSTs to /destinations/plex/pin/poll
//     every 2s until plex.tv returns an authToken, then swaps in JS
//     that fills the plex_token form input
//
// Token never touches the URL — the popup is just an HTMX-targeted
// container, and the page extracts the token from the poller response.
func (s *Server) handleDestinationsPlexPinStart(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	pin, err := s.plexCreatePin(ctx)
	if err != nil {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK,
			`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" role="status" aria-live="polite">`+
				`<strong>Could not start Plex sign-in.</strong> `+htmlEscape(err.Error())+`</div>`)
		return
	}
	authURL := s.plexAuthURL(pin.Code)

	// Render: inline <script> that opens the plex.tv popup immediately,
	// plus a fallback link if the popup is blocked, plus the self-polling
	// div. The popup-open lives inside the response because the user
	// just clicked "Sign in with Plex" — that gesture is still considered
	// active when the HTMX response is parsed, so window.open is allowed
	// without a blocker prompt in Chrome/Edge/Firefox. Safari is stricter
	// about popups from network responses; the visible fallback link
	// covers that case and any popup-blocker that did intervene.
	//
	// The poller is the HTMX equivalent of the old setInterval — every 2s
	// it POSTs to /destinations/plex/pin/poll and either replaces itself
	// with another polling div (still pending) or with the autofill script
	// (got token).
	authURLAttr := htmlEscape(authURL)
	authURLJS := jsString(authURL)
	var sb strings.Builder
	sb.WriteString(`<div class="info-box" style="border-color:var(--accent);margin:.5rem 0" role="status" aria-live="polite">`)
	sb.WriteString(`<strong>Approve access in the Plex window.</strong> The token will fill in automatically once you sign in. `)
	sb.WriteString(`If no window opened, <a href="`)
	sb.WriteString(authURLAttr)
	sb.WriteString(`" target="_blank" rel="noopener noreferrer">open plex.tv sign-in</a>.`)
	sb.WriteString(`</div>`)
	sb.WriteString(`<script>(function(){`)
	sb.WriteString(`try{window.open(`)
	sb.WriteString(authURLJS)
	sb.WriteString(`,"plexAuth","width=540,height=760,resizable=yes,scrollbars=yes,noopener");}catch(e){}`)
	sb.WriteString(`})();</script>`)
	sb.WriteString(renderPlexPollerDiv(pin.ID, pin.Code))
	writeSensitiveHTML(c, sb.String())
}

// renderPlexPollerDiv emits the self-replacing HTMX poller div. While
// the PIN is still pending the poll re-renders this same div (HTMX
// swap="outerHTML"), so the polling continues until the user approves.
// On approval the response is a script + autofill fragment instead.
func renderPlexPollerDiv(pinID int64, pinCode string) string {
	return `<div hx-post="/destinations/plex/pin/poll" ` +
		`hx-trigger="load delay:2s" ` +
		`hx-swap="outerHTML" ` +
		`hx-vals='{"pin_id":"` + strconv.FormatInt(pinID, 10) + `","pin_code":"` + htmlEscape(pinCode) + `"}' ` +
		`style="display:none"></div>`
}

// handleDestinationsPlexPinPoll polls plex.tv for the PIN's authToken.
// Three outcomes, each rendered as an HTML fragment:
//
//   - pending → another self-firing poller div (re-poll in 2s)
//   - approved → autofill script + chained "discover servers" trigger
//   - error → inline error box, polling stops
func (s *Server) handleDestinationsPlexPinPoll(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	pinID, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("pin_id")), 10, 64)
	if err != nil || pinID <= 0 {
		renderPlexPinError(c, "Invalid PIN ID — start sign-in again.")
		return
	}
	pinCode := strings.TrimSpace(c.PostForm("pin_code"))
	if pinCode == "" {
		renderPlexPinError(c, "Missing PIN code — start sign-in again.")
		return
	}

	pin, err := s.plexGetPin(ctx, pinID, pinCode)
	if err != nil {
		renderPlexPinError(c, "plex.tv error: "+err.Error())
		return
	}

	if strings.TrimSpace(pin.AuthToken) == "" {
		// Still pending — render another poller. HTMX will re-fire it in 2s.
		writeSensitiveHTML(c, renderPlexPollerDiv(pinID, pinCode))
		return
	}

	// Got the token. Render a tiny script that fills the form's plex_token
	// input and then clicks the "Discover servers" button so the URL list
	// populates in one shot. The script runs as the swapped-in HTML is
	// parsed; the message replaces the "waiting…" status above.
	var sb strings.Builder
	sb.WriteString(`<div class="info-box" style="border-color:var(--success);margin:.5rem 0" role="status" aria-live="polite">`)
	sb.WriteString(`<strong>Connected to Plex.</strong> Token saved to the form. Loading your servers…`)
	sb.WriteString(`</div>`)
	sb.WriteString(`<script>(function(){`)
	sb.WriteString(`var t=document.getElementById('plex_token');if(t){t.value=`)
	sb.WriteString(jsString(pin.AuthToken))
	sb.WriteString(`;t.dispatchEvent(new Event('change',{bubbles:true}));}`)
	sb.WriteString(`var b=document.getElementById('plex-discover-servers-btn');if(b){b.click();}`)
	sb.WriteString(`})();</script>`)
	writeSensitiveHTML(c, sb.String())
}

func renderPlexPinError(c *gin.Context, msg string) {
	writeSensitiveHTML(c,
		`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" role="status" aria-live="polite">`+
			`<strong>Plex sign-in failed.</strong> `+htmlEscape(msg)+`</div>`)
}

// jsString returns a JSON-encoded JS string literal safe for inline
// embedding inside a <script> tag. Go's encoding/json defaults to
// SetEscapeHTML(true) for json.Marshal, which already escapes <, >,
// and & as < / > / & — so a token containing "</script>"
// becomes "</script>" and cannot close the script tag.
// plex.tv tokens are URL-safe base64 in practice; this is purely
// defensive.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// handleDestinationsPlexDiscoverServers calls plex.tv resources API to
// list the user's owned/shared Plex servers, then renders a <select>
// of connection URLs. Picking one fills the `url` input via inline
// onchange (matching the ABS picker pattern).
func (s *Server) handleDestinationsPlexDiscoverServers(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	token, err := s.resolvePlexTokenFromForm(c)
	if err != nil {
		renderPlexDiscoverError(c, err.Error())
		return
	}

	servers, err := s.plexListServerOptions(ctx, token)
	if err != nil {
		renderPlexDiscoverError(c, "plex.tv error: "+err.Error())
		return
	}
	if len(servers) == 0 {
		renderPlexDiscoverWarning(c,
			"No Plex servers visible to this token. Make sure you've claimed at least one server on this Plex account.")
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="info-box" style="border-color:var(--success);margin:.5rem 0" role="status" aria-live="polite">`)
	sb.WriteString(fmt.Sprintf(`<strong>Found %d connection(s).</strong> Pick one — its URL will fill the URL field and the display name.`, len(servers)))
	sb.WriteString(`</div>`)
	sb.WriteString(`<label for="plex_server_picker" class="visually-hidden">Discovered Plex servers</label>`)
	// onchange fills the URL field unconditionally and the display_name
	// field only when empty — so a user who already typed "Living room"
	// doesn't get it overwritten on every picker change. The server's
	// own name (e.g. "Living room Plex") is carried on data-server-name,
	// stripped server-side of the trailing "(Plex Media Server)" product
	// suffix that plexListServerOptions adds for visual disambiguation.
	sb.WriteString(`<select id="plex_server_picker" class="form-control" style="margin:.25rem 0 .5rem 0" `)
	sb.WriteString(`onchange="var o=this.options[this.selectedIndex];`)
	sb.WriteString(`document.getElementById('url').value=this.value;`)
	sb.WriteString(`var dn=document.getElementById('display_name');`)
	sb.WriteString(`if(dn&&!dn.value){dn.value=o.getAttribute('data-server-name')||'';}">`)
	sb.WriteString(`<option value="">— pick a server —</option>`)
	for _, sv := range servers {
		label := sv.Name + " — " + sv.URL
		if sv.Local {
			label = "[LAN] " + label
		}
		sb.WriteString(`<option value="`)
		sb.WriteString(htmlEscape(sv.URL))
		sb.WriteString(`" data-server-name="`)
		sb.WriteString(htmlEscape(sv.DeviceName))
		sb.WriteString(`">`)
		sb.WriteString(htmlEscape(label))
		sb.WriteString(`</option>`)
	}
	sb.WriteString(`</select>`)
	writeSensitiveHTML(c, sb.String())
}

// handleDestinationsPlexDiscoverSections lists library sections on the
// configured Plex server and renders a <select>. Picking one fills the
// plex_section_id input.
func (s *Server) handleDestinationsPlexDiscoverSections(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	token, err := s.resolvePlexTokenFromForm(c)
	if err != nil {
		renderPlexDiscoverError(c, err.Error())
		return
	}
	urlStr := strings.TrimSpace(c.PostForm("url"))
	if urlStr == "" {
		renderPlexDiscoverError(c, "Enter the Plex server URL first (or pick one with Discover servers).")
		return
	}
	if err := validateRemoteURL(urlStr); err != nil {
		renderPlexDiscoverError(c, err.Error())
		return
	}

	sections, err := s.plexListSections(ctx, urlStr, token)
	if err != nil {
		renderPlexDiscoverError(c, "Plex server error: "+err.Error())
		return
	}
	if len(sections) == 0 {
		renderPlexDiscoverWarning(c,
			"This Plex server has no library sections visible to your token.")
		return
	}

	// Plex's "audiobook" libraries are typically type=artist (music libraries
	// configured for audiobooks). Show all sections so the user can pick
	// the right one — filtering would hide a misconfigured library and
	// leave the user stuck.
	var sb strings.Builder
	sb.WriteString(`<div class="info-box" style="border-color:var(--success);margin:.5rem 0" role="status" aria-live="polite">`)
	sb.WriteString(fmt.Sprintf(`<strong>Found %d section(s).</strong> Pick the one that holds your audiobooks.`, len(sections)))
	sb.WriteString(`</div>`)
	sb.WriteString(`<label for="plex_section_picker" class="visually-hidden">Discovered Plex sections</label>`)
	sb.WriteString(`<select id="plex_section_picker" class="form-control" style="margin:.25rem 0 .5rem 0" `)
	sb.WriteString(`onchange="document.getElementById('plex_section_id').value=this.value">`)
	sb.WriteString(`<option value="">— pick a section —</option>`)
	for _, sec := range sections {
		label := sec.Title + " (id=" + sec.ID + ", type=" + sec.Type + ")"
		sb.WriteString(`<option value="`)
		sb.WriteString(htmlEscape(sec.ID))
		sb.WriteString(`">`)
		sb.WriteString(htmlEscape(label))
		sb.WriteString(`</option>`)
	}
	sb.WriteString(`</select>`)
	writeSensitiveHTML(c, sb.String())
}

// resolvePlexTokenFromForm reads the Plex token from the form, OR for
// :id-bound routes, from the saved destination row when the form left it
// blank. Mirrors the secret-carry-over trick in destinationForTest.
func (s *Server) resolvePlexTokenFromForm(c *gin.Context) (string, error) {
	token := strings.TrimSpace(c.PostForm("plex_token"))
	if token != "" {
		return token, nil
	}
	if id := c.Param("id"); id != "" {
		row, err := s.db.GetLibraryDestination(c.Request.Context(), id)
		if err == nil && row != nil && strings.TrimSpace(row.PlexToken) != "" {
			return row.PlexToken, nil
		}
	}
	return "", errors.New("Plex token is required — sign in with Plex or paste a token first.")
}

func renderPlexDiscoverError(c *gin.Context, msg string) {
	writeSensitiveHTML(c,
		`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" role="status" aria-live="polite">`+
			`<strong>Failed.</strong> `+htmlEscape(msg)+`</div>`)
}

func renderPlexDiscoverWarning(c *gin.Context, msg string) {
	writeSensitiveHTML(c,
		`<div class="info-box" style="border-color:var(--warning);margin:.5rem 0" role="status" aria-live="polite">`+
			`<strong>Connected, but…</strong> `+htmlEscape(msg)+`</div>`)
}

// validateRemoteURL checks a user-supplied destination URL before the
// server issues an outbound request on the user's behalf. url.Parse is
// permissive (it accepts empty strings, file://, gopher://, …) so this
// guard rejects anything that isn't an http(s) URL with a non-empty host
// to keep the discover/test endpoints from being turned into a generic
// fetcher. RFC1918 / loopback addresses are NOT blocked — this app is
// designed to talk to LAN media servers, and blocking them would defeat
// the entire feature.
func validateRemoteURL(raw string) error {
	u, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("URL is malformed: %v", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("URL must start with http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("URL is missing a host (e.g. http://abs.local:13378)")
	}
	// Reject userinfo (http://user:pass@host) — confuses naive readers
	// about which host is being contacted (the parser sees `host`, the
	// eye sees `user`) and serves no legitimate purpose for an API key
	// the user is about to type into a dedicated field below.
	if u.User != nil {
		return fmt.Errorf("URL must not contain user:pass@ credentials")
	}
	return nil
}

// renderDiscoverResult emits the HTML fragment for the abs discover
// affordance. On success: a labeled <select> of libraries plus a hint
// that picking one will fill the library_id input. On failure: an
// inline error box matching the test-connection style.
func renderDiscoverResult(c *gin.Context, libs []mediaserver.ABSLibrary, errMsg string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	if errMsg != "" {
		c.String(http.StatusOK,
			`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" role="status" aria-live="polite">`+
				`<strong>Failed.</strong> `+htmlEscape(errMsg)+`</div>`)
		return
	}
	if len(libs) == 0 {
		c.String(http.StatusOK,
			`<div class="info-box" style="border-color:var(--warning);margin:.5rem 0" role="status" aria-live="polite">`+
				`<strong>No libraries visible to this token.</strong> `+
				`Check that the token has access to at least one library on this server.</div>`)
		return
	}

	bookLibs := make([]mediaserver.ABSLibrary, 0, len(libs))
	for _, l := range libs {
		if l.MediaType == "book" {
			bookLibs = append(bookLibs, l)
		}
	}

	// If the token can see the server but no book libraries are visible,
	// don't show a picker — there's nothing useful to pick. Surface that
	// as a warning so the user knows they need to grant the token access
	// to the audiobook library on the ABS side.
	if len(bookLibs) == 0 {
		other := len(libs)
		c.String(http.StatusOK,
			`<div class="info-box" style="border-color:var(--warning);margin:.5rem 0" role="status" aria-live="polite">`+
				`<strong>Connected, but no book libraries are visible to this token.</strong> `+
				htmlEscape(fmt.Sprintf("Found %d non-book libraries; none with mediaType=book. ", other))+
				`Grant this token access to your audiobook library in ABS, then try again.</div>`)
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="info-box" style="border-color:var(--success);margin:.5rem 0" role="status" aria-live="polite">`)
	sb.WriteString(`<strong>Connected.</strong> Found `)
	sb.WriteString(fmt.Sprintf("%d ", len(bookLibs)))
	if len(bookLibs) == 1 {
		sb.WriteString(`book library`)
	} else {
		sb.WriteString(`book libraries`)
	}
	if other := len(libs) - len(bookLibs); other > 0 {
		sb.WriteString(fmt.Sprintf(` (and %d non-book hidden)`, other))
	}
	sb.WriteString(`. Pick one — its UUID will fill the Library ID field.`)
	sb.WriteString(`</div>`)
	sb.WriteString(`<label for="abs_discover_picker" class="visually-hidden">Discovered libraries</label>`)
	sb.WriteString(`<select id="abs_discover_picker" class="form-control" style="margin:.25rem 0 .5rem 0" `)
	sb.WriteString(`onchange="document.getElementById('library_id').value=this.value">`)
	sb.WriteString(`<option value="">— pick a library —</option>`)
	for _, l := range bookLibs {
		label := l.Name
		if l.Path != "" {
			label += " — " + l.Path
		}
		sb.WriteString(`<option value="`)
		sb.WriteString(htmlEscape(l.ID))
		sb.WriteString(`">`)
		sb.WriteString(htmlEscape(label))
		sb.WriteString(`</option>`)
	}
	sb.WriteString(`</select>`)
	c.String(http.StatusOK, sb.String())
}

func renderTestResult(c *gin.Context, ok bool, success, fail string) {
	// Tiny inline HTML fragment. role="status" is set on the wrapper in
	// the form template; this fragment provides the inner content. The
	// banner color encodes status visually; the explicit "Connected" /
	// "Failed" prefix encodes it for SR users.
	c.Header("Content-Type", "text/html; charset=utf-8")
	if ok {
		c.String(http.StatusOK,
			`<div class="info-box" style="border-color:var(--success);margin:.5rem 0">`+
				`<strong>Connected.</strong> `+htmlEscape(success)+`</div>`)
		return
	}
	c.String(http.StatusOK,
		`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" tabindex="-1" id="test-result-failure">`+
			`<strong>Failed.</strong> `+htmlEscape(fail)+`</div>`)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// writeSensitiveHTML emits an HTML fragment that may carry secrets
// (Plex auth tokens, API keys discovered via plex.tv, library UUIDs).
// Cache-Control: no-store + Pragma: no-cache prevent any proxy or
// browser cache from storing the response — otherwise a token minted
// for user A could leak to user B sharing the same upstream cache.
// The Plex PIN-poll success response is the worst case (contains the
// authToken inline in a <script>), but the discover endpoints get the
// same treatment for symmetry.
func writeSensitiveHTML(c *gin.Context, body string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.String(http.StatusOK, body)
}

// handleDestinationsCreate persists a new destination after the form submit.
func (s *Server) handleDestinationsCreate(c *gin.Context) {
	d, err := s.destinationFromForm(c, "")
	if err != nil {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": err.Error()})
		return
	}
	// Disambiguate display names — turns "Plex" + "Plex" into "Plex" +
	// "Plex (2)". Only fires when a collision exists, so a single Plex
	// destination stays plainly named "Plex". The Plex server-picker
	// onchange autofills display_name from the discovered server name
	// when present, so the typical Plex flow never hits this fallback.
	d.DisplayName = s.uniqueDisplayName(c.Request.Context(), d.DisplayName, "")
	d.ID = uuid.NewString()
	d.Enabled = true
	d.CreatedAt = time.Now().UTC()
	if err := s.db.CreateLibraryDestination(c.Request.Context(), d); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Could not create destination: " + err.Error()})
		return
	}
	c.Redirect(http.StatusSeeOther, "/settings#library-destinations")
}

// uniqueDisplayName appends " (2)", " (3)", … to candidate when another
// destination already uses it. excludeID is the row being edited (so
// "edit-without-rename" doesn't trip the collision); pass "" on create.
// Case-insensitive comparison since DisplayName is a UI label, not a key.
func (s *Server) uniqueDisplayName(ctx context.Context, candidate, excludeID string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return candidate
	}
	rows, err := s.db.ListLibraryDestinations(ctx)
	if err != nil {
		// On query failure, return the candidate as-is — better to allow
		// a possible duplicate than block creation on a transient DB error.
		// The user can rename after the fact.
		return candidate
	}
	taken := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if r.ID == excludeID {
			continue
		}
		taken[strings.ToLower(strings.TrimSpace(r.DisplayName))] = struct{}{}
	}
	if _, exists := taken[strings.ToLower(candidate)]; !exists {
		return candidate
	}
	for n := 2; n < 1000; n++ {
		try := fmt.Sprintf("%s (%d)", candidate, n)
		if _, exists := taken[strings.ToLower(try)]; !exists {
			return try
		}
	}
	// Pathological: 998 collisions. Fall through with a uuid suffix so
	// we never loop forever, even though no realistic install hits this.
	return fmt.Sprintf("%s (%s)", candidate, uuid.NewString()[:8])
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
	// Disambiguate display name on edit too — but exclude the current row
	// so renaming a destination back to its existing value is a no-op
	// instead of bumping it to "Plex (2)".
	updated.DisplayName = s.uniqueDisplayName(c.Request.Context(), updated.DisplayName, existing.ID)
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

	// Display name is optional — default to the type label when blank
	// ("Audiobookshelf", "Plex", etc.). The CRUD UI rarely needs a custom
	// name unless the user runs multiple destinations of the same type.
	displayName := strings.TrimSpace(c.PostForm("display_name"))
	if displayName == "" {
		displayName = destinationTypeLabel(database.LibraryDestinationType(t))
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

	// On create (existingType == "") secrets must be present — otherwise
	// nullableStr would persist NULL and the DB CHECK constraint would
	// reject with a generic 500. We want a clear 400 with a user-facing
	// reason. On edit (existingType != "") the caller carries the saved
	// secret over when the form leaves it blank, so we don't gate here.
	creating := existingType == ""
	switch d.Type {
	case database.LibraryDestinationTypePlex:
		d.PlexToken = strings.TrimSpace(c.PostForm("plex_token"))
		d.PlexSectionID = strings.TrimSpace(c.PostForm("plex_section_id"))
		if d.PlexSectionID == "" {
			return nil, errors.New("Plex section ID is required")
		}
		if creating && d.PlexToken == "" {
			return nil, errors.New("Plex token is required")
		}
	case database.LibraryDestinationTypeEmby, database.LibraryDestinationTypeJellyfin, database.LibraryDestinationTypeABS:
		d.APIKey = strings.TrimSpace(c.PostForm("api_key"))
		d.LibraryID = strings.TrimSpace(c.PostForm("library_id"))
		if d.LibraryID == "" {
			return nil, errors.New("Library ID is required")
		}
		if creating && d.APIKey == "" {
			return nil, errors.New("API key is required")
		}
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
