package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/auth"
	"golang.org/x/oauth2"
)

// oidcFlowCookie holds the in-flight OIDC login state. It's encrypted with the
// credential box (GCM gives integrity), base64'd, and stored in a short-lived
// cookie — stateless, so it survives restarts and multiple replicas without a
// shared map.
const oidcFlowCookie = "oidcFlow"
const oidcFlowTTL = 10 * time.Minute

type oidcFlowState struct {
	State        string `json:"s"`
	Nonce        string `json:"n"`
	CodeVerifier string `json:"v"`
	ReturnURL    string `json:"r"`
	ExpiresAt    int64  `json:"e"` // unix seconds
}

// oidcProviderCache memoizes discovery (a network round-trip) per issuer URL.
// Authentik's discovery doc rarely changes; a short TTL keeps it fresh without
// a fetch on every login.
type oidcProviderEntry struct {
	provider  *oidc.Provider
	fetchedAt time.Time
}

var (
	oidcProviderMu    sync.Mutex
	oidcProviderCache = map[string]oidcProviderEntry{}
)

const oidcProviderTTL = 10 * time.Minute

func discoverOIDCProvider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	oidcProviderMu.Lock()
	defer oidcProviderMu.Unlock()
	if e, ok := oidcProviderCache[issuer]; ok && time.Since(e.fetchedAt) < oidcProviderTTL {
		return e.provider, nil
	}
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	oidcProviderCache[issuer] = oidcProviderEntry{provider: p, fetchedAt: time.Now()}
	return p, nil
}

// oidcRedirectURL builds the absolute callback URL the provider redirects back
// to. Must exactly match what's registered in Authentik. Scheme follows the
// same trusted-proxy logic the session cookie uses.
func (s *Server) oidcRedirectURL(c *gin.Context) string {
	scheme := "http"
	if s.authMgr.IsHTTPS(c) {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/auth/oidc/callback", scheme, c.Request.Host)
}

// oauthConfig assembles the oauth2.Config from resolved OIDC settings.
func (s *Server) oauthConfig(c *gin.Context, cfg *auth.OIDCConfig, provider *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  s.oidcRedirectURL(c),
		Scopes:       cfg.Scopes,
	}
}

