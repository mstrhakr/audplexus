// Package contributors contains normalization helpers for contributor metadata.
package contributors

import "strings"

// IsTranslator reports whether name ends with Audible's translator role suffix.
func IsTranslator(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), " - translator")
}

// FilterAuthors removes translator credits while preserving the order of authors.
func FilterAuthors(names []string) []string {
	authors := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || IsTranslator(name) {
			continue
		}
		authors = append(authors, name)
	}
	return authors
}
