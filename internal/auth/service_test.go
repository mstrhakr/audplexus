package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	hash, salt, iters, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword("hunter2", hash, salt, iters) {
		t.Errorf("verify rejected correct password")
	}
	if VerifyPassword("wrong", hash, salt, iters) {
		t.Errorf("verify accepted wrong password")
	}
}

func TestHashIsRandomized(t *testing.T) {
	// Two calls with the same password must yield different (salt, hash).
	// Otherwise the rainbow-table property of PBKDF2 is broken.
	h1, s1, _, _ := HashPassword("same")
	h2, s2, _, _ := HashPassword("same")
	if s1 == s2 {
		t.Errorf("salt was not randomized")
	}
	if h1 == h2 {
		t.Errorf("hash was not randomized")
	}
}

func TestVerifyRejectsCorruptHash(t *testing.T) {
	hash, salt, iters, _ := HashPassword("hunter2")
	tampered := strings.Repeat("A", len(hash))
	if VerifyPassword("hunter2", tampered, salt, iters) {
		t.Errorf("verify accepted a clearly-wrong hash")
	}
	if VerifyPassword("hunter2", hash, "not base64!!!", iters) {
		t.Errorf("verify accepted a non-base64 salt")
	}
}

func TestConstantTimeStringEqual(t *testing.T) {
	if !ConstantTimeStringEqual("abc", "abc") {
		t.Error("equal strings reported unequal")
	}
	if ConstantTimeStringEqual("abc", "abd") {
		t.Error("unequal strings reported equal")
	}
	if ConstantTimeStringEqual("abc", "abcd") {
		t.Error("different lengths reported equal")
	}
}

func TestDummyVerifyDoesNotPanic(t *testing.T) {
	// Smoke test: the timing-safe fallback path must not panic on any
	// input. We don't assert timing here — that's a flaky property — but
	// we do confirm the function returns without error for both empty and
	// pathological inputs.
	dummyVerifyPassword("")
	dummyVerifyPassword(strings.Repeat("a", 1024))
}
