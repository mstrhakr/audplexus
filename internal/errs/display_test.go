package errs

import "testing"

func TestCleanForDisplay(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plex 404 with html body",
			in:   `plex section items returned 404: <html><head><title>Not Found</title></head><body><h1>404 Not Found</h1></body></html>`,
			want: "Server reachable but the configured library/section was not found.",
		},
		{
			name: "401 auth failure",
			in:   "emby items returned 401: Access token is required.",
			want: "Authentication failed — check the token or API key.",
		},
		{
			name: "503 upstream",
			in:   "abs /api/libraries returned 503: service unavailable",
			want: "Upstream server error — the media server returned 503.",
		},
		{
			name: "non-http error keeps text",
			in:   "dial tcp 10.0.0.5:32400: connect: connection refused",
			want: "dial tcp 10.0.0.5:32400: connect: connection refused",
		},
		{
			name: "html only, no http code",
			in:   `<html><body><p>oops</p></body></html>`,
			want: "oops",
		},
		{
			name: "embedded h1 collapses",
			in:   `plex scan returned 404: <html><body><h1>404 Not Found</h1></body></html>`,
			want: "Server reachable but the configured library/section was not found.",
		},
		{
			name: "empty",
			in:   "  ",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanForDisplay(tc.in)
			if got != tc.want {
				t.Fatalf("CleanForDisplay(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}
