package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
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

var setupSteps = []setupStep{
	{NumLabel: "1", Label: "Welcome"},
	{NumLabel: "2", Label: "Audible"},
	{NumLabel: "3", Label: "Destinations"},
	{NumLabel: "4", Label: "Done"},
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
func (s *Server) handleSetupWizard(c *gin.Context) {
	ctx := c.Request.Context()

	step := 0
	if v := c.Query("step"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			step = n
		}
	}
	if step < 0 {
		step = 0
	}
	if step >= len(setupSteps) {
		step = len(setupSteps) - 1
	}

	data := s.authBaseData(ctx)
	data["Page"] = "setup"
	data["Steps"] = setupSteps
	data["CurrentStep"] = step
	data["AudiobooksPath"] = s.audiobooksPath

	// Destinations summary for steps 3 (list) and 4 (recap). We use the
	// rich destinationSummaries shape so the picker-list shows type +
	// display name + URL consistently with the live Destinations page.
	counts := s.libraryStatusCounts(ctx)
	data["Destinations"] = s.destinationSummaries(ctx, s.coverageDenominator(ctx, counts))

	// On the Audible step, if the user has already picked a marketplace
	// but isn't authenticated yet, generate the auth URL so the page can
	// render the "Open Amazon sign-in" button without a separate POST.
	if step == 1 && !s.audible.IsAuthenticated() {
		if authURL, err := s.audible.GetAuthURL(); err == nil {
			data["AuthURL"] = authURL.URL
			data["CodeVerifier"] = authURL.CodeVerifier
			data["DeviceSerial"] = authURL.DeviceSerial
		}
	}

	c.HTML(http.StatusOK, "setup.html", s.withSidebar(ctx, data))
}

// handleSetupMarketplace persists the marketplace selection submitted by
// the wizard's Audible step. Distinct from /auth/marketplace because we
// don't want to render settings.html on success — we want to bounce back
// into the wizard with the auth URL ready.
func (s *Server) handleSetupMarketplace(c *gin.Context) {
	mp := strings.ToLower(strings.TrimSpace(c.PostForm("marketplace")))
	if mp == "" {
		c.Redirect(http.StatusSeeOther, "/setup?step=1")
		return
	}
	_ = s.db.SetSetting(c.Request.Context(), "audible_marketplace", mp)
	c.Redirect(http.StatusSeeOther, "/setup?step=1")
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
