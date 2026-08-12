package hearthshelf

// Keeping a connected HearthShelf destination working after the credential it
// was created with stops being accepted.
//
// WHY THIS FILE EXISTS. The connect flow stores the Audiobookshelf API key the
// server issued at introduction time (see EnsureDestination) directly on the
// destination row, and every push reads that stored string. Nothing renewed it:
// the client could Refresh, but no production path ever called it, so once the
// server stopped honouring that key the destination failed every push and the
// only cure was for the user to reconnect by hand - which is exactly the
// busywork the one-click flow exists to remove.
//
// The renewal runs at the point every destination row is loaded for use
// (DestinationManager.ListEnabled), so a stale key is repaired before the push
// rather than after it has already failed.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mstrhakr/audplexus/internal/crypto"
	"github.com/mstrhakr/audplexus/internal/database"
)

// RenewalResult reports what a renewal attempt did, so callers can log it
// without re-deriving the outcome from the mutated row.
type RenewalResult struct {
	// Renewed is true when a new credential was written to the destination.
	Renewed bool
	// NeedsReconnect is true when the server rejected our refresh token. No
	// amount of retrying fixes this - the user has to reconnect.
	NeedsReconnect bool
}

// renewSkew is how long before a credential's stated expiry we renew it.
// Refreshing slightly early costs one extra round-trip; refreshing late costs a
// failed push, so the trade is not symmetric.
const renewSkew = 2 * time.Minute

// RenewDestination refreshes the stored credential for one HearthShelf-managed
// destination, updating both the connection and the destination row in place.
//
// It is a no-op (nil error, zero result) for a destination HearthShelf did not
// create - callers can hand it any row without checking first.
//
// A revoked connection surfaces as NeedsReconnect rather than an error: the
// caller's job is to carry on with the other destinations, and a user-visible
// "reconnect needed" is more useful than a push that fails with a bare 401.
func RenewDestination(
	ctx context.Context,
	db database.Database,
	box *crypto.Box,
	client *Client,
	row *database.LibraryDestination,
) (RenewalResult, error) {
	if row == nil || row.Type != database.LibraryDestinationTypeABS {
		return RenewalResult{}, nil
	}

	conns, err := LoadConnections(ctx, db, box)
	if err != nil {
		return RenewalResult{}, fmt.Errorf("load connections: %w", err)
	}
	idx := -1
	for i := range conns {
		if conns[i].DestinationID == row.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Not a HearthShelf row - a hand-configured Audiobookshelf destination.
		return RenewalResult{}, nil
	}
	conn := &conns[idx]
	if strings.TrimSpace(conn.RefreshToken) == "" {
		// Connected before refresh tokens were persisted, or the server never
		// issued one. Nothing to renew from; leave the existing key alone.
		return RenewalResult{}, nil
	}
	if !conn.needsRenewal() {
		return RenewalResult{}, nil
	}

	if client == nil {
		client = New(controlPlaneFor(ctx, db))
	}
	tokens, err := client.Refresh(ctx, conn.BaseURL, appIDFor(ctx, db), conn.RefreshToken)
	if err != nil {
		if errors.Is(err, ErrNeedsReconnect) {
			// Mark it so the UI can say so, and persist that - otherwise every
			// subsequent push retries a refresh that cannot succeed.
			conn.NeedsReconnect = true
			if saveErr := SaveConnections(ctx, db, box, conns); saveErr != nil {
				return RenewalResult{NeedsReconnect: true}, fmt.Errorf("save revoked state: %w", saveErr)
			}
			return RenewalResult{NeedsReconnect: true}, nil
		}
		return RenewalResult{}, fmt.Errorf("refresh %s: %w", conn.ServerName, err)
	}

	// An ABSENT refresh token means "keep the one you have" - a benign retry
	// inside the server's grace window. Writing the empty value over the stored
	// one would lock us out on the next renewal.
	if strings.TrimSpace(tokens.RefreshToken) != "" {
		conn.RefreshToken = tokens.RefreshToken
	}
	conn.NeedsReconnect = false
	if tokens.ExpiresIn > 0 {
		conn.CredentialExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).Unix()
	} else {
		conn.CredentialExpiresAt = 0
	}

	// The destination pushes with the ABS key, not the HearthShelf access
	// token - /api/* is Audiobookshelf's own surface and has never heard of our
	// token. A refresh that returns no ABS key leaves the existing one in place
	// rather than blanking the destination's credential.
	if strings.TrimSpace(tokens.ABSAPIKey) != "" {
		row.APIKey = tokens.ABSAPIKey
		if err := db.UpdateLibraryDestination(ctx, row); err != nil {
			return RenewalResult{}, fmt.Errorf("update destination: %w", err)
		}
	}
	if err := SaveConnections(ctx, db, box, conns); err != nil {
		return RenewalResult{}, fmt.Errorf("save connections: %w", err)
	}
	return RenewalResult{Renewed: true}, nil
}

// needsRenewal reports whether the stored credential is at or near expiry.
//
// A zero CredentialExpiresAt means we have never recorded one - either the
// connection predates this field or the server did not state a lifetime. In
// that case we do NOT refresh speculatively: the connect flow's key works, and
// refreshing every push would be a needless round-trip against every server.
// The 401 path handles the case where such a key does eventually lapse.
func (c *Connection) needsRenewal() bool {
	if c.NeedsReconnect {
		return false
	}
	if c.CredentialExpiresAt == 0 {
		return false
	}
	return time.Now().Add(renewSkew).Unix() >= c.CredentialExpiresAt
}

func controlPlaneFor(ctx context.Context, db database.Database) string {
	v, _ := db.GetSetting(ctx, SettingKeyControlPlane)
	if strings.TrimSpace(v) == "" {
		return DefaultControlPlane
	}
	return strings.TrimSpace(v)
}

func appIDFor(ctx context.Context, db database.Database) string {
	v, _ := db.GetSetting(ctx, SettingKeyAppID)
	return strings.TrimSpace(v)
}
