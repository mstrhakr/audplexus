package audio

import (
	"fmt"
	"strings"

	"github.com/mstrhakr/audplexus/internal/logging"
)

var tagLog = logging.Component("tagger")

// Metadata represents audio file metadata to embed.
type Metadata struct {
	Title       string
	Author      string
	Narrator    string
	Writer      string // Narrator/writer credit (may be multiple narrators)
	Publisher   string // Publisher name
	Copyright   string // Copyright holder/year
	Language    string // ISO 639-1 language code (e.g., "en", "fr")
	Album       string // Usually same as title
	AlbumArtist string // Usually same as author
	Genre       string
	Year        string
	Comment     string // Description
	Track       string // Series position (numeric, also written as `series-part` in audiobook-rich profile)
	Disc        string
	CoverPath   string // Path to cover image to embed

	// Audiobook-rich-only fields. Populated unconditionally by callers; emitted
	// into the file only when Profile == TagProfileAudiobookRich.
	Series     string // Series name (book.Series)
	SeriesPart string // Series position as a freeform string (book.SeriesPosition)
	ASIN       string // Audible identifier; lets audiobook servers match by ID

	// Profile selects which set of atoms gets emitted. Defaults to
	// TagProfileBasic (the historical Audplexus tag set).
	Profile TagProfile
}

// EmbedMetadata writes metadata tags to an M4B/M4A file using FFmpeg.
func (f *FFmpeg) EmbedMetadata(inputPath, outputPath string, meta Metadata) error {
	tagLog.Info().Str("input", inputPath).Str("title", meta.Title).Str("author", meta.Author).Str("profile", string(meta.Profile)).Msg("embedding metadata")
	return f.run(embedArgs(inputPath, outputPath, meta)...)
}

// embedArgs builds the full ffmpeg arg list for EmbedMetadata. Extracted
// for testability — the actual ffmpeg invocation is in EmbedMetadata.
func embedArgs(inputPath, outputPath string, meta Metadata) []string {
	args := []string{
		"-i", inputPath,
	}

	// Add cover art if provided
	if meta.CoverPath != "" {
		args = append(args,
			"-i", meta.CoverPath,
			"-map", "0:a",
			"-map", "1:v",
			"-disposition:v:0", "attached_pic",
		)
	}

	args = append(args, "-c", "copy")

	// AudiobookRich profile writes freeform tags (series, series-part, asin)
	// into the file. ffmpeg's mp4 muxer drops unknown -metadata keys unless
	// `use_metadata_tags` is set — which switches to the QuickTime `mdta`
	// metadata format. Tested ffmpeg 6.1.1: with this flag the keys round-
	// trip cleanly through ffprobe (which is what Audiobookshelf reads).
	// Without it, the tags vanish silently — verified empirically.
	if meta.Profile == TagProfileAudiobookRich {
		args = append(args, "-movflags", "use_metadata_tags")
	}

	// Build metadata options
	metaArgs := buildMetadataArgs(meta)
	args = append(args, metaArgs...)

	args = append(args, "-y", outputPath)

	return args
}

func buildMetadataArgs(meta Metadata) []string {
	var args []string
	add := func(key, value string) {
		if value != "" {
			args = append(args, "-metadata", fmt.Sprintf("%s=%s", key, value))
		}
	}

	add("title", meta.Title)
	add("artist", meta.Author)
	add("album_artist", meta.AlbumArtist)
	add("album", meta.Album)
	add("genre", meta.Genre)
	add("date", meta.Year)
	add("comment", meta.Comment)
	add("composer", meta.Narrator)   // narrator/reader
	add("copyright", meta.Copyright) // copyright holder/year
	add("publisher", meta.Publisher) // publisher name
	add("language", meta.Language)   // ISO 639-1 language code
	add("description", meta.Writer)  // writer/narrator credit
	add("track", meta.Track)
	add("disc", meta.Disc)

	// Media type = Audiobook (for iTunes/iOS)
	add("media_type", "2")

	// Audiobook-rich profile: emit freeform iTunes atoms for series and ASIN.
	// ffmpeg writes unknown -metadata keys as ----:com.apple.iTunes:<key>
	// freeform atoms in MP4 output, which is exactly what Audiobookshelf
	// reads via ffprobe for series auto-grouping. The album field stays
	// at meta.Album (the title) — the Plex album-collapse workaround is
	// preserved.
	if meta.Profile == TagProfileAudiobookRich {
		add("series", meta.Series)
		add("series-part", meta.SeriesPart)
		add("asin", meta.ASIN)
	}

	return args
}

// EmbedCover adds a cover image to an existing audio file.
func (f *FFmpeg) EmbedCover(inputPath, outputPath, coverPath string) error {
	tagLog.Info().Str("input", inputPath).Str("cover", coverPath).Msg("embedding cover art")
	return f.run(
		"-i", inputPath,
		"-i", coverPath,
		"-map", "0:a",
		"-map", "1:v",
		"-c", "copy",
		"-disposition:v:0", "attached_pic",
		"-y",
		outputPath,
	)
}

// FormatMetadataString creates a display string from metadata.
func FormatMetadataString(meta Metadata) string {
	var parts []string
	if meta.Title != "" {
		parts = append(parts, meta.Title)
	}
	if meta.Author != "" {
		parts = append(parts, "by "+meta.Author)
	}
	if meta.Narrator != "" {
		parts = append(parts, "narrated by "+meta.Narrator)
	}
	return strings.Join(parts, " ")
}

