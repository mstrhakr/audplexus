package hearthshelf

// Renewal is the path that keeps a one-click destination working after its
// credential lapses. It runs unattended on every push, so the failure modes
// that matter are the silent ones: a rotated refresh token dropped on the
// floor (locks the user out at the NEXT renewal, long after the change), an
// in-grace empty token written over a good one (same), and a revoked
// connection retried forever against a server that already said no.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mstrhakr/audplexus/internal/crypto"
	"github.com/mstrhakr/audplexus/internal/database"
)

// fakeDB records destination updates so a test can assert what was persisted.
// StubDB's destination methods are no-ops, which would hide exactly the writes
// these tests exist to check.
type fakeDB struct {
	*database.StubDB
	dest    *database.LibraryDestination
	updates int
}

func newFakeDB(dest *database.LibraryDestination) *fakeDB {
	return &fakeDB{StubDB: database.NewStubDB(), dest: dest}
}

func (f *fakeDB) GetLibraryDestination(_ context.Context, id string) (*database.LibraryDestination, error) {
	if f.dest != nil && f.dest.ID == id {
		return f.dest, nil
	}
	return nil, nil
}

func (f *fakeDB) UpdateLibraryDestination(_ context.Context, d *database.LibraryDestination) error {
	f.updates++
	f.dest = d
	return nil
}

func testBox(t *testing.T) *crypto.Box {
	t.Helper()
	box, err := crypto.NewBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// seed writes one connection bound to the destination row.
func seed(t *testing.T, db database.Database, box *crypto.Box, c Connection) {
	t.Helper()
	if err := SaveConnections(context.Background(), db, box, []Connection{c}); err != nil {
		t.Fatal(err)
	}
}

func expiredAt() int64 { return time.Now().Add(-time.Minute).Unix() }

// A rotated refresh token must land in storage. Keeping the old one means the
// next renewal presents a token the server has already retired.
func TestRenewPersistsRotatedRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "a2", "refresh_token": "r2",
			"abs_api_key": "abs-new", "expires_in": 900,
		})
	}))
	defer srv.Close()

	box := testBox(t)
	dest := &database.LibraryDestination{
		ID: "dest-1", Type: database.LibraryDestinationTypeABS, APIKey: "abs-old",
	}
	db := newFakeDB(dest)
	seed(t, db, box, Connection{
		ServerID: "s1", DestinationID: "dest-1", BaseURL: srv.URL,
		RefreshToken: "r1", CredentialExpiresAt: expiredAt(),
	})

	res, err := RenewDestination(context.Background(), db, box, New(""), dest)
	if err != nil || !res.Renewed {
		t.Fatalf("expected renewal, got res=%+v err=%v", res, err)
	}
	if dest.APIKey != "abs-new" {
		t.Fatalf("destination key not updated: %q", dest.APIKey)
	}
	conns, err := LoadConnections(context.Background(), db, box)
	if err != nil {
		t.Fatal(err)
	}
	if conns[0].RefreshToken != "r2" {
		t.Fatalf("rotated refresh token not persisted: %q", conns[0].RefreshToken)
	}
	if conns[0].CredentialExpiresAt <= time.Now().Unix() {
		t.Fatalf("expiry not advanced: %d", conns[0].CredentialExpiresAt)
	}
}

// An absent refresh token means "keep the one you have". Writing the empty
// value over the stored one would lock us out on the following renewal.
func TestRenewKeepsRefreshTokenWhenNoneReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "a2", "abs_api_key": "abs-new", "expires_in": 900,
		})
	}))
	defer srv.Close()

	box := testBox(t)
	dest := &database.LibraryDestination{
		ID: "dest-1", Type: database.LibraryDestinationTypeABS, APIKey: "abs-old",
	}
	db := newFakeDB(dest)
	seed(t, db, box, Connection{
		ServerID: "s1", DestinationID: "dest-1", BaseURL: srv.URL,
		RefreshToken: "r1", CredentialExpiresAt: expiredAt(),
	})

	if _, err := RenewDestination(context.Background(), db, box, New(""), dest); err != nil {
		t.Fatal(err)
	}
	conns, _ := LoadConnections(context.Background(), db, box)
	if conns[0].RefreshToken != "r1" {
		t.Fatalf("in-grace refresh clobbered the stored token: %q", conns[0].RefreshToken)
	}
}

