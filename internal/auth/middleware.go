package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/database"
)

// Cookie / header names. Centralized so handlers, middleware, and the
// frontend script all agree.
const (
	SessionCookieName = "audplexusAuth"
	CSRFCookieName    = "csrftoken"
	CSRFHeaderName    = "X-CSRF-Token"
	APIKeyHeaderName  = "X-Api-Key"
	APIKeyQueryName   = "apikey"

	ctxKeyUser    = "auth_user"
	ctxKeySession = "auth_session"
	ctxKeyAPIAuth = "auth_via_apikey"
	ctxKeyCSRF    = "csrf_token"
)

// Manager owns the configuration the middleware needs at request time.
// It reads auth settings from the DB on every request so changes in the
// Security settings page take effect without a restart. The values are
// cheap to read (key/value lookups).
type Manager struct {
	DB       database.Database
	Throttle *LoginThrottle
	// OnCSRFFailure, when set, is invoked by the CSRF middleware in place
	// of the default JSON 403 abort. Wired by the web package so browser
	// callers get the styled error page while /api/* + XHR keep the JSON
	// contract. Nil-safe — the middleware falls back to the JSON abort.
	OnCSRFFailure gin.HandlerFunc
}

// NewManager wires the middleware against a database and a fresh throttle.
func NewManager(db database.Database) *Manager {
	return &Manager{DB: db, Throttle: DefaultLoginThrottle()}
}

// CurrentMethod reads the configured auth method from settings, falling back
// to AuthMethodNone (preserves the existing-install upgrade semantics — new
// installs flip this to forms via the setup wizard before exposing routes).
func (m *Manager) CurrentMethod(ctx context.Context) AuthMethod {
	v, _ := m.DB.GetSetting(ctx, SettingKeyAuthMethod)
	if v == "" {
		return AuthMethodNone
	}
	method := AuthMethod(v)
	if !method.Valid() {
		return AuthMethodNone
	}
	return method
}

// CurrentRequired reads the auth-required mode, defaulting to "enabled".
func (m *Manager) CurrentRequired(ctx context.Context) AuthRequired {
	v, _ := m.DB.GetSetting(ctx, SettingKeyAuthRequired)
	if v == "" {
		return AuthRequiredEnabled
	}
	r := AuthRequired(v)
	if !r.Valid() {
		return AuthRequiredEnabled
	}
	return r
}

// trustedProxies returns the parsed CIDR list for the local-address check.
func (m *Manager) trustedProxies(ctx context.Context) []*net.IPNet {
	v, _ := m.DB.GetSetting(ctx, SettingKeyTrustedProxies)
	return ParseTrustedProxies(v)
}

// cookieDomain returns the configured Domain attribute for session/CSRF cookies.
// Empty string = default (host-only cookie scoped to the responding origin).
func (m *Manager) cookieDomain(ctx context.Context) string {
	v, _ := m.DB.GetSetting(ctx, SettingKeyCookieDomain)
	return strings.TrimSpace(v)
}

// localBypass reports whether the request should skip auth because of the
// AuthRequired knob.
func (m *Manager) localBypass(c *gin.Context, mode AuthRequired) bool {
	switch mode {
	case AuthRequiredDisabledForLocalhost:
		return IsLoopback(c.Request, m.trustedProxies(c.Request.Context()))
	case AuthRequiredDisabledForLocal:
		return IsLocalAddress(c.Request, m.trustedProxies(c.Request.Context()))
	}
	return false
}

// IsLocalRequest reports whether the request originated from a local-only
// network (loopback / RFC1918 / link-local / ULA), evaluated against the
// configured trusted_proxies CIDR list. Use this from handlers that need to
// gate a sensitive bootstrap action (e.g. initial-password creation when
// auth_method=none) on the request being trusted regardless of the
// AuthRequired knob.
func (m *Manager) IsLocalRequest(c *gin.Context) bool {
	return IsLocalAddress(c.Request, m.trustedProxies(c.Request.Context()))
}

// extractAPIKey returns the API key from the request. Accepts X-Api-Key and
// ?apikey= globally; Authorization: Bearer is only honored on /api/* paths so
// it doesn't collide with future OIDC/JWT integrations that might post Bearer
// tokens to UI routes.
func extractAPIKey(c *gin.Context) string {
	if v := c.GetHeader(APIKeyHeaderName); v != "" {
		return v
	}
	if v := c.Query(APIKeyQueryName); v != "" {
		return v
	}
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		if v := c.GetHeader("Authorization"); strings.HasPrefix(v, "Bearer ") {
			return strings.TrimPrefix(v, "Bearer ")
		}
	}
	return ""
}

