package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/mstrhakr/audplexus/internal/crypto"
	"github.com/mstrhakr/audplexus/internal/database"
)

// OIDC settings live in the settings table (DB-only, no env). The client
// secret is the one sensitive value and is stored encrypted with the same
// crypto.Box used for Audible credentials.
const (
	SettingKeyOIDCEnabled      = "oidc_enabled"
	SettingKeyOIDCProviderName = "oidc_provider_name"
	SettingKeyOIDCIssuerURL    = "oidc_issuer_url"
	SettingKeyOIDCClientID     = "oidc_client_id"
	SettingKeyOIDCClientSecret = "oidc_client_secret" // stored encrypted
	SettingKeyOIDCScopes       = "oidc_scopes"
)

// DefaultOIDCProviderName labels the login button when none is configured.
const DefaultOIDCProviderName = "Authentik"

// DefaultOIDCScopes is the minimum for OIDC: an ID token plus profile/email
// claims used to name the auto-provisioned user.
const DefaultOIDCScopes = "openid profile email"

// OIDCConfig is the resolved, decrypted OIDC configuration.
type OIDCConfig struct {
	Enabled      bool
	ProviderName string
	IssuerURL    string
	ClientID     string
	ClientSecret string // decrypted
	Scopes       []string
}

// Configured reports whether the essential fields are present, independent of
// the enabled flag. Used to validate before flipping Enabled on.
func (c *OIDCConfig) Configured() bool {
	return c.IssuerURL != "" && c.ClientID != "" && c.ClientSecret != ""
}

// OIDCEnabled is a cheap single-key check for the login-page hot path — avoids
// decrypting the secret just to decide whether to show the button.
func OIDCEnabled(ctx context.Context, db database.Database) bool {
	v, _ := db.GetSetting(ctx, SettingKeyOIDCEnabled)
	return v == "true"
}

// LoadOIDCConfig reads and decrypts the OIDC settings. A nil box means the
// secret is read as-is (tests); production always passes a box.
func LoadOIDCConfig(ctx context.Context, db database.Database, box *crypto.Box) (*OIDCConfig, error) {
	get := func(k string) string {
		v, _ := db.GetSetting(ctx, k)
		return v
	}

	cfg := &OIDCConfig{
		Enabled:      get(SettingKeyOIDCEnabled) == "true",
		ProviderName: get(SettingKeyOIDCProviderName),
		IssuerURL:    strings.TrimRight(get(SettingKeyOIDCIssuerURL), "/"),
		ClientID:     get(SettingKeyOIDCClientID),
	}
	if cfg.ProviderName == "" {
		cfg.ProviderName = DefaultOIDCProviderName
	}

	scopes := strings.Fields(get(SettingKeyOIDCScopes))
	if len(scopes) == 0 {
		scopes = strings.Fields(DefaultOIDCScopes)
	}
	cfg.Scopes = scopes

	if raw := get(SettingKeyOIDCClientSecret); raw != "" {
		if box != nil {
			plain, err := box.Decrypt([]byte(raw))
			if err != nil {
				return nil, fmt.Errorf("decrypt oidc client secret: %w", err)
			}
			cfg.ClientSecret = string(plain)
		} else {
			cfg.ClientSecret = raw
		}
	}
	return cfg, nil
}

// UpsertOIDCUser finds the local user for (issuer, subject) or provisions one.
// Every successfully-authenticated OIDC user becomes a full local user — this
// app has no user levels, and access control is delegated to the provider.
//
// On a username collision with an existing (forms or other-subject) user, the
// name is disambiguated with a short subject suffix so provisioning never
// fails on a shared preferred_username.
func UpsertOIDCUser(ctx context.Context, db database.Database, issuer, subject, preferredUsername, email string) (*database.User, error) {
	subject = strings.TrimSpace(subject)
	issuer = strings.TrimSpace(issuer)
	if subject == "" || issuer == "" {
		return nil, fmt.Errorf("oidc issuer and subject are required")
	}

	existing, err := db.GetUserByOIDC(ctx, issuer, subject)
	if err != nil {
		return nil, fmt.Errorf("lookup oidc user: %w", err)
	}
	if existing != nil {
		// Refresh the display name if the provider's changed; harmless if not.
		newName := resolveOIDCUsername(preferredUsername, email, subject)
		if newName != "" && newName != existing.Username {
			candidate := *existing
			candidate.Username = newName
			if err := db.UpsertUser(ctx, &candidate); err == nil {
				return &candidate, nil
			}
			// Name taken by someone else — keep the existing name, not fatal.
		}
		return existing, nil
	}

	base := resolveOIDCUsername(preferredUsername, email, subject)
	user := &database.User{
		Username:    base,
		Identifier:  uuid.NewString(),
		OIDCSubject: subject,
		OIDCIssuer:  issuer,
		AuthSource:  "oidc",
		// No password: forms login can never authenticate an OIDC user.
	}
	if err := db.UpsertUser(ctx, user); err != nil {
		if err == database.ErrDuplicateUser {
			// Username collides with an existing account — suffix and retry once.
			user.Username = base + "+oidc-" + shortSubject(subject)
			if err2 := db.UpsertUser(ctx, user); err2 != nil {
				return nil, fmt.Errorf("create oidc user: %w", err2)
			}
			return user, nil
		}
		return nil, fmt.Errorf("create oidc user: %w", err)
	}
	return user, nil
}

// resolveOIDCUsername picks the friendliest available display name from the
// claims, falling back to the subject so it's never empty.
func resolveOIDCUsername(preferredUsername, email, subject string) string {
	if s := strings.TrimSpace(preferredUsername); s != "" {
		return s
	}
	if s := strings.TrimSpace(email); s != "" {
		return s
	}
	return "oidc-" + shortSubject(subject)
}

func shortSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if len(subject) > 8 {
		return subject[:8]
	}
	return subject
}
