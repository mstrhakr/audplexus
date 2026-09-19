package library

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/go-audible"
)

// newTestSyncService creates a minimal SyncService with an in-memory SQLite DB
// and the default full phase set pre-loaded, suitable for unit tests.
func newTestSyncService(t *testing.T) *SyncService {
	t.Helper()
	db, err := database.NewSQLite(filepath.Join(t.TempDir(), "sync_test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	svc := NewSyncService(db, nil, t.TempDir())
	svc.mu.Lock()
	svc.progress.Phases = DefaultFullPhases()
	svc.mu.Unlock()
	return svc
}

func TestConvertBookFiltersTranslatorCreditsAndKeepsAuthorASIN(t *testing.T) {
	book := audible.Book{
		Authors: []audible.Contributor{
			{ASIN: "translator-id", Name: "Karl A. Klewer - translator"},
			{ASIN: "tolkien-id", Name: "J.R.R. Tolkien"},
			{ASIN: "coauthor-id", Name: "Christopher Tolkien"},
		},
	}

	got := convertBook(book)
	if got.Author != "J.R.R. Tolkien, Christopher Tolkien" {
		t.Errorf("Author = %q", got.Author)
	}
	if got.AuthorASIN != "tolkien-id" {
		t.Errorf("AuthorASIN = %q, want the first non-translator author", got.AuthorASIN)
	}
}

func TestSubPhaseFnFor_InsertsNewEntry(t *testing.T) {
	svc := newTestSyncService(t)
	fn := svc.subPhaseFnFor(PhasePlexSync)
	fn("dest-1", "Plex", "running", "", 0, 0)

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	for _, p := range svc.progress.Phases {
		if p.Name == PhasePlexSync {
			if len(p.SubPhases) != 1 {
				t.Fatalf("expected 1 sub-phase, got %d", len(p.SubPhases))
			}
			sp := p.SubPhases[0]
			if sp.ID != "dest-1" {
				t.Errorf("ID = %q, want dest-1", sp.ID)
			}
			if sp.Status != "running" {
				t.Errorf("Status = %q, want running", sp.Status)
			}
			if !sp.Indeterminate {
				t.Error("Indeterminate should be true for running with total=0")
			}
			return
		}
	}
	t.Fatal("PhasePlexSync not found in progress.Phases")
}

func TestSubPhaseFnFor_UpdatesExistingEntry(t *testing.T) {
	svc := newTestSyncService(t)
	fn := svc.subPhaseFnFor(PhasePlexSync)

	fn("dest-1", "Plex", "running", "", 0, 0)
	fn("dest-1", "Plex", "complete", "42 items", 42, 42)

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	for _, p := range svc.progress.Phases {
		if p.Name == PhasePlexSync {
			if len(p.SubPhases) != 1 {
				t.Fatalf("expected 1 sub-phase after update, got %d", len(p.SubPhases))
			}
			sp := p.SubPhases[0]
			if sp.Status != "complete" {
				t.Errorf("Status = %q, want complete", sp.Status)
			}
			if sp.Current != 42 || sp.Total != 42 {
				t.Errorf("Current/Total = %d/%d, want 42/42", sp.Current, sp.Total)
			}
			if sp.Percent != 1.0 {
				t.Errorf("Percent = %f, want 1.0", sp.Percent)
			}
			if sp.Indeterminate {
				t.Error("Indeterminate should be false for complete")
			}
			return
		}
	}
	t.Fatal("PhasePlexSync not found")
}

func TestSubPhaseFnFor_MultipleDestinations(t *testing.T) {
	svc := newTestSyncService(t)
	fn := svc.subPhaseFnFor(PhaseCollectionSync)

	fn("dest-a", "Alpha", "running", "", 0, 0)
	fn("dest-b", "Beta", "running", "", 0, 0)
	fn("dest-a", "Alpha", "complete", "", 0, 0)

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	for _, p := range svc.progress.Phases {
		if p.Name == PhaseCollectionSync {
			if len(p.SubPhases) != 2 {
				t.Fatalf("expected 2 sub-phases, got %d", len(p.SubPhases))
			}
			byID := make(map[string]SubPhaseStatus)
			for _, sp := range p.SubPhases {
				byID[sp.ID] = sp
			}
			if byID["dest-a"].Status != "complete" {
				t.Errorf("dest-a status = %q, want complete", byID["dest-a"].Status)
			}
			if byID["dest-b"].Status != "running" {
				t.Errorf("dest-b status = %q, want running", byID["dest-b"].Status)
			}
			return
		}
	}
	t.Fatal("PhaseCollectionSync not found")
}

func TestSetPhase_RunningClearsSubPhases(t *testing.T) {
	svc := newTestSyncService(t)

	// Populate sub-phases for PhasePlexSync.
	fn := svc.subPhaseFnFor(PhasePlexSync)
	fn("dest-1", "Plex", "complete", "ok", 5, 5)

	// Confirm sub-phase was added.
	svc.mu.RLock()
	var had int
	for _, p := range svc.progress.Phases {
		if p.Name == PhasePlexSync {
			had = len(p.SubPhases)
		}
	}
	svc.mu.RUnlock()
	if had != 1 {
		t.Fatalf("precondition: expected 1 sub-phase before setPhase, got %d", had)
	}

	// Transition to running — should wipe sub-phases.
	svc.setPhase(PhasePlexSync, "running", "scanning…")

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	for _, p := range svc.progress.Phases {
		if p.Name == PhasePlexSync {
			if len(p.SubPhases) != 0 {
				t.Errorf("expected SubPhases cleared on running, got %d entries", len(p.SubPhases))
			}
			return
		}
	}
	t.Fatal("PhasePlexSync not found")
}

func TestSubPhaseFnFor_NoopForUnknownPhase(t *testing.T) {
	svc := newTestSyncService(t)
	fn := svc.subPhaseFnFor(SyncPhase("nonexistent"))
	// Should not panic and should not modify any phases.
	fn("x", "X", "running", "", 0, 0)

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	for _, p := range svc.progress.Phases {
		if len(p.SubPhases) != 0 {
			t.Errorf("phase %s unexpectedly got a sub-phase", p.Name)
		}
	}
}

func TestSetPhaseProgress_ZeroTotalDoesNotPanic(t *testing.T) {
	phase := PhaseStatus{Name: PhaseAudibleSync}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("setPhaseProgress panicked for zero total: %v", r)
		}
	}()

	setPhaseProgress(&phase, 0, 0, false, "running")

	if phase.Percent != 0 {
		t.Fatalf("percent = %f, want 0", phase.Percent)
	}
	if phase.Total != 0 {
		t.Fatalf("total = %d, want 0", phase.Total)
	}
	if phase.Current != 0 {
		t.Fatalf("current = %d, want 0", phase.Current)
	}
}

