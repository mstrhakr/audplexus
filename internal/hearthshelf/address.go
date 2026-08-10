package hearthshelf

// Choosing which address to reach a HearthShelf server at, and proving the thing
// answering is really that server before handing it anything.
//
// WHY THIS FILE EXISTS. Audplexus and HearthShelf usually run on the same
// machine or the same LAN, so the fastest and most reliable route is the private
// address - and it keeps working when the household internet is down. But a
// private IP is spoofable: any device on the network can answer on 192.168.1.50,
// and what we would hand it (an introduction token, or later our access token)
// is a bearer credential for the user's library.
//
// Over the public internet TLS solves this for free - the origin is a CA-valid
// name. On a LAN there is no usable certificate, so the server proves itself
// instead: it holds an Ed25519 private key, the control plane gives us the
// PUBLIC half over TLS, and we challenge the candidate origin with a nonce it
// must sign. An impostor cannot answer, and we walk away without having sent
// anything.
//
// This mirrors what the HearthShelf apps already do (see serverIdentity.js on
// the server). Do not "simplify" it by trusting the LAN address directly.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PreferredAddress picks the best address for an introduction, verifying a LAN
// address before returning it.
//
// Order: verified LAN, then the server's preferred public URL, then the
// hs.direct fallback. The LAN address goes first because it is the common case
// for a same-network app and survives an internet outage - but ONLY once its
// identity is proven. If verification fails we fall through to the public
// addresses rather than failing outright: a wrong LAN answer is a reason to
// distrust that address, not a reason to give up on the server.
func PreferredAddress(ctx context.Context, hc *http.Client, in Introduction) (string, error) {
	if in.LocalURL != "" && in.IdentityKey != "" {
		ok, err := VerifyServerIdentity(ctx, hc, in.LocalURL, in.ServerID, in.IdentityKey)
		if err == nil && ok {
			return in.LocalURL, nil
		}
	}
	if in.ServerURL != "" {
		return in.ServerURL, nil
	}
	if in.FallbackURL != "" {
		return in.FallbackURL, nil
	}
	return "", errors.New("no usable address for server " + in.ServerID)
}

type identityResponse struct {
	ServerID  string `json:"server_id"`
	Signature string `json:"signature"`
}

// VerifyServerIdentity challenges an origin to prove it is the named server.
//
// The nonce is ours and single-use, so a captured response cannot be replayed at
// us later. The server id is inside the signed payload, so a signature captured
// from server A cannot be presented as proof of server B.
func VerifyServerIdentity(ctx context.Context, hc *http.Client, baseURL, serverID, identityKeyB64 string) (bool, error) {
	pub, err := base64.StdEncoding.DecodeString(identityKeyB64)
	if err != nil {
		return false, fmt.Errorf("bad identity key: %w", err)
	}
	// The control plane stores the key as SPKI DER; the raw Ed25519 key is the
	// trailing 32 bytes. Accept a bare 32-byte key too, so this keeps working if
	// the encoding is ever simplified.
	if len(pub) > ed25519.PublicKeySize {
		pub = pub[len(pub)-ed25519.PublicKeySize:]
	}
	if len(pub) != ed25519.PublicKeySize {
		return false, errors.New("identity key is not an Ed25519 public key")
	}

	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return false, err
	}
	nonceB64 := base64.StdEncoding.EncodeToString(nonce)

	body, _ := json.Marshal(map[string]string{"nonce": nonceB64})
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/hs/hosted/identity",
		bytes.NewReader(body),
	)
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := hc
	if client == nil {
		client = &http.Client{}
	}
	// Short timeout: this is a LAN round-trip, and a slow answer is more likely
	// to be the wrong device than the right one.
	ctxTimeout, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	res, err := client.Do(req.WithContext(ctxTimeout))
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("identity challenge: HTTP %d", res.StatusCode)
	}

	var out identityResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return false, err
	}
	if out.ServerID != serverID {
		return false, errors.New("identity challenge answered for a different server")
	}
	sig, err := base64.StdEncoding.DecodeString(out.Signature)
	if err != nil {
		return false, fmt.Errorf("bad signature encoding: %w", err)
	}

	// The canonical payload, byte-for-byte as the server composes it in
	// signIdentityChallenge (HearthShelf server/lib/serverIdentity.js): a
	// versioned prefix, the server id, and the nonce as the base64 STRING we
	// sent - not the raw nonce bytes. Getting this wrong fails closed (we would
	// simply never trust a LAN address), which is safe but silently costs the
	// offline path, so keep it in step with the server if v2 ever appears.
	payload := []byte("hs-identity:v1:" + serverID + ":" + nonceB64)
	return ed25519.Verify(ed25519.PublicKey(pub), payload, sig), nil
}
