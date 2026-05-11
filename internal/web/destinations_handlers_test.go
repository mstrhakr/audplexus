package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
)

func testFormContext(t *testing.T, form url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request = req
	return c, w
}

func TestRenderEmbyLikeDiscoverBranches(t *testing.T) {
	_, wErr := testFormContext(t, url.Values{})
	cErr, _ := gin.CreateTestContext(wErr)
	cErr.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	renderEmbyLikeDiscover(cErr, nil, "books", "Jellyfin", "picker", "boom")
	if !strings.Contains(wErr.Body.String(), "Failed.") {
		t.Fatalf("expected failed banner, got %q", wErr.Body.String())
	}

	_, wEmpty := testFormContext(t, url.Values{})
	cEmpty, _ := gin.CreateTestContext(wEmpty)
	cEmpty.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	renderEmbyLikeDiscover(cEmpty, nil, "books", "Jellyfin", "picker", "")
	if !strings.Contains(wEmpty.Body.String(), "No libraries visible") {
		t.Fatalf("expected empty warning, got %q", wEmpty.Body.String())
	}

	libs := []mediaServerLibrary{
		{ID: "1", Name: "Books", Kind: "books", Path: "/media/books"},
		{ID: "2", Name: "Movies", Kind: "movies", Path: "/media/movies"},
	}
	_, wOK := testFormContext(t, url.Values{})
	cOK, _ := gin.CreateTestContext(wOK)
	cOK.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	renderEmbyLikeDiscover(cOK, libs, "books", "Jellyfin", "picker", "")
	body := wOK.Body.String()
	if !strings.Contains(body, "Connected.") || !strings.Contains(body, "picker") || !strings.Contains(body, "Books (books)") {
		t.Fatalf("expected connected picker payload, got %q", body)
	}
	if strings.Contains(body, "Movies (movies)") {
		t.Fatalf("expected non-matching kind filtered out, got %q", body)
	}

	_, wWarn := testFormContext(t, url.Values{})
	cWarn, _ := gin.CreateTestContext(wWarn)
	cWarn.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	renderEmbyLikeDiscover(cWarn, libs, "audiobooks", "Emby", "picker2", "")
	bodyWarn := wWarn.Body.String()
	if !strings.Contains(bodyWarn, "CollectionType=audiobooks") || !strings.Contains(bodyWarn, "Movies (movies)") {
		t.Fatalf("expected fallback warning and all libraries, got %q", bodyWarn)
	}

	if got := wWarn.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestRenderDiscoverResultBranches(t *testing.T) {
	ctxErr, wErr := testFormContext(t, url.Values{})
	renderDiscoverResult(ctxErr, nil, "cannot connect")
	if !strings.Contains(wErr.Body.String(), "Failed.") {
		t.Fatalf("expected failed fragment")
	}

	ctxNone, wNone := testFormContext(t, url.Values{})
	renderDiscoverResult(ctxNone, nil, "")
	if !strings.Contains(wNone.Body.String(), "No libraries visible") {
		t.Fatalf("expected no-libraries fragment")
	}

	onlyNonBook := []mediaserver.ABSLibrary{{ID: "p1", Name: "Podcasts", MediaType: "podcast", Path: "/p"}}
	ctxNonBook, wNonBook := testFormContext(t, url.Values{})
	renderDiscoverResult(ctxNonBook, onlyNonBook, "")
	if !strings.Contains(wNonBook.Body.String(), "no book libraries") {
		t.Fatalf("expected non-book warning, got %q", wNonBook.Body.String())
	}

	libs := []mediaserver.ABSLibrary{
		{ID: "b1", Name: "Books", MediaType: "book", Path: "/books"},
		{ID: "p1", Name: "Podcasts", MediaType: "podcast", Path: "/podcasts"},
	}
	ctxOK, wOK := testFormContext(t, url.Values{})
	renderDiscoverResult(ctxOK, libs, "")
	body := wOK.Body.String()
	if !strings.Contains(body, "Found 1 book library") || !strings.Contains(body, "non-book hidden") || !strings.Contains(body, "value=\"b1\"") {
		t.Fatalf("expected successful picker with filtered rows, got %q", body)
	}
}

func TestRenderHelpersAndEscaping(t *testing.T) {
	ctxOK, wOK := testFormContext(t, url.Values{})
	renderTestResult(ctxOK, true, "all good", "")
	if !strings.Contains(wOK.Body.String(), "Connected.") {
		t.Fatalf("expected connected test result")
	}

	ctxFail, wFail := testFormContext(t, url.Values{})
	renderTestResult(ctxFail, false, "", "bad")
	if !strings.Contains(wFail.Body.String(), "Failed.") {
		t.Fatalf("expected failed test result")
	}

	escaped := htmlEscape(`a&<b>"'`)
	if escaped != "a&amp;&lt;b&gt;&quot;&#39;" {
		t.Fatalf("htmlEscape output mismatch: %q", escaped)
	}

	ctxSensitive, wSensitive := testFormContext(t, url.Values{})
	writeSensitiveHTML(ctxSensitive, "payload")
	if wSensitive.Body.String() != "payload" {
		t.Fatalf("writeSensitiveHTML body mismatch")
	}
	if got := wSensitive.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}

	poller := renderPlexPollerDiv(42, "pc")
	if !strings.Contains(poller, "/destinations/plex/pin/poll") || !strings.Contains(poller, `"pin_id":"42"`) {
		t.Fatalf("renderPlexPollerDiv output mismatch: %q", poller)
	}

	js := jsString(`a"</script><b`)
	if !strings.HasPrefix(js, `"`) || !strings.Contains(js, `\u003c/script\u003e`) {
		t.Fatalf("jsString must JSON-escape script-breaking content, got %q", js)
	}
}

