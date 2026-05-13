package organizer

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	SettingKeyAuthorDirTemplate = "naming_author_dir_template"
	SettingKeyBookDirTemplate   = "naming_book_dir_template"
	SettingKeyFileNameTemplate  = "naming_file_name_template"

	DefaultAuthorDirTemplate = "{author}"
	DefaultBookDirTemplate   = "{title}{ - series}{ [region]}"
	DefaultFileNameTemplate  = "{title}{ - subtitle}{ - series} {series_position} {asin}{ [region]}"
)

type namingData struct {
	author         string
	authorAsin     string
	title          string
	subtitle       string
	series         string
	seriesPosition string
	asin           string
	region         string
	narrator       string
	publisher      string
	language       string
	duration       string
	releaseDate    string
	purchaseDate   string
	drmType        string
}

func buildNamingData(author, authorAsin, title, subtitle, series, seriesPosition, asin, region, narrator, publisher, language, duration, releaseDate, purchaseDate, drmType string) namingData {
	author = strings.TrimSpace(author)
	authorAsin = strings.TrimSpace(authorAsin)
	title = strings.TrimSpace(title)
	subtitle = strings.TrimSpace(subtitle)
	series = strings.TrimSpace(series)
	seriesPosition = strings.TrimSpace(seriesPosition)
	asin = strings.TrimSpace(asin)
	region = strings.TrimSpace(region)
	narrator = strings.TrimSpace(narrator)
	publisher = strings.TrimSpace(publisher)
	language = strings.TrimSpace(language)
	duration = strings.TrimSpace(duration)
	releaseDate = strings.TrimSpace(releaseDate)
	purchaseDate = strings.TrimSpace(purchaseDate)
	drmType = strings.TrimSpace(drmType)

	if author == "" {
		author = "Unknown Author"
	}
	if title == "" {
		title = "Unknown Title"
	}

	return namingData{
		author:         author,
		authorAsin:     authorAsin,
		title:          title,
		subtitle:       subtitle,
		series:         series,
		seriesPosition: seriesPosition,
		asin:           asin,
		region:         region,
		narrator:       narrator,
		publisher:      publisher,
		language:       language,
		duration:       duration,
		releaseDate:    releaseDate,
		purchaseDate:   purchaseDate,
		drmType:        drmType,
	}
}

func (o *PlexOrganizer) loadNamingTemplatesFromDB(ctx context.Context) {
	if o.db == nil {
		return
	}
	o.SetNamingTemplates(
		readTemplateSetting(ctx, o.db.GetSetting, SettingKeyAuthorDirTemplate, DefaultAuthorDirTemplate),
		readTemplateSetting(ctx, o.db.GetSetting, SettingKeyBookDirTemplate, DefaultBookDirTemplate),
		readTemplateSetting(ctx, o.db.GetSetting, SettingKeyFileNameTemplate, DefaultFileNameTemplate),
	)
}

