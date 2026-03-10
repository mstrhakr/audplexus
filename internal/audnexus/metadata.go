package audnexus

import (
	"context"
	"strings"

	"github.com/mstrhakr/audible-plex-downloader/internal/audio"
	"github.com/mstrhakr/audible-plex-downloader/internal/database"
)

// EnrichMetadata fetches Audnexus data and merges with the existing book record.
// Audnexus values are preferred when available; Audible data is used as fallback.
func (c *Client) EnrichMetadata(ctx context.Context, book *database.Book) (*EnrichedBook, error) {
	enriched := &EnrichedBook{Book: book}

	anBook, err := c.GetBook(ctx, book.ASIN)
	if err != nil {
		anLog.Warn().Err(err).Str("asin", book.ASIN).Msg("audnexus book metadata unavailable, using audible data only")
	} else {
		enriched.AudnexusBook = anBook
	}

	if book.AuthorASIN != "" {
		anAuthor, err := c.GetAuthor(ctx, book.AuthorASIN)
		if err != nil {
			anLog.Debug().Err(err).Str("author_asin", book.AuthorASIN).Msg("audnexus author metadata unavailable")
		} else {
			enriched.AudnexusAuthor = anAuthor
		}
	}

	anChapters, err := c.GetChapters(ctx, book.ASIN)
	if err != nil {
		anLog.Debug().Err(err).Str("asin", book.ASIN).Msg("audnexus chapter data unavailable")
	} else {
		enriched.AudnexusChapters = anChapters
	}

	return enriched, nil
}

// EnrichedBook holds merged metadata from Audible (DB) and Audnexus.
type EnrichedBook struct {
	Book             *database.Book
	AudnexusBook     *BookResponse
	AudnexusAuthor   *AuthorResponse
	AudnexusChapters *ChapterResponse
}

// Title returns the best available title.
func (e *EnrichedBook) Title() string {
	if e.AudnexusBook != nil && e.AudnexusBook.Title != "" {
		return e.AudnexusBook.Title
	}
	return e.Book.Title
}

// Region returns the region code (e.g., "us", "uk").
func (e *EnrichedBook) Region() string {
	if e.AudnexusBook != nil && e.AudnexusBook.Region != "" {
		return e.AudnexusBook.Region
	}
	return ""
}

// Author returns the best available author name.
func (e *EnrichedBook) Author() string {
	if e.AudnexusBook != nil && len(e.AudnexusBook.Authors) > 0 {
		names := make([]string, len(e.AudnexusBook.Authors))
		for i, a := range e.AudnexusBook.Authors {
			names[i] = a.Name
		}
		return strings.Join(names, ", ")
	}
	return e.Book.Author
}

// Narrator returns the best available narrator name.
func (e *EnrichedBook) Narrator() string {
	if e.AudnexusBook != nil && len(e.AudnexusBook.Narrators) > 0 {
		names := make([]string, len(e.AudnexusBook.Narrators))
		for i, n := range e.AudnexusBook.Narrators {
			names[i] = n.Name
		}
		return strings.Join(names, ", ")
	}
	return e.Book.Narrator
}

// Description returns the best available description.
func (e *EnrichedBook) Description() string {
	if e.AudnexusBook != nil && e.AudnexusBook.Summary != "" {
		return e.AudnexusBook.Summary
	}
	if e.AudnexusBook != nil && e.AudnexusBook.Description != "" {
		return e.AudnexusBook.Description
	}
	return e.Book.Description
}

// Genre returns genres from Audnexus, or empty string.
func (e *EnrichedBook) Genre() string {
	if e.AudnexusBook == nil {
		return ""
	}
	var genres []string
	for _, g := range e.AudnexusBook.Genres {
		if g.Type == "genre" {
			genres = append(genres, g.Name)
		}
	}
	return strings.Join(genres, ", ")
}

// CoverURL returns the best available cover image URL.
func (e *EnrichedBook) CoverURL() string {
	if e.AudnexusBook != nil && e.AudnexusBook.Image != "" {
		return e.AudnexusBook.Image
	}
	return e.Book.CoverURL
}

// Series returns the series name.
func (e *EnrichedBook) Series() string {
	if e.AudnexusBook != nil && len(e.AudnexusBook.Series) > 0 {
		return e.AudnexusBook.Series[0].Name
	}
	return e.Book.Series
}

