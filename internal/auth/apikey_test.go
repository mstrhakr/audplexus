package auth

import (
	"context"
	"encoding/hex"
	"testing"
)

// fakeStore is a minimal in-memory SettingsStore for unit tests.
type fakeStore struct {
	m map[string]string
}

func (f *fakeStore) GetSetting(_ context.Context, k string) (string, error) { return f.m[k], nil }
func (f *fakeStore) SetSetting(_ context.Context, k, v string) error        { f.m[k] = v; return nil }

func TestGenerateAPIKeyIsHexAndUnique(t *testing.T) {
	a, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Errorf("api key is not valid hex: %v", err)
	}
	if len(a) != apiKeyByteLen*2 {
		t.Errorf("api key length = %d, want %d", len(a), apiKeyByteLen*2)
	}
	b, _ := GenerateAPIKey()
	if a == b {
		t.Errorf("two generated keys collided")
	}
}

func TestSeedAPIKeyIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := &fakeStore{m: map[string]string{}}
	first, err := SeedAPIKey(ctx, s)
	if err != nil || first == "" {
		t.Fatalf("first seed: key=%q err=%v", first, err)
	}
	second, err := SeedAPIKey(ctx, s)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if first != second {
		t.Errorf("seed mutated stored key: first=%s second=%s", first, second)
	}
}

func TestRotateAPIKeyReplacesValue(t *testing.T) {
	ctx := context.Background()
	s := &fakeStore{m: map[string]string{SettingKeyAPIKey: "old"}}
	new, err := RotateAPIKey(ctx, s)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if new == "old" {
		t.Errorf("rotate did not change the key")
	}
	if s.m[SettingKeyAPIKey] != new {
		t.Errorf("store not updated: got %s, want %s", s.m[SettingKeyAPIKey], new)
	}
}
