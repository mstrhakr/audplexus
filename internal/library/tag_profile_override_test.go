package library

import (
	"context"
	"errors"
	"testing"

	"github.com/mstrhakr/audplexus/internal/audio"
	"github.com/mstrhakr/audplexus/internal/database"
)

type fakeDestinationLister struct {
	rows []database.LibraryDestination
	err  error
}

func (f *fakeDestinationLister) ListEnabledLibraryDestinations(ctx context.Context) ([]database.LibraryDestination, error) {
	return f.rows, f.err
}

func TestResolveTagProfileForDownload(t *testing.T) {
	cases := []struct {
		name     string
		resolved audio.TagProfile
		rows     []database.LibraryDestination
		err      error
		want     audio.TagProfile
	}{
		{
			name:     "no destinations, basic stays basic",
			resolved: audio.TagProfileBasic,
			rows:     nil,
			want:     audio.TagProfileBasic,
		},
		{
			name:     "plex only, basic stays basic",
			resolved: audio.TagProfileBasic,
			rows: []database.LibraryDestination{
				{ID: "p1", Type: database.LibraryDestinationTypePlex},
			},
			want: audio.TagProfileBasic,
		},
		{
			name:     "abs present, basic overrides to rich",
			resolved: audio.TagProfileBasic,
			rows: []database.LibraryDestination{
				{ID: "a1", Type: database.LibraryDestinationTypeABS},
			},
			want: audio.TagProfileAudiobookRich,
		},
		{
			name:     "mixed plex + abs, basic overrides to rich",
			resolved: audio.TagProfileBasic,
			rows: []database.LibraryDestination{
				{ID: "p1", Type: database.LibraryDestinationTypePlex},
				{ID: "a1", Type: database.LibraryDestinationTypeABS},
			},
			want: audio.TagProfileAudiobookRich,
		},
		{
			name:     "already rich, no DB call needed",
			resolved: audio.TagProfileAudiobookRich,
			rows:     nil,
			want:     audio.TagProfileAudiobookRich,
		},
		{
			name:     "DB error falls back to resolved",
			resolved: audio.TagProfileBasic,
			err:      errors.New("db down"),
			want:     audio.TagProfileBasic,
		},
		{
			name:     "DB error with already-rich still returns rich",
			resolved: audio.TagProfileAudiobookRich,
			err:      errors.New("db down"),
			want:     audio.TagProfileAudiobookRich,
		},
		{
			// The DB layer filters disabled rows in
			// ListEnabledLibraryDestinations (see
			// TestListEnabledLibraryDestinationsFiltersDisabled). This
			// case documents the assumption: when the lister returns no
			// rows because all ABS destinations are disabled, the
			// override correctly leaves the profile alone.
			name:     "all abs disabled at DB layer leaves profile alone",
			resolved: audio.TagProfileBasic,
			rows:     []database.LibraryDestination{}, // lister filtered disabled abs row(s) out
			want:     audio.TagProfileBasic,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lister := &fakeDestinationLister{rows: tc.rows, err: tc.err}
			got := resolveTagProfileForDownload(context.Background(), lister, tc.resolved)
			if got != tc.want {
				t.Fatalf("resolveTagProfileForDownload(resolved=%q) = %q, want %q",
					tc.resolved, got, tc.want)
			}
		})
	}
}
