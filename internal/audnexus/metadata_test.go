package audnexus

import (
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestEnrichedBookAuthorFiltersRoleContributors(t *testing.T) {
	enriched := &EnrichedBook{
		Book: &database.Book{Author: "Audible fallback"},
		AudnexusBook: &BookResponse{
			Authors: []Person{
				{Name: "J.R.R. Tolkien"},
				{Name: "Christopher Tolkien"},
				{Name: "Karl A. Klewer - translator"},
			},
		},
	}

	const want = "J.R.R. Tolkien, Christopher Tolkien"
	if got := enriched.Author(); got != want {
		t.Errorf("Author() = %q, want %q", got, want)
	}
	if got := enriched.Writer(); got != "Written by "+want {
		t.Errorf("Writer() = %q, want %q", got, "Written by "+want)
	}
	if got := enriched.ToAudioMetadata().Author; got != want {
		t.Errorf("ToAudioMetadata().Author = %q, want %q", got, want)
	}
}

func TestEnrichedBookAuthorFallsBackWhenOnlyRoleContributors(t *testing.T) {
	enriched := &EnrichedBook{
		Book: &database.Book{Author: "J.R.R. Tolkien"},
		AudnexusBook: &BookResponse{
			Authors: []Person{{Name: "Karl A. Klewer - translator"}},
		},
	}

	if got, want := enriched.Author(), "J.R.R. Tolkien"; got != want {
		t.Errorf("Author() = %q, want fallback %q", got, want)
	}
}