// handleOIDCLogin (GET /auth/oidc/login) kicks off the auth-code flow: build the
// provider, mint state/nonce/PKCE, seal them into a cookie, and redirect to the
// provider's authorization endpoint.
func (s *Server) handleOIDCLogin(c *gin.Context) {
	ctx := c.Request.Context()
	if !s.authMgr.IsHTTPS(c) {
		webLog.Warn().Str("ip", c.ClientIP()).Msg("blocking oidc login over non-https request")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	// OIDC is only meaningful when forms auth is the active method (otherwise
	// the app is either fully open or fully locked). Guard so the flow can't be
	// driven in an incoherent state.
	if s.authMgr.CurrentMethod(ctx) != auth.AuthMethodForms {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	cfg, err := auth.LoadOIDCConfig(ctx, s.db, s.credBox)
	if err != nil || !cfg.Enabled || !cfg.Configured() {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	provider, err := discoverOIDCProvider(ctx, cfg.IssuerURL)
	if err != nil {
		webLog.Error().Err(err).Str("issuer", cfg.IssuerURL).Msg("oidc provider discovery failed")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}
	oauthCfg := s.oauthConfig(c, cfg, provider)

	state := randToken()
	nonce := randToken()
	verifier := oauth2.GenerateVerifier()

	flow := oidcFlowState{
		State:        state,
		Nonce:        nonce,
		CodeVerifier: verifier,
		ReturnURL:    sanitizeReturnURL(c.Query("returnUrl")),
		ExpiresAt:    time.Now().Add(oidcFlowTTL).Unix(),
	}
	if err := s.sealOIDCFlow(c, flow); err != nil {
		webLog.Error().Err(err).Msg("failed to seal oidc flow state")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	authURL := oauthCfg.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	)
	c.Redirect(http.StatusSeeOther, authURL)
}

// handleOIDCCallback (GET /auth/oidc/callback) completes the flow: validate
// state, exchange the code, verify the ID token + nonce, provision/find the
// user, and issue the normal session cookie.
func (s *Server) handleOIDCCallback(c *gin.Context) {
	ctx := c.Request.Context()
	if !s.authMgr.IsHTTPS(c) {
		webLog.Warn().Str("ip", c.ClientIP()).Msg("blocking oidc callback over non-https request")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	cfg, err := auth.LoadOIDCConfig(ctx, s.db, s.credBox)
	if err != nil || !cfg.Enabled || !cfg.Configured() {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	flow, ok := s.openOIDCFlow(c)
	s.clearOIDCFlow(c)
	if !ok {
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	if provErr := c.Query("error"); provErr != "" {
		webLog.Warn().Str("error", provErr).Str("desc", c.Query("error_description")).Msg("oidc provider returned error")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	// State must match what we sealed — CSRF protection for the callback.
	if !auth.ConstantTimeStringEqual(c.Query("state"), flow.State) {
		webLog.Warn().Str("ip", c.ClientIP()).Msg("oidc state mismatch")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	provider, err := discoverOIDCProvider(ctx, cfg.IssuerURL)
	if err != nil {
		webLog.Error().Err(err).Msg("oidc provider discovery failed on callback")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}
	oauthCfg := s.oauthConfig(c, cfg, provider)

	token, err := oauthCfg.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(flow.CodeVerifier))
	if err != nil {
		webLog.Error().Err(err).Msg("oidc code exchange failed")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		webLog.Error().Msg("oidc token response missing id_token")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		webLog.Error().Err(err).Msg("oidc id_token verification failed")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}
	if idToken.Nonce != flow.Nonce {
		webLog.Warn().Msg("oidc nonce mismatch")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		webLog.Error().Err(err).Msg("oidc claims decode failed")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	user, err := auth.UpsertOIDCUser(ctx, s.db, idToken.Issuer, idToken.Subject, claims.PreferredUsername, claims.Email)
	if err != nil {
		webLog.Error().Err(err).Msg("oidc user provisioning failed")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}

	sess, err := auth.IssueSession(ctx, s.db, user, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		webLog.Error().Err(err).Msg("oidc session issue failed")
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
		return
	}
	s.authMgr.SetSessionCookie(c, sess.Token, int(auth.SessionTTL.Seconds()))
	s.authMgr.RotateCSRFToken(c) // auth-state boundary, mirror form login

	authLog.Info().Str("ip", c.ClientIP()).Str("username", user.Username).
		Str("source", "oidc").Msg("Auth-Success oidc login")

	c.Redirect(http.StatusSeeOther, flow.ReturnURL)
}

// --- flow-state cookie helpers ---

func (s *Server) sealOIDCFlow(c *gin.Context, flow oidcFlowState) error {
	plain, err := json.Marshal(flow)
	if err != nil {
		return err
	}
	sealed, err := s.credBox.Encrypt(plain)
	if err != nil {
		return err
	}
	val := base64.RawURLEncoding.EncodeToString(sealed)
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oidcFlowCookie, val, int(oidcFlowTTL.Seconds()), "/auth/oidc", "", true, true)
	return nil
}

func (s *Server) openOIDCFlow(c *gin.Context) (oidcFlowState, bool) {
	raw, err := c.Cookie(oidcFlowCookie)
	if err != nil || raw == "" {
		return oidcFlowState{}, false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return oidcFlowState{}, false
	}
	plain, err := s.credBox.Decrypt(sealed)
	if err != nil {
		return oidcFlowState{}, false
	}
	var flow oidcFlowState
	if err := json.Unmarshal(plain, &flow); err != nil {
		return oidcFlowState{}, false
	}
	if time.Now().Unix() > flow.ExpiresAt {
		return oidcFlowState{}, false
	}
	return flow, true
}

func (s *Server) clearOIDCFlow(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oidcFlowCookie, "", -1, "/auth/oidc", "", true, true)
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// handleSecurityOIDC (POST /settings/security/oidc) saves the OIDC settings.
// The client secret is write-only: a blank submit preserves the stored value,
// and it's encrypted at rest with the credential box.
func (s *Server) handleSecurityOIDC(c *gin.Context) {
	ctx := c.Request.Context()
	enabled := c.PostForm("oidc_enabled") == "on"
	providerName := strings.TrimSpace(c.PostForm("oidc_provider_name"))
	issuer := strings.TrimRight(strings.TrimSpace(c.PostForm("oidc_issuer_url")), "/")
	clientID := strings.TrimSpace(c.PostForm("oidc_client_id"))
	clientSecret := strings.TrimSpace(c.PostForm("oidc_client_secret"))
	scopes := strings.TrimSpace(c.PostForm("oidc_scopes"))

	// Resolve the currently-stored secret so a blank field means "keep".
	existing, _ := auth.LoadOIDCConfig(ctx, s.db, s.credBox)
	haveSecret := existing != nil && existing.ClientSecret != ""

	if enabled {
		if issuer == "" || clientID == "" || (clientSecret == "" && !haveSecret) {
			s.renderSecurityPage(c, http.StatusBadRequest, gin.H{
				"Error": "Issuer URL, Client ID, and Client Secret are required to enable OIDC.",
			})
			return
		}
		if _, err := url.ParseRequestURI(issuer); err != nil {
			s.renderSecurityPage(c, http.StatusBadRequest, gin.H{"Error": "Issuer URL is not a valid URL."})
			return
		}
		// Fail fast: confirm discovery works before enabling, so a typo'd
		// issuer can't lock the operator into a broken login button.
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, derr := discoverOIDCProvider(dctx, issuer)
		cancel()
		if derr != nil {
			s.renderSecurityPage(c, http.StatusBadRequest, gin.H{
				"Error": "Could not reach the OIDC provider's discovery endpoint: " + derr.Error(),
			})
			return
		}
	}

	_ = s.db.SetSetting(ctx, auth.SettingKeyOIDCEnabled, boolToSetting(enabled))
	if providerName == "" {
		providerName = auth.DefaultOIDCProviderName
	}
	_ = s.db.SetSetting(ctx, auth.SettingKeyOIDCProviderName, providerName)
	_ = s.db.SetSetting(ctx, auth.SettingKeyOIDCIssuerURL, issuer)
	_ = s.db.SetSetting(ctx, auth.SettingKeyOIDCClientID, clientID)
	if scopes == "" {
		scopes = auth.DefaultOIDCScopes
	}
	_ = s.db.SetSetting(ctx, auth.SettingKeyOIDCScopes, scopes)

	// Only overwrite the secret when a new one was actually entered.
	if clientSecret != "" {
		sealed, err := s.credBox.Encrypt([]byte(clientSecret))
		if err != nil {
			s.renderSecurityPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to encrypt client secret: " + err.Error()})
			return
		}
		_ = s.db.SetSetting(ctx, auth.SettingKeyOIDCClientSecret, string(sealed))
	}

	authLog.Info().Bool("enabled", enabled).Str("issuer", issuer).Msg("oidc settings updated")
	s.renderSecurityPage(c, http.StatusOK, gin.H{"Success": "OIDC settings updated."})
}

func boolToSetting(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// oidcReadyForBootstrap reports whether OIDC is enabled, configured, and its
// discovery endpoint is reachable — i.e. enabling forms auth with zero local
// users won't lock the operator out, because they can still sign in via SSO
// (which auto-provisions their account on first login).
func (s *Server) oidcReadyForBootstrap(ctx context.Context) bool {
	cfg, err := auth.LoadOIDCConfig(ctx, s.db, s.credBox)
	if err != nil || !cfg.Enabled || !cfg.Configured() {
		return false
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, derr := discoverOIDCProvider(dctx, cfg.IssuerURL)
	return derr == nil
}
