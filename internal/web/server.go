package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/render"
	"github.com/mstrhakr/audible-plex-downloader/internal/audnexus"
	"github.com/mstrhakr/audible-plex-downloader/internal/database"
	"github.com/mstrhakr/audible-plex-downloader/internal/library"
	"github.com/mstrhakr/audible-plex-downloader/internal/logging"
	"github.com/mstrhakr/audible-plex-downloader/internal/organizer"
	audible "github.com/mstrhakr/go-audible"
)

var webLog = logging.Component("web")

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server is the web UI HTTP server.
type Server struct {
	router         *gin.Engine
	db             database.Database
	sync           *library.SyncService
	downloads      *library.DownloadManager
	audnexus       *audnexus.Client
	organizer      *organizer.PlexOrganizer
	audible        *audible.Client
	credPath       string
	port           int
	audiobooksPath string
	downloadsPath  string
	configPath     string
}

// NewServer creates a new web server with all handlers registered.
func NewServer(
	db database.Database,
	syncSvc *library.SyncService,
	dlMgr *library.DownloadManager,
	anClient *audnexus.Client,
	org *organizer.PlexOrganizer,
	audibleClient *audible.Client,
	credPath string,
	port int,
	audiobooksPath string,
	downloadsPath string,
	configPath string,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(ginLogger(), gin.Recovery())

	s := &Server{
		router:         router,
		db:             db,
		sync:           syncSvc,
		downloads:      dlMgr,
		audnexus:       anClient,
		organizer:      org,
		audible:        audibleClient,
		credPath:       credPath,
		port:           port,
		audiobooksPath: audiobooksPath,
		downloadsPath:  downloadsPath,
		configPath:     configPath,
	}

	s.setupTemplates()
	s.setupRoutes()

	return s
}

// multiRender implements gin's HTMLRender interface with per-page template sets
// so each page gets its own "content" definition (Go templates only keep the last
// {{define "content"}} when all files are parsed into one set).
type multiRender struct {
	templates map[string]*template.Template
}

func (r *multiRender) Instance(name string, data any) render.Render {
	t, ok := r.templates[name]
	if !ok {
		return render.HTML{Template: template.Must(template.New("error").Parse("template not found: " + name)), Data: data}
	}
	// Full-page templates that include "base"
	if t.Lookup("base") != nil {
		return render.HTML{Template: t, Name: "base", Data: data}
	}
	// Partial/fragment templates (e.g. library_table.html, settings_saved.html)
	return render.HTML{Template: t, Name: name, Data: data}
}

func (s *Server) setupTemplates() {
	funcMap := template.FuncMap{
		"formatDuration": func(seconds int64) string {
			h := seconds / 3600
			m := (seconds % 3600) / 60
			if h > 0 {
				return fmt.Sprintf("%dh %dm", h, m)
			}
			return fmt.Sprintf("%dm", m)
		},
		"formatDate": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("Jan 2, 2006")
		},
		"statusBadge": func(status database.BookStatus) string {
			switch status {
			case database.BookStatusComplete:
				return "badge-success"
			case database.BookStatusFailed:
				return "badge-error"
			case database.BookStatusDownloading, database.BookStatusDecrypting, database.BookStatusProcessing:
				return "badge-warning"
			case database.BookStatusQueued:
				return "badge-info"
			default:
				return "badge-neutral"
			}
		},
		"mul": func(a float64, b float64) float64 {
			return a * b
		},
		"deref": func(t *time.Time) time.Time {
			if t == nil {
				return time.Time{}
			}
			return *t
		},
	}

	// Parse the base layout once as a clonable template
	base := template.Must(template.New("base").Funcs(funcMap).ParseFS(templateFS, "templates/base.html"))

	// Parse all partial/fragment templates that may be referenced by page templates
	partials := []string{"templates/library_table.html", "templates/settings_saved.html", "templates/sync_status.html", "templates/dashboard_summary.html", "templates/dashboard_downloads.html"}
	baseWithPartials := template.Must(template.Must(base.Clone()).ParseFS(templateFS, partials...))

	r := &multiRender{templates: make(map[string]*template.Template)}

	// For each page template, clone base+partials and parse the page on top so
	// each gets its own isolated "content" definition.
	pages, _ := fs.Glob(templateFS, "templates/*.html")
	for _, page := range pages {
		name := page[len("templates/"):] // strip "templates/" prefix
		if name == "base.html" {
			continue
		}
		// Skip partials — they're already included in every page set
		isPartial := false
		for _, p := range partials {
			if page == p {
				isPartial = true
				break
			}
		}
		if isPartial {
			continue
		}
		t, err := template.Must(baseWithPartials.Clone()).ParseFS(templateFS, page)
		if err != nil {
			panic("parse template " + name + ": " + err.Error())
		}
		r.templates[name] = t
	}

	// Also register partials standalone for HTMX fragment responses
	for _, p := range partials {
		name := p[len("templates/"):]
		t := template.Must(template.New(name).Funcs(funcMap).ParseFS(templateFS, p))
		r.templates[name] = t
	}

	s.router.HTMLRender = r
}

