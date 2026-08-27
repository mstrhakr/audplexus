// Package crypto provides at-rest encryption for sensitive blobs (Audible
// account credentials). AES-256-GCM with a random per-install key persisted to
// a 0600 file in the config directory.
//
// Threat model: protects tokens in the database against casual exposure —
// database backups shared for debugging, DB files synced to less-trusted
// storage, SQL access without filesystem access. It does NOT protect against
// an attacker with full filesystem access (they can read the key file too);
// that requires an external secret store, which is out of scope for a
// self-hosted app.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"bytes"
	"fmt"
	"os"
)

// magic prefixes every ciphertext so Decrypt can distinguish encrypted blobs
// from legacy plaintext JSON (which always starts with '{'). Versioned so a
// future scheme change can coexist with v1 data.
var magic = []byte("AXC1")

const keySize = 32 // AES-256

// Credentials and OIDC state blobs are small; cap plaintext to avoid
// unbounded allocations on malformed or attacker-influenced payloads.
const maxEncryptPlaintextSize = 64 * 1024 * 1024

// LoadOrCreateKey returns the per-install encryption key, generating and
// persisting a new random one (file mode 0600) on first use.
func LoadOrCreateKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != keySize {
			return nil, fmt.Errorf("key file %s is %d bytes, want %d — refusing to guess; restore or delete it", path, len(data), keySize)
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read key file: %w", err)
	}

	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}
	return key, nil
}

// Box encrypts and decrypts blobs with a fixed key.
type Box struct {
	aead cipher.AEAD
}

// NewBox builds a Box from a 32-byte key (from LoadOrCreateKey).
func NewBox(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("key is %d bytes, want %d", len(key), keySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypt seals plaintext as magic || nonce || ciphertext.
func (b *Box) Encrypt(plain []byte) ([]byte, error) {
	if len(plain) > maxEncryptPlaintextSize {
		return nil, fmt.Errorf("plaintext too large: %d bytes", len(plain))
	}

	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	prefixLen := len(magic) + len(nonce) + b.aead.Overhead()
	maxInt := int(^uint(0) >> 1)
	if len(plain) > maxInt-prefixLen {
		return nil, fmt.Errorf("plaintext too large for allocation")
	}
	out := make([]byte, 0, prefixLen+len(plain))
	out = append(out, magic...)
	out = append(out, nonce...)
	return b.aead.Seal(out, nonce, plain, nil), nil
}

// IsEncrypted reports whether a blob carries the ciphertext magic.
func IsEncrypted(blob []byte) bool {
	return bytes.HasPrefix(blob, magic)
}

// Decrypt opens a blob sealed by Encrypt. Blobs WITHOUT the magic prefix are
// returned unchanged — that's the migration path for rows written plaintext
// before encryption landed; callers re-encrypt them on their next write.
func (b *Box) Decrypt(blob []byte) ([]byte, error) {
	if !IsEncrypted(blob) {
		return blob, nil
	}
	rest := blob[len(magic):]
	ns := b.aead.NonceSize()
	if len(rest) < ns {
		return nil, fmt.Errorf("ciphertext shorter than nonce")
	}
	plain, err := b.aead.Open(nil, rest[:ns], rest[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plain, nil
}
