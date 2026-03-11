package audnexus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mstrhakr/audible-plex-downloader/internal/logging"
)

var anLog = logging.Component("audnexus")

const defaultBaseURL = "https://api.audnex.us"

// Client is an Audnexus API client for enriched audiobook metadata.
type Client struct {
	baseURL    string
	httpClient *http.Client
	region     string // default region code (e.g., "us", "uk", "de")
}

// NewClient creates a new Audnexus client with default US region.
func NewClient() *Client {
	return &Client{
		baseURL: defaultBaseURL,
		region:  "us",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithRegion creates a new Audnexus client with specified region.
func NewClientWithRegion(region string) *Client {
	if region == "" {
		region = "us"
	}
	return &Client{
		baseURL: defaultBaseURL,
		region:  region,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SetRegion updates the default region for subsequent API calls.
func (c *Client) SetRegion(region string) {
	if region != "" {
		c.region = region
	}
}

// Region returns the current default region.
func (c *Client) Region() string {
	return c.region
}

// BookResponse is the Audnexus book metadata response.
type BookResponse struct {
	ASIN            string          `json:"asin"`
	Title           string          `json:"title"`
	Subtitle        string          `json:"subtitle"`
	Authors         []Person        `json:"authors"`
	Narrators       []Person        `json:"narrators"`
	Publisher       string          `json:"publisherName"`
	Summary         string          `json:"summary"`
	Description     string          `json:"description"`
	ReleaseDate     string          `json:"releaseDate"`
	RuntimeMinutes  int             `json:"runtimeLengthMin"`
	Image           string          `json:"image"`
	Genres          []Genre         `json:"genres"`
	Series          FlexibleSeries  `json:"seriesPrimary"`
	Language        string          `json:"language"`
	Rating          FlexibleFloat64 `json:"rating"`
	Region          string          `json:"region"`
	ContentDelivery string          `json:"contentDeliveryType"`
}

// FlexibleFloat64 tolerates APIs that inconsistently return numbers as JSON
// numbers, quoted numbers, null, or empty strings.
type FlexibleFloat64 float64

func (f *FlexibleFloat64) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" || trimmed == `""` {
		*f = 0
		return nil
	}

	var n float64
	if err := json.Unmarshal(data, &n); err == nil {
		*f = FlexibleFloat64(n)
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("decode float or string: %w", err)
	}

	s = strings.TrimSpace(s)
	if s == "" {
		*f = 0
		return nil
	}

	parsed, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("parse numeric string %q: %w", s, err)
	}

	*f = FlexibleFloat64(parsed)
	return nil
}

// FlexibleSeries handles APIs that return either an array of Series or a single Series object.
type FlexibleSeries []Series

func (fs *FlexibleSeries) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*fs = nil
		return nil
	}

	// Try array first
	var arr []Series
	if err := json.Unmarshal(data, &arr); err == nil {
		*fs = arr
		return nil
	}

	// Fall back to single object
	var single Series
	if err := json.Unmarshal(data, &single); err != nil {
		return fmt.Errorf("decode series array or object: %w", err)
	}

	*fs = []Series{single}
	return nil
}

// Person represents an author or narrator.
type Person struct {
	ASIN string `json:"asin"`
	Name string `json:"name"`
}

// Genre represents a genre/tag.
type Genre struct {
	ASIN string `json:"asin"`
	Name string `json:"name"`
	Type string `json:"type"` // "genre" or "tag"
}

// Series represents series information.
type Series struct {
	ASIN     string `json:"asin"`
	Name     string `json:"name"`
	Position string `json:"position"`
}

// AuthorResponse is the Audnexus author metadata response.
type AuthorResponse struct {
	ASIN        string  `json:"asin"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Image       string  `json:"image"`
	Genres      []Genre `json:"genres"`
	Region      string  `json:"region"`
}

// ChapterResponse is the Audnexus chapter metadata response.
type ChapterResponse struct {
	ASIN            string    `json:"asin"`
	BrandIntroDurMs int       `json:"brandIntroDurationMs"`
	BrandOutroDurMs int       `json:"brandOutroDurationMs"`
	Chapters        []Chapter `json:"chapters"`
	RuntimeMs       int       `json:"runtimeLengthMs"`
	Region          string    `json:"region"`
}

// Chapter represents a single chapter.
type Chapter struct {
	Title         string `json:"title"`
	LengthMs      int    `json:"lengthMs"`
	StartOffsetMs int    `json:"startOffsetMs"`
}

// GetBook fetches enriched book metadata from Audnexus.
func (c *Client) GetBook(ctx context.Context, asin string) (*BookResponse, error) {
	anLog.Debug().Str("asin", asin).Msg("fetching book metadata")

	var resp BookResponse
	if err := c.get(ctx, fmt.Sprintf("/books/%s", asin), &resp); err != nil {
		anLog.Warn().Err(err).Str("asin", asin).Msg("failed to fetch book metadata")
		return nil, err
	}

	anLog.Info().Str("asin", asin).Str("title", resp.Title).Msg("fetched book metadata")
	return &resp, nil
}

// GetAuthor fetches author metadata from Audnexus.
func (c *Client) GetAuthor(ctx context.Context, asin string) (*AuthorResponse, error) {
	anLog.Debug().Str("asin", asin).Msg("fetching author metadata")

	var resp AuthorResponse
	if err := c.get(ctx, fmt.Sprintf("/authors/%s", asin), &resp); err != nil {
		anLog.Warn().Err(err).Str("asin", asin).Msg("failed to fetch author metadata")
		return nil, err
	}

	anLog.Info().Str("asin", asin).Str("name", resp.Name).Msg("fetched author metadata")
	return &resp, nil
}

// GetChapters fetches chapter information from Audnexus.
func (c *Client) GetChapters(ctx context.Context, asin string) (*ChapterResponse, error) {
	anLog.Debug().Str("asin", asin).Msg("fetching chapter data")

	var resp ChapterResponse
	if err := c.get(ctx, fmt.Sprintf("/books/%s/chapters", asin), &resp); err != nil {
		anLog.Warn().Err(err).Str("asin", asin).Msg("failed to fetch chapter data")
		return nil, err
	}

	anLog.Info().Str("asin", asin).Int("chapters", len(resp.Chapters)).Msg("fetched chapter data")
	return &resp, nil
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	return c.getWithRegion(ctx, path, c.region, result)
}

func (c *Client) getWithRegion(ctx context.Context, path, region string, result any) error {
	reqURL := c.baseURL + path
	// Add region query parameter if specified
	if region != "" {
		if strings.Contains(path, "?") {
			reqURL += "&region=" + region
		} else {
			reqURL += "?region=" + region
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		anLog.Debug().Str("path", path).Str("region", region).Msg("resource not found on audnexus")
		return fmt.Errorf("not found: %s", path)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
