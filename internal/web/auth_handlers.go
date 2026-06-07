package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/auth"
	"github.com/mstrhakr/audplexus/internal/database"
	"golang.org/x/text/unicode/norm"
)

// throttleKey derives the per-(client, username) bucket key for the login
// throttle. Trim + NFC-normalize + lowercase so trivial variants
// ("admin ", " admin", "Admin", "аdmin" w/ Cyrillic а) all collapse to one
// bucket; otherwise an attacker spins up fresh buckets per encoding and
// bypasses the rate limit.
func throttleKey(clientIP, username string) string {
	u := strings.ToLower(strings.TrimSpace(norm.NFC.String(username)))
	return clientIP + "|" + u
}

// handleLoginGet renders the login form. If the visitor is already
// authenticated, bounce them to the dashboard so the back button after
// /logout doesn't dump them on a stale form.
func (s *Server) handleLoginGet(c *gin.Context) {
	if auth.CurrentUser(c) != nil {
		c.Redirect(http.StatusFound, "/")
		return
	}
	returnURL := sanitizeReturnURL(c.Query("returnUrl"))
	data := gin.H{
		"CSRFToken": auth.CSRFToken(c),
		"ReturnURL": returnURL,
		"Page":      "login",
	}
	if c.Query("loginFailed") == "true" {
		data["Error"] = "Invalid username or password."
	}
	c.HTML(http.StatusOK, "login.html", data)
}

// handleLoginPost verifies credentials, mints a session, and redirects.
// Throttled per (ip, username). On failure → /login?loginFailed=true.
func (s *Server) handleLoginPost(c *gin.Context) {
	ctx := c.Request.Context()
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")
	returnURL := sanitizeReturnURL(c.PostForm("returnUrl"))

	key := throttleKey(c.ClientIP(), username)
	if ok, retry := s.authMgr.Throttle.Allow(key); !ok {
		c.Header("Retry-After", "60")
		authLog.Warn().
			Str("ip", c.ClientIP()).
			Str("username", username).
			Time("retry_at", retry).
			Msg("Auth-Failure rate-limited")
		c.HTML(http.StatusTooManyRequests, "login.html", gin.H{
			"CSRFToken":   auth.CSRFToken(c),
			"ReturnURL":   returnURL,
			"Error":       "Too many sign-in attempts. Please wait a minute.",
			"LockedUntil": retry.Format("15:04:05"),
			"Page":        "login",
		})
		return
	}

	user, err := auth.VerifyLogin(ctx, s.db, username, password)
	if err != nil || user == nil {
		s.authMgr.Throttle.RecordFailure(key)
		authLog.Warn().
			Str("ip", c.ClientIP()).
			Str("username", username).
			Msg("Auth-Failure")
		// Rotate the CSRF token so any pre-login value an attacker may have
		// stashed (e.g. by visiting /login from a phishing iframe) cannot be
		// reused against the victim's eventual authenticated session.
		s.authMgr.RotateCSRFToken(c)
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true"+returnURLQuery(returnURL))
		return
	}

	// Successful login — clear failure counter, mint session, set cookie.
	s.authMgr.Throttle.Reset(key)
	sess, err := auth.IssueSession(ctx, s.db, user, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		authLog.Error().Err(err).Msg("issue session failed")
		c.String(http.StatusInternalServerError, "could not start session")
		return
	}
	s.authMgr.SetSessionCookie(c, sess.Token, int(auth.SessionTTL.Seconds()))
	// Rotate the CSRF token at the auth-state boundary so the post-login
	// session can't reuse a token an unauthenticated visitor (or attacker)
	// captured before the cookie pair was bound to a session.
	s.authMgr.RotateCSRFToken(c)
	authLog.Info().
		Str("ip", c.ClientIP()).
		Str("username", user.Username).
		Msg("Auth-Success")

	c.Redirect(http.StatusSeeOther, returnURL)
}

// returnURLQuery appends &returnUrl=... when the path is something other than
// the default "/". The login form re-renders with the encoded value so the
// post-login redirect targets the page the user originally requested.
func returnURLQuery(returnURL string) string {
	if returnURL == "" || returnURL == "/" {
		return ""
	}
	return "&returnUrl=" + url.QueryEscape(returnURL)
}

