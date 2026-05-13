package organizer

import "testing"



func TestSanitizePath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain name", "Craig Alanson", "Craig Alanson"},
		{"trailing dot", "St. John Jr.", "St. John Jr"},
		{"trailing dots", "Someone...", "Someone"},
		{"trailing space and dot", "Someone . ", "Someone"},
		{"smart quotes", "John \u2018Jack\u2019 Smith", "John 'Jack' Smith"},
		{"em dash", "Author \u2014 Name", "Author - Name"},
		{"unicode double quotes", "\u201CHello\u201D", "Hello"},
		{"non-breaking space", "Author\u00A0Name", "Author Name"},
		{"colons stripped", "Title: Subtitle", "Title Subtitle"},
		{"all unsafe", `<>:"/\|?*`, "_"},
		{"empty", "", "_"},
		{"only dots", "...", "_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizePath(tt.input)
			if got != tt.want {
				t.Fatalf("sanitizePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRenderNamingTemplateDefaultsMatchLegacy(t *testing.T) {
	o := NewPlexOrganizer(nil, nil, "/library", true, true, true)
	n := buildNamingData("Andy Weir", "B0034PJ05E", "Project Hail Mary", "A Novel", "Series", "1", "B08GB58KD5", "us", "Andy Narrator", "Publisher Inc", "en", "800", "2023-01-01", "2023-06-01", "NotProtected")

	if got, want := o.renderAuthorDirName(n), "Andy Weir"; got != want {
		t.Fatalf("author dir = %q, want %q", got, want)
	}
	// Default book template: {title}{ - series}{ [region]}
	if got, want := o.renderBookDirName(n), "Project Hail Mary - Series [us]"; got != want {
		t.Fatalf("book dir = %q, want %q", got, want)
	}
	// Default filename template: {title}{ - subtitle}{ - series}{ series_position}{ asin}{ [region]}
	if got, want := o.renderFileName(n), "Project Hail Mary - A Novel - Series 1 B08GB58KD5 [us]"; got != want {
		t.Fatalf("filename = %q, want %q", got, want)
	}
}

func TestRenderNamingTemplateCustomLayout(t *testing.T) {
	o := NewPlexOrganizer(nil, nil, "/library", true, true, true)
	o.SetNamingTemplates("{author}", "{series}/{title}", "{asin} - {title}")
	n := buildNamingData("Author", "AUTH123", "Title", "", "Saga", "2", "ASIN123", "", "", "", "", "", "", "", "")

	if got, want := o.renderBookDirName(n), "Saga/Title"; got != want {
		t.Fatalf("custom book dir = %q, want %q", got, want)
	}
	if got, want := o.renderFileName(n), "ASIN123 - Title"; got != want {
		t.Fatalf("custom filename = %q, want %q", got, want)
	}
}

func TestRenderNamingTemplateOptionalSegmentKeepsSpacingOnlyWhenPresent(t *testing.T) {
	o := NewPlexOrganizer(nil, nil, "/library", true, true, true)
	o.SetNamingTemplates("{author}", "{title}", "{title}{ - subtitle}{ [region]}")

	nWith := buildNamingData("Author", "", "Book", "Bonus", "", "", "", "us", "", "", "", "", "", "", "")
	if got, want := o.renderFileName(nWith), "Book - Bonus [us]"; got != want {
		t.Fatalf("filename with optional tokens = %q, want %q", got, want)
	}

	nWithout := buildNamingData("Author", "", "Book", "", "", "", "", "", "", "", "", "", "", "", "")
	if got, want := o.renderFileName(nWithout), "Book"; got != want {
		t.Fatalf("filename without optional tokens = %q, want %q", got, want)
	}
}

func TestRenderNamingTemplateWithCustomConditionals(t *testing.T) {
	o := NewPlexOrganizer(nil, nil, "/library", true, true, true)
	// Test that formatting tokens can be placed in conditionals
	o.SetNamingTemplates("{author}", "{title}{ [region]}", "{title}{ - asin}")
	n := buildNamingData("Author", "", "Book", "", "", "", "ASIN9", "us", "", "", "", "", "", "", "")

	if got, want := o.renderBookDirName(n), "Book [us]"; got != want {
		t.Fatalf("book dir with conditional region = %q, want %q", got, want)
	}
	if got, want := o.renderFileName(n), "Book - ASIN9"; got != want {
		t.Fatalf("filename with conditional asin = %q, want %q", got, want)
	}
}

func TestRenderNamingTemplatePreservesLiteralSpaceAroundToken(t *testing.T) {
	o := NewPlexOrganizer(nil, nil, "/library", true, true, true)
	o.SetNamingTemplates("{author}{ author_asin}", "{title}", "{title}")
	n := buildNamingData("pirateaba", "B07XCYVYMW", "Book", "", "", "", "ASIN9", "us", "", "", "", "", "", "", "")

	if got, want := o.renderAuthorDirName(n), "pirateaba B07XCYVYMW"; got != want {
		t.Fatalf("author dir with spaced token = %q, want %q", got, want)
	}
}

