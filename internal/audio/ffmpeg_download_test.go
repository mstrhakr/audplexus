package audio

import "testing"

func TestSanitizeArchiveBinaryName(t *testing.T) {
	tests := []struct {
		name      string
		entryName string
		ext       string
		wantBase  string
		wantOK    bool
	}{
		{name: "plain ffmpeg", entryName: "ffmpeg", ext: "", wantBase: "ffmpeg", wantOK: true},
		{name: "nested ffprobe", entryName: "build/bin/ffprobe", ext: "", wantBase: "ffprobe", wantOK: true},
		{name: "windows separators", entryName: `build\\bin\\ffmpeg.exe`, ext: ".exe", wantBase: "ffmpeg.exe", wantOK: true},
		{name: "traversal rejected", entryName: "../../ffmpeg", ext: "", wantBase: "", wantOK: false},
		{name: "absolute rejected", entryName: "/tmp/ffmpeg", ext: "", wantBase: "", wantOK: false},
		{name: "wrong binary rejected", entryName: "build/bin/not-ffmpeg", ext: "", wantBase: "", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotBase, gotOK := sanitizeArchiveBinaryName(tc.entryName, tc.ext)
			if gotOK != tc.wantOK {
				t.Fatalf("sanitizeArchiveBinaryName(%q) ok=%v, want %v", tc.entryName, gotOK, tc.wantOK)
			}
			if gotBase != tc.wantBase {
				t.Fatalf("sanitizeArchiveBinaryName(%q) base=%q, want %q", tc.entryName, gotBase, tc.wantBase)
			}
		})
	}
}

func TestSafeJoinBinPath(t *testing.T) {
	out, err := safeJoinBinPath("/tmp/audplexus/bin", "ffmpeg")
	if err != nil {
		t.Fatalf("safeJoinBinPath returned unexpected error: %v", err)
	}
	if out != "/tmp/audplexus/bin/ffmpeg" {
		t.Fatalf("safeJoinBinPath output = %q, want %q", out, "/tmp/audplexus/bin/ffmpeg")
	}
}
