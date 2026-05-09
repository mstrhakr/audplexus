package mediaserver

import "testing"

func TestNormalizeTitle(t *testing.T) {
	cases := []struct {
		a    string
		b    string
		same bool
	}{
		// HTML-encoded ampersand vs decoded
		{"Sense &amp; Respond", "Sense & Respond", true},
		// Decoded vs encoded the other direction
		{"Jonathan Strange & Mr Norrell", "Jonathan Strange &amp; Mr Norrell", true},
		// "&" should match "and"
		{"Jonathan Strange & Mr Norrell", "Jonathan Strange and Mr Norrell", true},
		// Leading "The" tolerated
		{"Wise Woman", "The Wise Woman", true},
		{"A Wrinkle in Time", "Wrinkle in Time", true},
		// Smart quotes, dashes
		{"It’s Complicated", "It's Complicated", true},
		// Punctuation differences
		{"Title: Subtitle", "Title Subtitle", true},
		// Different titles must not match
		{"Wise Woman", "Wise Man", false},
		{"The Eye of the World", "The Eye of the Bedlam Bride", false},
		// Whitespace tolerance
		{"  Foo   Bar  ", "foo bar", true},
	}
	for _, tc := range cases {
		gotA := normalizeTitle(tc.a)
		gotB := normalizeTitle(tc.b)
		eq := gotA == gotB
		if eq != tc.same {
			t.Errorf("normalize(%q)=%q vs normalize(%q)=%q: same=%v want %v",
				tc.a, gotA, tc.b, gotB, eq, tc.same)
		}
	}
}
