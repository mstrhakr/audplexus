package mediaserver

import (
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestChoosePlexCollectionRatingKeyUsesStoredDestinationID(t *testing.T) {
	t.Parallel()

	book := database.Book{ID: 1, Title: "One", Series: "Series", PlexRatingKey: "222", MediaServerID: "333"}
	valid := map[string]struct{}{"111": {}, "222": {}, "333": {}}

	if got := choosePlexCollectionRatingKey(book, "111", valid); got != "111" {
		t.Fatalf("choosePlexCollectionRatingKey() = %q, want %q", got, "111")
	}
}

func TestChoosePlexCollectionRatingKeyFallsBackToPlexRatingKey(t *testing.T) {
	t.Parallel()

	book := database.Book{ID: 1, Title: "One", Series: "Series", PlexRatingKey: "222", MediaServerID: "333"}
	valid := map[string]struct{}{"222": {}, "333": {}}

	if got := choosePlexCollectionRatingKey(book, "foreign", valid); got != "222" {
		t.Fatalf("choosePlexCollectionRatingKey() = %q, want %q", got, "222")
	}
}

func TestBuildPlexSeriesBooksSkipsUnknownIds(t *testing.T) {
	t.Parallel()

	books := []database.Book{{ID: 1, Title: "One", Series: "Series", PlexRatingKey: "222"}}
	valid := map[string]struct{}{"111": {}}
	got := buildPlexSeriesBooks(books, map[int64]string{1: "foreign"}, valid)
	if len(got) != 0 {
		t.Fatalf("got = %#v, want empty", got)
	}
}