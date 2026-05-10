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

	db := database.NewStubDB()
	db.SeedSettings(map[string]string{
		"abs_url":        url,
		"abs_api_key":    apiKey,
		"abs_library_id": libraryID,
	})

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
		outcomes := abs.OnBookOrganized(ctx, mediaserver.OrganizedBook{
			BookID:    1,
			ASIN:      testASIN,
			Title:     "abstest probe",
			LocalPath: "/audiobooks/test/test.m4b",
		})
		fmt.Println("OnBookOrganized outcomes:")
		anyFailed := false
		gotScan := false
		gotMatch := false
		for _, o := range outcomes {
			fmt.Printf("  op=%-15s status=%-25s detail=%q\n", o.Operation, o.Status, o.Detail)
			if o.Status == mediaserver.OutcomeFailed {
				anyFailed = true
			}
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
		if !gotMatch {
			fmt.Printf("warning: OnBookOrganized did not match item by ASIN %s (book may not be in ABS)\n", testASIN)
		}
		if anyFailed {
			fail("OnBookOrganized had failed outcomes")
		}
	}

	fmt.Println("\nALL TESTS PASSED")
}

func pass(msg string) { fmt.Printf("PASS: %s\n", msg) }
func fail(msg string) {
	fmt.Fprintf(os.Stderr, "FAIL: %s\n", msg)
	os.Exit(1)
}
