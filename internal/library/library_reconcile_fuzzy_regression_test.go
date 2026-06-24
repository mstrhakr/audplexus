package library

import (
	"path/filepath"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestFuzzyMatchRejectsPartialDirectoryTitleWithoutAuthorOrSeries(t *testing.T) {
	tests := []struct {
		name       string
		book       database.Book
		bookDir    string
		chapterOne string
	}{
		{
			name: "Crucible does not match Leadville Crucible",
			book: database.Book{
				Title:  "Crucible",
				Author: "Travis Bagwell",
				Series: "Awaken Online",
			},
			bookDir:    filepath.Join("root", "Aaron Crash", "American Dragons", "American Dragons 07 - Leadville Crucible"),
			chapterOne: "001 - Chapter 01 - Arrival.m4a",
		},
		{
			name: "Guardian does not match The Guardian Guild",
			book: database.Book{
				Title:  "Guardian",
				Author: "Jack Campbell",
				Series: "Lost Fleet Beyond the Frontier",
			},
			bookDir:    filepath.Join("root", "Jonathan Brooks", "Station Cores", "Station Cores 3 - The Guardian Guild"),
			chapterOne: "001 - Chapter 01 - Guild Hall.m4a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			discovered := map[string]int64{
				filepath.Join(tt.bookDir, tt.chapterOne):                    10,
				filepath.Join(tt.bookDir, "002 - Chapter 02 - Trouble.m4a"): 20,
				filepath.Join(tt.bookDir, "003 - Chapter 03 - Return.m4a"):  30,
			}

			path, size, ok := fuzzyMatchDiscoveredFile(&tt.book, discovered)
			if ok || path != "" || size != 0 {
				t.Fatalf("fuzzyMatchDiscoveredFile() = (%q, %d, %v), want no match", path, size, ok)
			}
		})
	}
}

func TestFuzzyMatchRejectsPartialStandaloneTitleWithoutAuthorOrSeries(t *testing.T) {
	root := filepath.Join("root", "Aaron Crash", "American Dragons")
	wrongFile := filepath.Join(root, "American Dragons 07 - Leadville Crucible.m4b")
	discovered := map[string]int64{wrongFile: 100}
	book := &database.Book{
		Title:  "Crucible",
		Author: "Travis Bagwell",
		Series: "Awaken Online",
	}

	path, size, ok := fuzzyMatchDiscoveredFile(book, discovered)
	if ok || path != "" || size != 0 {
		t.Fatalf("fuzzyMatchDiscoveredFile() = (%q, %d, %v), want no match", path, size, ok)
	}
}

func TestFuzzyMatchAcceptsPartialTitleWhenSeriesCorroborates(t *testing.T) {
	root := filepath.Join("root", "Flat Library")
	crucible := filepath.Join(root, "Awaken Online 08 - Crucible.m4b")
	discovered := map[string]int64{crucible: 100}
	book := &database.Book{
		Title:  "Crucible",
		Author: "Travis Bagwell",
		Series: "Awaken Online",
	}

	path, size, ok := fuzzyMatchDiscoveredFile(book, discovered)
	if !ok || path != crucible || size != 100 {
		t.Fatalf("fuzzyMatchDiscoveredFile() = (%q, %d, %v), want (%q, %d, true)", path, size, ok, crucible, 100)
	}
}