func (s *Server) setupRoutes() {
	// Serve static files
	staticSub, _ := fs.Sub(staticFS, "static")
	s.router.StaticFS("/static", http.FS(staticSub))

	// Pages
	s.router.GET("/", s.handleDashboard)
	s.router.GET("/library", s.handleLibrary)
	s.router.GET("/library/:id", s.handleBookDetail)
	s.router.GET("/downloads", s.handleDownloads)
	s.router.GET("/settings", s.handleSettings)

	// Auth
	s.router.GET("/auth", s.handleAuth)
	s.router.POST("/auth/start", s.handleAuthStart)
	s.router.POST("/auth/callback", s.handleAuthCallback)
	s.router.GET("/auth/status", s.handleAuthStatus)
	s.router.POST("/auth/plex/start", s.handlePlexStart)
	s.router.POST("/auth/plex/complete", s.handlePlexComplete)
	s.router.POST("/auth/plex/select", s.handlePlexSelect)
	s.router.POST("/auth/plex/section", s.handlePlexSectionSelect)
	s.router.POST("/auth/plex/scan", s.handlePlexScan)
	s.router.POST("/auth/plex/check", s.handlePlexCheck)

	// API / HTMX endpoints
	api := s.router.Group("/api")
	{
		api.POST("/sync", s.handleSyncTrigger)
		api.GET("/sync/status", s.handleSyncStatus)
		api.GET("/dashboard/summary", s.handleDashboardSummary)
		api.GET("/dashboard/downloads", s.handleDashboardDownloads)
		api.POST("/downloads/queue-all", s.handleQueueAll)
		api.POST("/downloads/queue/:asin", s.handleQueueBook)
		api.POST("/downloads/cancel/:id", s.handleCancelDownload)
		api.POST("/downloads/retry/:id", s.handleRetryDownload)
		api.POST("/downloads/retry-all", s.handleRetryAllDownloads)
		api.POST("/downloads/pause", s.handlePauseDownloads)
		api.POST("/downloads/resume", s.handleResumeDownloads)
		api.GET("/downloads/state", s.handleDownloadsState)
		api.GET("/events", s.handleSSE)
		api.POST("/settings", s.handleSaveSettings)
	}
}

// Start runs the HTTP server (blocking).
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	webLog.Info().Str("addr", addr).Msg("starting web server")
	return s.router.Run(addr)
}

// ginLogger returns a gin middleware that logs via our logging package.
func ginLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)

		webLog.Debug().
			Int("status", c.Writer.Status()).
			Str("method", c.Request.Method).
			Str("path", c.Request.URL.Path).
			Dur("latency", latency).
			Msg("request")
	}
}

