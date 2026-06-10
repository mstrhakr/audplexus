package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/auth"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/library"
)

// settingKeyOnboarded marks the setup wizard as completed. Stored as "1"
// once the user has either finished or explicitly skipped onboarding so
// they aren't redirected back to /setup on every visit.
const settingKeyOnboarded = "onboarded"

// setupStep is the static descriptor for a wizard step (rendered as the
// breadcrumb chip in the header). Step bodies live in setup.html keyed on
// the numeric index.
type setupStep struct {
	NumLabel string
	Label    string
}

// Step kind tags the panel a given index renders. Templates branch on
// .StepKind so panel ordering can change with the install kind (the Admin
// step is hidden on upgrade installs where auth_method=none) without
// hardcoding indices in HTML.
const (
	stepKindWelcome      = "welcome"
	stepKindAdmin        = "admin"
	stepKindAudible      = "audible"
	stepKindDestinations = "destinations"
	stepKindDone         = "done"
)

type wizardStep struct {
	setupStep
	Kind string
}

// buildSetupSteps returns the panels in display order. The Admin panel is
// only included when forms auth is configured AND we want the user to land
// on it (fresh install, or a re-run that wants to change credentials).
func buildSetupSteps(includeAdmin bool) []wizardStep {
	steps := []wizardStep{{setupStep{Label: "Welcome"}, stepKindWelcome}}
	if includeAdmin {
		steps = append(steps, wizardStep{setupStep{Label: "Admin"}, stepKindAdmin})
	}
	steps = append(steps,
		wizardStep{setupStep{Label: "Audible"}, stepKindAudible},
		wizardStep{setupStep{Label: "Destinations"}, stepKindDestinations},
		wizardStep{setupStep{Label: "Done"}, stepKindDone},
	)
	// Number labels are derived from the final position so they stay in
	// sequence whether or not the admin step is present.
	for i := range steps {
		steps[i].NumLabel = strconv.Itoa(i + 1)
	}
	return steps
}

// isFirstRun returns true when the wizard has neither been completed nor
// skipped yet. Reads from s.onboarded (atomic.Bool) so firstRunGate can
// answer for free on every GET — the DB-backed setting is the source
// of truth, but it's only read once at boot in NewServer and on
// targeted writes via setOnboarded.
//
// ctx kept in the signature for symmetry with the rest of the auth-
// adjacent helpers, even though the atomic read doesn't need it.
func (s *Server) isFirstRun(ctx context.Context) bool {
	return !s.onboarded.Load()
}

// setOnboarded persists the onboarded flag to the DB and updates the
// in-memory mirror in lockstep. value=true writes "1" and flips the
// atomic on; value=false writes "" (empty string is treated as not-
// onboarded by GetSetting → settingBool conventions) and flips it off.
func (s *Server) setOnboarded(ctx context.Context, value bool) error {
	stored := ""
	if value {
		stored = "1"
	}
	if err := s.db.SetSetting(ctx, settingKeyOnboarded, stored); err != nil {
		return err
	}
	s.onboarded.Store(value)
	return nil
}