func readTemplateSetting(ctx context.Context, getter func(context.Context, string) (string, error), key, fallback string) string {
	v, err := getter(ctx, key)
	if err != nil {
		return fallback
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

func normalizeTemplate(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

func (o *PlexOrganizer) SetNamingTemplates(authorDir, bookDir, fileName string) {
	o.mu.Lock()
	o.authorDirTpl = normalizeTemplate(authorDir, DefaultAuthorDirTemplate)
	o.bookDirTpl = normalizeTemplate(bookDir, DefaultBookDirTemplate)
	o.fileNameTpl = normalizeTemplate(fileName, DefaultFileNameTemplate)
	o.mu.Unlock()
}

func (o *PlexOrganizer) NamingTemplates() (authorDir, bookDir, fileName string) {
	o.mu.RLock()
	authorDir = o.authorDirTpl
	bookDir = o.bookDirTpl
	fileName = o.fileNameTpl
	o.mu.RUnlock()
	return
}

func (o *PlexOrganizer) ExpectedSingleFilePath(bookDir, ext string, n namingData) string {
	if strings.TrimSpace(ext) == "" {
		ext = ".m4b"
	}
	return filepath.Join(bookDir, o.renderFileName(n)+ext)
}

func (o *PlexOrganizer) ExpectedBookDir(n namingData) string {
	return filepath.Join(o.libraryRoot, o.renderAuthorDirName(n), o.renderBookDirName(n))
}

func (o *PlexOrganizer) renderAuthorDirName(n namingData) string {
	return sanitizeStructuredPath(renderNamingTemplate(o.currentAuthorDirTpl(), n))
}

func (o *PlexOrganizer) renderBookDirName(n namingData) string {
	return sanitizeStructuredPath(renderNamingTemplate(o.currentBookDirTpl(), n))
}

func (o *PlexOrganizer) renderFileName(n namingData) string {
	return sanitizePath(renderNamingTemplate(o.currentFileNameTpl(), n))
}

func (o *PlexOrganizer) currentAuthorDirTpl() string {
	o.mu.RLock()
	v := o.authorDirTpl
	o.mu.RUnlock()
	return normalizeTemplate(v, DefaultAuthorDirTemplate)
}

func (o *PlexOrganizer) currentBookDirTpl() string {
	o.mu.RLock()
	v := o.bookDirTpl
	o.mu.RUnlock()
	return normalizeTemplate(v, DefaultBookDirTemplate)
}

func (o *PlexOrganizer) currentFileNameTpl() string {
	o.mu.RLock()
	v := o.fileNameTpl
	o.mu.RUnlock()
	return normalizeTemplate(v, DefaultFileNameTemplate)
}

func renderNamingTemplate(tpl string, n namingData) string {
	values := map[string]string{
		"author":          n.author,
		"author_asin":     n.authorAsin,
		"title":           n.title,
		"subtitle":        n.subtitle,
		"series":          n.series,
		"series_position": n.seriesPosition,
		"asin":            n.asin,
		"region":          n.region,
		"narrator":        n.narrator,
		"publisher":       n.publisher,
		"language":        n.language,
		"duration":        n.duration,
		"release_date":    n.releaseDate,
		"purchase_date":   n.purchaseDate,
		"drm_type":        n.drmType,
	}

	// Curly-brace segments are conditional blocks: if a segment references one
	// or more known tokens and any of those tokens are empty, the whole segment
	// is omitted. This matches Sonarr-style "literal + token" behavior.
	out := segmentRE.ReplaceAllStringFunc(tpl, func(raw string) string {
		inner := raw[1 : len(raw)-1]
		return evalSegment(inner, values)
	})
	return strings.TrimSpace(out)
}

var segmentRE = regexp.MustCompile(`\{[^{}]*\}`)
var tokenWordRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

func evalSegment(inner string, values map[string]string) string {
	trimmed := strings.TrimSpace(inner)
	if inner == trimmed {
		if v, ok := values[trimmed]; ok {
			return v
		}
	}

	known := make(map[string]struct{})
	for _, word := range tokenWordRE.FindAllString(inner, -1) {
		if _, ok := values[word]; ok {
			known[word] = struct{}{}
		}
	}
	if len(known) == 0 {
		return inner
	}

	names := make([]string, 0, len(known))
	for name := range known {
		if strings.TrimSpace(values[name]) == "" {
			return ""
		}
		names = append(names, name)
	}

	// Replace longer token names first to avoid partial overlap collisions
	// (for example series vs series_position).
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	out := inner
	for _, name := range names {
		out = regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).ReplaceAllString(out, values[name])
	}
	return out
}

func sanitizeStructuredPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "_"
	}
	parts := strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	if len(parts) == 0 {
		return "_"
	}
	safe := make([]string, 0, len(parts))
	for _, p := range parts {
		p = sanitizePath(p)
		if strings.TrimSpace(p) == "" {
			continue
		}
		safe = append(safe, p)
	}
	if len(safe) == 0 {
		return "_"
	}
	return filepath.Join(safe...)
}
