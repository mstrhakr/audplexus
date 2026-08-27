package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}

	plain := []byte(`{"adp_token":"secret","refresh_token":"also-secret"}`)
	enc, err := box.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(enc) {
		t.Fatal("ciphertext missing magic prefix")
	}
	if bytes.Contains(enc, []byte("secret")) {
		t.Fatal("ciphertext leaks plaintext")
	}

	got, err := box.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch: %q", got)
	}
}

func TestDecryptLegacyPlaintextPassthrough(t *testing.T) {
	key, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	box, _ := NewBox(key)

	legacy := []byte(`{"adp_token":"pre-encryption row"}`)
	got, err := box.Decrypt(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, legacy) {
		t.Fatal("legacy plaintext should pass through unchanged")
	}
}

func TestKeyPersistsAcrossLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("key changed between loads")
	}
}

func TestCorruptKeyFileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(path, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("expected error for wrong-size key file")
	}
}

func TestTamperedCiphertextRejected(t *testing.T) {
	key, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	box, _ := NewBox(key)
	enc, _ := box.Encrypt([]byte("data"))
	enc[len(enc)-1] ^= 0xFF
	if _, err := box.Decrypt(enc); err == nil {
		t.Fatal("expected auth failure on tampered ciphertext")
	}
}

func TestEncryptRejectsOversizedPlaintext(t *testing.T) {
	key, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	box, _ := NewBox(key)

	plain := make([]byte, maxEncryptPlaintextSize+1)
	if _, err := box.Encrypt(plain); err == nil {
		t.Fatal("expected error for oversized plaintext")
	}
}
