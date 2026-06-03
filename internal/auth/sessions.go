package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/logging"
)

// SessionTTL is the sliding expiration for login sessions. Matches Sonarr.
const SessionTTL = 7 * 24 * time.Hour

// touchInterval is the minimum gap between last_seen writes for a single
// session. HTMX dashboards poll frequently — without this throttle every poll
// would be an extra write.
const touchInterval = 60 * time.Second

var authLog = logging.Component("auth")

// NewSessionToken returns 32 bytes of cryptographically random base64url data,
// suitable for use as both the cookie value and the primary key in the
// sessions table.
func NewSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// IssueSession creates and persists a new session row for the user.
func IssueSession(ctx context.Context, db database.Database, user *database.User, userAgent, ip string) (*database.Session, error) {
	token, err := NewSessionToken()
	if err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}
	now := time.Now()
	sess := &database.Session{
		Token:      token,
		UserID:     user.ID,
		Identifier: user.Identifier,
		ExpiresAt:  now.Add(SessionTTL),
		LastSeen:   now,
		CreatedAt:  now,
		UserAgent:  userAgent,
		IP:         ip,
	}
	if err := db.CreateSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// ResolveSession looks up a session by token, verifies it isn't expired, and
// confirms the user's identifier still matches the snapshot (i.e. the user
// hasn't rotated since this session was issued). It returns the session and
// the owning user, or (nil, nil, nil) for any invalid-but-not-error case.
//
// If the session is valid and last_seen is older than touchInterval, this
// also writes a touch. Failures to touch are logged but don't fail the
// resolve — the session is still valid for this request.
func ResolveSession(ctx context.Context, db database.Database, token string) (*database.Session, *database.User, error) {
	sess, err := db.GetSession(ctx, token)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup session: %w", err)
	}
	if sess == nil {
		return nil, nil, nil
	}
	now := time.Now()
	if now.After(sess.ExpiresAt) {
		return nil, nil, nil
	}
	user, err := db.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup session user: %w", err)
	}
	if user == nil || user.Identifier != sess.Identifier {
		// User was deleted or rotated — session is effectively dead.
		return nil, nil, nil
	}
	if now.Sub(sess.LastSeen) > touchInterval {
		if err := db.TouchSession(ctx, token, now); err != nil {
			authLog.Warn().Err(err).Msg("touch session failed")
		}
	}
	return sess, user, nil
}

// DeleteSession revokes a single session by token.
func DeleteSession(ctx context.Context, db database.Database, token string) error {
	return db.DeleteSession(ctx, token)
}

// StartGC launches a background goroutine that periodically deletes expired
// session rows. The interval is intentionally coarse — sessions naturally
// fail ResolveSession the moment they expire, so this is just storage cleanup.
func StartGC(ctx context.Context, db database.Database, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				deleted, err := db.DeleteExpiredSessions(ctx, now)
				if err != nil {
					authLog.Warn().Err(err).Msg("session gc failed")
					continue
				}
				if deleted > 0 {
					authLog.Debug().Int64("deleted", deleted).Msg("session gc")
				}
			}
		}
	}()
}
