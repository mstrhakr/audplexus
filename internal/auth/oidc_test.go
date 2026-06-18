package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func newOIDCTestDB(t *testing.T) database.Database {
	t.Helper()
	db, err := database.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestUpsertOIDCUser_ProvisionAndReuse(t *testing.T) {
	db := newOIDCTestDB(t)
	ctx := context.Background()

	u1, err := UpsertOIDCUser(ctx, db, "https://idp", "sub-1", "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if u1.Username != "alice" || u1.AuthSource != "oidc" {
		t.Fatalf("unexpected user: %+v", u1)
	}

	// Second login with the same subject reuses the same row, not a new one.
	u2, err := UpsertOIDCUser(ctx, db, "https://idp", "sub-1", "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if u2.ID != u1.ID {
		t.Fatalf("expected same user id, got %d vs %d", u2.ID, u1.ID)
	}
}

func TestUpsertOIDCUser_UsernameCollisionSuffixed(t *testing.T) {
	db := newOIDCTestDB(t)
	ctx := context.Background()

	// A forms admin already owns "admin".
	if _, err := CreateOrUpdateAdmin(ctx, db, "admin", "password123"); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	// An OIDC user whose preferred_username is also "admin" must still
	// provision — under a disambiguated name, not by clobbering the admin.
	u, err := UpsertOIDCUser(ctx, db, "https://idp", "sub-x", "admin", "x@example.com")
	if err != nil {
		t.Fatalf("provision colliding: %v", err)
	}
	if u.Username == "admin" {
		t.Fatal("OIDC user collided with the forms admin username")
	}
	if u.OIDCSubject != "sub-x" {
		t.Fatalf("unexpected subject: %+v", u)
	}

	// The forms admin is untouched and still password-authenticatable.
	admin, _ := db.GetUserByUsername(ctx, "admin")
	if admin == nil || admin.AuthSource != "forms" {
		t.Fatalf("forms admin damaged: %+v", admin)
	}
}

func TestUpsertOIDCUser_RequiresIssuerAndSubject(t *testing.T) {
	db := newOIDCTestDB(t)
	ctx := context.Background()
	if _, err := UpsertOIDCUser(ctx, db, "", "", "x", "y"); err == nil {
		t.Fatal("expected error for empty issuer/subject")
	}
}