// handleSetupWizard renders the onboarding wizard. ?step= selects which
// panel to show; out-of-range values clamp to the first step.
//
// The Admin panel is folded in as step 2 (right after Welcome) when forms
// auth is configured. On fresh installs that means the user always sees
// the same wizard shell — no separate /setup/admin redirect, no different
// page chrome. Upgrade installs (auth_method=none) skip the Admin panel
// entirely; the dashboard nag banner pushes them toward Settings → Security.
func (s *Server) handleSetupWizard(c *gin.Context) {
	ctx := c.Request.Context()

	// Include the Admin panel whenever forms auth is selected. This keeps
	// the breadcrumb correct both for fresh installs (where there's no user
	// yet) AND for re-runs of the wizard from Settings (where the user
	// might want to change credentials).
	formsAuth := s.authMgr.CurrentMethod(ctx) == auth.AuthMethodForms
	userCount, _ := s.db.CountUsers(ctx)
	includeAdmin := formsAuth
	steps := buildSetupSteps(includeAdmin)

	step := 0
	if v := c.Query("step"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			step = n
		}
	}
	if step < 0 {
		step = 0
	}
	if step >= len(steps) {
		step = len(steps) - 1
	}

	// Fresh-install gate: when forms auth is on but no user exists yet,
	// pin the user on the Admin panel until they create one. The breadcrumb
	// still renders all steps so they can see the path, but jumping forward
	// via the URL is bounced back here.
	if includeAdmin && userCount == 0 {
		adminIdx := indexOfStepKind(steps, stepKindAdmin)
		if adminIdx >= 0 && step > adminIdx {
			step = adminIdx
		}
	}

	data := s.authBaseData(ctx)
	data["Page"] = "setup"
	data["Steps"] = steps
	data["CurrentStep"] = step
	data["StepKind"] = steps[step].Kind
	data["AudiobooksPath"] = s.audiobooksPath
	data["CSRFToken"] = auth.CSRFToken(c)
	data["AdminExists"] = userCount > 0
	if u, _ := s.db.GetUserByID(ctx, 1); u != nil {
		data["AdminUsername"] = u.Username
	}

	// Destinations summary for the Destinations and Done panels. Rich
	// summary shape so the picker-list shows type + display name + URL.
	counts := s.libraryStatusCounts(ctx)
	data["Destinations"] = s.destinationSummaries(ctx, s.coverageDenominator(ctx, counts))

	// On the Audible step, if the user has already picked a marketplace
	// but isn't authenticated yet, generate the auth URL so the panel can
	// render the "Open Amazon sign-in" button without a separate POST.
	if steps[step].Kind == stepKindAudible && !s.audible.IsAuthenticated() {
		if authURL, err := s.audible.GetAuthURL(); err == nil {
			data["AuthURL"] = authURL.URL
			data["CodeVerifier"] = authURL.CodeVerifier
			data["DeviceSerial"] = authURL.DeviceSerial
		}
	}

	c.HTML(http.StatusOK, "setup.html", s.withSidebar(c, data))
}

// handleSetupRestorePage renders a one-off "restore from backup" page reached
// from the welcome step. POST goes to handleDBRestore. Lives under /setup/*
// so it's auth-exempt during first-run bootstrap (when there's no admin yet).
func (s *Server) handleSetupRestorePage(c *gin.Context) {
	ctx := c.Request.Context()
	data := s.authBaseData(ctx)
	data["Page"] = "setup"
	data["CSRFToken"] = auth.CSRFToken(c)
	c.HTML(http.StatusOK, "setup_restore.html", s.withSidebar(c, data))
}

// indexOfStepKind returns the position of the first step matching kind, or
// -1 if absent. Used by the admin gate to know which slot to pin the user on.
func indexOfStepKind(steps []wizardStep, kind string) int {
	for i, s := range steps {
		if s.Kind == kind {
			return i
		}
	}
	return -1
}

// handleSetupMarketplace persists the marketplace selection submitted by
// the wizard's Audible step. Distinct from /auth/marketplace because we
// don't want to render settings.html on success — we want to bounce back
// into the wizard with the auth URL ready.
func (s *Server) handleSetupMarketplace(c *gin.Context) {
	ctx := c.Request.Context()
	mp := strings.ToLower(strings.TrimSpace(c.PostForm("marketplace")))
	if mp != "" {
		_ = s.db.SetSetting(ctx, "audible_marketplace", mp)
	}
	// Bounce back to whichever index the Audible panel currently occupies
	// (depends on whether the Admin panel is in the flow).
	c.Redirect(http.StatusSeeOther, s.setupURLFor(ctx, stepKindAudible))
}

// setupURLFor returns /setup?step=N pointing at the panel of the given kind
// for the current install configuration. Used by POST handlers that need to
// redirect back into the wizard without hardcoding indices that shift when
// the Admin panel toggles.
func (s *Server) setupURLFor(ctx context.Context, kind string) string {
	includeAdmin := s.authMgr.CurrentMethod(ctx) == auth.AuthMethodForms
	steps := buildSetupSteps(includeAdmin)
	if idx := indexOfStepKind(steps, kind); idx >= 0 {
		return "/setup?step=" + strconv.Itoa(idx)
	}
	return "/setup"
}

