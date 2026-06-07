package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
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