// handleLogout deletes the current session and bounces to /login.
func (s *Server) handleLogout(c *gin.Context) {
	ctx := c.Request.Context()
	if sess := auth.CurrentSession(c); sess != nil {
		_ = auth.DeleteSession(ctx, s.db, sess.Token)
		authLog.Info().
			Str("ip", c.ClientIP()).
			Int64("user_id", sess.UserID).
			Msg("Auth-Logout")
	}
	s.authMgr.ClearSessionCookie(c)
	// Drop the CSRF token bound to the now-dead session so the next visitor
	// on this browser starts fresh.
	s.authMgr.RotateCSRFToken(c)
	c.Redirect(http.StatusSeeOther, "/login")
}

// renderSecurityPage renders the unified Settings page with the security
// section data merged in. Used by every POST handler in this file — the
// section lives on /settings under #section-security, so all responses
// re-render the same page with the success/error banner attached.
func (s *Server) renderSecurityPage(c *gin.Context, status int, extra gin.H) {
	ctx := c.Request.Context()
	data := s.settingsPageData(ctx)
	for k, v := range s.securityPageData(ctx, c) {
		data[k] = v
	}
	for k, v := range extra {
		data[k] = v
	}
	data["Page"] = "settings"
	data["FocusSection"] = "security"
	c.HTML(status, "settings.html", s.withSidebar(c, data))
}

func (s *Server) securityPageData(ctx context.Context, c *gin.Context) gin.H {
	apiKey, _ := s.db.GetSetting(ctx, auth.SettingKeyAPIKey)
	method := string(s.authMgr.CurrentMethod(ctx))
	required := string(s.authMgr.CurrentRequired(ctx))
	trustedProxies, _ := s.db.GetSetting(ctx, auth.SettingKeyTrustedProxies)
	cookieDomain, _ := s.db.GetSetting(ctx, auth.SettingKeyCookieDomain)

	// Surface the admin username if a single user exists. Saves the user
	// from having to retype it in the password form.
	var username string
	user := auth.CurrentUser(c)
	if user != nil {
		username = user.Username
	} else if u, _ := s.firstUser(ctx); u != nil {
		username = u.Username
	}

	return gin.H{
		"Page":           "security",
		"CSRFToken":      auth.CSRFToken(c),
		"APIKey":         apiKey,
		"AuthMethod":     method,
		"AuthRequired":   required,
		"TrustedProxies": trustedProxies,
		"CookieDomain":   cookieDomain,
		"Username":       username,
		"User":           user,
	}
}

// firstUser returns the oldest user row, or nil when no users exist. Wrapper
// around the DB method so future call sites have one place to swap.
func (s *Server) firstUser(ctx context.Context) (*database.User, error) {
	return s.db.GetFirstUser(ctx)
}

// passwordBootstrapAllowed reports whether the no-current-password branch of
// handleSecurityPassword may proceed. We allow it when (a) the caller is
// already authenticated (defense in depth: not possible today since
// CountUsers==0 implies no sessions, but safe if that ever changes) or (b)
// the request originates from a local-only network as defined by the
// trusted_proxies setting. Otherwise an anonymous LAN visitor on an upgrade
// install could create the first admin before the operator does.
func (s *Server) passwordBootstrapAllowed(c *gin.Context) bool {
	if auth.CurrentUser(c) != nil {
		return true
	}
	return s.authMgr.IsLocalRequest(c)
}