// handleSetupFinish marks onboarding complete and bounces to the dashboard.
// Kicks a quick sync only if the user actually authenticated — otherwise
// nothing to sync against.
func (s *Server) handleSetupFinish(c *gin.Context) {
	ctx := c.Request.Context()
	_ = s.setOnboarded(ctx, true)

	// Persist the "automatically download new books" choice from the final
	// step. An unchecked toggle submits nothing, so absence means off.
	autoDownload := c.PostForm("auto_queue_new") == "true"
	_ = s.db.SetSetting(ctx, library.SettingKeyAutoQueueNewBooks, strconv.FormatBool(autoDownload))
	if s.sched != nil {
		s.sched.SetAutoQueueNew(autoDownload)
	}

	if s.audible.IsAuthenticated() {
		// Fire-and-forget — sync runs asynchronously; UI will show progress
		// via the existing /api/sync/status polling on the dashboard.
		go func() {
			added, err := s.sync.QuickSync(context.Background())
			if err != nil {
				webLog.Warn().Err(err).Msg("setup: first quick sync failed")
				return
			}
			if autoDownload && added > 0 {
				if queued, qErr := s.downloads.QueueNewBooks(context.Background()); qErr != nil {
					webLog.Warn().Err(qErr).Int("added", added).Msg("setup: failed to auto-queue new books after first quick sync")
				} else {
					webLog.Info().Int("added", added).Int("queued", queued).Msg("setup: auto-queued new books after first quick sync")
				}
			}
		}()
	}

	c.Redirect(http.StatusSeeOther, "/")
}

// handleSetupSkip marks onboarding complete without running anything —
// the "Skip setup" link on step 0. Useful for testing or for users who
// configured everything via env vars before first boot.
func (s *Server) handleSetupSkip(c *gin.Context) {
	_ = s.setOnboarded(c.Request.Context(), true)
	c.Redirect(http.StatusSeeOther, "/")
}

// handleSetupRestart clears the onboarded flag and routes the user back
// to step 0. Triggered by the "Re-run setup wizard" button in Settings
// (for testing) and from the post-factory-reset redirect.
func (s *Server) handleSetupRestart(c *gin.Context) {
	_ = s.setOnboarded(c.Request.Context(), false)
	c.Redirect(http.StatusSeeOther, "/setup")
}

// firstRunGate redirects unauthenticated, not-yet-onboarded visitors from
// the main app pages to the wizard. Settings, diagnostics, the wizard
// itself, and POST/api routes pass through so users can always escape and
// so the wizard's own subroutes (auth callback, etc.) keep working.
func (s *Server) firstRunGate(c *gin.Context) {
	if c.Request.Method != http.MethodGet {
		c.Next()
		return
	}
	if !s.isFirstRun(c.Request.Context()) {
		c.Next()
		return
	}

	switch c.Request.URL.Path {
	case "/", "/library", "/downloads", "/destinations":
		c.Redirect(http.StatusSeeOther, "/setup")
		c.Abort()
		return
	}
	c.Next()
}

// refuseIfOnboarded bails out with 403 when the install has already finished
// onboarding. The wizard endpoints are auth-exempt so a first-run visitor can
// bootstrap; once that's done, those same endpoints must not be reachable
// without auth (otherwise an anonymous LAN visitor could re-bootstrap and
// reset the admin). Returns true when the caller already wrote a response —
// the handler must not continue.
func (s *Server) refuseIfOnboarded(c *gin.Context) bool {
	if s.onboarded.Load() {
		authLog.Warn().
			Str("ip", c.ClientIP()).
			Str("path", c.Request.URL.Path).
			Msg("setup wizard endpoint hit after onboarding — refusing")
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": "setup is already complete; use Settings → Security to manage the admin account",
		})
		return true
	}
	return false
}