// handleDashboard renders the main dashboard page.
func (s *Server) handleDashboard(c *gin.Context) {
	ctx := c.Request.Context()
	c.HTML(http.StatusOK, "dashboard.html", s.getDashboardData(ctx))
}

func (s *Server) getDashboardData(ctx context.Context) gin.H {
	_, totalBooks, _ := s.db.ListBooks(ctx, database.BookFilter{Limit: 1})
	completeStatus := database.BookStatusComplete
	_, completeBooks, _ := s.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 1})
	newStatus := database.BookStatusNew
	_, newBooks, _ := s.db.ListBooks(ctx, database.BookFilter{Status: &newStatus, Limit: 1})

	activeStatus := database.DownloadStatusActive
	activeDownloads, _ := s.db.ListDownloads(ctx, &activeStatus)
	pendingStatus := database.DownloadStatusPending
	pendingDownloads, _ := s.db.ListDownloads(ctx, &pendingStatus)

	failedStatus := database.DownloadStatusFailed
	failedDownloads, _ := s.db.ListDownloads(ctx, &failedStatus)
	failedRecent := failedDownloads
	if len(failedRecent) > 10 {
		failedRecent = failedRecent[:10]
	}
	downloadCompleteStatus := database.DownloadStatusComplete
	completeDownloads, _ := s.db.ListDownloads(ctx, &downloadCompleteStatus)
	if len(completeDownloads) > 10 {
		completeDownloads = completeDownloads[:10]
	}

	rowsForTitles := make([]database.DownloadQueue, 0, len(failedRecent)+len(completeDownloads))
	rowsForTitles = append(rowsForTitles, failedRecent...)
	rowsForTitles = append(rowsForTitles, completeDownloads...)
	downloadTitles := s.getDownloadTitles(ctx, rowsForTitles)

	lastSync, _ := s.db.GetLastSync(ctx)

	plexURL, plexToken := s.getPlexSettings(ctx)
	plexSectionID, _ := s.db.GetSetting(ctx, "plex_section_id")
	plexSectionTitle, _ := s.db.GetSetting(ctx, "plex_section_title")

	plexConfigured := strings.TrimSpace(plexURL) != "" && strings.TrimSpace(plexToken) != ""
	plexSectionConfigured := strings.TrimSpace(plexSectionID) != ""

	plexLibraryItems := 0
	plexLibraryItemsAvailable := false
	if plexConfigured && plexSectionConfigured {
		plexCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		items, err := s.plexSectionItemCount(plexCtx, plexURL, plexToken, strings.TrimSpace(plexSectionID))
		if err != nil {
			webLog.Debug().Err(err).Msg("failed to fetch Plex section item count for dashboard")
		} else {
			plexLibraryItems = items
			plexLibraryItemsAvailable = true
		}
	}

	return gin.H{
		"TotalBooks":      totalBooks,
		"CompleteBooks":   completeBooks,
		"NewBooks":        newBooks,
		"ActiveDL":        len(activeDownloads),
		"PendingDL":       len(pendingDownloads),
		"FailedDL":        len(failedDownloads),
		"FailedDownloads": failedRecent,
		"DoneDownloads":   completeDownloads,
		"DownloadTitles":  downloadTitles,
		"LastSync":        lastSync,
		"PlexConfigured":  plexConfigured,
		"PlexSection":     strings.TrimSpace(plexSectionTitle),
		"PlexItems":       plexLibraryItems,
		"PlexItemsSet":    plexLibraryItemsAvailable,
		"Page":            "dashboard",
	}
}

func (s *Server) getDownloadTitles(ctx context.Context, rows []database.DownloadQueue) map[string]string {
	titles := make(map[string]string)
	for _, row := range rows {
		if _, exists := titles[row.ASIN]; exists {
			continue
		}
		book, err := s.db.GetBookByASIN(ctx, row.ASIN)
		if err != nil || book == nil || book.Title == "" {
			continue
		}
		titles[row.ASIN] = book.Title
	}
	return titles
}

