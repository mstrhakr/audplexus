package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The picker must actually OFFER HearthShelf.
//
// This test exists because the connect flow shipped once with the client
// package built, compiling and unit-tested, but never referenced by any handler
// or template - so "go build" and "go test" both passed while the option was
// invisible in the UI. A test that renders the picker and looks for the entry
// point is what would have caught that.
func TestDestinationPickerOffersHearthShelf(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{router: gin.New()}
	s.setupTemplates()
	s.router.GET("/picker", func(c *gin.Context) {
		c.HTML(200, "destination_picker_body", gin.H{})
	})

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest("GET", "/picker", nil))
	if w.Code != 200 {
		t.Fatalf("picker returned %d", w.Code)
	}
	out := w.Body.String()

	if !strings.Contains(out, "HearthShelf") {
		t.Error("picker does not mention HearthShelf")
	}
	// The button must post to the connect flow, not the generic type form -
	// HearthShelf is not a destination type, it is a way of configuring one.
	if !strings.Contains(out, "/destinations/hearthshelf/start") {
		t.Error("picker does not wire the HearthShelf button to the connect flow")
	}
	// The other backends must survive alongside it.
	for _, want := range []string{"plex", "emby", "jellyfin", `value="abs"`} {
		if !strings.Contains(out, want) {
			t.Errorf("picker lost the %s option", want)
		}
	}
}

// The poller has to swap into a container the start response actually rendered,
// or the first poll silently goes nowhere and the flow hangs on "enter this
// code" forever.
func TestHearthShelfPollerTargetsRenderedContainer(t *testing.T) {
	poller := renderHSPollerDiv("dc-123", 5)
	if !strings.Contains(poller, `hx-target="#hs-connect-result"`) {
		t.Fatalf("poller does not target #hs-connect-result: %s", poller)
	}
	if !strings.Contains(poller, "dc-123") {
		t.Error("poller does not carry the device code")
	}
	if !strings.Contains(poller, `hx-trigger="load delay:5s"`) {
		t.Errorf("poller does not honour the interval: %s", poller)
	}
}

// A slow_down response must lengthen the interval rather than keep hammering -
// RFC 8628 penalises a client that ignores it, and the reference implementation
// should model good behaviour.
func TestHearthShelfPollerBacksOff(t *testing.T) {
	if !strings.Contains(renderHSPollerDiv("dc", 10), "delay:10s") {
		t.Error("poller ignored a longer interval")
	}
	// A nonsense interval must not produce a hot loop.
	if !strings.Contains(renderHSPollerDiv("dc", 0), "delay:5s") {
		t.Error("poller did not fall back to a safe interval")
	}
}
