package web

// The "Connect to HearthShelf" flow, wired into the destination picker.
//
// This is the UI half of internal/hearthshelf. Structurally it is the same
// shape as the Plex PIN flow above it: the user clicks connect, we show a code,
// and a self-firing HTMX poller waits for them to approve it in a browser. The
// difference is what arrives at the end - Plex hands back a token the user still
// has to point at a server, whereas HearthShelf hands back the server list, the
// address, and the credential all at once, so the destination can configure
// itself.
//
// Why a code and not a redirect: Audplexus is usually headless (a NAS, an Unraid
// box, a Pi) and configured from a different machine. A loopback redirect would
// land in the user's laptop browser, not here. The device grant needs no
// callback, no public URL, and no port forward - see internal/hearthshelf.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mstrhakr/audplexus/internal/hearthshelf"
)

// controlPlaneURL is the configured control plane, or the hosted default.
func (s *Server) controlPlaneURL(ctx context.Context) string {
	v, _ := s.db.GetSetting(ctx, hearthshelf.SettingKeyControlPlane)
	if strings.TrimSpace(v) == "" {
		return hearthshelf.DefaultControlPlane
	}
	return strings.TrimSpace(v)
}

// identity returns this install's own app registration, creating it on first
// use. Self-registering here is what keeps the user out of a developer console:
// they click connect, and the app quietly claims its own credential.
func (s *Server) hsIdentity(ctx context.Context) (*hearthshelf.Identity, *hearthshelf.Client, error) {
	client := hearthshelf.New(s.controlPlaneURL(ctx))
	id, err := hearthshelf.LoadIdentity(ctx, s.db, s.credBox)
	if err != nil {
		return nil, nil, err
	}
	if id != nil {
		return id, client, nil
	}
	reg, err := client.Register(ctx, "Audplexus")
	if err != nil {
		return nil, nil, err
	}
	id = &hearthshelf.Identity{AppID: reg.AppID, Secret: reg.ClientSecret}
	if err := hearthshelf.SaveIdentity(ctx, s.db, s.credBox, id); err != nil {
		return nil, nil, err
	}
	return id, client, nil
}

// handleHearthShelfConnectStart registers (if needed), requests a user code, and
// renders it with a poller. Mirrors handleDestinationsPlexPinStart.
func (s *Server) handleHearthShelfConnectStart(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	id, client, err := s.hsIdentity(ctx)
	if err != nil {
		renderHSError(c, "Could not reach HearthShelf: "+err.Error())
		return
	}

	dc, err := client.StartDeviceFlow(ctx, id.AppID, id.Secret)
	if err != nil {
		renderHSError(c, "Could not start the connection: "+err.Error())
		return
	}

	var sb strings.Builder
	sb.WriteString(`<header style="margin-bottom:1rem"><h2 style="margin:0">Connect to HearthShelf</h2>`)
	sb.WriteString(`<p class="muted" style="margin:.35rem 0 0 0">Approve this code in your browser. Audplexus will set up the destination itself - no URL or API key to copy.</p></header>`)
	// The poller swaps into this container, so it has to exist before the first
	// poll fires. Everything below it is replaced wholesale on each poll.
	sb.WriteString(`<div id="hs-connect-result">`)
	sb.WriteString(`<div class="info-box" style="border-color:var(--info);margin:.5rem 0" role="status" aria-live="polite">`)
	sb.WriteString(`<strong>Enter this code at HearthShelf.</strong>`)
	sb.WriteString(`<div style="font-size:1.6rem;letter-spacing:.25em;font-family:monospace;margin:.5rem 0">`)
	sb.WriteString(htmlEscape(dc.UserCode))
	sb.WriteString(`</div>`)
	sb.WriteString(`<a href="`)
	sb.WriteString(htmlEscape(dc.VerificationURIComplete))
	sb.WriteString(`" target="hsAuth" rel="noreferrer">Open `)
	sb.WriteString(htmlEscape(dc.VerificationURI))
	sb.WriteString(`</a> and approve, then pick which servers Audplexus may use.`)
	sb.WriteString(`</div>`)
	// The popup rides the user's click gesture, same reasoning as the Plex flow.
	sb.WriteString(`<script>(function(){try{window.__audplexusHSPopup=window.open(`)
	sb.WriteString(jsString(dc.VerificationURIComplete))
	sb.WriteString(`,"hsAuth","width=560,height=760,resizable=yes,scrollbars=yes");}catch(e){}})();</script>`)
	sb.WriteString(renderHSPollerDiv(dc.DeviceCode, dc.Interval))
	sb.WriteString(`</div>`)
	writeSensitiveHTML(c, sb.String())
}

