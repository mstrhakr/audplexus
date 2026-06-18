package database

import (
	"context"
	"testing"
)

// Verifies migration 011 applied (the new columns exist + are scannable) and
// that OIDC lookup/insert round-trips through the same UpsertUser path forms
// users use.
func TestOIDCUserRoundTrip(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	// No match before insert.
	got, err := db.GetUserByOIDC(ctx, "https://idp.example", "sub-123")
	if err != nil {
		t.Fatalf("GetUserByOIDC (empty): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}

	u := &User{
		Username:    "alice",
		Identifier:  "id-1",
		OIDCSubject: "sub-123",
		OIDCIssuer:  "https://idp.example",
		AuthSource:  "oidc",
	}
	if err := db.UpsertUser(ctx, u); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("expected ID assigned")
	}

	got, err = db.GetUserByOIDC(ctx, "https://idp.example", "sub-123")
	if err != nil {
		t.Fatalf("GetUserByOIDC: %v", err)
	}
	if got == nil || got.OIDCSubject != "sub-123" || got.AuthSource != "oidc" {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	// A forms user (empty subject) must never match an OIDC lookup, even with
	// the same empty-string issuer — the partial index + the subject<>'' guard
	// protect this.
	f := &User{Username: "bob", Identifier: "id-2", Password: "x", Salt: "y", Iterations: 1}
	if err := db.UpsertUser(ctx, f); err != nil {
		t.Fatalf("UpsertUser forms: %v", err)
	}
	if got, _ := db.GetUserByOIDC(ctx, "", ""); got != nil {
		t.Fatalf("forms user leaked into OIDC lookup: %+v", got)
	}
	// And forms user defaults auth_source to 'forms'.
	reload, _ := db.GetUserByUsername(ctx, "bob")
	if reload == nil || reload.AuthSource != "forms" {
		t.Fatalf("expected forms auth_source, got %+v", reload)
	}
}
