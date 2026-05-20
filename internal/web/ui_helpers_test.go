package web

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/database"
)

// TestPaginationNumbers exercises the compact paginator. The two
// branches that matter are "small total (no ellipsis)" and "large
// total (windowed with ellipsis markers around the current page)".
func TestPaginationNumbers(t *testing.T) {
	tests := []struct {
		name    string
		current int
		total   int
		want    []string
	}{
		{name: "single page", current: 1, total: 1, want: []string{"1"}},
		{name: "exactly 7 — no ellipsis", current: 4, total: 7, want: []string{"1", "2", "3", "4", "5", "6", "7"}},
		{name: "current near start, total > 7", current: 2, total: 10, want: []string{"1", "2", "3", "…", "10"}},
		{name: "current in middle, total > 7", current: 5, total: 10, want: []string{"1", "…", "4", "5", "6", "…", "10"}},
		{name: "current near end, total > 7", current: 9, total: 10, want: []string{"1", "…", "8", "9", "10"}},
		{name: "current = 1 of many", current: 1, total: 12, want: []string{"1", "2", "…", "12"}},
		{name: "current = total of many", current: 12, total: 12, want: []string{"1", "…", "11", "12"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paginationNumbers(tt.current, tt.total)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("paginationNumbers(%d, %d) = %v, want %v", tt.current, tt.total, got, tt.want)
			}
		})
	}
}

