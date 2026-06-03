// Package auth implements a Sonarr/Radarr-style auth layer for audplexus:
// a single-admin Forms login, a rotatable API key for external integrations,
// and a configurable "authentication required" knob (always / disabled for
// local addresses / disabled for localhost).
//
// Crypto choices match the *arr stack so users see the same security properties:
// PBKDF2-HMAC-SHA512, 10000 iterations, 16-byte salt, 32-byte hash. All
// secret comparisons go through subtle.ConstantTimeCompare.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"

	"golang.org/x/crypto/pbkdf2"
	"crypto/sha512"
)

const (
	pbkdf2Iterations = 10000
	pbkdf2SaltSize   = 16
	pbkdf2KeySize    = 32
)

// ErrInvalidCredentials is returned when verification fails for any reason.
// The login handler maps this to a generic 401 so we don't leak whether the
// username or password was wrong.
var ErrInvalidCredentials = errors.New("invalid credentials")

// HashPassword returns base64(hash), base64(salt), iterations using PBKDF2-HMAC-SHA512.
// Same parameters as Sonarr's UserService.
func HashPassword(password string) (hash, salt string, iterations int, err error) {
	saltBytes := make([]byte, pbkdf2SaltSize)
	if _, err := rand.Read(saltBytes); err != nil {
		return "", "", 0, err
	}
	hashBytes := pbkdf2.Key([]byte(password), saltBytes, pbkdf2Iterations, pbkdf2KeySize, sha512.New)
	return base64.StdEncoding.EncodeToString(hashBytes),
		base64.StdEncoding.EncodeToString(saltBytes),
		pbkdf2Iterations, nil
}

// VerifyPassword constant-time compares the candidate password against the stored hash.
func VerifyPassword(password, storedHashB64, storedSaltB64 string, iterations int) bool {
	saltBytes, err := base64.StdEncoding.DecodeString(storedSaltB64)
	if err != nil {
		return false
	}
	storedHash, err := base64.StdEncoding.DecodeString(storedHashB64)
	if err != nil {
		return false
	}
	candidate := pbkdf2.Key([]byte(password), saltBytes, iterations, len(storedHash), sha512.New)
	return subtle.ConstantTimeCompare(candidate, storedHash) == 1
}

// dummyVerifyPassword runs a PBKDF2 derivation with the configured parameters
// against throwaway salt. The login handler calls this when the username
// doesn't exist so the response timing matches a real verification, blocking
// username enumeration via timing side channels.
func dummyVerifyPassword(password string) {
	salt := make([]byte, pbkdf2SaltSize)
	_, _ = rand.Read(salt)
	_ = pbkdf2.Key([]byte(password), salt, pbkdf2Iterations, pbkdf2KeySize, sha512.New)
}

// ConstantTimeStringEqual compares two strings without leaking length difference
// timing for the contents (length itself is not secret here — API keys are
// fixed-length).
func ConstantTimeStringEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