// handleDashboardSummary renders only the dashboard summary block for HTMX polling.
func (s *Server) handleDashboardSummary(c *gin.Context) {
	ctx := c.Request.Context()
	c.HTML(http.StatusOK, "dashboard_summary.html", s.getDashboardData(ctx))
}

// handleDashboardDownloads renders dashboard done/failed download tables for HTMX polling.
func (s *Server) handleDashboardDownloads(c *gin.Context) {
	ctx := c.Request.Context()
	c.HTML(http.StatusOK, "dashboard_downloads.html", s.getDashboardData(ctx))
}

// handleLibrary renders the library page with search/filter support.
func (s *Server) handleLibrary(c *gin.Context) {
	ctx := c.Request.Context()

	filter := database.BookFilter{
		Search:  c.Query("search"),
		SortBy:  c.DefaultQuery("sort", "purchase_date"),
		SortDir: c.DefaultQuery("dir", "desc"),
		Limit:   50,
	}

	if statusStr := c.Query("status"); statusStr != "" {
		status := database.BookStatus(statusStr)
		filter.Status = &status
	}

	books, total, err := s.db.ListBooks(ctx, filter)
	if err != nil {
		webLog.Error().Err(err).Msg("failed to list books")
		c.HTML(http.StatusInternalServerError, "library.html", gin.H{"Error": "Failed to load library"})
		return
	}

	data := gin.H{
		"Books":  books,
		"Total":  total,
		"Filter": filter,
		"Page":   "library",
	}

	// For HTMX partial requests, render only the table body
	if c.GetHeader("HX-Request") == "true" {
		c.HTML(http.StatusOK, "library_table.html", data)
		return
	}
	c.HTML(http.StatusOK, "library.html", data)
}

// handleBookDetail renders the detail page for a single book.
func (s *Server) handleBookDetail(c *gin.Context) {
	ctx := c.Request.Context()

	var id int64
	if _, err := fmt.Sscanf(c.Param("id"), "%d", &id); err != nil {
		c.HTML(http.StatusBadRequest, "dashboard.html", gin.H{"Error": "Invalid book ID"})
		return
	}

	book, err := s.db.GetBook(ctx, id)
	if err != nil || book == nil {
		c.HTML(http.StatusNotFound, "dashboard.html", gin.H{"Error": "Book not found"})
		return
	}

	c.HTML(http.StatusOK, "book_detail.html", gin.H{
		"Book": book,
		"Page": "library",
	})
}

// handleDownloads renders the download queue page.
func (s *Server) handleDownloads(c *gin.Context) {
	ctx := c.Request.Context()

	activeStatus := database.DownloadStatusActive
	active, _ := s.db.ListDownloads(ctx, &activeStatus)
	pendingStatus := database.DownloadStatusPending
	pending, _ := s.db.ListDownloads(ctx, &pendingStatus)
	completeStatus := database.DownloadStatusComplete
	complete, _ := s.db.ListDownloads(ctx, &completeStatus)
	failedStatus := database.DownloadStatusFailed
	failed, _ := s.db.ListDownloads(ctx, &failedStatus)
	queueState := s.downloads.QueueState()

	c.HTML(http.StatusOK, "downloads.html", gin.H{
		"Active":           active,
		"Pending":          pending,
		"Complete":         complete,
		"Failed":           failed,
		"QueuePaused":      queueState.Paused,
		"QueuePauseReason": queueState.Reason,
		"QueuePausedAt":    queueState.PausedAt,
		"Page":             "downloads",
	})
}

// handleSettings renders the settings page.
func (s *Server) handleSettings(c *gin.Context) {
	ctx := c.Request.Context()

	syncSchedule, _ := s.db.GetSetting(ctx, "sync_schedule")
	outputFormat, _ := s.db.GetSetting(ctx, "output_format")

	devices, _ := s.db.ListDevices(ctx)

	c.HTML(http.StatusOK, "settings.html", gin.H{
		"SyncSchedule":   syncSchedule,
		"OutputFormat":   outputFormat,
		"Devices":        devices,
		"Page":           "settings",
		"AudiobooksPath": s.audiobooksPath,
		"DownloadsPath":  s.downloadsPath,
		"ConfigPath":     s.configPath,
	})
}

