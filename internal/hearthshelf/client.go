// Package hearthshelf connects Audplexus to a HearthShelf server in one click.
//
// WHAT THIS REPLACES. Today, sending books to HearthShelf means the user opens
// Audiobookshelf, mints an API key, copies the server URL, finds the library id,
// and pastes all three in here. This package removes all of that: the user
// clicks "Connect to HearthShelf", approves a short code in their browser, and
// the destination configures itself.
//
// HOW IT WORKS (OAuth 2.0 Device Authorization Grant, RFC 8628):
//
//  1. On first use we SELF-REGISTER with the control plane and get our own
//     app_id + secret. Every Audplexus install is its own app - there is no
//     shared credential baked into the binary, which there could not safely be
//     in open-source software anyway.
//  2. We ask for a user code and show it. The user approves it at
//     app.hearthshelf.com from whatever device they are already holding.
//  3. On approval we receive one short-lived INTRODUCTION token per server the
//     user picked, and present it to that server directly.
//  4. The server issues our real credential. From then on we talk only to the
//     server - the control plane is out of the loop, so the connection keeps
//     working even if app.hearthshelf.com is unreachable.
//
// WHY DEVICE FLOW AND NOT A REDIRECT. Audplexus is usually headless - a NAS, an
// Unraid box, a Pi - and configured from a different machine. A loopback
// redirect (127.0.0.1) would land in the user's LAPTOP browser, not here. Device
// flow needs no callback, no public URL, and no port forward. Nothing ever
// connects TO us.
package hearthshelf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultControlPlane is the hosted control plane. Overridable for self-hosted
// control planes and for testing.
const DefaultControlPlane = "https://api.hearthshelf.com"

// Family identifies Audplexus as a software family to the control plane. It is a
// label for grouping and consent copy ("your instance of Audplexus"), NOT a
// credential - anything can claim it. That is safe because an instance app can
// only ever be authorized by the account running it.
const Family = "audplexus"

// Scopes we request. Audplexus files finished audiobooks into a library, so it
// needs to write library content and read it back to confirm the book landed. It
// deliberately does NOT ask for progress or admin: asking for more than you use
// is how a consent screen becomes frightening and how a leak becomes worse.
var Scopes = []string{"library:read", "library:write"}

// Client talks to the control plane and to HearthShelf servers.
type Client struct {
	ControlPlane string
	HTTP         *http.Client
}

func New(controlPlane string) *Client {
	if strings.TrimSpace(controlPlane) == "" {
		controlPlane = DefaultControlPlane
	}
	return &Client{
		ControlPlane: strings.TrimRight(controlPlane, "/"),
		HTTP:         &http.Client{Timeout: 30 * time.Second},
	}
}

// --- errors ----------------------------------------------------------------

// ErrAuthorizationPending is returned while the user has not yet approved.
var ErrAuthorizationPending = errors.New("authorization pending")

// ErrSlowDown means we polled faster than the interval we were given. The caller
// must lengthen its interval - the server is telling us we are being rude.
var ErrSlowDown = errors.New("slow down")

// ErrAccessDenied means the user declined.
var ErrAccessDenied = errors.New("access denied")

// ErrExpiredToken means the user code expired before it was approved.
var ErrExpiredToken = errors.New("device code expired")

// ErrNeedsReconnect means our stored credential is no longer accepted - the user
// revoked us, or the server lost its state. The caller should surface
// "reconnect needed" rather than retrying in a loop: no amount of retrying will
// fix a revoked connection, and hammering a box that just cut us off is exactly
// what its rate limiter is there to stop.
var ErrNeedsReconnect = errors.New("connection revoked; reconnect required")

// --- registration ----------------------------------------------------------

type Registration struct {
	AppID        string `json:"app_id"`
	ClientSecret string `json:"client_secret"`
}

// Register claims this install's own app identity. Called once, on first
// connect; the result is persisted (secret encrypted) and reused thereafter.
func (c *Client) Register(ctx context.Context, name string) (*Registration, error) {
	body := map[string]any{
		"name":   name,
		"kind":   "instance",
		"family": Family,
		"scopes": strings.Join(Scopes, " "),
	}
	var out Registration
	if err := c.post(ctx, "/apps/register", body, "", &out); err != nil {
		return nil, err
	}
	if out.AppID == "" || out.ClientSecret == "" {
		return nil, errors.New("control plane returned an empty registration")
	}
	return &out, nil
}

// --- device flow -----------------------------------------------------------

