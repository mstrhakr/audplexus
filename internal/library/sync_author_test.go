package library

import (
	"testing"

	"github.com/mstrhakr/go-audible"
)

func TestConvertBookFiltersRoleContributorsAndKeepsCoauthors(t *testing.T) {
	book := convertBook(audible.Book{
		ASIN: "B012345678",
		Authors: []audible.Contributor{
			{ASIN: "TRANSLATOR", Name: "Karl A. Klewer - translator"},
			{ASIN: "ENCODED_TRANSLATOR", Name: "Another Name - translator&nbsp;"},
			{ASIN: "TOLKIEN", Name: "J.R.R. Tolkien"},
			{ASIN: "CHRISTOPHER", Name: "Christopher Tolkien"},
		},
	})

	if got, want := book.Author, "J.R.R. Tolkien, Christopher Tolkien"; got != want {
		t.Errorf("Author = %q, want %q", got, want)
	}
	if got, want := book.AuthorASIN, "TOLKIEN"; got != want {
		t.Errorf("AuthorASIN = %q, want first actual author ASIN %q", got, want)
	}
}
