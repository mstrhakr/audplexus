package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/database"
)

func newWebTestDB(t *testing.T) *database.SQLiteDB {
	t.Helper()
	db, err := database.NewSQLite(filepath.Join(t.TempDir(), "web-test.db"))
	if err != nil {
		t.Fatalf("NewSQLite() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return db
}

func TestHandleDestinationsCreate_E2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newWebTestDB(t)
	s := &Server{db: db}

	r := gin.New()
	r.POST("/destinations", s.handleDestinationsCreate)

	form := url.Values{}
	form.Set("type", "plex")
	form.Set("display_name", "Main Plex")
	form.Set("url", "http://plex.local:32400/")
	form.Set("plex_token", "secret-token")
	form.Set("plex_section_id", "7")

	req := httptest.NewRequest(http.MethodPost, "/destinations", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if got := w.Header().Get("Location"); got != "/destinations" {
		t.Fatalf("Location = %q, want /destinations", got)
	}

	rows, err := db.ListLibraryDestinations(context.Background())
	if err != nil {
		t.Fatalf("ListLibraryDestinations() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("destinations count = %d, want 1", len(rows))
	}

	d := rows[0]
	if d.Type != database.LibraryDestinationTypePlex {
		t.Fatalf("type = %q, want plex", d.Type)
	}
	if d.DisplayName != "Main Plex" {
		t.Fatalf("display_name = %q, want Main Plex", d.DisplayName)
	}
	if d.URL != "http://plex.local:32400" {
		t.Fatalf("url = %q, want normalized URL", d.URL)
	}
	if d.PlexSectionID != "7" || d.PlexToken != "secret-token" {
		t.Fatalf("plex settings mismatch: %+v", d)
	}
	if !d.Enabled {
		t.Fatalf("enabled = false, want true")
	}
}