type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartDeviceFlow asks for a user code to show the user.
func (c *Client) StartDeviceFlow(ctx context.Context, appID, secret string) (*DeviceCode, error) {
	body := map[string]any{
		"app_id":        appID,
		"client_secret": secret,
		"scopes":        strings.Join(Scopes, " "),
	}
	var out DeviceCode
	if err := c.post(ctx, "/apps/device/code", body, "", &out); err != nil {
		return nil, err
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return &out, nil
}

// Introduction is one server the user approved, with the token that introduces
// us to it and every address we might reach it at.
type Introduction struct {
	ServerID    string `json:"server_id"`
	ServerName  string `json:"server_name"`
	ServerURL   string `json:"server_url"`
	FallbackURL string `json:"fallback_url"`
	LocalURL    string `json:"local_url"`
	IdentityKey string `json:"identity_key"`
	IntroToken  string `json:"introduction_token"`
}

type deviceTokenResponse struct {
	Introductions []Introduction `json:"introductions"`
	Scopes        string         `json:"scopes"`
	Error         string         `json:"error"`
}

// PollDeviceFlow checks whether the user has approved yet.
//
// Returns ErrAuthorizationPending / ErrSlowDown while waiting. Callers must
// honour the interval and back off further on ErrSlowDown - see WaitForApproval,
// which does this correctly and should be preferred.
func (c *Client) PollDeviceFlow(ctx context.Context, appID, secret, deviceCode string) ([]Introduction, error) {
	body := map[string]any{
		"app_id":        appID,
		"client_secret": secret,
		"device_code":   deviceCode,
	}
	var out deviceTokenResponse
	err := c.post(ctx, "/apps/device/token", body, "", &out)
	if err != nil {
		// RFC 8628 signals flow state through the error field of a 400, so a
		// non-2xx here is expected traffic, not a failure.
		var apiErr *apiError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case "authorization_pending":
				return nil, ErrAuthorizationPending
			case "slow_down":
				return nil, ErrSlowDown
			case "access_denied":
				return nil, ErrAccessDenied
			case "expired_token":
				return nil, ErrExpiredToken
			}
		}
		return nil, err
	}
	return out.Introductions, nil
}

// WaitForApproval polls until the user approves, declines, or the code expires.
//
// Honours the server's interval and lengthens it on slow_down, as RFC 8628
// requires - a client that ignores that gets progressively penalised, and being
// the reference implementation, this one should model good behaviour.
func (c *Client) WaitForApproval(ctx context.Context, appID, secret string, dc *DeviceCode) ([]Introduction, error) {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for {
		if time.Now().After(deadline) {
			return nil, ErrExpiredToken
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		intros, err := c.PollDeviceFlow(ctx, appID, secret, dc.DeviceCode)
		switch {
		case err == nil:
			return intros, nil
		case errors.Is(err, ErrAuthorizationPending):
			continue
		case errors.Is(err, ErrSlowDown):
			interval += 5 * time.Second
			continue
		default:
			return nil, err
		}
	}
}

// --- introduction to a server ----------------------------------------------

// TokenSet is the credential a HearthShelf server issued us. The refresh token
// is long-lived and MUST be persisted encrypted; the access token is short-lived
// and can be kept in memory.
type TokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// Introduce presents an introduction token to a server and receives our real
// credential. After this the control plane is no longer involved.
//
// `baseURL` should come from PreferredAddress, which picks a LAN address when
// one is available and verified.
func (c *Client) Introduce(ctx context.Context, baseURL, introToken string) (*TokenSet, error) {
	var out TokenSet
	body := map[string]any{"introduction_token": introToken}
	if err := c.postTo(ctx, strings.TrimRight(baseURL, "/")+"/hs/apps/introduce", body, "", &out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, errors.New("server returned an empty token set")
	}
	return &out, nil
}

// Refresh exchanges our refresh token for a new pair.
//
// The server ROTATES: a successful refresh usually returns a NEW refresh token
// which must replace the stored one. It may return none, which means "keep the
// one you have" (a benign retry inside the server's grace window) - so an empty
// RefreshToken must never be treated as a failure or written over the stored value.
//
// A 401 means we were revoked. Surface ErrNeedsReconnect and stop; retrying
// cannot help.
func (c *Client) Refresh(ctx context.Context, baseURL, appID, refreshToken string) (*TokenSet, error) {
	var out TokenSet
	body := map[string]any{"app_id": appID, "refresh_token": refreshToken}
	err := c.postTo(ctx, strings.TrimRight(baseURL, "/")+"/hs/apps/token", body, "", &out)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			return nil, ErrNeedsReconnect
		}
		return nil, err
	}
	return &out, nil
}

// --- discovery (RFC 9728) --------------------------------------------------

// ResourceMetadata is a HearthShelf server's published metadata. Fetching it
// tells us which control plane a given server trusts, so a user pointing us at
// an arbitrary server does not have to tell us anything else.
type ResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// Discover reads a server's protected-resource metadata. Unauthenticated.
func (c *Client) Discover(ctx context.Context, serverURL string) (*ResourceMetadata, error) {
	u := strings.TrimRight(serverURL, "/") + "/.well-known/oauth-protected-resource"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery failed: HTTP %d", res.StatusCode)
	}
	var out ResourceMetadata
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- HTTP plumbing ---------------------------------------------------------

type apiError struct {
	Status int
	Code   string
	Detail string
}

func (e *apiError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

func (c *Client) post(ctx context.Context, path string, body any, bearer string, out any) error {
	return c.postTo(ctx, c.ControlPlane+path, body, bearer, out)
}

func (c *Client) postTo(ctx context.Context, url string, body any, bearer string, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(raw, &e)
		return &apiError{Status: res.StatusCode, Code: e.Error, Detail: e.Detail}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
