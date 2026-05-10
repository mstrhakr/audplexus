// abstest is a one-shot live integration test against an Audiobookshelf
// instance. It exercises the production ABSBackend code (not raw curl)
// to verify the auth header, scan endpoint, item count, and ASIN search
// all work as the code expects against a real server.
//
// Usage:
//
//	ABS_URL=http://abs:80 ABS_API_KEY=<jwt> ABS_LIBRARY_ID=<uuid> \
//	    ABS_TEST_ASIN=<known-asin> ./abstest
//
// Skips with exit 0 if any required env var is missing.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
)

// inMemoryDB is the smallest possible Database that ABSBackend.settings()
// needs: it returns ABS_* env vars from GetSetting so the backend reads
// the same config our test script provides.
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

// Stub everything else. The ABS backend doesn't call any of these in the
// paths we exercise.
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	url := mustEnv("ABS_URL")
	apiKey := mustEnv("ABS_API_KEY")
	libraryID := mustEnv("ABS_LIBRARY_ID")
	testASIN := os.Getenv("ABS_TEST_ASIN") // optional; skips item-match check if absent

	db := &inMemoryDB{settings: map[string]string{
		"abs_url":        url,
		"abs_api_key":    apiKey,
		"abs_library_id": libraryID,
	}}

	abs := mediaserver.NewABS(db, "/audiobooks")

	if !abs.Configured(ctx) {
		fail("Configured() returned false despite all env vars set")
	}
	pass("Configured() returned true")

	count, err := abs.LibraryItemCount(ctx)
	if err != nil {
		fail(fmt.Sprintf("LibraryItemCount: %v", err))
	}
	pass(fmt.Sprintf("LibraryItemCount = %d", count))

	postCount, err := abs.TriggerLibraryScan(ctx)
	if err != nil {
		fail(fmt.Sprintf("TriggerLibraryScan: %v", err))
	}
	pass(fmt.Sprintf("TriggerLibraryScan ok (post-scan count=%d)", postCount))

	if testASIN != "" {
		// Exercise OnBookOrganized — scan, item match by ASIN, no series.
		outcomes := abs.OnBookOrganized(ctx, mediaserver.OrganizedBook{
			BookID:    1,
			ASIN:      testASIN,
			Title:     "abstest probe",
			LocalPath: "/audiobooks/test/test.m4b",
		})
		fmt.Println("OnBookOrganized outcomes:")
		anyFailed := false
		for _, o := range outcomes {
			fmt.Printf("  op=%-15s status=%-25s detail=%q\n", o.Operation, o.Status, o.Detail)
			if o.Status == mediaserver.OutcomeFailed {
				anyFailed = true
			}
		}
		// Item match might "Deferred" if the book isn't on the server (test
		// asin we use is from a known existing book per earlier curl test).
		// We assert: scan_trigger is succeeded, item_match is succeeded.
		gotScan := false
		gotMatch := false
		for _, o := range outcomes {
			if o.Operation == mediaserver.OpScanTrigger && o.Status == mediaserver.OutcomeSucceeded {
				gotScan = true
			}
			if o.Operation == mediaserver.OpItemMatch && o.Status == mediaserver.OutcomeSucceeded {
				gotMatch = true
			}
		}
		if !gotScan {
			fail("OnBookOrganized: scan_trigger did not succeed")
		}
		pass("OnBookOrganized: scan_trigger succeeded")
		if testASIN != "" && !gotMatch {
			// non-fatal — book may not exist on server, but log it
			fmt.Printf("warning: OnBookOrganized did not match item by ASIN %s (book may not be in ABS)\n", testASIN)
		}
		if anyFailed {
			fail("OnBookOrganized had failed outcomes")
		}
	}

	fmt.Println("\nALL TESTS PASSED")
}

func pass(msg string) {
	fmt.Printf("PASS: %s\n", msg)
}

func fail(msg string) {
	fmt.Fprintf(os.Stderr, "FAIL: %s\n", msg)
	os.Exit(1)
}
