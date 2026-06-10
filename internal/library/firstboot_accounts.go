package library

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mstrhakr/audplexus/internal/crypto"
	"github.com/mstrhakr/audplexus/internal/database"

	audible "github.com/mstrhakr/go-audible"
)

// SynthesizeAudibleAccountIfEmpty is the first-boot synthesis pass for the
// multi-account model. If audible_accounts is empty AND a legacy
// credentials.json exists on disk, it creates a single account row from that
// file so existing single-account installs upgrade seamlessly — the live
// client set is then driven by the DB, not the file.
//
// Mirrors SynthesizeLibraryDestinationsIfEmpty. No-op once any account exists.
// The credentials.json file is left in place (not deleted) for back-compat and
// as a recovery breadcrumb.
func SynthesizeAudibleAccountIfEmpty(ctx context.Context, db database.Database, credPath string, box *crypto.Box) error {
	existing, err := db.ListAudibleAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list audible_accounts during first-boot synthesis: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}

	blob, err := os.ReadFile(credPath)
	if err != nil {
		// No legacy credentials file — nothing to synthesize. The user will
		// connect an account via the Settings / setup UI, which creates the
		// first row directly.
		return nil
	}

	// Parse just enough to capture marketplace + customer id for the row.
	tmp := audible.NewClient(audible.MarketplaceUS)
	if err := tmp.UnmarshalCredentials(blob); err != nil {
		return fmt.Errorf("parse legacy credentials.json: %w", err)
	}
	creds := tmp.GetCredentials()
	if creds == nil {
		return nil
	}

	marketplace := strings.TrimSpace(creds.Marketplace)
	if marketplace == "" {
		// Fall back to the stored setting if the blob didn't carry one.
		marketplace, _ = db.GetSetting(ctx, "audible_marketplace")
		marketplace = strings.TrimSpace(marketplace)
	}
	if marketplace == "" {
		marketplace = "us"
	}

	// Seal the blob before it touches the DB. The legacy credentials.json on
	// disk stays as the user's plaintext copy; the DB row is encrypted.
	stored := blob
	if box != nil {
		sealed, err := box.Encrypt(blob)
		if err != nil {
			return fmt.Errorf("encrypt synthesized credentials: %w", err)
		}
		stored = sealed
	}

	account := &database.AudibleAccount{
		ID:          NewAccountID(),
		DisplayName: defaultAccountName(creds),
		Marketplace: strings.ToLower(marketplace),
		CustomerID:  creds.CustomerID,
		Credentials: stored,
		Enabled:     true,
	}
	if err := db.CreateAudibleAccount(ctx, account); err != nil {
		return fmt.Errorf("create synthesized audible account: %w", err)
	}
	return nil
}

// defaultAccountName derives a friendly default label for a synthesized or
// freshly-authed account.
func defaultAccountName(creds *audible.Credentials) string {
	if creds != nil && creds.DeviceInfo.DeviceName != "" {
		return creds.DeviceInfo.DeviceName
	}
	return "Audible Account"
}