// SeriesPosition returns the position in series.
func (e *EnrichedBook) SeriesPosition() string {
	if e.AudnexusBook != nil && len(e.AudnexusBook.Series) > 0 {
		return e.AudnexusBook.Series[0].Position
	}
	return e.Book.SeriesPosition
}

// ChapterMarks converts Audnexus chapters to audio.ChapterMark slices.
// Returns nil if no chapter data is available.
func (e *EnrichedBook) ChapterMarks() []audio.ChapterMark {
	if e.AudnexusChapters == nil || len(e.AudnexusChapters.Chapters) == 0 {
		return nil
	}

	marks := make([]audio.ChapterMark, len(e.AudnexusChapters.Chapters))
	for i, ch := range e.AudnexusChapters.Chapters {
		endMs := ch.StartOffsetMs + ch.LengthMs
		marks[i] = audio.ChapterMark{
			Title:   ch.Title,
			StartMs: ch.StartOffsetMs,
			EndMs:   endMs,
		}
	}
	return marks
}

// Writer returns the full cast and writer information if available.
func (e *EnrichedBook) Writer() string {
	// Build a combined cast field from narrators and authors if we have Audnexus data
	var parts []string

	if e.AudnexusBook != nil {
		// Add narrators/readers
		if len(e.AudnexusBook.Narrators) > 0 {
			names := make([]string, len(e.AudnexusBook.Narrators))
			for i, n := range e.AudnexusBook.Narrators {
				names[i] = n.Name
			}
			parts = append(parts, "Read by "+strings.Join(names, ", "))
		}

		// Add authors (writers)
		if len(e.AudnexusBook.Authors) > 0 {
			names := make([]string, len(e.AudnexusBook.Authors))
			for i, a := range e.AudnexusBook.Authors {
				names[i] = a.Name
			}
			parts = append(parts, "Written by "+strings.Join(names, ", "))
		}
	}

	return strings.Join(parts, " | ")
}

// Publisher returns the publisher name.
func (e *EnrichedBook) Publisher() string {
	if e.AudnexusBook != nil && e.AudnexusBook.Publisher != "" {
		return e.AudnexusBook.Publisher
	}
	return e.Book.Publisher
}

// Copyright returns copyright information.
func (e *EnrichedBook) Copyright() string {
	if e.AudnexusBook != nil && e.AudnexusBook.ReleaseDate != "" {
		// Extract year from release date
		if len(e.AudnexusBook.ReleaseDate) >= 4 {
			year := e.AudnexusBook.ReleaseDate[:4]
			// Format as "© YYYY Publisher"
			pub := e.Publisher()
			if pub != "" {
				return "© " + year + " " + pub
			}
			return "© " + year
		}
	}
	if !e.Book.ReleaseDate.IsZero() {
		year := e.Book.ReleaseDate.Format("2006")
		pub := e.Publisher()
		if pub != "" {
			return "© " + year + " " + pub
		}
		return "© " + year
	}
	return ""
}

// Language returns the language code (e.g., "en", "fr").
func (e *EnrichedBook) Language() string {
	if e.AudnexusBook != nil && e.AudnexusBook.Language != "" {
		return e.AudnexusBook.Language
	}
	return e.Book.Language
}

// ToAudioMetadata builds an audio.Metadata struct from the merged data.
func (e *EnrichedBook) ToAudioMetadata() audio.Metadata {
	album := e.Title()
	if s := e.Series(); s != "" {
		album = s
	}

	year := ""
	if e.AudnexusBook != nil && e.AudnexusBook.ReleaseDate != "" {
		if len(e.AudnexusBook.ReleaseDate) >= 4 {
			year = e.AudnexusBook.ReleaseDate[:4]
		}
	} else if !e.Book.ReleaseDate.IsZero() {
		year = e.Book.ReleaseDate.Format("2006")
	}

	return audio.Metadata{
		Title:       e.Title(),
		Author:      e.Author(),
		Narrator:    e.Narrator(),
		Writer:      e.Writer(),
		Publisher:   e.Publisher(),
		Copyright:   e.Copyright(),
		Language:    e.Language(),
		Album:       album,
		AlbumArtist: e.Author(),
		Genre:       e.Genre(),
		Year:        year,
		Comment:     e.Description(),
		Track:       e.SeriesPosition(),
	}
}
