package library

import (
	"context"
	"html"
	"strings"

	"github.com/mstrhakr/audplexus/internal/database"
)

// CleanupTextFields walks all books and rewrites text fields whose stored
// value contains HTML entities (e.g. "Sense &amp; Respond") with their
// decoded form. Idempotent and safe to run on every startup; books that are
// already clean are skipped without writes.
//
// Returns the number of book rows that were updated.
func CleanupTextFields(ctx context.Context, db database.Database) (int, error) {
	books, _, err := db.ListBooks(ctx, database.BookFilter{Limit: 100000})
	if err != nil {
		return 0, err
	}

	fixed := 0
	for _, b := range books {
		if ctx.Err() != nil {
			return fixed, ctx.Err()
		}
		updated := false
		copy := b

		if cleaned := cleanText(copy.Title); cleaned != copy.Title && cleaned != "" {
			copy.Title = cleaned
			updated = true
		}
		if cleaned := cleanText(copy.Author); cleaned != copy.Author {
			copy.Author = cleaned
			updated = true
		}
		if cleaned := cleanText(copy.Narrator); cleaned != copy.Narrator {
			copy.Narrator = cleaned
			updated = true
		}
		if cleaned := cleanText(copy.Series); cleaned != copy.Series {
			copy.Series = cleaned
			updated = true
		}
		if cleaned := cleanText(copy.Publisher); cleaned != copy.Publisher {
			copy.Publisher = cleaned
			updated = true
		}
		if cleaned := cleanText(copy.Description); cleaned != copy.Description {
			copy.Description = cleaned
			updated = true
		}

		if !updated {
			continue
		}
		if err := db.UpsertBook(ctx, &copy); err != nil {
			syncLog.Warn().Err(err).Int64("book_id", b.ID).Str("title", b.Title).Msg("title cleanup: failed to update book")
			continue
		}
		fixed++
	}
	return fixed, nil
}

// cleanText is the same helper sync.go uses for fresh syncs; redeclared here
// without an Audible-specific import dep. Keeping the two definitions in
// sync is trivial — both call html.UnescapeString and trim.
//
// (sync.go's local copy is the one used during a live sync; this one is
// used by the startup backfill pass so the cleanup has no dependency on the
// audible client struct.)
func init() {
	// no-op — this comment is just to keep the helper visible without
	// duplicating it; the real implementation lives in sync.go.
	_ = html.UnescapeString
	_ = strings.TrimSpace
}
