package library

import (
	"context"
	"strings"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestConvertBookValidation(t *testing.T) {
	db := database.NewStubDB()
	dm := &DownloadManager{
		db:             db,
		convertingASINs: make(map[string]struct{}),
	}

	ctx := context.Background()

	// Test invalid format
	err := dm.ConvertBook(ctx, 1, "invalid")
	if err == nil || !strings.Contains(err.Error(), "invalid target format") {
		t.Fatalf("expected invalid target format error, got %v", err)
	}

	// Test case normalization (should accept lowercase and mixed case)
	// Note: Will fail on actual convert since book not in DB, but validates format acceptance
	dm.db = &database.StubDB{}
	err = dm.ConvertBook(ctx, 1, "M4B") // uppercase
	if err == nil || !strings.Contains(err.Error(), "book") {
		t.Fatalf("format uppercase should be accepted or fail on DB lookup, got %v", err)
	}

	err = dm.ConvertBook(ctx, 1, "MP3") // uppercase
	if err == nil || !strings.Contains(err.Error(), "book") {
		t.Fatalf("format uppercase should be accepted or fail on DB lookup, got %v", err)
	}
}
