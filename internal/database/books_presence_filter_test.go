package database

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestListBooksPresenceFilters covers the presence-filter slices on
// BookFilter (OnDisk + PresentIn/MissingFrom destinations). Verifies
// the WHERE-clause builder constructs subqueries that intersect with
// book_library_destinations correctly.
func TestListBooksPresenceFilters(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	// Two destinations: Plex + ABS.
	plex := &LibraryDestination{
		ID: uuid.NewString(), DisplayName: "Plex",
		Type: LibraryDestinationTypePlex, Enabled: true,
		URL: "http://plex.lan", PlexToken: "tok", PlexSectionID: "1",
		AudiobookPath: "/m",
	}
	abs := &LibraryDestination{
		ID: uuid.NewString(), DisplayName: "ABS",
		Type: LibraryDestinationTypeABS, Enabled: true,
		URL: "http://abs.lan", APIKey: "k", LibraryID: "lib",
		AudiobookPath: "/m",
	}
	if err := db.CreateLibraryDestination(ctx, plex); err != nil {
		t.Fatalf("create plex: %v", err)
	}
	if err := db.CreateLibraryDestination(ctx, abs); err != nil {
		t.Fatalf("create abs: %v", err)
	}

	// Three books:
	//   A: on disk, synced to plex, synced to abs
	//   B: on disk, synced to plex only
	//   C: not on disk, no destination sync rows
	books := []*Book{
		{ASIN: "B0001", Title: "A", Author: "x", Status: BookStatusComplete, FilePath: "/m/a.m4b"},
		{ASIN: "B0002", Title: "B", Author: "x", Status: BookStatusComplete, FilePath: "/m/b.m4b"},
		{ASIN: "B0003", Title: "C", Author: "x", Status: BookStatusNew, FilePath: ""},
		{ASIN: "B0004", Title: "D", Author: "x", Status: BookStatusUnavailable, FilePath: ""},
	}
	for _, b := range books {
		if err := db.UpsertBook(ctx, b); err != nil {
			t.Fatalf("upsert %s: %v", b.ASIN, err)
		}
	}
	bookA, _ := db.GetBookByASIN(ctx, "B0001")
	bookB, _ := db.GetBookByASIN(ctx, "B0002")
	for _, bd := range []*BookDestination{
		{BookID: bookA.ID, DestinationID: plex.ID, SyncState: BookDestSyncSynced},
		{BookID: bookA.ID, DestinationID: abs.ID, SyncState: BookDestSyncSynced},
		{BookID: bookB.ID, DestinationID: plex.ID, SyncState: BookDestSyncSynced},
	} {
		if err := db.UpsertBookDestination(ctx, bd); err != nil {
			t.Fatalf("upsert book-dest: %v", err)
		}
	}

	tests := []struct {
		name     string
		filter   BookFilter
		wantASIN []string
	}{
		{"on disk only", BookFilter{OnDisk: ptrBool(true)}, []string{"B0001", "B0002"}},
		{"not on disk", BookFilter{OnDisk: ptrBool(false)}, []string{"B0003", "B0004"}},
		{"present in plex", BookFilter{PresentInDestinations: []string{plex.ID}}, []string{"B0001", "B0002"}},
		{"present in abs", BookFilter{PresentInDestinations: []string{abs.ID}}, []string{"B0001"}},
		{"missing from abs", BookFilter{MissingFromDestinations: []string{abs.ID}}, []string{"B0002", "B0003", "B0004"}},
		{"in plex AND missing from abs", BookFilter{PresentInDestinations: []string{plex.ID}, MissingFromDestinations: []string{abs.ID}}, []string{"B0002"}},
		{"available only", BookFilter{ExcludeStatuses: []BookStatus{BookStatusUnavailable}}, []string{"B0001", "B0002", "B0003"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := db.ListBooks(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListBooks: %v", err)
			}
			gotASIN := make([]string, 0, len(got))
			for _, b := range got {
				gotASIN = append(gotASIN, b.ASIN)
			}
			if !sameStringSet(gotASIN, tc.wantASIN) {
				t.Fatalf("got %v, want %v", gotASIN, tc.wantASIN)
			}
		})
	}
}

func ptrBool(b bool) *bool { return &b }

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}
