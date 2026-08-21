package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/library"

	audible "github.com/mstrhakr/go-audible"
)

// accountView is the per-account view-model for the Settings UI. Credentials
// are never exposed; only an Authenticated flag derived from their presence.
type accountView struct {
	ID            string
	DisplayName   string
	Marketplace   string
	Authenticated bool
	Enabled       bool
	IsPrimary     bool
}

// accountsForView returns every account for the Settings page, ordered with the
// primary first.
func (s *Server) accountsForView(ctx context.Context) []accountView {
	rows, err := s.db.ListAudibleAccounts(ctx)
	if err != nil {
		webLog.Warn().Err(err).Msg("accounts: list failed")
		return nil
	}
	primaryID := ""
	if s.accounts != nil {
		primaryID = s.accounts.PrimaryAccountID()
	}
	out := make([]accountView, 0, len(rows))
	for i := range rows {
		r := rows[i]
		out = append(out, accountView{
			ID:            r.ID,
			DisplayName:   accountDisplayName(&r),
			Marketplace:   r.Marketplace,
			Authenticated: r.HasCredentials(),
			Enabled:       r.Enabled,
			IsPrimary:     r.ID == primaryID,
		})
	}
	return out
}

func accountDisplayName(a *database.AudibleAccount) string {
	if strings.TrimSpace(a.DisplayName) != "" {
		return a.DisplayName
	}
	return "Audible Account"
}

// refreshPrimaryAudible repoints the legacy single-client field at the primary
// account's client. Called after any account mutation so the health/marketplace
// call sites that still read s.audible stay consistent.
func (s *Server) refreshPrimaryAudible() {
	if s.accounts == nil {
		return
	}
	if c := s.accounts.Primary(); c != nil {
		s.audible = c
	}
	// If there's no primary account yet, leave s.audible pointing at the
	// throwaway client passed to NewServer so the legacy call sites that read
	// it (marketplace display, activation bytes) never nil-deref. It simply
	// reports unauthenticated until an account is connected.
}

// audibleAuthenticated reports whether ANY connected account is authenticated.
// Replaces the old single-client check at the sync/health gates.
func (s *Server) audibleAuthenticated() bool {
	if s.accounts != nil {
		return s.accounts.IsAuthenticated()
	}
	return s.audible != nil && s.audible.IsAuthenticated()
}

// reloadAccounts rebuilds the in-memory client set and repoints s.audible.
func (s *Server) reloadAccounts(ctx context.Context) {
	if s.accounts == nil {
		return
	}
	if err := s.accounts.Reload(ctx); err != nil {
		webLog.Warn().Err(err).Msg("accounts: reload failed")
	}
	s.refreshPrimaryAudible()
}

// resolveAccountID returns the account id a request operates on. An explicit
// account_id form/query value wins; otherwise it falls back to the primary
// account so single-account installs keep working with the legacy forms that
// don't send an id.
func (s *Server) resolveAccountID(c *gin.Context) string {
	if id := strings.TrimSpace(c.PostForm("account_id")); id != "" {
		return id
	}
	if id := strings.TrimSpace(c.Query("account_id")); id != "" {
		return id
	}
	if s.accounts != nil {
		return s.accounts.PrimaryAccountID()
	}
	return ""
}

// ensurePrimaryAccount returns the primary account id, creating a fresh
// (unauthenticated) primary account row if none exists yet. Used by the setup
// wizard so the very first connect has an account to store credentials against.
func (s *Server) ensurePrimaryAccount(ctx context.Context) string {
	if s.accounts != nil {
		if id := s.accounts.PrimaryAccountID(); id != "" {
			return id
		}
	}
	mp, _ := s.db.GetSetting(ctx, "audible_marketplace")
	mp = strings.ToLower(strings.TrimSpace(mp))
	if mp == "" {
		mp = "us"
	}
	account := &database.AudibleAccount{
		ID:          library.NewAccountID(),
		DisplayName: "Audible Account",
		Marketplace: mp,
		Enabled:     true,
	}
	if err := s.db.CreateAudibleAccount(ctx, account); err != nil {
		webLog.Warn().Err(err).Msg("accounts: failed to create primary account")
		return ""
	}
	s.reloadAccounts(ctx)
	return account.ID
}

// handleAccountAdd creates a new, not-yet-authenticated account row with the
// chosen marketplace, then kicks off the Audible OAuth flow scoped to it. Used
// by the "Add account" button on Settings (post-setup multi-account).
func (s *Server) handleAccountAdd(c *gin.Context) {
	ctx := c.Request.Context()
	mp := strings.ToLower(strings.TrimSpace(c.PostForm("marketplace")))
	if mp == "" {
		mp = "us"
	}
	if _, ok := audible.GetMarketplace(mp); !ok {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Unknown Audible region selected."})
		return
	}

	name := strings.TrimSpace(c.PostForm("display_name"))
	if name == "" {
		name = "Audible Account"
	}

	account := &database.AudibleAccount{
		ID:          library.NewAccountID(),
		DisplayName: name,
		Marketplace: mp,
		Enabled:     true,
	}
	if err := s.db.CreateAudibleAccount(ctx, account); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to create account: " + err.Error()})
		return
	}
	s.reloadAccounts(ctx)

	// Kick straight into the OAuth flow for the new account.
	s.startAuthForAccount(c, account.ID, mp)
}

