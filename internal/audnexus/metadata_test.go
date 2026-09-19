package audnexus

import (
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestEnrichedBookFiltersTranslatorCredits(t *testing.T) {
	book := &EnrichedBook{
		Book: &database.Book{Author: "Fallback Author"},
		AudnexusBook: &BookResponse{Authors: []Person{
			{Name: "J.R.R. Tolkien"},
			{Name: "Karl A. Klewer - translator"},
			{Name: "Christopher Tolkien"},
		}},
	}

	if got, want := book.Author(), "J.R.R. Tolkien, Christopher Tolkien"; got != want {
		t.Errorf("Author() = %q, want %q", got, want)
	}
	if got, want := book.Writer(), "Written by J.R.R. Tolkien, Christopher Tolkien"; got != want {
		t.Errorf("Writer() = %q, want %q", got, want)
	}
}

func TestEnrichedBookFallsBackWhenOnlyTranslatorIsListed(t *testing.T) {
	book := &EnrichedBook{
		Book:         &database.Book{Author: "Fallback Author"},
		AudnexusBook: &BookResponse{Authors: []Person{{Name: "Translator - translator"}}},
	}

	if got, want := book.Author(), "Fallback Author"; got != want {
		t.Errorf("Author() = %q, want fallback %q", got, want)
	}
	if got := book.Writer(); got != "" {
		t.Errorf("Writer() = %q, want empty when no writer is listed", got)
	}
}