// Authenticate resolves the caller's identity and stores it on the gin
// context. It NEVER blocks the request — Require does that. Splitting them
// lets /login render with a "you are already logged in" hint without bouncing
// the user.
//
// Resolution order: API key (constant-time compare against stored), then
// session cookie. Stores `auth_user`, `auth_session`, `auth_via_apikey`.
func (m *Manager) Authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// API key check first — it's a constant-time compare against settings.
		if provided := extractAPIKey(c); provided != "" {
			stored, _ := GetAPIKey(ctx, m.DB)
			if stored != "" && ConstantTimeStringEqual(stored, provided) {
				c.Set(ctxKeyAPIAuth, true)
				c.Next()
				return
			}
		}

		// Session cookie.
		if cookie, err := c.Cookie(SessionCookieName); err == nil && cookie != "" {
			sess, user, err := ResolveSession(ctx, m.DB, cookie)
			if err == nil && sess != nil && user != nil {
				c.Set(ctxKeyUser, user)
				c.Set(ctxKeySession, sess)
			}
		}
		c.Next()
	}
}

// Require enforces auth based on the current auth_method and auth_required
// settings. Must run AFTER Authenticate. Exempt paths bypass the check.
//
// Exempt paths typically include /login, /logout, /static, /healthz. The
// caller passes them in so this package doesn't need to know about route
// layout.
func (m *Manager) Require(exempt func(path string) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if exempt != nil && exempt(c.Request.URL.Path) {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		if m.CurrentMethod(ctx) == AuthMethodNone {
			c.Next()
			return
		}
		if _, ok := c.Get(ctxKeyAPIAuth); ok {
			c.Next()
			return
		}
		if _, ok := c.Get(ctxKeyUser); ok {
			c.Next()
			return
		}
		if m.localBypass(c, m.CurrentRequired(ctx)) {
			c.Next()
			return
		}
		// Not authenticated and no bypass applies. Log + redirect/401.
		authLog.Info().
			Str("ip", c.ClientIP()).
			Str("path", c.Request.URL.Path).
			Msg("Auth-Unauthorized")
		m.unauthorized(c)
	}
}

