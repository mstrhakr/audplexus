package mediaserver

import (
	"testing"
)

func TestCapabilitySetOperations(t *testing.T) {
	// Test NewCapabilitySet
	set := NewCapabilitySet(CapTriggerScan, CapSeriesGrouping)

	// Test Has
	if !set.Has(CapTriggerScan) {
		t.Fatalf("expected Has(CapTriggerScan) = true")
	}
	if !set.Has(CapSeriesGrouping) {
		t.Fatalf("expected Has(CapSeriesGrouping) = true")
	}
	if set.Has(CapFranchiseTag) {
		t.Fatalf("expected Has(CapFranchiseTag) = false")
	}
}

func TestOutcomeIsTerminal(t *testing.T) {
	tests := []struct {
		name       string
		status     OutcomeStatus
		wantTerminal bool
	}{
		{"succeeded", OutcomeSucceeded, true},
		{"failed", OutcomeFailed, false}, // non-terminal, may retry
		{"unsupported", OutcomeUnsupported, true},
		{"skipped_existing", OutcomeSkippedExisting, true},
		{"skipped_not_configured", OutcomeSkippedNotConfigured, true},
	}

	for _, tt := range tests {
		o := Outcome{Status: tt.status}
		if o.IsTerminal() != tt.wantTerminal {
			t.Fatalf("%s: IsTerminal() = %v, want %v", tt.name, o.IsTerminal(), tt.wantTerminal)
		}
	}
}