// handleSyncTrigger triggers a manual library sync.
func (s *Server) handleSyncTrigger(c *gin.Context) {
	if !s.audible.IsAuthenticated() {
		msg := "Not authenticated — please sign in on the Auth page first."
		if c.GetHeader("HX-Request") == "true" {
			c.HTML(http.StatusOK, "sync_status.html", gin.H{"Message": msg, "Status": "failed"})
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": msg})
		return
	}

	progress := s.sync.GetProgress()
	if progress.Running {
		if c.GetHeader("HX-Request") == "true" {
			c.HTML(http.StatusOK, "sync_status.html", gin.H{
				"Running":      true,
				"BooksFound":   progress.BooksFound,
				"BooksScanned": progress.BooksScanned,
				"BooksAdded":   progress.BooksAdded,
				"Percent":      progress.Percent(),
				"Message":      "Sync already running",
			})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "sync already running"})
		return
	}

	go func() {
		added, err := s.sync.Sync(context.Background())
		if err != nil {
			if errors.Is(err, library.ErrSyncInProgress) {
				return
			}
			webLog.Error().Err(err).Msg("manual sync failed")
			return
		}
		webLog.Info().Int("added", added).Msg("manual sync complete")
	}()

	if c.GetHeader("HX-Request") == "true" {
		started := s.sync.GetProgress()
		c.HTML(http.StatusOK, "sync_status.html", gin.H{
			"Running":      true,
			"BooksFound":   started.BooksFound,
			"BooksScanned": started.BooksScanned,
			"BooksAdded":   started.BooksAdded,
			"Percent":      started.Percent(),
			"Message":      "Sync started",
		})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "started"})
}

// handleSyncStatus renders sync progress for HTMX polling.
func (s *Server) handleSyncStatus(c *gin.Context) {
	progress := s.sync.GetProgress()

	if progress.Running {
		c.HTML(http.StatusOK, "sync_status.html", gin.H{
			"Running":      true,
			"BooksFound":   progress.BooksFound,
			"BooksScanned": progress.BooksScanned,
			"BooksAdded":   progress.BooksAdded,
			"Percent":      progress.Percent(),
			"Message":      "Sync in progress",
		})
		return
	}

	if progress.Status == "failed" {
		c.HTML(http.StatusOK, "sync_status.html", gin.H{
			"Status":  "failed",
			"Message": "Sync failed: " + progress.Error,
		})
		return
	}

	if progress.Status == "complete" {
		msg := fmt.Sprintf("Sync complete! %d new books added.", progress.BooksAdded)
		c.HTML(http.StatusOK, "sync_status.html", gin.H{
			"Status":  "complete",
			"Message": msg,
		})
		return
	}

	c.HTML(http.StatusOK, "sync_status.html", gin.H{})
}

// handleQueueAll queues all new books for download.
func (s *Server) handleQueueAll(c *gin.Context) {
	ctx := c.Request.Context()

	count, err := s.downloads.QueueNewBooks(ctx)
	if err != nil {
		webLog.Error().Err(err).Msg("failed to queue new books")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"queued": count})
}