func TestSuspiciousZeroLibrary_ReturnsErrorWhenPriorBooksExist(t *testing.T) {
	prev := &database.SyncHistory{BooksFound: 5, Status: "complete"}

	err := suspiciousZeroLibrary(0, prev)
	if err == nil {
		t.Fatal("expected suspicious-zero error, got nil")
	}
	if got, want := err.Error(), "0 books"; got == "" || !contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
}

func TestSuspiciousZeroLibrary_AllowsFreshEmptyLibrary(t *testing.T) {
	if err := suspiciousZeroLibrary(0, nil); err != nil {
		t.Fatalf("expected no error for a truly empty first sync, got %v", err)
	}
	if err := suspiciousZeroLibrary(0, &database.SyncHistory{BooksFound: 0, Status: "complete"}); err != nil {
		t.Fatalf("expected no error for previously empty library, got %v", err)
	}
}

func TestLibraryIdentifier_UsesBestIDForLegacyISBN10(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/1.0/library" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"asin":"3838795407","title":"Legacy Title"}],"total_results":1}`))
	}))
	defer server.Close()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	client := audible.NewClient(audible.MarketplaceUS)
	client.SetAPIEndpoint(server.URL)
	client.SetCredentials(&audible.Credentials{
		ADPToken:         "token",
		AccessToken:      "access",
		RefreshToken:     "refresh",
		ExpiresAt:        time.Now().Add(time.Hour),
		DevicePrivateKey: string(pemBytes),
		DeviceInfo: audible.DeviceInfo{
			DeviceSerialNumber: "serial",
			DeviceType:         "type",
		},
	})

	books, _, err := fetchEntireLibraryWithTotal(context.Background(), client, []string{"product_desc"})
	if err != nil {
		t.Fatalf("fetchEntireLibraryWithTotal: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("len(books) = %d, want 1", len(books))
	}
	if got := books[0].BestID(); got != "3838795407" {
		t.Fatalf("books[0].BestID() = %q, want %q", got, "3838795407")
	}

	owners := make(map[string][]string)
	seen := make(map[string]struct{})
	for _, item := range books {
		id := libraryBookID(item)
		owners[id] = append(owners[id], "acct-1")
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate library ID %q retained in canonical owner map", id)
		}
		seen[id] = struct{}{}
	}
	if _, ok := owners["3838795407"]; !ok {
		t.Fatalf("missing canonical owner stamp for legacy ISBN-10 key")
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})())
}