// TestParseCompletedWindow is the URL-input sanitizer for the
// Completed tab on the pipeline page. Any unrecognized value must
// fall through to the 24h default — we don't want a casual click on
// /downloads?completed_window=foo to silently surface all-time
// history.
func TestParseCompletedWindow(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "24h", want: "24h"},
		{in: "7d", want: "7d"},
		{in: "30d", want: "30d"},
		{in: "all", want: "all"},
		{in: "", want: "24h"},
		{in: "  7D  ", want: "7d"}, // case + whitespace tolerant
		{in: "bogus", want: "24h"},
		{in: "1h", want: "24h"}, // unsupported value defaults to 24h
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := parseCompletedWindow(tt.in)
			if got != tt.want {
				t.Fatalf("parseCompletedWindow(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestFilterCompletedByWindow covers the three real windows plus the
// "all" pass-through. We build rows at known times relative to "now"
// so the test isn't time-dependent — each row's CompletedAt is set
// to N hours ago, then asserted against the threshold.
//
// Rows with a nil CompletedAt fall back to UpdatedAt — that branch
// matters for legacy rows from before CompletedAt was written, so
// it's covered explicitly.
func TestFilterCompletedByWindow(t *testing.T) {
	now := time.Now()
	hoursAgo := func(h int) time.Time { return now.Add(time.Duration(-h) * time.Hour) }
	completedAt := func(h int) *time.Time { t := hoursAgo(h); return &t }

	rows := []database.DownloadQueue{
		{ASIN: "recent", CompletedAt: completedAt(1)},       // 1h ago
		{ASIN: "yesterday", CompletedAt: completedAt(23)},   // just under 24h
		{ASIN: "two-days", CompletedAt: completedAt(48)},    // 2 days
		{ASIN: "ten-days", CompletedAt: completedAt(10 * 24)}, // 10 days
		{ASIN: "old", CompletedAt: completedAt(45 * 24)},    // 45 days
		// CompletedAt nil → falls back to UpdatedAt
		{ASIN: "legacy-recent", UpdatedAt: hoursAgo(2)},
	}

	tests := []struct {
		window string
		wantIn []string // ASINs expected to survive the filter
	}{
		{window: "24h", wantIn: []string{"recent", "yesterday", "legacy-recent"}},
		{window: "7d", wantIn: []string{"recent", "yesterday", "two-days", "legacy-recent"}},
		{window: "30d", wantIn: []string{"recent", "yesterday", "two-days", "ten-days", "legacy-recent"}},
		{window: "all", wantIn: []string{"recent", "yesterday", "two-days", "ten-days", "old", "legacy-recent"}},
	}
	for _, tt := range tests {
		t.Run(tt.window, func(t *testing.T) {
			got := filterCompletedByWindow(rows, tt.window)
			gotASINs := make([]string, 0, len(got))
			for _, r := range got {
				gotASINs = append(gotASINs, r.ASIN)
			}
			if !sameStringSet(gotASINs, tt.wantIn) {
				t.Fatalf("window %q: got ASINs %v, want %v", tt.window, gotASINs, tt.wantIn)
			}
		})
	}
}

// TestFirstRunGate covers the path-allowlist + method gate. The four
// app pages should 303 to /setup when the onboarded flag isn't set;
// everything else passes through. POSTs always pass through so the
// wizard's own form submits and the auth callback can land.
func TestFirstRunGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newWebTestDB(t)
	s := &Server{db: db} // onboarded setting absent → isFirstRun = true

	tests := []struct {
		name           string
		method         string
		path           string
		wantStatus     int
		wantLocation   string
	}{
		{name: "GET /", method: http.MethodGet, path: "/", wantStatus: http.StatusSeeOther, wantLocation: "/setup"},
		{name: "GET /library", method: http.MethodGet, path: "/library", wantStatus: http.StatusSeeOther, wantLocation: "/setup"},
		{name: "GET /downloads", method: http.MethodGet, path: "/downloads", wantStatus: http.StatusSeeOther, wantLocation: "/setup"},
		{name: "GET /destinations", method: http.MethodGet, path: "/destinations", wantStatus: http.StatusSeeOther, wantLocation: "/setup"},
		// Pass-through routes — these must remain reachable so users
		// can complete the wizard or escape it to fix things.
		{name: "GET /setup", method: http.MethodGet, path: "/setup", wantStatus: http.StatusOK},
		{name: "GET /settings", method: http.MethodGet, path: "/settings", wantStatus: http.StatusOK},
		{name: "GET /diagnostics", method: http.MethodGet, path: "/diagnostics", wantStatus: http.StatusOK},
		{name: "GET /api/foo", method: http.MethodGet, path: "/api/foo", wantStatus: http.StatusOK},
		{name: "GET /static/whatever", method: http.MethodGet, path: "/static/whatever", wantStatus: http.StatusOK},
		// POSTs to gated paths bypass the gate so wizard forms work.
		{name: "POST /", method: http.MethodPost, path: "/", wantStatus: http.StatusOK},
		{name: "POST /library", method: http.MethodPost, path: "/library", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(s.firstRunGate)
			// Catch-all handler returns 200 so we can tell the gate
			// passed through (vs aborting with a redirect).
			r.Any("/*any", func(c *gin.Context) {
				c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantLocation != "" {
				if got := w.Header().Get("Location"); got != tt.wantLocation {
					t.Fatalf("Location = %q, want %q", got, tt.wantLocation)
				}
			}
		})
	}
}

// TestFirstRunGate_OnceOnboarded verifies the gate passes everything
// through after the onboarded flag is set. We set the setting to "1"
// (the value handleSetupFinish writes) and re-issue the GETs that
// would otherwise redirect.
func TestFirstRunGate_OnceOnboarded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newWebTestDB(t)
	s := &Server{db: db}
	if err := db.SetSetting(t.Context(), settingKeyOnboarded, "1"); err != nil {
		t.Fatalf("SetSetting() error = %v", err)
	}

	for _, path := range []string{"/", "/library", "/downloads", "/destinations"} {
		t.Run(path, func(t *testing.T) {
			r := gin.New()
			r.Use(s.firstRunGate)
			r.Any("/*any", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (onboarded should pass through); Location=%q", w.Code, w.Header().Get("Location"))
			}
		})
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	want := make(map[string]int, len(b))
	for _, s := range b {
		want[s]++
	}
	for _, s := range a {
		if want[s] == 0 {
			return false
		}
		want[s]--
	}
	return true
}
