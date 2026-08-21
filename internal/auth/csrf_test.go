package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/database"
)

// csrfTestRouter builds a router with the CSRF middleware and two routes that
// mirror the real download-queue endpoints.
func csrfTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	m := &Manager{DB: database.NewStubDB()}
	r := gin.New()
	r.Use(m.CSRF(nil))
	r.GET("/api/downloads/state", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/api/downloads/resume", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

// mintCSRFToken performs a GET and returns the csrftoken cookie the middleware
// stamps, the same way a normal page load seeds the browser.
func mintCSRFToken(t *testing.T, r *gin.Engine) string {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/downloads/state", nil))
	for _, ck := range w.Result().Cookies() {
		if ck.Name == CSRFCookieName {
			return ck.Value
		}
	}
	t.Fatal("CSRF middleware did not mint a csrftoken cookie on GET")
	return ""
}

// A cookie-authenticated POST that omits X-CSRF-Token must be rejected. This is
// the regression that broke the queue pause/resume button: the click handler
// used a bare fetch(), so the header never went out and the server 403'd.
func TestCSRFRejectsPOSTWithoutHeader(t *testing.T) {
	r := csrfTestRouter()
	token := mintCSRFToken(t, r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/downloads/resume", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("POST without X-CSRF-Token: got %d, want %d", w.Code, http.StatusForbidden)
	}
}

// Echoing the cookie back in X-CSRF-Token is what apiFetch() in base.html does,
// and it must pass the double-submit check.
func TestCSRFAcceptsPOSTWithHeaderFromCookie(t *testing.T) {
	r := csrfTestRouter()
	token := mintCSRFToken(t, r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/downloads/resume", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	req.Header.Set(CSRFHeaderName, token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST with X-CSRF-Token: got %d, want %d", w.Code, http.StatusOK)
	}
}

// A header that doesn't match the cookie is the actual attack shape, and must
// still be rejected — the fix attaches a real token, it doesn't weaken the check.
func TestCSRFRejectsMismatchedHeader(t *testing.T) {
	r := csrfTestRouter()
	token := mintCSRFToken(t, r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/downloads/resume", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	req.Header.Set(CSRFHeaderName, "not-the-right-token")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("POST with mismatched token: got %d, want %d", w.Code, http.StatusForbidden)
	}
}
