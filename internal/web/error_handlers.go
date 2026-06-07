package web

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"
)

// errorPageOpts controls the variant of the shared error page.
type errorPageOpts struct {
	StatusCode    int
	StatusLabel   string // "Not Found", "Forbidden", etc.
	Eyebrow       string // small uppercase tag above title
	Title         string // big sentence-case headline
	Message       string // 1-2 sentence explanation
	Detail        string // optional raw error / payload, shown in <details>
	ShowRefresh   bool
	ShowBack      bool
}

// renderErrorPage writes the shared full-page error template. Used by the
// NoRoute handler, the CSRF middleware hook, and any handler that wants to
// surface a clean failure UI instead of raw JSON. The template is standalone
// (no sidebar, no CSRF token plumbing) so it works even when middleware has
// aborted.
func renderErrorPage(c *gin.Context, opts errorPageOpts) {
	code := opts.StatusCode
	if code == 0 {
		code = http.StatusInternalServerError
	}
	label := opts.StatusLabel
	if label == "" {
		label = http.StatusText(code)
	}

	codeStr := padStatus(code)
	data := gin.H{
		"Page":          "error",
		"StatusCode":    code,
		"StatusLabel":   strings.ToUpper(label),
		"StatusDigit1":  string(codeStr[0]),
		"StatusDigit2":  string(codeStr[1]),
		"StatusDigit3":  string(codeStr[2]),
		"Eyebrow":       strings.ToUpper(opts.Eyebrow),
		"Title":         opts.Title,
		"Message":       opts.Message,
		"Detail":        opts.Detail,
		"ShowRefresh":   opts.ShowRefresh,
		"ShowBack":      opts.ShowBack,
		"RequestPath":   c.Request.URL.Path,
		"RequestMethod": c.Request.Method,
		"TraceID":       traceID(),
		// Stub fields base.html may reference even though the error page
		// doesn't render the chrome — keeps template execution from
		// erroring on missing keys.
		"CSRFToken": "",
	}

	c.HTML(code, "error_page.html", data)
}

// padStatus normalizes a status code to exactly 3 chars for the big numeric
// display (e.g. 42 → "042"). Practically every HTTP status is already 3
// digits; the pad is defensive only.
func padStatus(code int) string {
	out := []byte("000")
	n := code
	for i := 2; i >= 0 && n > 0; i-- {
		out[i] = byte('0' + n%10)
		n /= 10
	}
	if code >= 1000 {
		return "ERR"
	}
	return string(out)
}

// traceID returns a short random hex id stamped into the error page for
// support correlation. Not a real distributed trace — just a "you can quote
// this in a bug report" handle.
func traceID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

// handleNotFound renders the shared 404 page. Wired via router.NoRoute so any
// unmatched path lands here instead of gin's plain "404 not found" string.
func (s *Server) handleNotFound(c *gin.Context) {
	// API + XHR clients still get JSON — they're not browsers, they don't
	// want HTML chrome.
	if wantsJSON(c) {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	renderErrorPage(c, errorPageOpts{
		StatusCode:  http.StatusNotFound,
		StatusLabel: "Not Found",
		Eyebrow:     "Off the map",
		Title:       "We couldn't find that page.",
		Message:     "The URL you followed doesn't match anything Audplexus serves. It may have moved, or the link may be a typo.",
		ShowRefresh: false,
		ShowBack:    true,
	})
}

// handleCSRFFailure is invoked by auth.Manager when a non-safe request fails
// the CSRF check from a browser context. Keeps the JSON contract for API/XHR
// callers and renders the styled page for everyone else.
func (s *Server) handleCSRFFailure(c *gin.Context) {
	if wantsJSON(c) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf token invalid"})
		return
	}
	c.Status(http.StatusForbidden)
	renderErrorPage(c, errorPageOpts{
		StatusCode:  http.StatusForbidden,
		StatusLabel: "Session Expired",
		Eyebrow:     "Stale session",
		Title:       "Your session token expired.",
		Message:     "The form you submitted was signed with a token Audplexus no longer recognizes — usually because this tab sat open across a restart or a sign-out. Refresh to mint a new one, then try again.",
		Detail:      `{"error":"csrf token invalid"}`,
		ShowRefresh: true,
		ShowBack:    true,
	})
	c.Abort()
}

// recoveryMiddleware replaces gin.Recovery() so panics in handlers route
// through the styled error page instead of gin's default plain-text dump.
// Logs the panic + stack trace, then renders the page (or JSON for API/XHR
// callers). Must run before any handler that can panic — installed at
// router construction time.
func (s *Server) recoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			stack := debug.Stack()
			webLog.Error().
				Str("path", c.Request.URL.Path).
				Str("method", c.Request.Method).
				Str("panic", fmt.Sprintf("%v", r)).
				Bytes("stack", stack).
				Msg("handler panic recovered")

			// If the handler already started writing a response, we can't
			// safely overwrite headers — gin will log the double-write and
			// the client gets a truncated payload, but at least we don't
			// crash the server.
			if c.Writer.Written() {
				c.Abort()
				return
			}

			if wantsJSON(c) {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
				return
			}
			renderErrorPage(c, errorPageOpts{
				StatusCode:  http.StatusInternalServerError,
				StatusLabel: "Server Error",
				Eyebrow:     "Unhandled fault",
				Title:       "Something went wrong on our side.",
				Message:     "Audplexus hit an unexpected error processing that request. The fault has been logged with the trace id below — quote it if you open a bug report.",
				Detail:      fmt.Sprintf("%v", r),
				ShowRefresh: true,
				ShowBack:    true,
			})
			c.Abort()
		}()
		c.Next()
	}
}