// handleSetupAdminPost creates the initial admin user (or updates the
// existing one's password if the operator re-enters the wizard) and bounces
// the user forward to the Audible step. Posted from the Admin panel of the
// wizard.
func (s *Server) handleSetupAdminPost(c *gin.Context) {
	if s.refuseIfOnboarded(c) {
		return
	}
	ctx := c.Request.Context()
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")

	if username == "" || len(password) < 8 {
		// Re-render the wizard with the Admin panel + an error banner. We
		// pin the step in the query string so the user lands back on the
		// right panel after the redirect-less form re-render.
		s.renderSetupWithError(c, stepKindAdmin, "Username and an 8+ character password are required.")
		return
	}

	user, err := auth.CreateOrUpdateAdmin(ctx, s.db, username, password)
	if err != nil {
		if errors.Is(err, database.ErrDuplicateUser) {
			s.renderSetupWithError(c, stepKindAdmin, "A user with that username already exists. Pick a different username.")
			return
		}
		s.renderSetupWithError(c, stepKindAdmin, "Could not create account: "+err.Error())
		return
	}
	// Re-assert auth settings in case SeedAuthDefaults didn't run (the user
	// reached this step manually) — forms + enabled is the intended shape.
	_ = s.db.SetSetting(ctx, auth.SettingKeyAuthMethod, string(auth.AuthMethodForms))
	if existing, _ := s.db.GetSetting(ctx, auth.SettingKeyAuthRequired); existing == "" {
		_ = s.db.SetSetting(ctx, auth.SettingKeyAuthRequired, string(auth.AuthRequiredEnabled))
	}

	sess, err := auth.IssueSession(ctx, s.db, user, c.Request.UserAgent(), c.ClientIP())
	if err == nil {
		s.authMgr.SetSessionCookie(c, sess.Token, int(auth.SessionTTL.Seconds()))
	}
	authLog.Info().Str("ip", c.ClientIP()).Str("username", user.Username).Msg("admin account created")
	c.Redirect(http.StatusSeeOther, s.setupURLFor(ctx, stepKindAudible))
}

// handleSetupAdminSkip lets the operator bail out of the Admin step without
// creating an account. We flip auth_method to none so the server boots
// unauthenticated; the dashboard nag banner then pushes them toward
// Settings → Security. They land on the Audible step to continue setup.
func (s *Server) handleSetupAdminSkip(c *gin.Context) {
	if s.refuseIfOnboarded(c) {
		return
	}
	ctx := c.Request.Context()
	_ = s.db.SetSetting(ctx, auth.SettingKeyAuthMethod, string(auth.AuthMethodNone))
	authLog.Warn().Str("ip", c.ClientIP()).Msg("admin creation skipped — server is unauthenticated")
	// auth_method is now none, so the Admin step disappears from the
	// breadcrumb on the next render. Send them to the Audible step.
	c.Redirect(http.StatusSeeOther, s.setupURLFor(ctx, stepKindAudible))
}

// renderSetupWithError re-renders the wizard at the given step kind with an
// inline error banner. Used by Admin form validation failures so the user
// stays inside the wizard instead of being bounced around.
func (s *Server) renderSetupWithError(c *gin.Context, kind, errMsg string) {
	ctx := c.Request.Context()
	includeAdmin := s.authMgr.CurrentMethod(ctx) == auth.AuthMethodForms
	steps := buildSetupSteps(includeAdmin)
	stepIdx := indexOfStepKind(steps, kind)
	if stepIdx < 0 {
		stepIdx = 0
	}
	userCount, _ := s.db.CountUsers(ctx)
	data := s.authBaseData(ctx)
	data["Page"] = "setup"
	data["Steps"] = steps
	data["CurrentStep"] = stepIdx
	data["StepKind"] = steps[stepIdx].Kind
	data["AudiobooksPath"] = s.audiobooksPath
	data["CSRFToken"] = auth.CSRFToken(c)
	data["AdminExists"] = userCount > 0
	data["Error"] = errMsg
	c.HTML(http.StatusBadRequest, "setup.html", s.withSidebar(c, data))
}
