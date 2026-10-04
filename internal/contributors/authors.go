// Package contributors contains shared helpers for contributor metadata.
package contributors

import (
	"strings"
)

// IsNonAuthorRole reports whether a contributor name ends with a known role
// annotation rather than naming an author. Audible and Audnexus encode some
// contributor roles in names, for example "Karl A. Klewer - translator".
func IsNonAuthorRole(name string) bool {
	separator := strings.LastIndex(name, " - ")
	if separator < 0 {
		return false
	}

	role := strings.ToLower(strings.TrimSpace(name[separator+3:]))
	switch role {
	case "translator", "editor", "illustrator", "introduction", "foreword",
		"afterword", "preface", "abridger", "adapter", "adaptor", "compiler",
		"contributor", "annotator":
		return true
	default:
		return false
	}
}
