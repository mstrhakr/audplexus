package scheduler

import (
	"testing"

	"github.com/mstrhakr/audplexus/internal/library"
)

func TestNewDefaults(t *testing.T) {
	s := New(nil, nil)
	if s == nil {
		t.Fatalf("New() returned nil")
	}
	if s.cron == nil {
		t.Fatalf("cron should be initialized")
	}
	if s.syncMode != library.SyncModeFull {
		t.Fatalf("default syncMode = %q, want %q", s.syncMode, library.SyncModeFull)
	}
	if s.autoQueue {
		t.Fatalf("default autoQueue = true, want false")
	}
}

func TestSetAutoQueueNew(t *testing.T) {
	s := New(nil, nil)
	s.SetAutoQueueNew(true)
	if !s.autoQueue {
		t.Fatalf("autoQueue should be true")
	}
	s.SetAutoQueueNew(false)
	if s.autoQueue {
		t.Fatalf("autoQueue should be false")
	}
}

func TestSetSyncMode(t *testing.T) {
	s := New(nil, nil)

	s.SetSyncMode("quick")
	if s.syncMode != library.SyncModeQuick {
		t.Fatalf("syncMode = %q, want quick", s.syncMode)
	}

	s.SetSyncMode("invalid")
	if s.syncMode != library.SyncModeFull {
		t.Fatalf("syncMode = %q, want full fallback", s.syncMode)
	}
}

func TestSetSyncScheduleValidationAndReplacement(t *testing.T) {
	s := New(nil, nil)

	if err := s.SetSyncSchedule("not-a-cron"); err == nil {
		t.Fatalf("SetSyncSchedule(invalid) should error")
	}
	if s.syncEntry != 0 {
		t.Fatalf("syncEntry should remain 0 after invalid schedule")
	}

	if err := s.SetSyncSchedule("@every 24h"); err != nil {
		t.Fatalf("SetSyncSchedule(valid) error = %v", err)
	}
	first := s.syncEntry
	if first == 0 {
		t.Fatalf("syncEntry should be non-zero for valid schedule")
	}

	if err := s.SetSyncSchedule("@every 12h"); err != nil {
		t.Fatalf("SetSyncSchedule(replace) error = %v", err)
	}
	if s.syncEntry == 0 {
		t.Fatalf("syncEntry should remain non-zero after replacement")
	}
	if s.syncEntry == first {
		t.Fatalf("syncEntry should change after replacement")
	}

	if err := s.SetSyncSchedule(""); err != nil {
		t.Fatalf("SetSyncSchedule(disable) error = %v", err)
	}
	if s.syncEntry != 0 {
		t.Fatalf("syncEntry should be 0 after disable")
	}
}

func TestStartStopWithoutJobs(t *testing.T) {
	s := New(nil, nil)
	s.Start()
	s.Stop()
}
