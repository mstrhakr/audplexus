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

	db := database.NewStubDB()
	db.SeedSettings(map[string]string{
		"jellyfin_url":        url,
		"jellyfin_api_key":    apiKey,
		"jellyfin_library_id": libraryID,
	})
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
		gotScan := false
		for _, o := range outcomes {
			fmt.Printf("  op=%-15s status=%-25s detail=%q\n", o.Operation, o.Status, o.Detail)
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
