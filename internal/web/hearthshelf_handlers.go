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
	"github.com/mstrhakr/audplexus/internal/mediaserver"
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
	// Servers where we had to choose between several book libraries - worth
	// telling the user, since we picked for them.
	var multi []string
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
		// Record when the credential lapses so the renewal path knows to act
		// before a push fails. Zero means the server stated no lifetime, which
		// RenewDestination reads as "do not refresh speculatively".
		if tokens.ExpiresIn > 0 {
			conn.CredentialExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).Unix()
		}
		// Carry over an existing destination id so reconnecting updates the row
		// rather than leaving an orphan beside a new one.
		for _, existing := range conns {
			if existing.ServerID == conn.ServerID {
				conn.DestinationID = existing.DestinationID
			}
		}
		// Resolve the audiobook library NOW, while we hold a working token.
		//
		// The schema requires a library_id on every abs destination (the CHECK in
		// 005_library_destinations), so "create it disabled and let the user pick
		// later" is not possible - the insert is rejected outright. It is also the
		// wrong shape for a one-click flow: we are already authenticated against
		// the server, so asking the user to go and find a UUID would give back the
		// busywork this whole feature exists to remove.
		//
		// One book library is the overwhelmingly common case and is chosen
		// silently. With several we still have to pick one to satisfy the
		// constraint, so we take the first and say so - the destination can be
		// edited afterwards like any other.
		// ABS credential, NOT the HearthShelf access token - /api/* is
		// Audiobookshelf's own surface and does not know our token. Using the
		// wrong one here returned 401 from every server.
		absKey := tokens.ABSAPIKey
		if absKey == "" {
			failed = append(failed, in.ServerName+" (server did not return an Audiobookshelf key)")
			continue
		}
		// Keep using the address we reached the server on. The box deliberately
		// does NOT tell us where its ABS lives, because that is its own internal
		// address (127.0.0.1 inside the all-in-one container) - nginx serves ABS's
		// /api/* on the same origin we already reached, which is the address that
		// actually works from out here.
		libs, libErr := mediaserver.ListLibraries(ctx, base, absKey)
		if libErr != nil {
			failed = append(failed, in.ServerName+" (could not list libraries: "+libErr.Error()+")")
			continue
		}
		var books []mediaserver.ABSLibrary
		for _, l := range libs {
			if strings.EqualFold(l.MediaType, "book") {
				books = append(books, l)
			}
		}
		if len(books) == 0 {
			failed = append(failed, in.ServerName+" (no audiobook library on that server)")
			continue
		}
		if len(books) > 1 {
			multi = append(multi, in.ServerName+" -> "+books[0].Name)
		}

		// The destination stores the ABS key - it is what the push path uses.
		if err := hearthshelf.EnsureDestination(ctx, s.db, &conn, absKey, books[0].ID); err != nil {
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

	writeSensitiveHTML(c, renderHSConnectResult(connected, failed, multi))
}

// renderHSConnectResult builds the fragment shown when the flow finishes.
// Extracted so the modal-close and library-choice behaviour can be tested
// directly - both were bugs that only showed up in a real browser.
func renderHSConnectResult(connected, failed, multi []string) string {
	var sb strings.Builder
	if len(connected) > 0 {
		sb.WriteString(`<div class="info-box" style="border-color:var(--success);margin:.5rem 0" role="status" aria-live="polite">`)
		sb.WriteString(`<strong>Connected to `)
		sb.WriteString(htmlEscape(strings.Join(connected, ", ")))
		sb.WriteString(`.</strong> Pick a library below to start sending books.`)
		sb.WriteString(`</div>`)
	}
	if len(multi) > 0 {
		sb.WriteString(`<div class="info-box" style="border-color:var(--info);margin:.5rem 0" role="status" aria-live="polite">`)
		sb.WriteString(`<strong>Picked a library for you:</strong> `)
		sb.WriteString(htmlEscape(strings.Join(multi, ", ")))
		sb.WriteString(`. That server has more than one audiobook library - edit the destination to change it.`)
		sb.WriteString(`</div>`)
	}
	if len(failed) > 0 {
		sb.WriteString(`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" role="status" aria-live="polite">`)
		sb.WriteString(`<strong>Could not finish:</strong> `)
		sb.WriteString(htmlEscape(strings.Join(failed, "; ")))
		sb.WriteString(`</div>`)
	}
	// Close the approval popup either way - the browser side of the flow is over.
	sb.WriteString(`<script>(function(){`)
	sb.WriteString(`try{var w=window.__audplexusHSPopup;if(w&&!w.closed){w.close();}}catch(e){}`)
	// dest-created closes this modal and refreshes the list, so fire it ONLY when
	// something actually connected. Firing it on failure tore the modal down
	// before the user could read (or copy) the error - which is exactly what you
	// most need when a connection fails.
	if len(connected) > 0 && len(failed) == 0 {
		sb.WriteString(`try{document.body.dispatchEvent(new CustomEvent('dest-created'));}catch(e){}`)
	}
	sb.WriteString(`})();</script>`)
	// A partial or total failure leaves the modal open with the message in it, so
	// add a way out that does not depend on the user finding the close button.
	if len(failed) > 0 {
		sb.WriteString(`<div style="margin-top:.75rem;display:flex;gap:.5rem">`)
		sb.WriteString(`<button type="button" class="btn" hx-post="/destinations/hearthshelf/start" `)
		sb.WriteString(`hx-target="#dest-modal-content" hx-swap="innerHTML">Try again</button>`)
		if len(connected) > 0 {
			// Some servers did connect; let the user accept that and see the list.
			sb.WriteString(`<button type="button" class="btn" onclick="try{document.body.dispatchEvent(new CustomEvent('dest-created'));}catch(e){}">Done</button>`)
		}
		sb.WriteString(`</div>`)
	}
	return sb.String()
}

// renderHSPollerDiv is the self-firing poller: HTMX re-POSTs it every `interval`
// seconds until the flow resolves.
func renderHSPollerDiv(deviceCode string, interval int) string {
	if interval < 1 {
		interval = 5
	}
	// hx-vals, NOT hidden <input>s. HTMX serializes form fields from an enclosing
	// <form>; loose inputs inside a bare <div> are not included, so the poll
	// arrived with an empty device_code and the handler immediately gave up with
	// "lost track of this connection" on the very first tick. hx-vals is what the
	// Plex PIN poller beside this uses, for the same reason.
	var sb strings.Builder
	sb.WriteString(`<div hx-post="/destinations/hearthshelf/poll" hx-trigger="load delay:`)
	sb.WriteString(strconv.Itoa(interval))
	sb.WriteString(`s" hx-target="#hs-connect-result" hx-swap="innerHTML" hx-vals='{"device_code":"`)
	sb.WriteString(htmlEscape(deviceCode))
	sb.WriteString(`","interval":"`)
	sb.WriteString(strconv.Itoa(interval))
	sb.WriteString(`"}' style="display:none"></div>`)
	return sb.String()
}

func renderHSError(c *gin.Context, msg string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK,
		`<div class="info-box" style="border-color:var(--error);margin:.5rem 0" role="status" aria-live="polite">`+
			htmlEscape(msg)+`</div>`)
}
