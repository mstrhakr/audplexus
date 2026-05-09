package mediaserver

import "testing"

func TestFranchiseFromSeries(t *testing.T) {
	cases := []struct {
		series string
		want   string
	}{
		{"Star Wars: The Ascendancy Trilogy", "Star Wars"},
		{"Star Wars (Zahn)", "Star Wars"},
		{"Star Wars: Darth Bane Trilogy - Legends", "Star Wars"},
		{"Star Wars: The Old Republic - Legends", "Star Wars"},
		// Single-word series should not double as a franchise.
		{"Bobiverse", ""},
		{"Discworld", ""},
		// Series whose head before colon is the same as the whole.
		{"Cradle", ""},
		// Trim whitespace.
		{"  Foundation : Original Trilogy  ", ""}, // single-word head "Foundation" rejected
		// Empty/blank input.
		{"", ""},
		{"   ", ""},
		// Multi-word franchise with sub-series after dash (no colon) - no franchise extracted.
		{"Wheel of Time", ""},
		// Both colon and paren — colon wins.
		{"Star Wars: Thrawn (Zahn)", "Star Wars"},
	}
	for _, tc := range cases {
		got := franchiseFromSeries(tc.series)
		if got != tc.want {
			t.Errorf("franchiseFromSeries(%q) = %q, want %q", tc.series, got, tc.want)
		}
	}
}
