package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

// apiKeyByteLen is the number of random bytes behind an API key. 32 bytes
// hex-encoded yields a 64-char key — same shape as Sonarr's.
const apiKeyByteLen = 32

// EnvAPIKey is the env var that overrides the stored API key on first boot
// only. Set it once for a docker-compose deployment, then it's persisted to
// the DB and the env var is ignored on subsequent boots.
const EnvAPIKey = "AUDPLEXUS_API_KEY"

// SettingKeyAPIKey is the settings table key holding the current API key.
const SettingKeyAPIKey = "api_key"

// GenerateAPIKey returns a hex-encoded 32-byte random key.
func GenerateAPIKey() (string, error) {
	buf := make([]byte, apiKeyByteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// SettingsStore is the minimal subset of database.Database we need for
// settings ops. Kept as an interface so apikey logic can be unit-tested
// without spinning up the full DB.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// SeedAPIKey ensures an API key is present in settings. If $AUDPLEXUS_API_KEY
// is set and no key is stored yet, that value seeds the row; otherwise a fresh
// random key is generated. Idempotent — calling twice does nothing on the
// second invocation. Returns the resolved key.
func SeedAPIKey(ctx context.Context, store SettingsStore) (string, error) {
	existing, err := store.GetSetting(ctx, SettingKeyAPIKey)
	if err != nil {
		return "", fmt.Errorf("read api key: %w", err)
	}
	if existing != "" {
		return existing, nil
	}
	seed := os.Getenv(EnvAPIKey)
	if seed == "" {
		seed, err = GenerateAPIKey()
		if err != nil {
			return "", fmt.Errorf("generate api key: %w", err)
		}
	}
	if err := store.SetSetting(ctx, SettingKeyAPIKey, seed); err != nil {
		return "", fmt.Errorf("persist api key: %w", err)
	}
	return seed, nil
}

// RotateAPIKey replaces the stored key with a fresh random one and returns it.
func RotateAPIKey(ctx context.Context, store SettingsStore) (string, error) {
	key, err := GenerateAPIKey()
	if err != nil {
		return "", err
	}
	if err := store.SetSetting(ctx, SettingKeyAPIKey, key); err != nil {
		return "", err
	}
	return key, nil
}

// GetAPIKey returns the currently stored API key (or "" if none).
func GetAPIKey(ctx context.Context, store SettingsStore) (string, error) {
	return store.GetSetting(ctx, SettingKeyAPIKey)
}
