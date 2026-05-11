package mediaserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListLibraries(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantErr     string // substring; "" means expect success
		wantLen     int
		wantFirstID string
	}{
		{
			name:        "wrapped object shape (older ABS)",
			status:      http.StatusOK,
			contentType: "application/json",
			body: `{"libraries":[
				{"id":"aaa","name":"Audiobooks","mediaType":"book","folders":[{"fullPath":"/data/abs/books"}]},
				{"id":"bbb","name":"Podcasts","mediaType":"podcast","folders":[{"fullPath":"/data/abs/pod"}]}
			]}`,
			wantLen:     2,
			wantFirstID: "aaa",
		},
		{
			name:        "bare array shape (newer ABS)",
			status:      http.StatusOK,
			contentType: "application/json",
			body: `[
				{"id":"xyz","name":"Books","mediaType":"book","folders":[]},
				{"id":"qqq","name":"Other","mediaType":"podcast","folders":[{"fullPath":"/data/pod"}]}
			]`,
			wantLen:     2,
			wantFirstID: "xyz",
		},
		{
			name:        "empty wrapped",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"libraries":[]}`,
			wantLen:     0,
		},
		{
			name:        "empty bare array",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `[]`,
			wantLen:     0,
		},
		{
			name:        "leading whitespace then bare array",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        "\n\r\t  [{\"id\":\"ws\",\"name\":\"Books\",\"mediaType\":\"book\"}]",
			wantLen:     1,
			wantFirstID: "ws",
		},
		{
			name:        "empty body errors cleanly",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        "",
			wantErr:     "empty body",
		},
		{
			name:        "whitespace-only body errors cleanly",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        "   \n\t  ",
			wantErr:     "empty body",
		},
		{
			name:        "non-2xx surfaces status and body",
			status:      http.StatusUnauthorized,
			contentType: "application/json",
			body:        `{"error":"invalid token"}`,
			wantErr:     "401",
		},
		{
			name:        "malformed JSON in object shape",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"libraries":[{`,
			wantErr:     "parse abs libraries",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuthHeader string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/libraries" {
					t.Errorf("unexpected path: %q", r.URL.Path)
				}
				gotAuthHeader = r.Header.Get("Authorization")
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			libs, err := ListLibraries(context.Background(), srv.URL, "tok-XYZ")

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q; got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := len(libs); got != tc.wantLen {
				t.Fatalf("want %d libs, got %d", tc.wantLen, got)
			}
			if tc.wantFirstID != "" && libs[0].ID != tc.wantFirstID {
				t.Fatalf("want first ID %q, got %q", tc.wantFirstID, libs[0].ID)
			}
			if gotAuthHeader != "Bearer tok-XYZ" {
				t.Fatalf("auth header = %q, want %q", gotAuthHeader, "Bearer tok-XYZ")
			}
		})
	}
}
