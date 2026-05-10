package audio

import (
	"strings"
	"testing"
)

func TestBuildMetadataArgsBasicProfileDoesNotEmitSeriesAtoms(t *testing.T) {
	meta := Metadata{
		Title:      "Foundation",
		Author:     "Isaac Asimov",
		Album:      "Foundation",
		Series:     "Foundation",
		SeriesPart: "1",
		ASIN:       "B0XYZTEST",
		Profile:    TagProfileBasic,
	}
	args := buildMetadataArgs(meta)
	joined := strings.Join(args, " ")

	mustContain := []string{"title=Foundation", "artist=Isaac Asimov", "album=Foundation", "media_type=2"}
	for _, want := range mustContain {
		if !strings.Contains(joined, want) {
			t.Errorf("buildMetadataArgs missing %q in output:\n%s", want, joined)
		}
	}

	mustNotContain := []string{"series=", "series-part=", "asin="}
	for _, banned := range mustNotContain {
		if strings.Contains(joined, banned) {
			t.Errorf("buildMetadataArgs(Basic profile) emitted %q — series atoms must only appear under audiobook-rich:\n%s", banned, joined)
		}
	}
}

func TestBuildMetadataArgsAudiobookRichEmitsSeriesAtoms(t *testing.T) {
	meta := Metadata{
		Title:      "Foundation",
		Author:     "Isaac Asimov",
		Album:      "Foundation",
		Series:     "Foundation",
		SeriesPart: "1",
		ASIN:       "B0XYZTEST",
		Profile:    TagProfileAudiobookRich,
	}
	args := buildMetadataArgs(meta)
	joined := strings.Join(args, " ")

	for _, want := range []string{"series=Foundation", "series-part=1", "asin=B0XYZTEST"} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildMetadataArgs(AudiobookRich) missing %q:\n%s", want, joined)
		}
	}

	// Album must STILL be the title — the Plex album-collapse workaround is
	// preserved by deliberately keeping series in its own atom rather than
	// the album field.
	if !strings.Contains(joined, "album=Foundation") {
		t.Errorf("buildMetadataArgs must keep album=Title even under AudiobookRich (Plex album-collapse workaround):\n%s", joined)
	}
	if strings.Contains(joined, "album=Foundation, ") || strings.Contains(joined, "album=Foundation/") {
		t.Errorf("album field should be just the title, not include series:\n%s", joined)
	}
}

func TestBuildMetadataArgsAudiobookRichSkipsEmptyExtraFields(t *testing.T) {
	// Standalone book — no series, no ASIN. AudiobookRich profile should not
	// emit empty series/series-part/asin atoms.
	meta := Metadata{
		Title:   "Standalone Book",
		Author:  "Anon",
		Album:   "Standalone Book",
		Profile: TagProfileAudiobookRich,
	}
	args := buildMetadataArgs(meta)
	joined := strings.Join(args, " ")

	for _, banned := range []string{"series=", "series-part=", "asin="} {
		if strings.Contains(joined, banned) {
			t.Errorf("buildMetadataArgs emitted empty %q for standalone book — should skip:\n%s", banned, joined)
		}
	}
}

func TestEmbedArgsAudiobookRichSetsMovFlagsUseMetadataTags(t *testing.T) {
	// Critical empirical detail: ffmpeg silently drops unknown -metadata
	// keys (series, series-part, asin) on mp4 output unless
	// `-movflags use_metadata_tags` is set. Without this flag, AudiobookRich
	// would appear to succeed but no series atoms would be in the file.
	// Verified against ffmpeg 6.1.1 — this test guards against accidental
	// removal of the flag.
	args := embedArgs("/in.m4b", "/out.m4b", Metadata{
		Title:      "Foundation",
		Series:     "Foundation",
		SeriesPart: "1",
		Profile:    TagProfileAudiobookRich,
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-movflags use_metadata_tags") {
		t.Errorf("AudiobookRich profile must include `-movflags use_metadata_tags` so series atoms aren't silently dropped:\n%s", joined)
	}
}

func TestEmbedArgsBasicDoesNotSetMovFlags(t *testing.T) {
	// Basic profile preserves today's exact behavior — no muxer flag
	// changes. Existing libraries upgrade with zero diff.
	args := embedArgs("/in.m4b", "/out.m4b", Metadata{
		Title:   "Foundation",
		Profile: TagProfileBasic,
	})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-movflags") {
		t.Errorf("Basic profile must not set -movflags (preserves historical behavior):\n%s", joined)
	}
}

func TestBuildMetadataArgsZeroProfileDefaultsToBasic(t *testing.T) {
	// A zero-value Metadata has Profile == "" (TagProfile zero value).
	// That should behave as Basic — no series atoms emitted.
	meta := Metadata{
		Title:      "Foundation",
		Series:     "Foundation",
		SeriesPart: "1",
		ASIN:       "B0",
	}
	args := buildMetadataArgs(meta)
	joined := strings.Join(args, " ")
	for _, banned := range []string{"series=", "series-part=", "asin="} {
		if strings.Contains(joined, banned) {
			t.Errorf("zero-value Profile must not emit %q (must behave as Basic):\n%s", banned, joined)
		}
	}
}
