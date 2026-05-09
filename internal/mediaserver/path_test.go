package mediaserver

import "testing"

func TestTranslateScanPath(t *testing.T) {
	cases := []struct {
		name        string
		localScan   string
		localRoot   string
		serverRoot  string
		wantPath    string
		wantOK      bool
	}{
		{
			name:       "subdir under both roots",
			localScan:  "/audiobooks/Larry Correia/Tom Stranger",
			localRoot:  "/audiobooks",
			serverRoot: "/mnt/Books",
			wantPath:   "/mnt/Books/Larry Correia/Tom Stranger",
			wantOK:     true,
		},
		{
			name:       "scan at root maps to root",
			localScan:  "/audiobooks",
			localRoot:  "/audiobooks",
			serverRoot: "/mnt/Books",
			wantPath:   "/mnt/Books",
			wantOK:     true,
		},
		{
			name:       "scan path outside local root rejected",
			localScan:  "/elsewhere/Foo",
			localRoot:  "/audiobooks",
			serverRoot: "/mnt/Books",
			wantOK:     false,
		},
		{
			name:       "windows server root keeps backslashes",
			localScan:  "/audiobooks/Author/Book",
			localRoot:  "/audiobooks",
			serverRoot: `D:\Media\Books`,
			wantPath:   `D:\Media\Books\Author\Book`,
			wantOK:     true,
		},
		{
			name:       "empty server root returns false",
			localScan:  "/audiobooks/Author/Book",
			localRoot:  "/audiobooks",
			serverRoot: "",
			wantOK:     false,
		},
		{
			name:       "trailing slash on server root tolerated",
			localScan:  "/audiobooks/Author/Book",
			localRoot:  "/audiobooks",
			serverRoot: "/mnt/Books/",
			wantPath:   "/mnt/Books/Author/Book",
			wantOK:     true,
		},
		{
			name:       "empty local root accepts only equivalent server root",
			localScan:  "/mnt/Books",
			localRoot:  "",
			serverRoot: "/mnt/Books",
			wantPath:   "/mnt/Books",
			wantOK:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := translateScanPath(tc.localScan, tc.localRoot, tc.serverRoot)
			if ok != tc.wantOK {
				t.Fatalf("ok mismatch: got %v want %v (path %q)", ok, tc.wantOK, got)
			}
			if ok && got != tc.wantPath {
				t.Fatalf("path mismatch: got %q want %q", got, tc.wantPath)
			}
		})
	}
}
