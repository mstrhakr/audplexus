package contributors

import (
	"reflect"
	"testing"
)

func TestFilterAuthorsDropsTranslatorCreditsAndPreservesCoAuthors(t *testing.T) {
	got := FilterAuthors([]string{
		" J.R.R. Tolkien ",
		"Karl A. Klewer - translator",
		"Christopher Tolkien",
		"",
	})
	want := []string{"J.R.R. Tolkien", "Christopher Tolkien"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterAuthors() = %#v, want %#v", got, want)
	}
}

func TestIsTranslatorIsCaseInsensitive(t *testing.T) {
	if !IsTranslator("Karl A. Klewer - Translator") {
		t.Fatal("IsTranslator() did not recognize a case-insensitive translator suffix")
	}
	if IsTranslator("Karl A. Klewer") {
		t.Fatal("IsTranslator() classified an author without a role suffix as a translator")
	}
}