func TestResolveSecretsFromForm(t *testing.T) {
	db := newWebTestDB(t)
	s := &Server{db: db}
	ctx := context.Background()

	id := uuid.NewString()
	err := db.CreateLibraryDestination(ctx, &database.LibraryDestination{
		ID:          id,
		Type:        database.LibraryDestinationTypeABS,
		DisplayName: "ABS",
		Enabled:     true,
		URL:         "http://abs.local",
		APIKey:      "saved-api-key",
		LibraryID:   "lib-1",
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateLibraryDestination abs: %v", err)
	}

	idPlex := uuid.NewString()
	err = db.CreateLibraryDestination(ctx, &database.LibraryDestination{
		ID:            idPlex,
		Type:          database.LibraryDestinationTypePlex,
		DisplayName:   "Plex",
		Enabled:       true,
		URL:           "http://plex.local:32400",
		PlexToken:     "saved-plex-token",
		PlexSectionID: "7",
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateLibraryDestination plex: %v", err)
	}

	// Direct form key wins.
	cDirect, _ := testFormContext(t, url.Values{"api_key": {"from-form"}})
	apiKey, err := s.resolveAPIKeyFromForm(cDirect)
	if err != nil || apiKey != "from-form" {
		t.Fatalf("resolveAPIKeyFromForm direct = (%q,%v)", apiKey, err)
	}

	// Saved key fallback via :id.
	cFallback, _ := testFormContext(t, url.Values{})
	cFallback.Params = gin.Params{{Key: "id", Value: id}}
	apiKey, err = s.resolveAPIKeyFromForm(cFallback)
	if err != nil || apiKey != "saved-api-key" {
		t.Fatalf("resolveAPIKeyFromForm fallback = (%q,%v)", apiKey, err)
	}

	// Missing key yields error.
	cErr, _ := testFormContext(t, url.Values{})
	_, err = s.resolveAPIKeyFromForm(cErr)
	if err == nil {
		t.Fatalf("resolveAPIKeyFromForm missing key should error")
	}

	// Plex variants.
	cPlexDirect, _ := testFormContext(t, url.Values{"plex_token": {"plex-form"}})
	plexToken, err := s.resolvePlexTokenFromForm(cPlexDirect)
	if err != nil || plexToken != "plex-form" {
		t.Fatalf("resolvePlexTokenFromForm direct = (%q,%v)", plexToken, err)
	}

	cPlexFallback, _ := testFormContext(t, url.Values{})
	cPlexFallback.Params = gin.Params{{Key: "id", Value: idPlex}}
	plexToken, err = s.resolvePlexTokenFromForm(cPlexFallback)
	if err != nil || plexToken != "saved-plex-token" {
		t.Fatalf("resolvePlexTokenFromForm fallback = (%q,%v)", plexToken, err)
	}

	cPlexErr, _ := testFormContext(t, url.Values{})
	_, err = s.resolvePlexTokenFromForm(cPlexErr)
	if err == nil {
		t.Fatalf("resolvePlexTokenFromForm missing token should error")
	}
}

func TestUniqueDisplayNameAndDestinationFromForm(t *testing.T) {
	db := newWebTestDB(t)
	s := &Server{db: db}
	ctx := context.Background()

	err := db.CreateLibraryDestination(ctx, &database.LibraryDestination{
		ID:            "one",
		Type:          database.LibraryDestinationTypePlex,
		DisplayName:   "Plex",
		Enabled:       true,
		URL:           "http://plex-1",
		PlexToken:     "t1",
		PlexSectionID: "1",
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateLibraryDestination one: %v", err)
	}
	err = db.CreateLibraryDestination(ctx, &database.LibraryDestination{
		ID:            "two",
		Type:          database.LibraryDestinationTypePlex,
		DisplayName:   "Plex (2)",
		Enabled:       true,
		URL:           "http://plex-2",
		PlexToken:     "t2",
		PlexSectionID: "2",
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateLibraryDestination two: %v", err)
	}

	if got := s.uniqueDisplayName(ctx, "Plex", ""); got != "Plex (3)" {
		t.Fatalf("uniqueDisplayName collision = %q, want Plex (3)", got)
	}
	if got := s.uniqueDisplayName(ctx, "Plex", "one"); got != "Plex" {
		t.Fatalf("uniqueDisplayName excluding self = %q, want Plex", got)
	}

	// destinationFromForm: invalid type.
	cInvalid, _ := testFormContext(t, url.Values{"type": {"bad"}, "url": {"http://x"}})
	if _, err := s.destinationFromForm(cInvalid, ""); err == nil {
		t.Fatalf("destinationFromForm invalid type should error")
	}

	// destinationFromForm: plex create happy path.
	cPlex, _ := testFormContext(t, url.Values{
		"type":            {"plex"},
		"display_name":    {"Main Plex"},
		"url":             {"http://plex.local:32400/"},
		"plex_token":      {"ptok"},
		"plex_section_id": {"7"},
	})
	d, err := s.destinationFromForm(cPlex, "")
	if err != nil {
		t.Fatalf("destinationFromForm plex create error: %v", err)
	}
	if d.Type != database.LibraryDestinationTypePlex || d.URL != "http://plex.local:32400" || d.PlexToken != "ptok" || d.PlexSectionID != "7" {
		t.Fatalf("destinationFromForm plex mismatch: %+v", d)
	}

	// destinationFromForm: missing URL error.
	cNoURL, _ := testFormContext(t, url.Values{"type": {"plex"}, "plex_token": {"x"}, "plex_section_id": {"1"}})
	if _, err := s.destinationFromForm(cNoURL, ""); err == nil {
		t.Fatalf("destinationFromForm missing URL should error")
	}

	// destinationFromForm: create ABS missing API key should error.
	cABSNoKey, _ := testFormContext(t, url.Values{
		"type":       {"abs"},
		"url":        {"http://abs.local"},
		"library_id": {"lib-1"},
	})
	if _, err := s.destinationFromForm(cABSNoKey, ""); err == nil {
		t.Fatalf("destinationFromForm abs create without api key should error")
	}

	// destinationFromForm: edit path allows empty API key (caller carries existing value over).
	cABSEdit, _ := testFormContext(t, url.Values{
		"url":        {"http://abs.local/"},
		"library_id": {"lib-1"},
	})
	dEdit, err := s.destinationFromForm(cABSEdit, "abs")
	if err != nil {
		t.Fatalf("destinationFromForm abs edit should succeed: %v", err)
	}
	if dEdit.Type != database.LibraryDestinationTypeABS || dEdit.URL != "http://abs.local" || dEdit.APIKey != "" {
		t.Fatalf("destinationFromForm abs edit mismatch: %+v", dEdit)
	}
}
