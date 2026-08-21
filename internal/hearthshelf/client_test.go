package hearthshelf

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The device-flow errors are RFC 8628 wire format. A client that mistranslates
// them either gives up on a pending authorization or hammers a server that
// asked it to slow down, so each mapping is pinned.
func TestPollDeviceFlowMapsRFC8628Errors(t *testing.T) {
	cases := []struct {
		code string
		want error
	}{
		{"authorization_pending", ErrAuthorizationPending},
		{"slow_down", ErrSlowDown},
		{"access_denied", ErrAccessDenied},
		{"expired_token", ErrExpiredToken},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.code})
			}))
			defer srv.Close()

			c := New(srv.URL)
			_, err := c.PollDeviceFlow(context.Background(), "app", "secret", "dc")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPollDeviceFlowReturnsIntroductions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"introductions": []map[string]string{
				{"server_id": "srv1", "server_url": "https://box.example", "introduction_token": "tok"},
			},
			"scopes": "library:read library:write",
		})
	}))
	defer srv.Close()

	intros, err := New(srv.URL).PollDeviceFlow(context.Background(), "app", "secret", "dc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(intros) != 1 || intros[0].ServerID != "srv1" || intros[0].IntroToken != "tok" {
		t.Fatalf("unexpected introductions: %+v", intros)
	}
}

// A revoked connection must surface as ErrNeedsReconnect, not as a generic
// failure the caller might retry forever.
func TestRefreshRevokedSurfacesReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer srv.Close()

	_, err := New("").Refresh(context.Background(), srv.URL, "app", "refresh")
	if !errors.Is(err, ErrNeedsReconnect) {
		t.Fatalf("got %v, want ErrNeedsReconnect", err)
	}
}

// A rotated refresh token must replace the stored one; an ABSENT one means
// "keep what you have" and must not be mistaken for a failure.
func TestRefreshRotationAndGrace(t *testing.T) {
	withRotation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "a1", "refresh_token": "r2", "expires_in": 900,
		})
	}))
	defer withRotation.Close()

	ts, err := New("").Refresh(context.Background(), withRotation.URL, "app", "r1")
	if err != nil || ts.RefreshToken != "r2" {
		t.Fatalf("rotation not surfaced: %+v err=%v", ts, err)
	}

	noRotation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a2", "expires_in": 900})
	}))
	defer noRotation.Close()

	ts2, err := New("").Refresh(context.Background(), noRotation.URL, "app", "r2")
	if err != nil {
		t.Fatalf("in-grace refresh should succeed: %v", err)
	}
	if ts2.RefreshToken != "" {
		t.Fatalf("expected no new refresh token, got %q", ts2.RefreshToken)
	}
}

// The LAN identity challenge is what stands between us and handing a bearer
// credential to any device that answers on a private IP. The payload format must
// match the server byte-for-byte, so this test pins it.
func TestVerifyServerIdentity(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const serverID = "srv-abc"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Nonce string `json:"nonce"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Exactly as HearthShelf's signIdentityChallenge composes it.
		sig := ed25519.Sign(priv, []byte("hs-identity:v1:"+serverID+":"+body.Nonce))
		_ = json.NewEncoder(w).Encode(map[string]string{
			"server_id": serverID,
			"signature": base64.StdEncoding.EncodeToString(sig),
		})
	}))
	defer srv.Close()

	keyB64 := base64.StdEncoding.EncodeToString(pub)
	ok, err := VerifyServerIdentity(context.Background(), srv.Client(), srv.URL, serverID, keyB64)
	if err != nil || !ok {
		t.Fatalf("genuine server should verify: ok=%v err=%v", ok, err)
	}

	// An impostor holding a DIFFERENT key must fail.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	ok, _ = VerifyServerIdentity(
		context.Background(), srv.Client(), srv.URL, serverID,
		base64.StdEncoding.EncodeToString(otherPub),
	)
	if ok {
		t.Fatal("a server signing with the wrong key must not verify")
	}

	// A response claiming a different server id must fail, so a signature
	// captured from box A cannot be replayed as proof of box B.
	ok, _ = VerifyServerIdentity(context.Background(), srv.Client(), srv.URL, "other-srv", keyB64)
	if ok {
		t.Fatal("a mismatched server id must not verify")
	}
}

// An unverifiable LAN address must be skipped in favour of the public URL rather
// than used anyway - and a server with only a public address must still work.
func TestPreferredAddressFallsBackWhenLANUnverified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	pub, _, _ := ed25519.GenerateKey(nil)
	in := Introduction{
		ServerID:    "srv1",
		LocalURL:    srv.URL,
		IdentityKey: base64.StdEncoding.EncodeToString(pub),
		ServerURL:   "https://public.example",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	got, err := PreferredAddress(ctx, srv.Client(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://public.example" {
		t.Fatalf("expected fallback to public URL, got %q", got)
	}

	only := Introduction{ServerID: "srv2", ServerURL: "https://only.example"}
	got, err = PreferredAddress(ctx, srv.Client(), only)
	if err != nil || got != "https://only.example" {
		t.Fatalf("public-only server should work: %q err=%v", got, err)
	}
}
