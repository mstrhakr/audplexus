package database

import (
	"fmt"
	"strings"
	"testing"
)

// TestLibraryDestinationStringerRedactsSecrets defends against accidental
// secret leakage via fmt.Printf("%+v"/"%v"/"%s") on a destination row.
// json:"-" tags only stop json.Marshal — they do nothing for fmt's
// default formatter, which uses reflection over all fields.
func TestLibraryDestinationStringerRedactsSecrets(t *testing.T) {
	d := LibraryDestination{
		ID:            "test-id",
		DisplayName:   "Living Room Plex",
		Type:          LibraryDestinationTypePlex,
		Enabled:       true,
		URL:           "http://plex.lan:32400",
		PlexToken:     "SUPERSECRET_PLEX_TOKEN_DO_NOT_LEAK",
		PlexSectionID: "5",
		APIKey:        "SUPERSECRET_API_KEY_DO_NOT_LEAK",
	}

	for label, formatted := range map[string]string{
		"%v":  fmt.Sprintf("%v", d),
		"%s":  fmt.Sprintf("%s", d),
		"%#v": fmt.Sprintf("%#v", d),
	} {
		if strings.Contains(formatted, "SUPERSECRET_PLEX_TOKEN_DO_NOT_LEAK") {
			t.Errorf("%s leaked PlexToken: %s", label, formatted)
		}
		if strings.Contains(formatted, "SUPERSECRET_API_KEY_DO_NOT_LEAK") {
			t.Errorf("%s leaked APIKey: %s", label, formatted)
		}
		if !strings.Contains(formatted, "<redacted>") {
			t.Errorf("%s should mark secrets as <redacted>; got %s", label, formatted)
		}
	}
}

func TestLibraryDestinationStringerShowsUnsetSecretsAsUnset(t *testing.T) {
	d := LibraryDestination{
		ID: "id", Type: LibraryDestinationTypeABS, URL: "http://abs", LibraryID: "lib",
		// APIKey and PlexToken intentionally empty.
	}
	formatted := fmt.Sprintf("%v", d)
	if !strings.Contains(formatted, "APIKey:<unset>") {
		t.Errorf("expected APIKey:<unset> for empty secret, got: %s", formatted)
	}
	if !strings.Contains(formatted, "PlexToken:<unset>") {
		t.Errorf("expected PlexToken:<unset> for empty secret, got: %s", formatted)
	}
}
