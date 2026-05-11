package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestFileValidator_DetectSuspicious(t *testing.T) {
	fv := NewFileValidator(nil, nil)

	if bad, reason := fv.detectSuspicious(3600, 128, 1000); !bad || reason == "" {
		t.Fatalf("small file should be suspicious")
	}
	if bad, reason := fv.detectSuspicious(120, 128, 10*1024*1024); !bad || reason == "" {
		t.Fatalf("short duration should be suspicious")
	}
	if bad, reason := fv.detectSuspicious(3600, 32, 50*1024*1024); !bad || reason == "" {
		t.Fatalf("low bitrate should be suspicious")
	}
	if bad, reason := fv.detectSuspicious(3600, 128, 20_000_000_000); !bad || reason == "" {
		t.Fatalf("oversized file should be suspicious")
	}

	if bad, reason := fv.detectSuspicious(3600, 128, 40*1024*1024); bad || reason != "" {
		t.Fatalf("normal characteristics should be non-suspicious, got bad=%v reason=%q", bad, reason)
	}
}

func TestFileValidator_ExtractASIN(t *testing.T) {
	fv := NewFileValidator(nil, nil)

	if got := fv.extractASIN("/library/B012345678.m4b"); got != "B012345678" {
		t.Fatalf("extractASIN filename = %q, want B012345678", got)
	}
	if got := fv.extractASIN("/library/Author/B012345678 Title/book.m4b"); got != "B012345678" {
		t.Fatalf("extractASIN parent dir = %q, want B012345678", got)
	}
	if got := fv.extractASIN("/library/not-an-asin-file.m4b"); got != "" {
		t.Fatalf("extractASIN invalid should be empty, got %q", got)
	}
}

func TestFileValidator_ProbeNoFFmpeg(t *testing.T) {
	fv := NewFileValidator(nil, nil)
	if _, _, _, err := fv.probeFile("/tmp/a.m4b"); err == nil {
		t.Fatalf("probeFile with nil ffmpeg should error")
	}
}

func TestIsAlphanumeric(t *testing.T) {
	if !isAlphanumeric("B012345678") {
		t.Fatalf("expected true for alphanumeric")
	}
	if isAlphanumeric("B01234-678") {
		t.Fatalf("expected false for non-alphanumeric")
	}
}

func TestFileValidator_ScanAndMarkForRedownload(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	book := &database.Book{ASIN: "B012345678", Title: "Title", Status: database.BookStatusComplete, FilePath: "/x", FileSize: 123}
	if err := db.UpsertBook(ctx, book); err != nil {
		t.Fatalf("UpsertBook: %v", err)
	}
	stored, err := db.GetBookByASIN(ctx, "B012345678")
	if err != nil || stored == nil {
		t.Fatalf("GetBookByASIN: (%v,%v)", stored, err)
	}

	root := t.TempDir()
	file := filepath.Join(root, "B012345678.m4b")
	if err := os.WriteFile(file, []byte("broken"), 0o600); err != nil {
		t.Fatalf("WriteFile m4b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignore.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile txt: %v", err)
	}

	fv := NewFileValidator(nil, db)
	reports, ids, err := fv.ScanLibraryFiles(ctx, root)
	if err != nil {
		t.Fatalf("ScanLibraryFiles: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports len = %d, want 1", len(reports))
	}
	if reports[0].ASIN != "B012345678" || reports[0].IsValid {
		t.Fatalf("expected invalid report with asin, got %+v", reports[0])
	}
	if len(ids) != 1 || ids[0] != stored.ID {
		t.Fatalf("booksToRedownload = %v, want [%d]", ids, stored.ID)
	}

	if err := fv.MarkBooksForRedownload(ctx, []int64{stored.ID, 999999}, "test"); err != nil {
		t.Fatalf("MarkBooksForRedownload: %v", err)
	}
	updated, err := db.GetBook(ctx, stored.ID)
	if err != nil || updated == nil {
		t.Fatalf("GetBook after mark: (%v,%v)", updated, err)
	}
	if updated.Status != database.BookStatusNew || updated.FilePath != "" || updated.FileSize != 0 {
		t.Fatalf("book not reset for redownload: %+v", updated)
	}
}