// handleHearthShelfConnectPoll checks whether the user has approved yet.
//
// Three outcomes, same as the Plex poller: pending re-arms itself, failure stops
// with a message, success introduces us to each approved server and creates the
// destinations.
func (s *Server) handleHearthShelfConnectPoll(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	deviceCode := strings.TrimSpace(c.PostForm("device_code"))
	if deviceCode == "" {
		renderHSError(c, "Lost track of this connection - start again.")
		return
	}
	interval := 5
	if v := strings.TrimSpace(c.PostForm("interval")); v != "" {
		if n, err := time.ParseDuration(v + "s"); err == nil && n > 0 {
			interval = int(n.Seconds())
		}
	}

	id, client, err := s.hsIdentity(ctx)
	if err != nil {
		renderHSError(c, "Could not reach HearthShelf: "+err.Error())
		return
	}

	intros, err := client.PollDeviceFlow(ctx, id.AppID, id.Secret, deviceCode)
	switch {
	case err == nil:
		// approved - fall through
	case errors.Is(err, hearthshelf.ErrAuthorizationPending):
		writeSensitiveHTML(c, renderHSPollerDiv(deviceCode, interval))
		return
	case errors.Is(err, hearthshelf.ErrSlowDown):
		// Back off as the server asked, then keep waiting.
		writeSensitiveHTML(c, renderHSPollerDiv(deviceCode, interval+5))
		return
	case errors.Is(err, hearthshelf.ErrAccessDenied):
		renderHSError(c, "The request was declined in HearthShelf.")
		return
	case errors.Is(err, hearthshelf.ErrExpiredToken):
		renderHSError(c, "That code expired. Start the connection again.")
		return
	default:
		renderHSError(c, "HearthShelf error: "+err.Error())
		return
	}

	if len(intros) == 0 {
		renderHSError(c, "No servers were selected, so there is nothing to connect.")
		return
	}

	conns, err := hearthshelf.LoadConnections(ctx, s.db, s.credBox)
	if err != nil {
		renderHSError(c, "Could not read saved connections: "+err.Error())
		return
	}

	var connected []string
	var failed []string
	for _, in := range intros {
		// Prefer the LAN address when its identity checks out - it is the common
		// case for a same-network install and it survives an internet outage.
		base, err := hearthshelf.PreferredAddress(ctx, client.HTTP, in)
		if err != nil {
			failed = append(failed, in.ServerName+" (no reachable address)")
			continue
		}
		tokens, err := client.Introduce(ctx, base, in.IntroToken)
		if err != nil {
			failed = append(failed, in.ServerName+" ("+err.Error()+")")
			continue
		}

		conn := hearthshelf.Connection{
			ServerID:     in.ServerID,
			ServerName:   in.ServerName,
			BaseURL:      base,
			RefreshToken: tokens.RefreshToken,
			Scopes:       tokens.Scope,
		}
		// Carry over an existing destination id so reconnecting updates the row
		// rather than leaving an orphan beside a new one.
		for _, existing := range conns {
			if existing.ServerID == conn.ServerID {
				conn.DestinationID = existing.DestinationID
			}
		}
		// Library is chosen afterwards, so the destination starts disabled -
		// enabling something that cannot route a book would only manufacture
		// failures the user did not ask for.
		if err := hearthshelf.EnsureDestination(ctx, s.db, &conn, tokens.AccessToken, ""); err != nil {
			failed = append(failed, in.ServerName+" ("+err.Error()+")")
			continue
		}
		conns = hearthshelf.UpsertConnection(conns, conn)
		connected = append(connected, conn.ServerName)
	}

	if err := hearthshelf.SaveConnections(ctx, s.db, s.credBox, conns); err != nil {
		renderHSError(c, "Connected, but could not save: "+err.Error())
		return
	}

	var sb strings.Builder
	if len(connected) > 0 {
		sb.WriteString(`<div class="info-box" style="border-color:var(--success);margin:.5rem 0" role="status" aria-live="polite">`)
		sb.WriteString(`<strong>Connected to `)
		sb.WriteString(htmlEscape(strings.Join(connected, ", ")))
		sb.WriteString(`.</strong> Pick a library below to start sending books.`)
		sb.WriteString(`</div>`)
	}
	if len(failed) > 0 {
		sb.WriteString(`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" role="status" aria-live="polite">`)
		sb.WriteString(`<strong>Could not finish:</strong> `)
		sb.WriteString(htmlEscape(strings.Join(failed, "; ")))
		sb.WriteString(`</div>`)
	}
	// Close the approval popup and refresh the destinations list so the new rows
	// appear without a manual reload.
	sb.WriteString(`<script>(function(){`)
	sb.WriteString(`try{var w=window.__audplexusHSPopup;if(w&&!w.closed){w.close();}}catch(e){}`)
	sb.WriteString(`try{document.body.dispatchEvent(new CustomEvent('dest-created'));}catch(e){}`)
	sb.WriteString(`})();</script>`)
	writeSensitiveHTML(c, sb.String())
}

// renderHSPollerDiv is the self-firing poller: HTMX re-POSTs it every `interval`
// seconds until the flow resolves.
func renderHSPollerDiv(deviceCode string, interval int) string {
	if interval < 1 {
		interval = 5
	}
	var sb strings.Builder
	sb.WriteString(`<div hx-post="/destinations/hearthshelf/poll" hx-trigger="load delay:`)
	sb.WriteString(strconv.Itoa(interval))
	sb.WriteString(`s" hx-target="#hs-connect-result" hx-swap="innerHTML" style="display:none">`)
	sb.WriteString(`<input type="hidden" name="device_code" value="`)
	sb.WriteString(htmlEscape(deviceCode))
	sb.WriteString(`"><input type="hidden" name="interval" value="`)
	sb.WriteString(strconv.Itoa(interval))
	sb.WriteString(`"></div>`)
	return sb.String()
}

func renderHSError(c *gin.Context, msg string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK,
		`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" role="status" aria-live="polite">`+
			htmlEscape(msg)+`</div>`)
}
