package hearthshelf

// Persistence for the HearthShelf connection, and the payoff: turning a
// completed authorization into a working library destination without the user
// typing a URL or an API key.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/mstrhakr/audplexus/internal/crypto"
	"github.com/mstrhakr/audplexus/internal/database"
)

// crypto.Box produces raw binary ciphertext, and settings are TEXT columns, so
// every sealed value is base64-wrapped on the way in and unwrapped on the way
// out. Storing the raw bytes as a Go string would corrupt them the moment the
// driver or the DB touched encoding.
func seal(box *crypto.Box, plain []byte) (string, error) {
	enc, err := box.Encrypt(plain)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func unseal(box *crypto.Box, stored string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		// Not base64: a value written before this wrapper existed. Box.Decrypt
		// passes non-magic blobs through unchanged, so plaintext still works.
		return box.Decrypt([]byte(stored))
	}
	return box.Decrypt(raw)
}

// Settings keys. The app secret and refresh token are the two sensitive values
// and are stored encrypted with the same crypto.Box used for Audible
// credentials and the OIDC client secret.
const (
	SettingKeyControlPlane = "hs_control_plane_url"
	SettingKeyAppID        = "hs_app_id"
	SettingKeyAppSecret    = "hs_app_secret"  // encrypted
	SettingKeyConnections  = "hs_connections" // encrypted (holds refresh tokens)
)

// Connection is one connected HearthShelf server.
type Connection struct {
	ServerID     string `json:"server_id"`
	ServerName   string `json:"server_name"`
	BaseURL      string `json:"base_url"`
	RefreshToken string `json:"refresh_token"`
	Scopes       string `json:"scopes"`
	// DestinationID links this connection to the library_destinations row it
	// created, so disconnecting can clean up and reconnecting can update in
	// place rather than leaving a stale duplicate.
	DestinationID string `json:"destination_id,omitempty"`
}

// Identity is this install's own app registration.
type Identity struct {
	AppID  string
	Secret string
}

// LoadIdentity returns this install's app credentials, or nil if not registered.
func LoadIdentity(ctx context.Context, db database.Database, box *crypto.Box) (*Identity, error) {
	appID, _ := db.GetSetting(ctx, SettingKeyAppID)
	if strings.TrimSpace(appID) == "" {
		return nil, nil
	}
	enc, _ := db.GetSetting(ctx, SettingKeyAppSecret)
	if strings.TrimSpace(enc) == "" {
		return nil, nil
	}
	secret, err := unseal(box, enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt app secret: %w", err)
	}
	return &Identity{AppID: appID, Secret: string(secret)}, nil
}

// SaveIdentity persists this install's app credentials.
func SaveIdentity(ctx context.Context, db database.Database, box *crypto.Box, id *Identity) error {
	enc, err := seal(box, []byte(id.Secret))
	if err != nil {
		return fmt.Errorf("encrypt app secret: %w", err)
	}
	if err := db.SetSetting(ctx, SettingKeyAppID, id.AppID); err != nil {
		return err
	}
	return db.SetSetting(ctx, SettingKeyAppSecret, enc)
}

// LoadConnections returns the connected servers.
func LoadConnections(ctx context.Context, db database.Database, box *crypto.Box) ([]Connection, error) {
	enc, _ := db.GetSetting(ctx, SettingKeyConnections)
	if strings.TrimSpace(enc) == "" {
		return nil, nil
	}
	raw, err := unseal(box, enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt connections: %w", err)
	}
	var out []Connection
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse connections: %w", err)
	}
	return out, nil
}

// SaveConnections persists the connected servers (refresh tokens encrypted).
func SaveConnections(ctx context.Context, db database.Database, box *crypto.Box, conns []Connection) error {
	raw, err := json.Marshal(conns)
	if err != nil {
		return err
	}
	enc, err := seal(box, raw)
	if err != nil {
		return fmt.Errorf("encrypt connections: %w", err)
	}
	return db.SetSetting(ctx, SettingKeyConnections, enc)
}

// UpsertConnection replaces a connection for the same server, or appends it.
// Reconnecting must not leave a second entry pointing at the same box.
func UpsertConnection(conns []Connection, c Connection) []Connection {
	for i := range conns {
		if conns[i].ServerID == c.ServerID {
			// Preserve the destination link across a reconnect so the existing
			// library destination is updated rather than orphaned.
			if c.DestinationID == "" {
				c.DestinationID = conns[i].DestinationID
			}
			conns[i] = c
			return conns
		}
	}
	return append(conns, c)
}

// RemoveConnection drops a server from the list.
func RemoveConnection(conns []Connection, serverID string) []Connection {
	out := conns[:0]
	for _, c := range conns {
		if c.ServerID != serverID {
			out = append(out, c)
		}
	}
	return out
}

// EnsureDestination creates or updates the library destination for a connected
// server. THIS IS THE ONE-CLICK PAYOFF: the URL and credential come from the
// authorization, so the user is asked only for a genuine choice (which library),
// never for an address or an API key.
//
// The destination is created disabled when no library has been chosen yet -
// enabling something that cannot yet route a book would just produce failures
// the user did not ask for.
func EnsureDestination(
	ctx context.Context,
	db database.Database,
	conn *Connection,
	accessToken string,
	libraryID string,
) error {
	if conn.DestinationID != "" {
		existing, err := db.GetLibraryDestination(ctx, conn.DestinationID)
		if err == nil && existing != nil {
			existing.URL = conn.BaseURL
			existing.APIKey = accessToken
			if libraryID != "" {
				existing.LibraryID = libraryID
				existing.Enabled = true
			}
			existing.DisplayName = destinationName(conn)
			return db.UpdateLibraryDestination(ctx, existing)
		}
		// The row was deleted behind our back; fall through and make a new one.
	}

	d := &database.LibraryDestination{
		// The store requires the caller to generate the id.
		ID:          uuid.NewString(),
		DisplayName: destinationName(conn),
		Type:        database.LibraryDestinationTypeABS,
		Enabled:     libraryID != "",
		URL:         conn.BaseURL,
		APIKey:      accessToken,
		LibraryID:   libraryID,
	}
	if err := db.CreateLibraryDestination(ctx, d); err != nil {
		return err
	}
	conn.DestinationID = d.ID
	return nil
}

func destinationName(c *Connection) string {
	if strings.TrimSpace(c.ServerName) != "" {
		return c.ServerName
	}
	return "HearthShelf"
}
