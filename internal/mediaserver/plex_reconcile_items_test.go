package mediaserver

import (
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestBuildPlexSeriesBooksFromDestinationsUsesStoredIds(t *testing.T) {
	t.Parallel()

	books := []database.Book{{ID: 1, Title: "One", Series: "Series"}}
	valid := map[string]struct{}{"111": {}}

	got := buildPlexSeriesBooksFromDestinations(books, map[int64]string{1: "111"}, valid)
	seriesBooks := got["Series"]
	if len(seriesBooks) != 1 || seriesBooks[0].RatingKey != "111" {
		t.Fatalf("seriesBooks = %#v", seriesBooks)
	}
}

func TestBuildPlexSeriesBooksFromDestinationsSkipsUnknownIds(t *testing.T) {
	t.Parallel()

	books := []database.Book{{ID: 1, Title: "One", Series: "Series"}}
	valid := map[string]struct{}{"111": {}}
	got := buildPlexSeriesBooksFromDestinations(books, map[int64]string{1: "foreign"}, valid)
	if len(got) != 0 {
		t.Fatalf("got = %#v, want empty", got)
	}
}