// errorPageInterceptor catches handlers that set a 5xx status (or 502/503/504
// upstream-style failures) but never wrote a body. Buffers the response and,
// if the status qualifies and the body is empty / tiny / a raw gin error
// string, swaps in the styled error page. Handlers that DO render a real
// response — even a 500 page of their own — pass through unchanged.
//
// Skips /api/* + XHR + Accept: application/json so JSON contracts are never
// rewritten as HTML.
func (s *Server) errorPageInterceptor() gin.HandlerFunc {
	return func(c *gin.Context) {
		if wantsJSON(c) {
			c.Next()
			return
		}
		// /static and SSE streams should never be rewritten — they're not
		// page navigations and a styled HTML body would corrupt them.
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/static/") || path == "/api/events" {
			c.Next()
			return
		}

		bw := &bufferedWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = bw
		c.Next()

		status := bw.Status()

		flush := func() {
			if bw.status != 0 {
				bw.ResponseWriter.WriteHeader(bw.status)
			}
			if bw.body.Len() > 0 {
				_, _ = bw.ResponseWriter.Write(bw.body.Bytes())
			}
		}

		if status < 500 || status > 599 {
			// Not a server error — flush as-is.
			flush()
			return
		}

		body := bw.body.Bytes()
		ct := bw.ResponseWriter.Header().Get("Content-Type")

		// If the handler already produced a substantial HTML response, leave
		// it alone. Threshold is conservative: real pages are kilobytes;
		// gin's default "500\n" and bare error strings are bytes.
		if len(body) > 512 && bytes.Contains(body, []byte("<html")) {
			flush()
			return
		}
		// Respect explicit JSON responses — handlers (or our own recovery
		// middleware on the wantsJSON path) deliberately wrote machine-
		// readable bodies; don't rewrite them as HTML.
		if strings.Contains(ct, "application/json") {
			flush()
			return
		}

		// Drop the headers the handler set (likely Content-Type: text/plain
		// for a raw error string) so renderErrorPage can write fresh HTML
		// headers when it calls c.HTML.
		bw.ResponseWriter.Header().Del("Content-Length")
		bw.ResponseWriter.Header().Del("Content-Type")

		webLog.Warn().
			Int("status", status).
			Str("path", path).
			Int("body_bytes", len(body)).
			Msg("server error intercepted; rendering styled error page")

		// Swap c.Writer back to the real one so c.HTML inside renderErrorPage
		// writes to the actual connection, not our buffer.
		c.Writer = bw.ResponseWriter
		renderErrorPage(c, errorPageOpts{
			StatusCode:  status,
			StatusLabel: http.StatusText(status),
			Eyebrow:     "Service fault",
			Title:       errorTitleForStatus(status),
			Message:     errorMessageForStatus(status),
			Detail:      strings.TrimSpace(string(body)),
			ShowRefresh: true,
			ShowBack:    true,
		})
	}
}

// bufferedWriter wraps gin.ResponseWriter so the interceptor can decide AFTER
// the handler returns whether to keep the body or swap in the error page.
// All writes go through the buffer until the interceptor flushes them.
type bufferedWriter struct {
	gin.ResponseWriter
	body   *bytes.Buffer
	status int
}

func (b *bufferedWriter) Write(p []byte) (int, error)       { return b.body.Write(p) }
func (b *bufferedWriter) WriteString(s string) (int, error) { return b.body.WriteString(s) }

// WriteHeader records but does NOT forward — the interceptor decides after
// the handler returns whether to flush the original status (good path) or
// discard it and stamp a fresh status from renderErrorPage (rewrite path).
func (b *bufferedWriter) WriteHeader(code int) { b.status = code }

func (b *bufferedWriter) Status() int {
	if b.status != 0 {
		return b.status
	}
	return b.ResponseWriter.Status()
}

// Written reports whether the handler called WriteHeader or wrote any body.
// gin's Writer.Written() inspects the underlying ResponseWriter, which we
// haven't touched, so override it to consult our buffer instead — otherwise
// handlers (and our recovery middleware) think nothing has been written.
func (b *bufferedWriter) Written() bool {
	return b.status != 0 || b.body.Len() > 0
}

func errorTitleForStatus(code int) string {
	switch code {
	case http.StatusBadGateway:
		return "Upstream service didn't answer."
	case http.StatusServiceUnavailable:
		return "Audplexus is temporarily unavailable."
	case http.StatusGatewayTimeout:
		return "Upstream service timed out."
	default:
		return "Something went wrong on our side."
	}
}

func errorMessageForStatus(code int) string {
	switch code {
	case http.StatusBadGateway:
		return "A backend Audplexus depends on (Audible, your media server, or the database) returned an invalid response. Retry usually clears it; if not, check Diagnostics."
	case http.StatusServiceUnavailable:
		return "The service is starting up, restarting, or temporarily overloaded. Try again in a few seconds."
	case http.StatusGatewayTimeout:
		return "The upstream service took too long to respond. This usually means the network or remote server is slow — retry, or check Diagnostics."
	default:
		return "Audplexus hit an unexpected error processing that request. The fault has been logged with the trace id below — quote it if you open a bug report."
	}
}

// wantsJSON returns true when the caller is clearly programmatic (an /api/
// route, an XHR request, or one that Accepts JSON) and should receive the
// machine-readable error body instead of the HTML page.
func wantsJSON(c *gin.Context) bool {
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		return true
	}
	if strings.EqualFold(c.GetHeader("X-Requested-With"), "XMLHttpRequest") {
		return true
	}
	accept := c.GetHeader("Accept")
	if strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html") {
		return true
	}
	return false
}