// handleQueueBook queues a single book for download by ASIN.
func (s *Server) handleQueueBook(c *gin.Context) {
	ctx := c.Request.Context()
	asin := c.Param("asin")

	book, err := s.db.GetBookByASIN(ctx, asin)
	if err != nil || book == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "book not found"})
		return
	}

	didQueue, err := s.downloads.QueueBook(ctx, book.ID, book.ASIN, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !didQueue {
		c.JSON(http.StatusOK, gin.H{"status": "skipped", "asin": asin, "reason": "already_exists"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "queued", "asin": asin})
}

// handleCancelDownload cancels a queued download.
func (s *Server) handleCancelDownload(c *gin.Context) {
	ctx := c.Request.Context()

	var id int64
	if _, err := fmt.Sscanf(c.Param("id"), "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := s.db.CancelDownload(ctx, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
}

// handleRetryDownload resets a failed download back to pending.
func (s *Server) handleRetryDownload(c *gin.Context) {
	ctx := c.Request.Context()

	var id int64
	if _, err := fmt.Sscanf(c.Param("id"), "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := s.db.RetryDownload(ctx, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "retrying"})
}

// handleRetryAllDownloads resets all failed downloads back to pending.
func (s *Server) handleRetryAllDownloads(c *gin.Context) {
	ctx := c.Request.Context()

	count, err := s.db.RetryAllDownloads(ctx)
	if err != nil {
		webLog.Error().Err(err).Msg("failed to retry all downloads")
		if c.GetHeader("HX-Request") == "true" {
			c.HTML(http.StatusOK, "sync_status.html", gin.H{"Message": "Retry failed: " + err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	msg := fmt.Sprintf("%d failed downloads re-queued.", count)
	if c.GetHeader("HX-Request") == "true" {
		c.HTML(http.StatusOK, "sync_status.html", gin.H{"Message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"retried": count})
}

// handlePauseDownloads pauses queue workers from claiming new jobs.
func (s *Server) handlePauseDownloads(c *gin.Context) {
	changed := s.downloads.Pause("paused manually from web UI")
	c.JSON(http.StatusOK, gin.H{"paused": true, "changed": changed})
}

// handleResumeDownloads resumes queue workers.
func (s *Server) handleResumeDownloads(c *gin.Context) {
	changed := s.downloads.Resume()
	c.JSON(http.StatusOK, gin.H{"paused": false, "changed": changed})
}

// handleDownloadsState returns queue pause/resume state for polling or UI actions.
func (s *Server) handleDownloadsState(c *gin.Context) {
	state := s.downloads.QueueState()
	c.JSON(http.StatusOK, state)
}

// handleSSE streams pipeline events via Server-Sent Events.
func (s *Server) handleSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	subID, events := s.downloads.Subscribe()
	defer s.downloads.Unsubscribe(subID)

	ctx := c.Request.Context()
	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case evt, ok := <-events:
			if !ok {
				return false
			}
			c.SSEvent("pipeline", evt)
			return true
		}
	})
}

// handleSaveSettings saves settings from the settings form.
func (s *Server) handleSaveSettings(c *gin.Context) {
	ctx := c.Request.Context()

	if schedule := c.PostForm("sync_schedule"); schedule != "" {
		_ = s.db.SetSetting(ctx, "sync_schedule", schedule)
	}
	if format := c.PostForm("output_format"); format != "" {
		_ = s.db.SetSetting(ctx, "output_format", format)
	}

	if c.GetHeader("HX-Request") == "true" {
		c.HTML(http.StatusOK, "settings_saved.html", gin.H{"Message": "Settings saved"})
		return
	}
	c.Redirect(http.StatusSeeOther, "/settings")
}

// handleAuth renders the authentication page.
func (s *Server) handleAuth(c *gin.Context) {
	s.renderAuthPage(c, http.StatusOK, nil)
}

func (s *Server) authBaseData(ctx context.Context) gin.H {
	plexURL, _ := s.db.GetSetting(ctx, "plex_url")
	plexToken, _ := s.db.GetSetting(ctx, "plex_token")
	plexSectionID, _ := s.db.GetSetting(ctx, "plex_section_id")
	plexSectionTitle, _ := s.db.GetSetting(ctx, "plex_section_title")

	plexConfigured := plexURL != "" && plexToken != ""
	plexSectionConfigured := strings.TrimSpace(plexSectionID) != ""

	data := gin.H{
		"Page":                  "auth",
		"Authenticated":         s.audible.IsAuthenticated(),
		"PlexURL":               plexURL,
		"PlexTokenSet":          plexToken != "",
		"PlexConfigured":        plexConfigured,
		"PlexSectionID":         plexSectionID,
		"PlexSectionTitle":      plexSectionTitle,
		"PlexSectionConfigured": plexSectionConfigured,
	}

	if plexConfigured {
		sections, err := s.plexListSections(ctx, plexURL, plexToken)
		if err == nil && len(sections) > 0 {
			data["PlexSections"] = sections
		}
	}

	return data
}

func (s *Server) renderAuthPage(c *gin.Context, status int, extra gin.H) {
	data := s.authBaseData(c.Request.Context())
	for k, v := range extra {
		data[k] = v
	}
	c.HTML(status, "auth.html", data)
}

// handleAuthStart generates an OAuth URL and shows it to the user.
func (s *Server) handleAuthStart(c *gin.Context) {
	authURL, err := s.audible.GetAuthURL()
	if err != nil {
		webLog.Error().Err(err).Msg("failed to generate auth URL")
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{
			"Error": "Failed to generate login URL: " + err.Error(),
		})
		return
	}

	webLog.Info().Msg("auth URL generated")

	s.renderAuthPage(c, http.StatusOK, gin.H{
		"AuthURL":      authURL.URL,
		"CodeVerifier": authURL.CodeVerifier,
		"DeviceSerial": authURL.DeviceSerial,
	})
}

// handleAuthCallback receives the authorization code (via GET redirect from Amazon
// or POST form) and completes authentication.
func (s *Server) handleAuthCallback(c *gin.Context) {
	// Extract authorization code: try query param first (GET redirect), then form (POST fallback)
	code := c.Query("openid.oa2.authorization_code")
	if code == "" {
		// Legacy POST flow: user pasted a full redirect URL
		if redirectURL := c.PostForm("redirect_url"); redirectURL != "" {
			var err error
			code, err = audible.HandleAuthRedirect(redirectURL)
			if err != nil {
				webLog.Error().Err(err).Msg("failed to parse redirect URL")
				s.renderAuthPage(c, http.StatusBadRequest, gin.H{
					"Error": "Invalid redirect URL: " + err.Error(),
				})
				return
			}
		}
	}

	if code == "" {
		s.renderAuthPage(c, http.StatusBadRequest, gin.H{
			"Error": "No authorization code received. Please try again.",
		})
		return
	}

	webLog.Info().Msg("authorization code received, registering device")

	// Authenticate (device registration + token exchange)
	ctx := c.Request.Context()
	err := s.audible.Authenticate(ctx, audible.DeviceRegistrationRequest{
		AuthorizationCode: code,
		CodeVerifier:      c.PostForm("code_verifier"),
		DeviceSerial:      c.PostForm("device_serial"),
	})
	if err != nil {
		webLog.Error().Err(err).Msg("authentication failed")
		s.renderAuthPage(c, http.StatusInternalServerError, gin.H{
			"Error": "Authentication failed: " + err.Error(),
		})
		return
	}

	// Save credentials to disk
	creds := s.audible.GetCredentials()
	if creds != nil && s.credPath != "" {
		data, err := json.MarshalIndent(creds, "", "  ")
		if err == nil {
			if err := os.WriteFile(s.credPath, data, 0600); err != nil {
				webLog.Error().Err(err).Msg("failed to save credentials")
			} else {
				webLog.Info().Str("path", s.credPath).Msg("credentials saved")
			}
		}
	}

	webLog.Info().Msg("authentication successful")
	s.renderAuthPage(c, http.StatusOK, gin.H{
		"Authenticated": true,
		"Success":       "Successfully authenticated with Audible!",
	})
}

// handleAuthStatus returns the current auth state (for HTMX polling).
func (s *Server) handleAuthStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"authenticated": s.audible.IsAuthenticated(),
	})
}