// A revoked connection must be recorded, so later pushes stop re-attempting a
// refresh that cannot succeed.
func TestRenewMarksRevokedConnection(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer srv.Close()

	box := testBox(t)
	dest := &database.LibraryDestination{
		ID: "dest-1", Type: database.LibraryDestinationTypeABS, APIKey: "abs-old",
	}
	db := newFakeDB(dest)
	seed(t, db, box, Connection{
		ServerID: "s1", DestinationID: "dest-1", BaseURL: srv.URL,
		RefreshToken: "r1", CredentialExpiresAt: expiredAt(),
	})

	res, err := RenewDestination(context.Background(), db, box, New(""), dest)
	if err != nil || !res.NeedsReconnect {
		t.Fatalf("expected NeedsReconnect, got res=%+v err=%v", res, err)
	}
	if dest.APIKey != "abs-old" {
		t.Fatalf("credential should be left alone on revocation: %q", dest.APIKey)
	}

	// Second pass must not hit the server again.
	res2, err := RenewDestination(context.Background(), db, box, New(""), dest)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Renewed {
		t.Fatal("revoked connection should not report a renewal")
	}
	if calls != 1 {
		t.Fatalf("revoked connection retried the server %d times, want 1", calls)
	}
}

// A credential that is not near expiry must not trigger a round-trip - renewal
// runs on every push, so a speculative refresh would hit every server every time.
func TestRenewSkipsWhenNotNearExpiry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
	}))
	defer srv.Close()

	box := testBox(t)
	dest := &database.LibraryDestination{
		ID: "dest-1", Type: database.LibraryDestinationTypeABS, APIKey: "abs-old",
	}
	db := newFakeDB(dest)
	seed(t, db, box, Connection{
		ServerID: "s1", DestinationID: "dest-1", BaseURL: srv.URL,
		RefreshToken: "r1",
		// Comfortably in the future, beyond renewSkew.
		CredentialExpiresAt: time.Now().Add(time.Hour).Unix(),
	})

	res, err := RenewDestination(context.Background(), db, box, New(""), dest)
	if err != nil || res.Renewed {
		t.Fatalf("expected no renewal, got res=%+v err=%v", res, err)
	}
	if calls != 0 {
		t.Fatalf("refreshed a credential that was not near expiry (%d calls)", calls)
	}
}

// A hand-configured Audiobookshelf destination has no HearthShelf connection
// behind it and must pass through untouched.
func TestRenewIgnoresNonHearthShelfDestination(t *testing.T) {
	box := testBox(t)
	dest := &database.LibraryDestination{
		ID: "other", Type: database.LibraryDestinationTypeABS, APIKey: "typed-by-hand",
	}
	db := newFakeDB(dest)
	// A connection exists, but it points at a DIFFERENT destination row.
	seed(t, db, box, Connection{
		ServerID: "s1", DestinationID: "dest-1", BaseURL: "http://unused",
		RefreshToken: "r1", CredentialExpiresAt: expiredAt(),
	})

	res, err := RenewDestination(context.Background(), db, box, New(""), dest)
	if err != nil || res.Renewed || res.NeedsReconnect {
		t.Fatalf("unrelated destination touched: res=%+v err=%v", res, err)
	}
	if dest.APIKey != "typed-by-hand" {
		t.Fatalf("credential was modified: %q", dest.APIKey)
	}
	if db.updates != 0 {
		t.Fatalf("unrelated destination was written %d times", db.updates)
	}
}
