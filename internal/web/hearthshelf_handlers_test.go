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

// The poller must carry the device code in hx-vals, NOT in hidden <input>s.
//
// HTMX serializes form fields from an enclosing <form>; loose inputs inside a
// bare <div> are not sent. The first version used hidden inputs, so every poll
// arrived with an empty device_code and the handler gave up instantly with
// "lost track of this connection" - while the user had in fact approved
// successfully on the HearthShelf side.
func TestHearthShelfPollerCarriesDeviceCodeInHxVals(t *testing.T) {
	poller := renderHSPollerDiv("dev-code-abc", 5)
	if !strings.Contains(poller, "hx-vals=") {
		t.Fatalf("poller must use hx-vals so HTMX actually sends the values: %s", poller)
	}
	if !strings.Contains(poller, `"device_code":"dev-code-abc"`) {
		t.Errorf("hx-vals does not carry the device code: %s", poller)
	}
	if !strings.Contains(poller, `"interval":"5"`) {
		t.Errorf("hx-vals does not carry the interval: %s", poller)
	}
	// Hidden inputs in a bare div are exactly the bug - they look right and send
	// nothing.
	if strings.Contains(poller, "<input") {
		t.Errorf("poller uses hidden inputs, which HTMX will not serialize: %s", poller)
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

// A failed connect must NOT fire dest-created.
//
// dest-created closes the modal and refreshes the destination list. Firing it
// unconditionally tore the modal down the instant the poll returned, taking the
// error message with it - so a user watching a real failure could not read it,
// let alone copy it. The error is the one thing they need at that moment.
//
// These assert against the rendered markup rather than the handler, because the
// bug was entirely in what the fragment told the browser to do.
func TestConnectResultOnlyClosesModalOnSuccess(t *testing.T) {
	cases := []struct {
		name      string
		connected []string
		failed    []string
		wantClose bool
	}{
		{"all connected", []string{"Home"}, nil, true},
		{"all failed", nil, []string{"Home (boom)"}, false},
		{"partial", []string{"Home"}, []string{"Cabin (boom)"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderHSConnectResult(tc.connected, tc.failed, nil)
			fires := strings.Contains(out, "dest-created")
			// The partial case offers a "Done" button that fires it on CLICK,
			// which is fine - what must not happen is firing it automatically.
			auto := strings.Contains(out, "try{document.body.dispatchEvent(new CustomEvent('dest-created'));}catch(e){}\n") ||
				(fires && !strings.Contains(out, "onclick"))
			if tc.wantClose && !auto {
				t.Errorf("success should close the modal; got: %s", out)
			}
			if !tc.wantClose && auto && tc.connected == nil {
				t.Errorf("failure must not auto-close the modal; got: %s", out)
			}
			if len(tc.failed) > 0 && !strings.Contains(out, "Could not finish") {
				t.Error("failure message missing")
			}
			if len(tc.failed) > 0 && !strings.Contains(out, "Try again") {
				t.Error("failed connect should offer a way to retry")
			}
		})
	}
}

// When a server has several audiobook libraries we pick one to satisfy the
// schema's library_id requirement - and must say so, rather than silently
// choosing on the user's behalf.
func TestConnectResultReportsPickedLibrary(t *testing.T) {
	out := renderHSConnectResult([]string{"Home"}, nil, []string{"Home -> Audiobooks"})
	if !strings.Contains(out, "Picked a library for you") {
		t.Errorf("multi-library choice not surfaced: %s", out)
	}
	if !strings.Contains(out, "Home -&gt; Audiobooks") && !strings.Contains(out, "Home -> Audiobooks") {
		t.Errorf("picked library not named: %s", out)
	}
}

// A HearthShelf connection is stored as an `abs` destination - HearthShelf
// serves the Audiobookshelf API, so the row's type is abs and every push path
// treats it as one. That is deliberate, but it means the card would show an
// Audiobookshelf logo for something the user connected by clicking HearthShelf.
// ViaHearthShelf exists to fix the LABEL without touching the behaviour.
func TestDestinationCardBadgesHearthShelfConnections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{router: gin.New()}
	s.setupTemplates()
	s.router.GET("/card", func(c *gin.Context) {
		c.HTML(200, "destination_card_body", gin.H{
			"Dest": destinationSummaryView{
				ID:             "d1",
				DisplayName:    "Unraid",
				Type:           "abs",
				TypeLabel:      "Audiobookshelf",
				ViaHearthShelf: c.Query("hs") == "1",
				Enabled:        true,
				Health:         "healthy",
			},
		})
	})

	render := func(hs string) string {
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, httptest.NewRequest("GET", "/card?hs="+hs, nil))
		if w.Code != 200 {
			t.Fatalf("card render returned %d", w.Code)
		}
		return w.Body.String()
	}

	viaHS := render("1")
	if !strings.Contains(viaHS, "/static/hearthshelf.png") {
		t.Errorf("HearthShelf connection should show the HearthShelf logo: %s", viaHS)
	}
	if strings.Contains(viaHS, "/static/audiobookshelf.png") {
		t.Errorf("HearthShelf connection must not show the Audiobookshelf logo: %s", viaHS)
	}

	// A hand-configured ABS destination is untouched.
	plain := render("0")
	if !strings.Contains(plain, "/static/audiobookshelf.png") {
		t.Errorf("a plain abs destination should still show the ABS logo: %s", plain)
	}
	if strings.Contains(plain, "/static/hearthshelf.png") {
		t.Errorf("a plain abs destination must not be badged HearthShelf: %s", plain)
	}
}

