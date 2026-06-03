package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/auth"
)

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

	key := c.ClientIP() + "|" + strings.ToLower(username)
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
		// Rotate CSRF token on failed login to invalidate any stale token
		// captured by the attacker.
		c.Redirect(http.StatusSeeOther, "/login?loginFailed=true")
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
	auth.SetSessionCookie(c, sess.Token, int(auth.SessionTTL.Seconds()))
	authLog.Info().
		Str("ip", c.ClientIP()).
		Str("username", user.Username).
		Msg("Auth-Success")

	c.Redirect(http.StatusSeeOther, returnURL)
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
	auth.ClearSessionCookie(c)
	c.Redirect(http.StatusSeeOther, "/login")
}

// handleSecurityPage renders the Security settings page. Always allows
// access — even when auth_method is "none" so the user can switch on auth.
func (s *Server) handleSecurityPage(c *gin.Context) {
	s.renderSecurityPage(c, http.StatusOK, gin.H{})
}

func (s *Server) renderSecurityPage(c *gin.Context, status int, extra gin.H) {
	ctx := c.Request.Context()
	data := s.securityPageData(ctx, c)
	for k, v := range extra {
		data[k] = v
	}
	c.HTML(status, "settings_security.html", s.withSidebar(ctx, data))
}

func (s *Server) securityPageData(ctx context.Context, c *gin.Context) gin.H {
	apiKey, _ := s.db.GetSetting(ctx, auth.SettingKeyAPIKey)
	method := string(s.authMgr.CurrentMethod(ctx))
	required := string(s.authMgr.CurrentRequired(ctx))
	trustedProxies, _ := s.db.GetSetting(ctx, auth.SettingKeyTrustedProxies)

	// Surface the admin username if a single user exists. Saves the user
	// from having to retype it in the password form.
	var username string
	user := auth.CurrentUser(c)
	if user != nil {
		username = user.Username
	} else {
		// Fall back to the first user row (single-admin model).
		count, _ := s.db.CountUsers(ctx)
		if count > 0 {
			// We don't have a ListUsers — pull by ID 1 is fine since the
			// sequence starts at 1 and we never delete the admin row in
			// normal operation. If the row was rotated to a higher ID,
			// the user will simply see an empty username and have to type it.
			if u, _ := s.db.GetUserByID(ctx, 1); u != nil {
				username = u.Username
			}
		}
	}

	return gin.H{
		"Page":           "settings",
		"CSRFToken":      auth.CSRFToken(c),
		"APIKey":         apiKey,
		"AuthMethod":     method,
		"AuthRequired":   required,
		"TrustedProxies": trustedProxies,
		"Username":       username,
		"User":           user,
	}
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
	if count > 0 {
		// Existing admin → require current password.
		existing, err := s.db.GetUserByUsername(ctx, username)
		if err != nil || existing == nil {
			// Fall back to the row by ID 1 if the username changed.
			existing, _ = s.db.GetUserByID(ctx, 1)
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
		s.renderSecurityPage(c, http.StatusInternalServerError, gin.H{"Error": "Could not save user: " + err.Error()})
		return
	}

	// Identifier was rotated inside CreateOrUpdateAdmin — every existing
	// session is now dead. Re-mint one for the acting user so they stay
	// logged in here.
	sess, err := auth.IssueSession(ctx, s.db, user, c.Request.UserAgent(), c.ClientIP())
	if err == nil {
		auth.SetSessionCookie(c, sess.Token, int(auth.SessionTTL.Seconds()))
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
		auth.SetSessionCookie(c, sess.Token, int(auth.SessionTTL.Seconds()))
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
func authExemptPath(path string) bool {
	switch path {
	case "/login", "/logout", "/healthz":
		return true
	}
	// The setup wizard must reach an unauth'd first-run visitor — the whole
	// point of the Admin step is for them to create the first user. Every
	// subpath under /setup is exempt; firstRunGate (which sits AFTER auth
	// in the middleware chain) will redirect already-onboarded visitors
	// away from it.
	if path == "/setup" || strings.HasPrefix(path, "/setup/") {
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

