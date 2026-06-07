package audio

import (
	"context"
	"testing"
)

func TestParseTagProfileDefaults(t *testing.T) {
	cases := map[string]TagProfile{
		"":                 DefaultTagProfile,
		"basic":            TagProfileBasic,
		"BASIC":            TagProfileBasic,
		"audiobook-rich":   TagProfileAudiobookRich,
		"AUDIOBOOK-RICH":   TagProfileAudiobookRich,
		" audiobook-rich ": TagProfileAudiobookRich,
		"audiobook_rich":   TagProfileAudiobookRich,
		"rich":             TagProfileAudiobookRich,
		"unknown":          DefaultTagProfile, // anything unrecognized falls back to default
		"plex":             DefaultTagProfile, // doesn't accidentally match a media-server type
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := ParseTagProfile(input); got != want {
				t.Errorf("ParseTagProfile(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestResolveTagProfileNilDB(t *testing.T) {
	// A nil DB must not panic; falls back to the default profile.
	if got := ResolveTagProfile(context.Background(), nil); got != DefaultTagProfile {
		t.Errorf("ResolveTagProfile(nil db) = %q, want %q", got, DefaultTagProfile)
	}
}

type fakeSettingsReader struct {
	value string
	err   error
}

func (f *fakeSettingsReader) GetSetting(ctx context.Context, key string) (string, error) {
	if key != SettingKeyTagProfile {
		// Only the tag-profile key is read by ResolveTagProfile — assert that.
		panic("unexpected key: " + key)
	}
	return f.value, f.err
}

func TestResolveTagProfileFromDB(t *testing.T) {
	for _, tc := range []struct {
		stored string
		want   TagProfile
	}{
		{"", DefaultTagProfile},                     // unset = default profile
		{"basic", TagProfileBasic},                  // explicit basic
		{"audiobook-rich", TagProfileAudiobookRich}, // explicit rich
		{"junk", DefaultTagProfile},                 // unknown stored value falls back to default
	} {
		t.Run(tc.stored, func(t *testing.T) {
			db := &fakeSettingsReader{value: tc.stored}
			if got := ResolveTagProfile(context.Background(), db); got != tc.want {
				t.Errorf("ResolveTagProfile(stored=%q) = %q, want %q", tc.stored, got, tc.want)
			}
		})
	}
}

func TestAllTagProfilesIncludesBothInOrder(t *testing.T) {
	all := AllTagProfiles()
	if len(all) != 2 {
		t.Fatalf("AllTagProfiles() = %d entries, want 2", len(all))
	}
	if all[0] != TagProfileBasic {
		t.Errorf("AllTagProfiles()[0] = %q, want Basic first", all[0])
	}
	if all[1] != TagProfileAudiobookRich {
		t.Errorf("AllTagProfiles()[1] = %q, want AudiobookRich second", all[1])
	}
}

func TestProfileLabelAndDescriptionPopulated(t *testing.T) {
	for _, p := range AllTagProfiles() {
		if p.Label() == "" {
			t.Errorf("profile %q has empty Label()", p)
		}
		if p.Description() == "" {
			t.Errorf("profile %q has empty Description()", p)
		}
	}
}
