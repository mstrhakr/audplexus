package organizer

import "testing"

func TestBuildFilenameBase(t *testing.T) {
	tests := []struct {
		name           string
		title          string
		subtitle       string
		series         string
		seriesPosition string
		asin           string
		region         string
		want           string
	}{
		{
			name:           "title series asin and region",
			title:          "Harry Potter and the Goblet of Fire",
			series:         "Harry Potter",
			seriesPosition: "4",
			asin:           "B017V4IM1G",
			region:         "us",
			want:           "Harry Potter and the Goblet of Fire - Harry Potter 4 B017V4IM1G [us]",
		},
		{
			name:     "title with meaningful subtitle",
			title:    "The Expanse",
			subtitle: "A Telltale Series",
			asin:     "B08G9PRS1K",
			want:     "The Expanse: A Telltale Series B08G9PRS1K",
		},
		{
			name:  "title and asin without series or region",
			title: "Leviathan Wakes",
			asin:  "B073H9PF2D",
			want:  "Leviathan Wakes B073H9PF2D",
		},
		{
			name:   "title with series but no asin",
			title:  "Project Hail Mary",
			series: "Standalone",
			want:   "Project Hail Mary - Standalone",
		},
		{
			name:  "unknown title fallback",
			title: "",
			want:  "Unknown Title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFilenameBase(tt.title, tt.subtitle, tt.series, tt.seriesPosition, tt.asin, tt.region)
			if got != tt.want {
				t.Fatalf("buildFilenameBase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildBookDirectoryName(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		asin   string
		region string
		want   string
	}{
		{
			name:   "title with asin and region",
			title:  "Project Hail Mary",
			asin:   "B08GB58KD5",
			region: "us",
			want:   "Project Hail Mary B08GB58KD5 [us]",
		},
		{
			name:  "title with asin no region",
			title: "Project Hail Mary",
			asin:  "B08GB58KD5",
			want:  "Project Hail Mary B08GB58KD5",
		},
		{
			name:  "title only when asin missing",
			title: "Project Hail Mary",
			asin:  "",
			want:  "Project Hail Mary",
		},
		{
			name:  "unknown title fallback",
			title: "",
			asin:  "B08GB58KD5",
			want:  "Unknown Title B08GB58KD5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildBookDirectoryName(tt.title, tt.asin, tt.region)
			if got != tt.want {
				t.Fatalf("buildBookDirectoryName() = %q, want %q", got, tt.want)
			}
		})
	}
}