// handleSecurityAuthMethod saves the auth_method + auth_required selection.
func (s *Server) handleSecurityAuthMethod(c *gin.Context) {
	ctx := c.Request.Context()
	method := auth.AuthMethod(strings.TrimSpace(c.PostForm("auth_method")))
	required := auth.AuthRequired(strings.TrimSpace(c.PostForm("auth_required")))
	trustedProxies := strings.TrimSpace(c.PostForm("trusted_proxies"))

	if !method.Valid() {
		s.renderSecurityPage(c, http.StatusBadRequest, gin.H{"Error": "Unknown authentication method."})
		return
	}
	if !required.Valid() {
		s.renderSecurityPage(c, http.StatusBadRequest, gin.H{"Error": "Unknown auth-required mode."})
		return
	}

	// Switching to forms without a user would lock everyone out — require
	// that the admin row exists first.
	if method == auth.AuthMethodForms {
		count, _ := s.db.CountUsers(ctx)
		if count == 0 {
			s.renderSecurityPage(c, http.StatusBadRequest, gin.H{
				"Error": "Set an admin password in the next section before turning Forms authentication on.",
			})
			return
		}
	}

	_ = s.db.SetSetting(ctx, auth.SettingKeyAuthMethod, string(method))
	_ = s.db.SetSetting(ctx, auth.SettingKeyAuthRequired, string(required))
	_ = s.db.SetSetting(ctx, auth.SettingKeyTrustedProxies, trustedProxies)

	s.renderSecurityPage(c, http.StatusOK, gin.H{"Success": "Authentication settings updated."})
}

// handleSecurityAPIKeyRotate generates a fresh API key and saves it.
func (s *Server) handleSecurityAPIKeyRotate(c *gin.Context) {
	ctx := c.Request.Context()
	if _, err := auth.RotateAPIKey(ctx, s.db); err != nil {
		s.renderSecurityPage(c, http.StatusInternalServerError, gin.H{"Error": "Could not rotate API key: " + err.Error()})
		return
	}
	authLog.Info().Str("ip", c.ClientIP()).Msg("API key rotated")
	s.renderSecurityPage(c, http.StatusOK, gin.H{"Success": "API key rotated. Update every external integration with the new value."})
}

// handleSecurityPassword sets or changes the admin password. When no user
// exists yet (initial setup), current_password is not required. Otherwise
// the current password must match before the change is accepted.
//
// Security: this handler is reachable when auth_method=none (the whole point
// of the Security page is to let unauthenticated upgrade installs turn auth
// ON). That means an anonymous LAN visitor could otherwise post to this
// endpoint and create their own admin before the legit operator does. We
// guard the no-current-password path with the same trust check the auth
// middleware uses for bypass: only allow it when the request is locally
// bypassed (loopback / private network as configured) OR when the caller is
// already an authenticated admin. The dedicated bootstrap path for fresh
// installs is /setup/admin, which has its own onboarded-gate.
func (s *Server) handleSecurityPassword(c *gin.Context) {
	ctx := c.Request.Context()
	username := strings.TrimSpace(c.PostForm("username"))
	currentPwd := c.PostForm("current_password")
	newPwd := c.PostForm("new_password")

	if username == "" || newPwd == "" {
		s.renderSecurityPage(c, http.StatusBadRequest, gin.H{"Error": "Username and new password are required."})
		return
	}
	if len(newPwd) < 8 {
		s.renderSecurityPage(c, http.StatusBadRequest, gin.H{"Error": "Password must be at least 8 characters."})
		return
	}

	count, _ := s.db.CountUsers(ctx)
	if count == 0 {
		// Bootstrap path — no admin exists yet. Only permit when the caller
		// is trusted by the local-bypass rules OR is already authenticated.
		// An anonymous LAN visitor on an upgrade install with auth_method=
		// none must not be able to claim the admin account out from under
		// the legit operator.
		if !s.passwordBootstrapAllowed(c) {
			authLog.Warn().
				Str("ip", c.ClientIP()).
				Msg("Auth-Failure password bootstrap rejected — not local/authenticated")
			s.renderSecurityPage(c, http.StatusForbidden, gin.H{
				"Error": "Use the setup wizard at /setup to create the initial admin account, or call this endpoint from a trusted local address.",
			})
			return
		}
	} else {
		// Existing admin → require current password.
		existing, err := s.db.GetUserByUsername(ctx, username)
		if err != nil || existing == nil {
			// Fall back to the first user if the username changed.
			existing, _ = s.firstUser(ctx)
		}
		if existing == nil {
			s.renderSecurityPage(c, http.StatusBadRequest, gin.H{"Error": "No existing admin found."})
			return
		}
		if !auth.VerifyPassword(currentPwd, existing.Password, existing.Salt, existing.Iterations) {
			authLog.Warn().Str("ip", c.ClientIP()).Msg("Auth-Failure password change")
			s.renderSecurityPage(c, http.StatusUnauthorized, gin.H{"Error": "Current password is incorrect."})
			return
		}
	}

	user, err := auth.CreateOrUpdateAdmin(ctx, s.db, username, newPwd)
	if err != nil {
		if errors.Is(err, database.ErrDuplicateUser) {
			s.renderSecurityPage(c, http.StatusConflict, gin.H{"Error": "A user with that username already exists. Pick a different username."})
			return
		}
		s.renderSecurityPage(c, http.StatusInternalServerError, gin.H{"Error": "Could not save user: " + err.Error()})
		return
	}

	// Identifier was rotated inside CreateOrUpdateAdmin — every existing
	// session is now dead. Re-mint one for the acting user so they stay
	// logged in here.
	sess, err := auth.IssueSession(ctx, s.db, user, c.Request.UserAgent(), c.ClientIP())
	if err == nil {
		s.authMgr.SetSessionCookie(c, sess.Token, int(auth.SessionTTL.Seconds()))
	}
	authLog.Info().Str("ip", c.ClientIP()).Str("username", user.Username).Msg("password changed")
	s.renderSecurityPage(c, http.StatusOK, gin.H{"Success": "Password updated. Other browser sessions have been signed out."})
}

