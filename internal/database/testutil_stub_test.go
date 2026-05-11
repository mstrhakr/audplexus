package database

import (
	"context"
	"testing"
)

func TestStubDB_SettingsAndLifecycle(t *testing.T) {
	ctx := context.Background()
	s := NewStubDB()

	s.SeedSettings(map[string]string{"a": "1", "b": "2"})
	v, err := s.GetSetting(ctx, "a")
	if err != nil || v != "1" {
		t.Fatalf("GetSetting(a) = (%q, %v), want (1, nil)", v, err)
	}

	if err := s.SetSetting(ctx, "c", "3"); err != nil {
		t.Fatalf("SetSetting(c) error: %v", err)
	}
	v, err = s.GetSetting(ctx, "c")
	if err != nil || v != "3" {
		t.Fatalf("GetSetting(c) = (%q, %v), want (3, nil)", v, err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}
	if err := s.Reset(ctx); err != nil {
		t.Fatalf("Reset error: %v", err)
	}
}

func TestStubDB_AllNoopMethods(t *testing.T) {
	ctx := context.Background()
	s := NewStubDB()

	if b, err := s.GetBook(ctx, 1); err != nil || b != nil {
		t.Fatalf("GetBook expected (nil,nil), got (%v,%v)", b, err)
	}
	if b, err := s.GetBookByASIN(ctx, "B000000001"); err != nil || b != nil {
		t.Fatalf("GetBookByASIN expected (nil,nil), got (%v,%v)", b, err)
	}
	if rows, total, err := s.ListBooks(ctx, BookFilter{}); err != nil || rows != nil || total != 0 {
		t.Fatalf("ListBooks expected (nil,0,nil), got (%v,%d,%v)", rows, total, err)
	}
	if err := s.UpsertBook(ctx, &Book{ASIN: "B000000001"}); err != nil {
		t.Fatalf("UpsertBook error: %v", err)
	}
	if err := s.UpdateBookStatus(ctx, 1, BookStatusNew); err != nil {
		t.Fatalf("UpdateBookStatus error: %v", err)
	}
	if err := s.UpdateBookPlexInfo(ctx, 1, "k", "t"); err != nil {
		t.Fatalf("UpdateBookPlexInfo error: %v", err)
	}
	if err := s.UpdateBookMediaServerInfo(ctx, 1, "k", "t"); err != nil {
		t.Fatalf("UpdateBookMediaServerInfo error: %v", err)
	}
	if err := s.DeleteBook(ctx, 1); err != nil {
		t.Fatalf("DeleteBook error: %v", err)
	}

	if err := s.EnqueueDownload(ctx, &DownloadQueue{}); err != nil {
		t.Fatalf("EnqueueDownload error: %v", err)
	}
	if d, err := s.GetNextPendingDownload(ctx); err != nil || d != nil {
		t.Fatalf("GetNextPendingDownload expected (nil,nil), got (%v,%v)", d, err)
	}
	if err := s.UpdateDownload(ctx, &DownloadQueue{}); err != nil {
		t.Fatalf("UpdateDownload error: %v", err)
	}
	if dl, err := s.ListDownloads(ctx, nil); err != nil || dl != nil {
		t.Fatalf("ListDownloads expected (nil,nil), got (%v,%v)", dl, err)
	}
	if err := s.CancelDownload(ctx, 1); err != nil {
		t.Fatalf("CancelDownload error: %v", err)
	}
	if err := s.RetryDownload(ctx, 1); err != nil {
		t.Fatalf("RetryDownload error: %v", err)
	}
	if n, err := s.RetryAllDownloads(ctx); err != nil || n != 0 {
		t.Fatalf("RetryAllDownloads expected (0,nil), got (%d,%v)", n, err)
	}

	if err := s.CreateSync(ctx, &SyncHistory{}); err != nil {
		t.Fatalf("CreateSync error: %v", err)
	}
	if err := s.UpdateSync(ctx, &SyncHistory{}); err != nil {
		t.Fatalf("UpdateSync error: %v", err)
	}
	if sh, err := s.GetLastSync(ctx); err != nil || sh != nil {
		t.Fatalf("GetLastSync expected (nil,nil), got (%v,%v)", sh, err)
	}

	if d, err := s.GetActiveDevice(ctx); err != nil || d != nil {
		t.Fatalf("GetActiveDevice expected (nil,nil), got (%v,%v)", d, err)
	}
	if err := s.SaveDevice(ctx, &Device{}); err != nil {
		t.Fatalf("SaveDevice error: %v", err)
	}
	if devs, err := s.ListDevices(ctx); err != nil || devs != nil {
		t.Fatalf("ListDevices expected (nil,nil), got (%v,%v)", devs, err)
	}
	if err := s.DeleteDevice(ctx, 1); err != nil {
		t.Fatalf("DeleteDevice error: %v", err)
	}

	if err := s.CreateLibraryDestination(ctx, &LibraryDestination{}); err != nil {
		t.Fatalf("CreateLibraryDestination error: %v", err)
	}
	if d, err := s.GetLibraryDestination(ctx, "id"); err != nil || d != nil {
		t.Fatalf("GetLibraryDestination expected (nil,nil), got (%v,%v)", d, err)
	}
	if rows, err := s.ListLibraryDestinations(ctx); err != nil || rows != nil {
		t.Fatalf("ListLibraryDestinations expected (nil,nil), got (%v,%v)", rows, err)
	}
	if rows, err := s.ListEnabledLibraryDestinations(ctx); err != nil || rows != nil {
		t.Fatalf("ListEnabledLibraryDestinations expected (nil,nil), got (%v,%v)", rows, err)
	}
	if err := s.UpdateLibraryDestination(ctx, &LibraryDestination{}); err != nil {
		t.Fatalf("UpdateLibraryDestination error: %v", err)
	}
	if err := s.DeleteLibraryDestination(ctx, "id"); err != nil {
		t.Fatalf("DeleteLibraryDestination error: %v", err)
	}
	if err := s.UpsertBookDestination(ctx, &BookDestination{}); err != nil {
		t.Fatalf("UpsertBookDestination error: %v", err)
	}
	if rows, err := s.GetBookDestinations(ctx, 1); err != nil || rows != nil {
		t.Fatalf("GetBookDestinations expected (nil,nil), got (%v,%v)", rows, err)
	}
	if row, err := s.GetBookDestination(ctx, 1, "id"); err != nil || row != nil {
		t.Fatalf("GetBookDestination expected (nil,nil), got (%v,%v)", row, err)
	}
	if rows, err := s.ListBookDestinationsBy(ctx, "id", nil); err != nil || rows != nil {
		t.Fatalf("ListBookDestinationsBy expected (nil,nil), got (%v,%v)", rows, err)
	}
}
