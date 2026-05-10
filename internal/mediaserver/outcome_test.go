package mediaserver

import (
	"errors"
	"testing"
	"time"
)

func TestSucceeded(t *testing.T) {
	o := Succeeded(OpScanTrigger, "scanned", "item-42", 25*time.Millisecond)
	if o.Status != OutcomeSucceeded {
		t.Fatalf("status = %q, want %q", o.Status, OutcomeSucceeded)
	}
	if o.Operation != OpScanTrigger {
		t.Fatalf("op = %q, want %q", o.Operation, OpScanTrigger)
	}
	if o.ServerItemID != "item-42" {
		t.Fatalf("serverItemID = %q, want %q", o.ServerItemID, "item-42")
	}
	if o.DurationMs != 25 {
		t.Fatalf("duration = %d ms, want 25", o.DurationMs)
	}
	if o.Err != nil {
		t.Fatalf("err = %v, want nil for succeeded outcome", o.Err)
	}
	if !o.IsTerminal() {
		t.Fatal("succeeded should be terminal")
	}
}

func TestFailed(t *testing.T) {
	srcErr := errors.New("network unreachable")
	o := Failed(OpItemMatch, srcErr, "match retry exhausted")
	if o.Status != OutcomeFailed {
		t.Fatalf("status = %q, want %q", o.Status, OutcomeFailed)
	}
	if !errors.Is(o.Err, srcErr) {
		t.Fatalf("err = %v, want wrap of %v", o.Err, srcErr)
	}
	if o.IsTerminal() {
		t.Fatal("failed should NOT be terminal — caller may retry")
	}
}

func TestSkippedConfigured(t *testing.T) {
	o := SkippedConfigured(OpScanTrigger)
	if o.Status != OutcomeSkippedNotConfigured {
		t.Fatalf("status = %q, want %q", o.Status, OutcomeSkippedNotConfigured)
	}
	if !errors.Is(o.Err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", o.Err)
	}
	if !o.IsTerminal() {
		t.Fatal("skipped_not_configured should be terminal — no retry without config")
	}
}

func TestUnsupported(t *testing.T) {
	o := Unsupported(OpFranchiseTag, "plex has no per-item tag concept")
	if o.Status != OutcomeUnsupported {
		t.Fatalf("status = %q, want %q", o.Status, OutcomeUnsupported)
	}
	if !errors.Is(o.Err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", o.Err)
	}
	if !o.IsTerminal() {
		t.Fatal("unsupported should be terminal — never silently retry an unsupported op")
	}
	if o.Detail == "" {
		t.Fatal("unsupported outcomes must explain why")
	}
}

func TestSkippedExisting(t *testing.T) {
	o := SkippedExisting(OpSeriesGrouping, "already in collection")
	if o.Status != OutcomeSkippedExisting {
		t.Fatalf("status = %q, want %q", o.Status, OutcomeSkippedExisting)
	}
	if o.Err != nil {
		t.Fatalf("err = %v, want nil for skipped_existing", o.Err)
	}
	if !o.IsTerminal() {
		t.Fatal("skipped_existing should be terminal")
	}
}

func TestDeferredIsTerminalForRetry(t *testing.T) {
	// Deferred means async ingest will pick it up — caller should NOT
	// auto-retry, but Reconcile may observe the eventual end-state.
	o := Outcome{Operation: OpScanTrigger, Status: OutcomeDeferred, Detail: "abs watcher will pick up"}
	if !o.IsTerminal() {
		t.Fatal("deferred should be terminal — caller does not retry")
	}
}

func TestOrganizedBookCarriesEverythingNeeded(t *testing.T) {
	// Compile-test that the OrganizedBook contract carries the data each
	// backend needs without forcing the caller to thread through DB rereads.
	// If a field is removed without adding a replacement, this test fails.
	b := OrganizedBook{
		BookID:         42,
		ASIN:           "B0XYZ",
		Title:          "Foundation",
		Author:         "Isaac Asimov",
		Series:         "Foundation",
		SeriesPosition: "1",
		LocalPath:      "/audiobooks/Asimov/Foundation.m4b",
		CoverPath:      "/audiobooks/Asimov/folder.jpg",
		OrganizedAt:    time.Now(),
	}
	if b.BookID == 0 || b.ASIN == "" || b.Title == "" || b.LocalPath == "" {
		t.Fatal("OrganizedBook missing required fields")
	}
}