// handleSecurityRevokeAllSessions rotates the user identifier (invalidating
// every session) then re-mints the acting one.
func (s *Server) handleSecurityRevokeAllSessions(c *gin.Context) {
	ctx := c.Request.Context()
	user := auth.CurrentUser(c)
	if user == nil {
		s.renderSecurityPage(c, http.StatusUnauthorized, gin.H{"Error": "Sign in first."})
		return
	}
	newID, err := auth.RotateIdentifier(ctx, s.db, user.ID)
	if err != nil {
		s.renderSecurityPage(c, http.StatusInternalServerError, gin.H{"Error": "Could not revoke sessions: " + err.Error()})
		return
	}
	user.Identifier = newID
	if sess, err := auth.IssueSession(ctx, s.db, user, c.Request.UserAgent(), c.ClientIP()); err == nil {
		s.authMgr.SetSessionCookie(c, sess.Token, int(auth.SessionTTL.Seconds()))
	}
	authLog.Info().Int64("user_id", user.ID).Msg("all sessions revoked")
	s.renderSecurityPage(c, http.StatusOK, gin.H{"Success": "All other sessions have been signed out."})
}

// sanitizeReturnURL accepts only same-origin relative paths. Anything else
// falls back to "/" so the login form can't be weaponized as an open redirect.
func sanitizeReturnURL(raw string) string {
	if raw == "" {
		return "/"
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return raw
}

// authExemptPath returns true for paths that bypass Require. /static, the
// login flow itself, and /healthz must remain reachable without a session
// (and without an API key) so users can recover.
//
// The setup wizard is exempt ONLY while onboarding is in progress. Once
// onboarded=true, /setup/* is back behind auth — otherwise an anonymous LAN
// visitor could call /setup/restart to reset onboarded and then re-bootstrap
// the admin. (Handlers also defense-in-depth refuse when onboarded, but
// keeping them off the public surface is cleaner.)
func (s *Server) authExemptPath(path string) bool {
	switch path {
	case "/login", "/logout", "/healthz":
		return true
	}
	if (path == "/setup" || strings.HasPrefix(path, "/setup/")) && !s.onboarded.Load() {
		return true
	}
	return strings.HasPrefix(path, "/static/")
}

// csrfExemptPath returns true for paths that bypass the CSRF check. The
// SSE stream is GET-only (so already exempt), and /api/* may be hit by
// external integrations using the API key (also exempt via the api-key
// path inside CSRF middleware). Nothing extra needed here today, but the
// hook stays for future per-route opt-outs.
func csrfExemptPath(path string) bool {
	_ = path
	return false
}