// handleAccountRemove deletes an account and rebuilds the client set. Books that
// were stamped with this account fall back to the primary account for any
// future re-download (best-effort; sync will re-stamp on the next run).
func (s *Server) handleAccountRemove(c *gin.Context) {
	ctx := c.Request.Context()
	id := strings.TrimSpace(c.PostForm("account_id"))
	if id == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Missing account."})
		return
	}
	if err := s.db.DeleteAudibleAccount(ctx, id); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to remove account: " + err.Error()})
		return
	}
	s.reloadAccounts(ctx)
	s.renderAuthPage(c, http.StatusOK, gin.H{"Success": "Account removed."})
}

// handleAccountRegion updates one account's marketplace.
func (s *Server) handleAccountRegion(c *gin.Context) {
	ctx := c.Request.Context()
	id := s.resolveAccountID(c)
	mp := strings.ToLower(strings.TrimSpace(c.PostForm("marketplace")))
	if id == "" || mp == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Select an Audible region."})
		return
	}
	if _, ok := audible.GetMarketplace(mp); !ok {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Unknown Audible region selected."})
		return
	}
	acct, err := s.db.GetAudibleAccount(ctx, id)
	if err != nil || acct == nil {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{"Error": "Account not found."})
		return
	}
	acct.Marketplace = mp
	if err := s.db.UpdateAudibleAccount(ctx, acct); err != nil {
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to save region: " + err.Error()})
		return
	}
	// Keep the legacy single-account setting in sync for the primary account so
	// other call sites that still read "audible_marketplace" stay correct.
	if s.accounts != nil && id == s.accounts.PrimaryAccountID() {
		_ = s.db.SetSetting(ctx, "audible_marketplace", mp)
	}
	s.reloadAccounts(ctx)
	s.renderAuthPage(c, http.StatusOK, gin.H{"Success": "Audible region updated to " + strings.ToUpper(mp) + "."})
}

// rememberPendingAuth stores the PKCE verifier + device serial for an account
// so the callback can complete even via a GET redirect that omits them.
func (s *Server) rememberPendingAuth(accountID, verifier, serial string) {
	if accountID == "" {
		return
	}
	s.pendingAuth.mu.Lock()
	defer s.pendingAuth.mu.Unlock()
	if s.pendingAuth.entries == nil {
		s.pendingAuth.entries = map[string]pendingAuthState{}
	}
	s.pendingAuth.entries[accountID] = pendingAuthState{codeVerifier: verifier, deviceSerial: serial}
}

// takePendingAuth returns and clears the stored OAuth state for an account.
func (s *Server) takePendingAuth(accountID string) (verifier, serial string) {
	s.pendingAuth.mu.Lock()
	defer s.pendingAuth.mu.Unlock()
	st, ok := s.pendingAuth.entries[accountID]
	if !ok {
		return "", ""
	}
	delete(s.pendingAuth.entries, accountID)
	return st.codeVerifier, st.deviceSerial
}

// startAuthForAccount generates the OAuth URL for a specific account and renders
// the auth panel with the verifier/serial embedded. Shared by add + re-auth.
func (s *Server) startAuthForAccount(c *gin.Context, accountID, marketplace string) {
	mp, ok := audible.GetMarketplace(marketplace)
	if !ok {
		mp = audible.MarketplaceUS
	}
	client := audible.NewClient(mp)
	authURL, err := client.GetAuthURL()
	if err != nil {
		webLog.Error().Err(err).Msg("failed to generate auth URL")
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{"Error": "Failed to generate login URL: " + err.Error()})
		return
	}
	s.rememberPendingAuth(accountID, authURL.CodeVerifier, authURL.DeviceSerial)

	// Name the account in the sign-in panel so it's obvious which one this
	// Amazon login is for (matters once there's more than one).
	accountName := ""
	if acct, _ := s.db.GetAudibleAccount(c.Request.Context(), accountID); acct != nil {
		accountName = accountDisplayName(acct)
	}

	s.renderAuthPage(c, http.StatusOK, gin.H{
		"AuthURL":          authURL.URL,
		"CodeVerifier":     authURL.CodeVerifier,
		"DeviceSerial":     authURL.DeviceSerial,
		"AuthAccountID":    accountID,
		"AuthAccountName":  accountName,
		"AuthAccountStart": true,
	})
}

// persistAccountCredentials writes a freshly-authenticated client's credentials
// to its account row, then reloads the client set.
func (s *Server) persistAccountCredentials(ctx context.Context, accountID string, client *audible.Client) error {
	if s.accounts == nil {
		return nil
	}
	if err := s.accounts.PersistClientCredentials(ctx, accountID, client); err != nil {
		return err
	}
	s.reloadAccounts(ctx)
	return nil
}