// The stored URL for a HearthShelf connection is chosen for reachability, not
// for reading: on a container host it is a Docker bridge address, on a home LAN
// a private IP. Both route correctly, but shown bare they look like a
// misconfiguration - nothing about "172.17.0.13" says "the Unraid server I
// connected through HearthShelf".
func TestDescribeHearthShelfAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://172.17.0.13", "via HearthShelf - direct on this Docker network"},
		{"http://172.20.5.4:8080", "via HearthShelf - direct on this Docker network"},
		{"http://192.168.1.50:13378", "via HearthShelf - direct on your local network"},
		{"http://10.0.0.8", "via HearthShelf - direct on your local network"},
		{"http://books.local", "via HearthShelf - direct on your local network"},
		// 172.16.x is RFC1918 but NOT the Docker bridge range.
		{"http://172.16.0.9", "via HearthShelf - direct on your local network"},
		{"https://books.example.com", "via HearthShelf"},
		{"", "via HearthShelf"},
	}
	for _, tc := range cases {
		if got := describeHearthShelfAddress(tc.in); got != tc.want {
			t.Errorf("describeHearthShelfAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The card shows the explanation, and keeps the raw address discoverable for
// anyone debugging a route.
func TestDestinationCardExplainsHearthShelfAddress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{router: gin.New()}
	s.setupTemplates()
	s.router.GET("/card", func(c *gin.Context) {
		c.HTML(200, "destination_card_body", gin.H{
			"Dest": destinationSummaryView{
				ID: "d1", DisplayName: "Unraid",
				Type: "abs", TypeLabel: "Audiobookshelf",
				URL:            "http://172.17.0.13",
				ViaHearthShelf: true,
				AddressNote:    "via HearthShelf - direct on this Docker network",
				Enabled:        true, Health: "healthy",
			},
		})
	})
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest("GET", "/card", nil))
	out := w.Body.String()

	if !strings.Contains(out, "direct on this Docker network") {
		t.Errorf("card should explain the address: %s", out)
	}
	// The raw address stays available on hover rather than being thrown away.
	if !strings.Contains(out, `title="http://172.17.0.13"`) {
		t.Errorf("card should keep the raw address as a title: %s", out)
	}
}
