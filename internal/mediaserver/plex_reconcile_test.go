package mediaserver

import (
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestChoosePlexRatingKeyPrefersPlexScopedID(t *testing.T) {
	t.Parallel()

	valid := map[string]struct{}{
		"211020": {},
		"211150": {},
	}
	book := database.Book{
		ID:           1,
		Title:        "Anvil Dark",
		Series:       "Backyard Starship",
		PlexRatingKey: "211150",
		MediaServerID: "f090e5cd-d744-4081-8df2-051be2e6275d",
	}

	if got := choosePlexRatingKey(book, valid); got != "211150" {
		t.Fatalf("choosePlexRatingKey() = %q, want %q", got, "211150")
	}
}

func TestChoosePlexRatingKeyRejectsForeignIDs(t *testing.T) {
	t.Parallel()

	valid := map[string]struct{}{
		"211020": {},
	}
	book := database.Book{
		ID:            1,
		Title:         "Legacy of Stars",
		Series:        "Backyard Starship",
		MediaServerID: "f090e5cd-d744-4081-8df2-051be2e6275d",
	}

	if got := choosePlexRatingKey(book, valid); got != "" {
		t.Fatalf("choosePlexRatingKey() = %q, want empty", got)
	}
}

func TestBuildPlexSeriesBooksSkipsInvalidEntries(t *testing.T) {
	t.Parallel()

	valid := map[string]struct{}{
		"211020": {},
	}
	books := []database.Book{
		{ID: 1, Title: "Good", Series: "Series", PlexRatingKey: "211020"},
		{ID: 2, Title: "Bad", Series: "Series", MediaServerID: "64a3f55a-e333-4001-827a-d90e57ec1647"},
	}

	got := buildPlexSeriesBooks(books, valid)
	seriesBooks := got["Series"]
	if len(seriesBooks) != 1 {
		t.Fatalf("len(seriesBooks) = %d, want 1", len(seriesBooks))
	}
	if seriesBooks[0].Title != "Good" || seriesBooks[0].RatingKey != "211020" {
		t.Fatalf("seriesBooks[0] = %+v", seriesBooks[0])
	}
}