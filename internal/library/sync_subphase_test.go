package library

import (
	"path/filepath"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
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
