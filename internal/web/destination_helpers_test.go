package web

import (
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestDestinationTypeLabel(t *testing.T) {
	tests := []struct {
		typ  database.LibraryDestinationType
		want string
	}{
		{database.LibraryDestinationTypePlex, "Plex"},
		{database.LibraryDestinationTypeEmby, "Emby"},
		{database.LibraryDestinationTypeJellyfin, "Jellyfin"},
		{database.LibraryDestinationTypeABS, "Audiobookshelf"},
		{database.LibraryDestinationType("unknown"), "unknown"},
	}

	for _, tt := range tests {
		got := destinationTypeLabel(tt.typ)
		if got != tt.want {
			t.Fatalf("destinationTypeLabel(%q) = %q, want %q", tt.typ, got, tt.want)
		}
	}
}
