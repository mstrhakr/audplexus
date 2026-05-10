package audio

import (
	"context"
	"strings"
)

// TagProfile selects which set of metadata atoms get embedded into the m4b
// when EmbedMetadata runs. Existing libraries upgrade transparently to the
// Basic profile (today's behavior). Audiobook-rich is opt-in.
type TagProfile string

const (
	// TagProfileBasic writes the same set of atoms Audplexus has always
	// written: title, artist, album_artist, album, genre, date, comment,
	// composer, copyright, publisher, language, description, track,
	// media_type. No series-specific atoms — preserves the historical
	// album=Title workaround that prevents Plex from collapsing every book
	// in a series into one giant album.
	TagProfileBasic TagProfile = "basic"

	// TagProfileAudiobookRich extends Basic with three freeform iTunes
	// atoms that audiobook-aware servers read for series autodetection:
	//   - series       (book.Series)
	//   - series-part  (book.SeriesPosition)
	//   - asin         (book.ASIN)
	// Audiobookshelf reads these via ffprobe and uses them for series
	// auto-grouping — Audplexus's existing API-call-based collection
	// management becomes belt+suspenders rather than load-bearing.
	// The album field stays at book.Title — the Plex album-collapse
	// workaround is preserved.
	TagProfileAudiobookRich TagProfile = "audiobook-rich"
)

// SettingKeyTagProfile is the DB setting key that stores the active tag
// profile. Stored alongside other Audplexus settings.
const SettingKeyTagProfile = "tag_profile"

// settingsReader is the subset of database.Database that tag-profile
// resolution needs. Defined locally so internal/audio doesn't need to
// import internal/database (which would create a cycle since database
// callers may want to import audio for the type constants).
type settingsReader interface {
	GetSetting(ctx context.Context, key string) (string, error)
}

// ResolveTagProfile reads the active tag profile from the DB setting,
// falling back to Basic for any unset or unknown value. Existing installs
// keep today's behavior automatically.
func ResolveTagProfile(ctx context.Context, db settingsReader) TagProfile {
	if db == nil {
		return TagProfileBasic
	}
	raw, _ := db.GetSetting(ctx, SettingKeyTagProfile)
	return ParseTagProfile(raw)
}

// ParseTagProfile maps a setting string to a TagProfile, defaulting to
// Basic for empty or unrecognized values. Lenient — used when reading
// existing settings where unknown legacy values should fall back gracefully.
func ParseTagProfile(s string) TagProfile {
	p, _ := parseTagProfile(s)
	return p
}

// ParseTagProfileStrict is like ParseTagProfile but rejects unknown values
// instead of silently mapping them to Basic. Used at the form-handler
// boundary where the user typed (or selected) something explicitly and
// silent fallback would hide a typo.
func ParseTagProfileStrict(s string) (TagProfile, bool) {
	return parseTagProfile(s)
}

func parseTagProfile(s string) (TagProfile, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(TagProfileBasic):
		return TagProfileBasic, true
	case string(TagProfileAudiobookRich), "audiobook_rich", "rich":
		return TagProfileAudiobookRich, true
	default:
		return TagProfileBasic, false
	}
}

// AllTagProfiles returns the user-selectable profiles in display order.
// Used by the Settings UI to render the profile dropdown.
func AllTagProfiles() []TagProfile {
	return []TagProfile{TagProfileBasic, TagProfileAudiobookRich}
}

// Label returns a human-readable name for a profile. Used in the UI
// dropdown.
func (p TagProfile) Label() string {
	switch p {
	case TagProfileAudiobookRich:
		return "Audiobook-rich"
	default:
		return "Basic"
	}
}

// Description returns a one-line explainer suitable for an inline help
// note next to the Settings dropdown.
func (p TagProfile) Description() string {
	switch p {
	case TagProfileAudiobookRich:
		return "Adds series, series-part, and asin atoms for audiobook-aware servers (Audiobookshelf reads these for series grouping)."
	default:
		return "Today's tag set. Safe choice for existing libraries — no behavior change."
	}
}
