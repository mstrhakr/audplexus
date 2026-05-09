package mediaserver

import (
	"path/filepath"
	"strings"
)

// translateScanPath maps a local filesystem path under localLibraryRoot into
// the server-visible path under serverLibraryRoot. Used because Audplexus and
// the media server typically mount the same data at different paths (e.g.
// /audiobooks vs /mnt/Books in their respective containers).
//
// Returns ok=false when the path can't be translated (e.g. localScanPath is
// outside localLibraryRoot, or the server root is empty).
func translateScanPath(localScanPath, localLibraryRoot, serverLibraryRoot string) (string, bool) {
	localScanPath = strings.TrimSpace(localScanPath)
	localLibraryRoot = strings.TrimSpace(localLibraryRoot)
	serverLibraryRoot = strings.TrimSpace(serverLibraryRoot)

	if localScanPath == "" || serverLibraryRoot == "" {
		return "", false
	}

	if localLibraryRoot == "" {
		if pathsEquivalent(localScanPath, serverLibraryRoot) {
			return serverLibraryRoot, true
		}
		return "", false
	}

	rel, err := filepath.Rel(localLibraryRoot, localScanPath)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}

	if rel == "." {
		return serverLibraryRoot, true
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if strings.Contains(serverLibraryRoot, `\`) && !strings.Contains(serverLibraryRoot, "/") {
		return strings.TrimRight(serverLibraryRoot, `\`) + `\` + strings.Join(parts, `\`), true
	}
	return strings.TrimRight(serverLibraryRoot, "/") + "/" + strings.Join(parts, "/"), true
}

func pathsEquivalent(a, b string) bool {
	a = strings.TrimRight(strings.TrimSpace(a), `/\`)
	b = strings.TrimRight(strings.TrimSpace(b), `/\`)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}
