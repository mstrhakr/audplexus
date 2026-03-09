package organizer

import "testing"

func TestBuildFilenameBase(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		author string
		want   string
	}{
		{
			name:   "title and author",
			title:  "Harry Potter and the Goblet of Fire",
			author: "J.K. Rowling",
			want:   "Harry Potter and the Goblet of Fire - J.K. Rowling",
		},
		{
			name:   "title only when author missing",
			title:  "Leviathan Wakes",
			author: "",
			want:   "Leviathan Wakes",
		},
		{
			name:   "unknown title fallback",
			title:  "",
			author: "James S. A. Corey",
			want:   "Unknown Title - James S. A. Corey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFilenameBase(tt.title, tt.author)
			if got != tt.want {
				t.Fatalf("buildFilenameBase() = %q, want %q", got, tt.want)
			}
		})
	}
}
