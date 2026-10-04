package contributors

import "testing"

func TestIsNonAuthorRole(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "Karl A. Klewer - translator", want: true},
		{name: "A. Editor - EDITOR", want: true},
		{name: "J.R.R. Tolkien", want: false},
		{name: "Tolkien - The Hobbit", want: false},
		{name: "Jean-Paul Sartre", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNonAuthorRole(tc.name); got != tc.want {
				t.Errorf("IsNonAuthorRole(%q) = %t, want %t", tc.name, got, tc.want)
			}
		})
	}
}
