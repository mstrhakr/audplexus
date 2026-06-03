package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/mstrhakr/audplexus/internal/database"
)

// CreateOrUpdateAdmin creates the single admin user (or updates the existing
// one's password / username). Called from the setup wizard and from the
// "change password" handler.
//
// The identifier UUID is rotated on every password change so all sessions
// for the user are invalidated (their snapshot no longer matches). The
// acting session, if any, must be re-minted by the caller within the same
// flow to keep them logged in.
func CreateOrUpdateAdmin(ctx context.Context, db database.Database, username, password string) (*database.User, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, fmt.Errorf("username and password required")
	}

	hash, salt, iters, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	existing, err := db.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("lookup user: %w", err)
	}

	u := &database.User{
		Username:   username,
		Password:   hash,
		Salt:       salt,
		Iterations: iters,
		Identifier: uuid.NewString(),
	}
	if existing != nil {
		u.ID = existing.ID
		u.CreatedAt = existing.CreatedAt
	}

	if err := db.UpsertUser(ctx, u); err != nil {
		return nil, fmt.Errorf("save user: %w", err)
	}
	return u, nil
}

// VerifyLogin looks up the user and verifies the password. On any failure it
// still runs a PBKDF2 derivation to keep the response time independent of
// whether the username exists.
func VerifyLogin(ctx context.Context, db database.Database, username, password string) (*database.User, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		dummyVerifyPassword(password)
		return nil, ErrInvalidCredentials
	}
	user, err := db.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	if user == nil {
		dummyVerifyPassword(password)
		return nil, ErrInvalidCredentials
	}
	if !VerifyPassword(password, user.Password, user.Salt, user.Iterations) {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// RotateIdentifier bumps the user's identifier UUID. Every session whose
// snapshot doesn't match will fail the next request. Caller is responsible
// for re-issuing a session for the acting user if it needs to stay logged in.
func RotateIdentifier(ctx context.Context, db database.Database, userID int64) (string, error) {
	id := uuid.NewString()
	if err := db.RotateUserIdentifier(ctx, userID, id); err != nil {
		return "", err
	}
	return id, nil
}
