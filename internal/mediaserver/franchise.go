package mediaserver

import "strings"

// franchiseFromSeries extracts a broader "franchise" label from a series
// name when one is implied by the naming convention. Audnexus often returns
// series names like "Star Wars: Darth Bane Trilogy" or "Star Wars (Zahn)"
// where the part before the colon or parenthesis is the franchise the user
// thinks in (Star Wars), and the rest is the specific series within it.
//
// Returns an empty string when no franchise is implied. The franchise is
// only emitted when it differs from the original series name AND looks like
// a real label (contains a space-separated token and is at least 3 chars).
func franchiseFromSeries(series string) string {
	s := strings.TrimSpace(series)
	if s == "" {
		return ""
	}

	// Prefer the colon split — it's the most common Audible convention.
	if i := strings.Index(s, ":"); i > 0 {
		head := strings.TrimSpace(s[:i])
		if isFranchiseLabel(head) && head != s {
			return head
		}
	}

	// Then the parenthesis split — "Star Wars (Zahn)", "Discworld (Death)".
	if i := strings.Index(s, "("); i > 0 {
		head := strings.TrimSpace(s[:i])
		if isFranchiseLabel(head) && head != s {
			return head
		}
	}

	return ""
}

func isFranchiseLabel(s string) bool {
	if len(s) < 3 {
		return false
	}
	// Reject single-word labels that are likely the series itself.
	// "Bobiverse" and "Discworld" should not also become franchises.
	if !strings.Contains(s, " ") {
		return false
	}
	return true
}
