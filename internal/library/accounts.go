package library

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/mstrhakr/audplexus/internal/crypto"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/rs/zerolog/log"

	audible "github.com/mstrhakr/go-audible"
)

// AccountManager owns one *audible.Client per connected Audible account and is
// the single source of truth for "which client downloads/syncs this book".
//
// It mirrors DestinationManager: most installs have exactly one account, but
// the manager treats one and many uniformly. Clients are built from the
// credentials stored on each audible_accounts row at boot (and rebuilt on
// Reload after the Settings UI adds / re-auths / removes an account).
type AccountManager struct {
	db database.Database

	// box encrypts/decrypts the credentials blobs at rest. May be nil (tests),
	// in which case blobs are stored plaintext — production wiring in main.go
	// always provides one.
	box *crypto.Box

	mu      sync.RWMutex
	clients map[string]*audible.Client // keyed by account id
	order   []string                   // account ids in stable creation order
}

// NewAccountManager builds a manager and loads all enabled accounts from the
// DB. A load error for an individual account is logged and skipped so one bad
// row never blocks the others.
func NewAccountManager(ctx context.Context, db database.Database, box *crypto.Box) *AccountManager {
	m := &AccountManager{db: db, box: box, clients: map[string]*audible.Client{}}
	if err := m.Reload(ctx); err != nil {
		log.Warn().Err(err).Msg("account manager: initial load failed")
	}
	return m
}

// Reload rebuilds the in-memory client set from the DB. Safe to call after any
// mutation to the audible_accounts table.
func (m *AccountManager) Reload(ctx context.Context) error {
	accounts, err := m.db.ListEnabledAudibleAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list enabled audible accounts: %w", err)
	}

	clients := make(map[string]*audible.Client, len(accounts))
	order := make([]string, 0, len(accounts))
	for i := range accounts {
		a := accounts[i]
		client, err := m.buildAccountClient(&a)
		if err != nil {
			log.Warn().Err(err).Str("account_id", a.ID).Str("name", a.DisplayName).
				Msg("account manager: skipping account that failed to build")
			continue
		}
		// Lazy migration: rows written before encryption landed carry
		// plaintext blobs. Re-seal them now so a DB dump taken after this
		// boot no longer contains raw tokens.
		if m.box != nil && len(a.Credentials) > 0 && !crypto.IsEncrypted(a.Credentials) {
			if sealed, encErr := m.box.Encrypt(a.Credentials); encErr == nil {
				a.Credentials = sealed
				if upErr := m.db.UpdateAudibleAccount(ctx, &a); upErr != nil {
					log.Warn().Err(upErr).Str("account_id", a.ID).Msg("account manager: failed to re-encrypt legacy credentials")
				} else {
					log.Info().Str("account_id", a.ID).Msg("account manager: re-encrypted legacy plaintext credentials")
				}
			}
		}
		clients[a.ID] = client
		order = append(order, a.ID)
	}

	m.mu.Lock()
	m.clients = clients
	m.order = order
	m.mu.Unlock()

	log.Info().Int("accounts", len(order)).Msg("account manager: loaded audible accounts")
	return nil
}

// buildAccountClient constructs a client for one account, decrypting and
// setting its credentials (if present) and marketplace.
func (m *AccountManager) buildAccountClient(a *database.AudibleAccount) (*audible.Client, error) {
	mp := audible.MarketplaceUS
	if a.Marketplace != "" {
		if found, ok := audible.GetMarketplace(a.Marketplace); ok {
			mp = found
		}
	}
	client := audible.NewClient(mp)
	if len(a.Credentials) > 0 {
		blob := a.Credentials
		if m.box != nil {
			plain, err := m.box.Decrypt(blob)
			if err != nil {
				return nil, fmt.Errorf("decrypt credentials: %w", err)
			}
			blob = plain
		}
		if err := client.UnmarshalCredentials(blob); err != nil {
			return nil, fmt.Errorf("unmarshal credentials: %w", err)
		}
	}
	return client, nil
}

// ClientForAccount returns the client for a specific account id, or nil if the
// account isn't loaded (disabled, deleted, or failed to build).
func (m *AccountManager) ClientForAccount(accountID string) *audible.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[accountID]
}

// ClientForBook resolves the client that owns a book. It looks up the book's
// stored account_id; if empty (legacy / single-account) or no longer present,
// it falls back to the primary account. Returns nil only when no usable
// account exists at all.
func (m *AccountManager) ClientForBook(ctx context.Context, asin string) *audible.Client {
	accountID, _ := m.db.GetBookAccount(ctx, asin)
	if accountID != "" {
		if c := m.ClientForAccount(accountID); c != nil {
			return c
		}
	}
	return m.Primary()
}

// Primary returns the first loaded account's client (stable creation order), or
// nil when no accounts are configured. Used for legacy single-client call sites
// (health badges, activation bytes, marketplace display) that predate
// multi-account.
func (m *AccountManager) Primary() *audible.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.order) == 0 {
		return nil
	}
	return m.clients[m.order[0]]
}

// PrimaryAccountID returns the id of the primary account, or "".
func (m *AccountManager) PrimaryAccountID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.order) == 0 {
		return ""
	}
	return m.order[0]
}

// EnabledAccounts is a (id, client) pair used when iterating all accounts for a
// merge-all sync.
type EnabledAccount struct {
	ID     string
	Client *audible.Client
}

// EnabledAccounts returns all loaded accounts in stable order. Sync iterates
// these to merge every account's library.
func (m *AccountManager) EnabledAccounts() []EnabledAccount {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]EnabledAccount, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, EnabledAccount{ID: id, Client: m.clients[id]})
	}
	return out
}

// Count returns the number of loaded (enabled) accounts.
func (m *AccountManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.order)
}

// IsAuthenticated reports whether ANY loaded account is authenticated. Used by
// the legacy health checks that asked the single client the same question.
func (m *AccountManager) IsAuthenticated() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, id := range m.order {
		if c := m.clients[id]; c != nil && c.IsAuthenticated() {
			return true
		}
	}
	return false
}

// PersistClientCredentials writes a client's current credentials back to its
// account row. Call after Authenticate or a token refresh so the DB stays the
// source of truth across restarts.
func (m *AccountManager) PersistClientCredentials(ctx context.Context, accountID string, client *audible.Client) error {
	if client == nil {
		return nil
	}
	creds := client.GetCredentials()
	if creds == nil {
		return nil
	}
	blob, err := client.MarshalCredentials()
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if m.box != nil {
		if blob, err = m.box.Encrypt(blob); err != nil {
			return fmt.Errorf("encrypt credentials: %w", err)
		}
	}
	acct, err := m.db.GetAudibleAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("get account: %w", err)
	}
	if acct == nil {
		return fmt.Errorf("account %s not found", accountID)
	}
	acct.Credentials = blob
	if creds.CustomerID != "" {
		acct.CustomerID = creds.CustomerID
	}
	if creds.Marketplace != "" {
		acct.Marketplace = creds.Marketplace
	}
	return m.db.UpdateAudibleAccount(ctx, acct)
}

// NewAccountID returns a fresh account id. Exposed so the web layer creating a
// pending account and the manager agree on id shape.
func NewAccountID() string { return uuid.NewString() }