// unauthorized writes the right response shape for the client kind:
//   - HX-Request → HX-Redirect header so HTMX actually navigates
//   - /api/* or XHR → 401 JSON
//   - HTML GET → 302 to /login with returnUrl
func (m *Manager) unauthorized(c *gin.Context) {
	if c.GetHeader("HX-Request") == "true" {
		c.Header("HX-Redirect", loginURLFor(c))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/api/") ||
		strings.EqualFold(c.GetHeader("X-Requested-With"), "XMLHttpRequest") ||
		strings.Contains(c.GetHeader("Accept"), "application/json") {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	if c.Request.Method == http.MethodGet {
		c.Redirect(http.StatusFound, loginURLFor(c))
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusUnauthorized)
}

func loginURLFor(c *gin.Context) string {
	if c.Request.URL.Path == "/" || c.Request.URL.Path == "/login" {
		return "/login"
	}
	q := url.Values{}
	q.Set("returnUrl", c.Request.URL.RequestURI())
	return "/login?" + q.Encode()
}

// --- CSRF ---

// CSRF implements the double-submit cookie pattern. On every GET it makes
// sure a csrftoken cookie exists (creating one if not). On every non-GET it
// constant-time compares the cookie against the X-CSRF-Token header.
//
// Exemptions:
//   - API-key-authenticated requests (no cookie session = no CSRF surface)
//   - GET/HEAD/OPTIONS
//   - Paths returned true by `exempt`
func (m *Manager) CSRF(exempt func(path string) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		safeMethod := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions

		// Ensure a token is in the cookie + context. Even on safe methods we
		// stamp one so templates can render <input type=hidden name=csrf_token>.
		token, _ := c.Cookie(CSRFCookieName)
		if token == "" {
			t, err := newCSRFToken()
			if err == nil {
				token = t
				m.setCSRFCookie(c, token)
			}
		}
		c.Set(ctxKeyCSRF, token)

		if safeMethod {
			c.Next()
			return
		}
		if exempt != nil && exempt(c.Request.URL.Path) {
			c.Next()
			return
		}
		// API-key authenticated POSTs don't need CSRF — they're not cookie-
		// authenticated, so the threat model doesn't apply.
		if _, ok := c.Get(ctxKeyAPIAuth); ok {
			c.Next()
			return
		}

		header := c.GetHeader(CSRFHeaderName)
		if header == "" {
			// Fall back to a form field for plain HTML POSTs.
			header = c.PostForm("csrf_token")
		}
		if token == "" || header == "" || !ConstantTimeStringEqual(token, header) {
			authLog.Warn().
				Str("ip", c.ClientIP()).
				Str("path", c.Request.URL.Path).
				Msg("CSRF token mismatch")
			if m.OnCSRFFailure != nil {
				m.OnCSRFFailure(c)
				if c.IsAborted() {
					return
				}
			}
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf token invalid"})
			return
		}
		c.Next()
	}
}

func newCSRFToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (m *Manager) setCSRFCookie(c *gin.Context, token string) {
	// Not HttpOnly: the htmx hook reads it from JS to set the header.
	// SameSite=Lax + same-origin + the header check is the actual CSRF
	// defense; this cookie just carries the bound token.
	secure := m.isHTTPS(c)
	domain := m.cookieDomain(c.Request.Context())
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(CSRFCookieName, token, int(SessionTTL.Seconds()), "/", domain, secure, false)
}

// SetSessionCookie writes the audplexusAuth cookie with the right security
// flags for the request scheme. HttpOnly so JS can't read it; SameSite=Lax
// so cross-site POSTs are blocked but normal link clicks still log you in;
// Secure when TLS is in use.
func (m *Manager) SetSessionCookie(c *gin.Context, token string, ttl int) {
	secure := m.isHTTPS(c)
	domain := m.cookieDomain(c.Request.Context())
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(SessionCookieName, token, ttl, "/", domain, secure, true)
}

// ClearSessionCookie expires the session cookie. Gin/net.http uses MaxAge<0
// to indicate deletion.
func (m *Manager) ClearSessionCookie(c *gin.Context) {
	domain := m.cookieDomain(c.Request.Context())
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(SessionCookieName, "", -1, "/", domain, m.isHTTPS(c), true)
}

// RotateCSRFToken mints a fresh CSRF token, replaces the cookie, and updates
// the context entry so the rest of the request renders templates with the new
// value. Call after every auth-state transition (login success, login failure,
// logout) so an attacker who pre-captured a token by visiting /login can't
// reuse it once the victim authenticates.
func (m *Manager) RotateCSRFToken(c *gin.Context) string {
	t, err := newCSRFToken()
	if err != nil {
		return ""
	}
	m.setCSRFCookie(c, t)
	c.Set(ctxKeyCSRF, t)
	return t
}

// CSRFToken returns the bound CSRF token for the current request (or "" if
// CSRF middleware didn't run).
func CSRFToken(c *gin.Context) string {
	v, _ := c.Get(ctxKeyCSRF)
	s, _ := v.(string)
	return s
}

// CurrentUser returns the authenticated user (or nil if anonymous / API key).
func CurrentUser(c *gin.Context) *database.User {
	v, ok := c.Get(ctxKeyUser)
	if !ok {
		return nil
	}
	u, _ := v.(*database.User)
	return u
}

// CurrentSession returns the request's session (nil for API key / anonymous).
func CurrentSession(c *gin.Context) *database.Session {
	v, ok := c.Get(ctxKeySession)
	if !ok {
		return nil
	}
	s, _ := v.(*database.Session)
	return s
}

// IsAPIKeyAuth reports whether the request authenticated via API key.
func IsAPIKeyAuth(c *gin.Context) bool {
	_, ok := c.Get(ctxKeyAPIAuth)
	return ok
}

// isHTTPS reports whether the request reached the server over TLS. Direct
// TLS termination wins outright; X-Forwarded-Proto is only honored when the
// immediate peer matches the trusted_proxies CIDR list. This matches the
// trust model used for X-Forwarded-For in local.go — an unauthenticated LAN
// attacker can't spoof Secure-cookie-suppression by setting their own XFP.
// (Manager method so it can read trusted_proxies from settings.)

// IsHTTPS is the exported view of isHTTPS for callers that need to build
// absolute URLs with the correct scheme (e.g. the OIDC redirect URL).
func (m *Manager) IsHTTPS(c *gin.Context) bool { return m.isHTTPS(c) }

func (m *Manager) isHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	trusted := m.trustedProxies(c.Request.Context())
	if len(trusted) == 0 {
		return false
	}
	peer := requestIP(c.Request)
	if peer == nil || !ipIn(peer, trusted) {
		return false
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}
