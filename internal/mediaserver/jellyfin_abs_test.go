package mediaserver

import (
	"context"
	"testing"
)

func TestJellyfinAndABSImplementInterface(t *testing.T) {
	var _ Backend = (*JellyfinBackend)(nil)
	var _ Backend = (*ABSBackend)(nil)
}

func TestJellyfinNotConfiguredReturnsTypedSkip(t *testing.T) {
	t.Setenv("JELLYFIN_URL", "")
	t.Setenv("JELLYFIN_API_KEY", "")
	t.Setenv("JELLYFIN_LIBRARY_ID", "")
	db := newSettingsOnlyStubDB()
	jf := NewJellyfin(db, nil, "/audiobooks")

	outcomes := jf.OnBookOrganized(context.Background(), OrganizedBook{BookID: 1, Title: "T", LocalPath: "/x"})
	if len(outcomes) == 0 {
		t.Fatal("not-configured jellyfin returned no outcomes")
	}
	for _, o := range outcomes {
		if o.Status != OutcomeSkippedNotConfigured {
			t.Errorf("op=%s status=%q, want SkippedNotConfigured", o.Operation, o.Status)
		}
	}
}

func TestABSNotConfiguredReturnsTypedSkip(t *testing.T) {
	t.Setenv("ABS_URL", "")
	t.Setenv("ABS_API_KEY", "")
	t.Setenv("ABS_LIBRARY_ID", "")
	db := newSettingsOnlyStubDB()
	abs := NewABS(db, "/audiobooks")

	outcomes := abs.OnBookOrganized(context.Background(), OrganizedBook{BookID: 1, ASIN: "B0", Title: "T", LocalPath: "/x"})
	if len(outcomes) == 0 {
		t.Fatal("not-configured abs returned no outcomes")
	}
	for _, o := range outcomes {
		if o.Status != OutcomeSkippedNotConfigured {
			t.Errorf("op=%s status=%q, want SkippedNotConfigured", o.Operation, o.Status)
		}
	}
}

func TestJellyfinCapabilitiesIncludesAudioBookFeatureSet(t *testing.T) {
	jf := NewJellyfin(nil, nil, "/audiobooks")
	caps := jf.Capabilities()
	for _, want := range []Capability{CapTriggerScan, CapPerItemRefresh, CapSeriesGrouping, CapFranchiseTag, CapImageUpload, CapItemCount, CapAuthorImages, CapBoxSetCovers} {
		if !caps.Has(want) {
			t.Errorf("Jellyfin capability %q missing", want)
		}
	}
}

func TestABSCapabilitiesAreSmallButCompleteForAudiobooks(t *testing.T) {
	abs := NewABS(nil, "/audiobooks")
	caps := abs.Capabilities()
	for _, want := range []Capability{CapTriggerScan, CapPerItemRefresh, CapSeriesGrouping, CapItemCount} {
		if !caps.Has(want) {
			t.Errorf("ABS capability %q missing", want)
		}
	}
	// ABS does NOT have these.
	for _, banned := range []Capability{CapFranchiseTag, CapBoxSetCovers, CapAuthorImages, CapImageUpload} {
		if caps.Has(banned) {
			t.Errorf("ABS should not advertise %q", banned)
		}
	}
}

func TestEnsureLockedFieldTagsAddsTagsAndPreservesExistingNonTagsEntries(t *testing.T) {
	got := ensureLockedFieldTags([]any{"Cast", "Studios"})
	wantSet := map[string]bool{"Tags": true, "Cast": true, "Studios": true}
	if len(got) != len(wantSet) {
		t.Fatalf("ensureLockedFieldTags = %v (len %d), want %v", got, len(got), wantSet)
	}
	for _, v := range got {
		if !wantSet[v] {
			t.Errorf("unexpected entry: %q", v)
		}
	}
}

func TestEnsureLockedFieldTagsHandlesNilExisting(t *testing.T) {
	got := ensureLockedFieldTags(nil)
	if len(got) != 1 || got[0] != "Tags" {
		t.Errorf("ensureLockedFieldTags(nil) = %v, want [Tags]", got)
	}
}
