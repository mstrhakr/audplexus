// jellytest is a one-shot live integration test against Jellyfin. Same
// shape as cmd/abstest — exercises production JellyfinBackend code
// against a real Jellyfin server.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
)

type inMemoryDB struct {
	settings map[string]string
}

func (m *inMemoryDB) GetSetting(ctx context.Context, key string) (string, error) {
	return m.settings[key], nil
}
func (m *inMemoryDB) SetSetting(ctx context.Context, key, value string) error {
	m.settings[key] = value
	return nil
}
func (m *inMemoryDB) Close() error                                  { return nil }
func (m *inMemoryDB) Migrate() error                                { return nil }
func (m *inMemoryDB) Reset(ctx context.Context) error               { return nil }
func (m *inMemoryDB) GetBook(ctx context.Context, id int64) (*database.Book, error) {
	return nil, nil
}
func (m *inMemoryDB) GetBookByASIN(ctx context.Context, asin string) (*database.Book, error) {
	return nil, nil
}
func (m *inMemoryDB) ListBooks(ctx context.Context, f database.BookFilter) ([]database.Book, int, error) {
	return nil, 0, nil
}
func (m *inMemoryDB) UpsertBook(ctx context.Context, b *database.Book) error { return nil }
func (m *inMemoryDB) UpdateBookStatus(ctx context.Context, id int64, s database.BookStatus) error {
	return nil
}
func (m *inMemoryDB) UpdateBookPlexInfo(ctx context.Context, id int64, k, t string) error {
	return nil
}
func (m *inMemoryDB) UpdateBookMediaServerInfo(ctx context.Context, id int64, k, t string) error {
	return nil
}
func (m *inMemoryDB) DeleteBook(ctx context.Context, id int64) error { return nil }
func (m *inMemoryDB) EnqueueDownload(ctx context.Context, i *database.DownloadQueue) error {
	return nil
}
func (m *inMemoryDB) GetNextPendingDownload(ctx context.Context) (*database.DownloadQueue, error) {
	return nil, nil
}
func (m *inMemoryDB) UpdateDownload(ctx context.Context, i *database.DownloadQueue) error {
	return nil
}
func (m *inMemoryDB) ListDownloads(ctx context.Context, s *database.DownloadStatus) ([]database.DownloadQueue, error) {
	return nil, nil
}
func (m *inMemoryDB) CancelDownload(ctx context.Context, id int64) error    { return nil }
func (m *inMemoryDB) RetryDownload(ctx context.Context, id int64) error     { return nil }
func (m *inMemoryDB) RetryAllDownloads(ctx context.Context) (int64, error)  { return 0, nil }
func (m *inMemoryDB) CreateSync(ctx context.Context, s *database.SyncHistory) error  { return nil }
func (m *inMemoryDB) UpdateSync(ctx context.Context, s *database.SyncHistory) error  { return nil }
func (m *inMemoryDB) GetLastSync(ctx context.Context) (*database.SyncHistory, error) { return nil, nil }
func (m *inMemoryDB) GetActiveDevice(ctx context.Context) (*database.Device, error)  { return nil, nil }
func (m *inMemoryDB) SaveDevice(ctx context.Context, d *database.Device) error       { return nil }
func (m *inMemoryDB) ListDevices(ctx context.Context) ([]database.Device, error)     { return nil, nil }
func (m *inMemoryDB) DeleteDevice(ctx context.Context, id int64) error               { return nil }
func (m *inMemoryDB) CreateLibraryDestination(ctx context.Context, d *database.LibraryDestination) error {
	return nil
}
func (m *inMemoryDB) GetLibraryDestination(ctx context.Context, id string) (*database.LibraryDestination, error) {
	return nil, nil
}
func (m *inMemoryDB) ListLibraryDestinations(ctx context.Context) ([]database.LibraryDestination, error) {
	return nil, nil
}
func (m *inMemoryDB) ListEnabledLibraryDestinations(ctx context.Context) ([]database.LibraryDestination, error) {
	return nil, nil
}
func (m *inMemoryDB) UpdateLibraryDestination(ctx context.Context, d *database.LibraryDestination) error {
	return nil
}
func (m *inMemoryDB) DeleteLibraryDestination(ctx context.Context, id string) error { return nil }
func (m *inMemoryDB) UpsertBookDestination(ctx context.Context, bd *database.BookDestination) error {
	return nil
}
func (m *inMemoryDB) GetBookDestinations(ctx context.Context, bookID int64) ([]database.BookDestination, error) {
	return nil, nil
}
func (m *inMemoryDB) GetBookDestination(ctx context.Context, bookID int64, destID string) (*database.BookDestination, error) {
	return nil, nil
}
func (m *inMemoryDB) ListBookDestinationsBy(ctx context.Context, destID string, state *database.BookDestinationSyncState) ([]database.BookDestination, error) {
	return nil, nil
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		fmt.Fprintf(os.Stderr, "skip: %s not set\n", k)
		os.Exit(0)
	}
	return v
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	url := mustEnv("JELLYFIN_URL")
	apiKey := mustEnv("JELLYFIN_API_KEY")
	libraryID := mustEnv("JELLYFIN_LIBRARY_ID")
	expectedTitle := os.Getenv("JELLYFIN_TEST_TITLE")

	db := &inMemoryDB{settings: map[string]string{
		"jellyfin_url":        url,
		"jellyfin_api_key":    apiKey,
		"jellyfin_library_id": libraryID,
	}}
	jf := mediaserver.NewJellyfin(db, nil, "/audiobooks")

	if !jf.Configured(ctx) {
		fail("Configured() returned false")
	}
	pass("Configured() returned true")

	count, err := jf.LibraryItemCount(ctx)
	if err != nil {
		fail(fmt.Sprintf("LibraryItemCount: %v", err))
	}
	pass(fmt.Sprintf("LibraryItemCount = %d", count))

	postCount, err := jf.TriggerLibraryScan(ctx)
	if err != nil {
		fail(fmt.Sprintf("TriggerLibraryScan: %v", err))
	}
	pass(fmt.Sprintf("TriggerLibraryScan ok (post-scan count=%d)", postCount))

	if expectedTitle != "" {
		fmt.Println("OnBookOrganized outcomes:")
		outcomes := jf.OnBookOrganized(ctx, mediaserver.OrganizedBook{
			BookID:    1,
			Title:     expectedTitle,
			Series:    "TestSeries",
			LocalPath: "/audiobooks/test/test.m4b",
		})
		for _, o := range outcomes {
			fmt.Printf("  op=%-15s status=%-25s detail=%q\n", o.Operation, o.Status, o.Detail)
		}
		// Assert scan_trigger succeeded as the minimum.
		gotScan := false
		for _, o := range outcomes {
			if o.Operation == mediaserver.OpScanTrigger && o.Status == mediaserver.OutcomeSucceeded {
				gotScan = true
			}
		}
		if !gotScan {
			fail("OnBookOrganized: scan_trigger did not succeed")
		}
		pass("OnBookOrganized: scan_trigger succeeded")
	}

	fmt.Println("\nALL TESTS PASSED")
}

func pass(msg string) { fmt.Printf("PASS: %s\n", msg) }
func fail(msg string) {
	fmt.Fprintf(os.Stderr, "FAIL: %s\n", msg)
	os.Exit(1)
}
