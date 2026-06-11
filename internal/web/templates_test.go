package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestTemplatesParseAndRenderReorganizePreview ensures every embedded template
// parses (setupTemplates panics on error) and that the reorganize preview
// partial renders standalone the way the HTMX endpoint serves it.
func TestTemplatesParseAndRenderReorganizePreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{router: gin.New()}
	s.setupTemplates()

	s.router.GET("/preview", func(c *gin.Context) {
		c.HTML(200, "reorganize_preview.html", gin.H{
			"Moves": []reorgMove{{
				Title: "Project Hail Mary", Author: "Andy Weir",
				From: "/lib/Old/Book", To: "/lib/New/Book",
				Prefix: "/lib/", FromRest: "Old/Book", ToRest: "New/Book",
			}},
			"MoveCount": 1,
			"Unchanged": 2,
			"Missing":   1,
			"Total":     4,
		})
	})

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest("GET", "/preview", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Move 1 book", "already organized", "missing on disk", "Old/Book", "New/Book"} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered preview missing %q:\n%s", want, body)
		}
	}

	// Empty plan renders the nothing-to-do state without an apply button.
	s.router.GET("/preview-empty", func(c *gin.Context) {
		c.HTML(200, "reorganize_preview.html", gin.H{
			"Moves": []reorgMove{}, "MoveCount": 0, "Unchanged": 4, "Missing": 0, "Total": 4,
		})
	})
	w = httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest("GET", "/preview-empty", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "nothing to reorganize") || strings.Contains(body, "reorg-confirm-btn") {
		t.Fatalf("empty plan should render no-op state without apply button:\n%s", body)
	}
}
