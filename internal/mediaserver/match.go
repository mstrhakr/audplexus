package mediaserver

import (
	"html"
	"strings"
)

// normalizeTitle returns a comparison-friendly form of a book title that
// tolerates the small differences that show up between Audible's metadata,
// what Audplexus persists in the database, and what a media server indexes
// from disk. Differences seen in the wild include:
//
//   - HTML entity encoding ("Sense &amp; Respond" vs "Sense & Respond"),
//     where Audible's API sometimes returns pre-encoded titles.
//   - Leading articles ("The Wise Woman" vs "Wise Woman"), where one source
//     drops the article and another keeps it.
//   - "&" vs "and" (Audible vs publisher casing), e.g.
//     "Jonathan Strange & Mr Norrell" vs "Jonathan Strange and Mr Norrell".
//   - Punctuation, smart quotes, and surrounding whitespace.
//
// All of these would otherwise cause a reconcile pass to think a book is
// missing from the media server when it is in fact already indexed.
func normalizeTitle(s string) string {
	s = html.UnescapeString(s)
	s = strings.ToLower(s)
	s = strings.NewReplacer(
		"‘", "'",
		"’", "'",
		"“", "\"",
		"”", "\"",
		"–", "-",
		"—", "-",
	).Replace(s)

	// "&" and "and" are interchangeable in titles; normalize to a single form.
	s = strings.ReplaceAll(s, " & ", " and ")

	// Strip a leading article so "The Wise Woman" matches "Wise Woman".
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			break
		}
	}

	// Drop punctuation that doesn't carry meaning for matching.
	s = strings.NewReplacer(
		":", "",
		"-", " ",
		"_", " ",
		".", "",
		",", "",
		"'", "",
		"\"", "",
		"(", "",
		")", "",
		"[", "",
		"]", "",
		"!", "",
		"?", "",
	).Replace(s)

	// Collapse runs of whitespace and trim.
	s = strings.Join(strings.Fields(s), " ")
	return s
}
