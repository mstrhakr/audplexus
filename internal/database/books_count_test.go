package database

import (
	"context"
	"testing"
)

// TestCountBooksByStatus is the integration test for the GROUP BY
// query backing the library page's filter tabs. Seeds the table with
// a known status mix and asserts the returned counts.
func TestCountBooksByStatus(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()

	// Empty table returns an empty map (not nil-vs-empty pedantry: the
	// caller treats both the same, but be explicit).
	got, err := db.CountBooksByStatus(ctx)
	if err != nil {
		t.Fatalf("CountBooksByStatus on empty table: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty table: got %v, want empty map", got)
	}

	// Seed: 3 new, 2 complete, 1 failed, 1 downloading.
	seed := []struct {
		asin   string
		status BookStatus
	}{
		{"B0001", BookStatusNew},
		{"B0002", BookStatusNew},
		{"B0003", BookStatusNew},
		{"B0004", BookStatusComplete},
		{"B0005", BookStatusComplete},
		{"B0006", BookStatusFailed},
		{"B0007", BookStatusDownloading},
	}
	for _, s := range seed {
		if err := db.UpsertBook(ctx, &Book{ASIN: s.asin, Title: s.asin, Author: "x", Status: s.status}); err != nil {
			t.Fatalf("UpsertBook %s: %v", s.asin, err)
		}
	}

	got, err = db.CountBooksByStatus(ctx)
	if err != nil {
		t.Fatalf("CountBooksByStatus after seed: %v", err)
	}
	want := map[BookStatus]int{
		BookStatusNew:         3,
		BookStatusComplete:    2,
		BookStatusFailed:      1,
		BookStatusDownloading: 1,
	}
	if len(got) != len(want) {
		t.Fatalf("status keys: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("status %s: got %d, want %d", k, got[k], v)
		}
	}
}
