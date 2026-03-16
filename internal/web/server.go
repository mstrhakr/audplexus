package web

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	plexCountCache struct {
		mu        sync.Mutex
		key       string
		count     int
		fetchedAt time.Time
		ok        bool
	}
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

	// Wire up Plex callbacks so sync can query/trigger Plex without importing web.
	syncSvc.SetPlexCallbacks(s.plexQueryForSync, s.plexTriggerScanForSync)
	syncSvc.SetPlexReconcileCallback(func(ctx context.Context, progressFn func(current, total int)) error {
		return dlMgr.ReconcilePlexLibrary(ctx, progressFn)
	})

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
	static := s.router.Group("/static")
	static.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=604800")
		c.Next()
	})
	static.StaticFS("/", http.FS(staticSub))

	// Pages
	s.router.GET("/", s.handleDashboard)
	s.router.GET("/library", s.handleLibrary)
	s.router.GET("/library/:id", s.handleBookDetail)
	s.router.GET("/downloads", s.handleDownloads)
	s.router.GET("/settings", s.handleSettings)
	s.router.GET("/diagnostics", s.handleDiagnostics)

	// Auth
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
		api.POST("/sync/quick", s.handleQuickSyncTrigger)
		api.POST("/sync/full", s.handleFullSyncTrigger)
		api.POST("/sync/retry", s.handleSyncRetry)
		api.POST("/sync/phase/:phase", s.handleRunPhase)
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
		api.GET("/pipeline/state", s.handlePipelineState)

		api.GET("/events", s.handleSSE)
		api.POST("/settings", s.handleSaveSettings)
		api.GET("/settings/db-backup", s.handleDBBackup)
		api.POST("/settings/factory-reset", s.handleFactoryReset)

		// Diagnostics
		api.GET("/diagnostics/compare", s.handleDiagnosticsCompare)
		api.GET("/diagnostics/plex-items", s.handleDiagnosticsPlexItems)
		api.POST("/downloads/redownload/:asin", s.handleRedownload)
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
		path := c.Request.URL.Path
		realIP := c.GetHeader("X-Real-IP")
		if realIP == "" {
			realIP = c.GetHeader("X-Forwarded-For")
		}
		if realIP == "" {
			realIP = c.ClientIP()
		}
		realIP = normalizeClientIPForLog(realIP)

		// /api/events is a long-lived SSE stream; duration mostly reflects
		// connection lifetime rather than handler slowness.
		if path == "/api/events" {
			webLog.Trace().
				Int("status", c.Writer.Status()).
				Str("method", c.Request.Method).
				Str("path", path).
				Str("from", realIP).
				Dur("stream_duration", latency).
				Msg("sse stream closed")
			return
		}

		evt := webLog.Debug()
		if latency >= 2*time.Second {
			evt = webLog.Warn()
		}

		evt.
			Int("status", c.Writer.Status()).
			Str("method", c.Request.Method).
			Str("from", realIP).
			Str("path", path).
			Dur("latency", latency).
			// Include proxyied real ip if present, since c.ClientIP() will return the proxy's IP.
			Msg(c.Request.Method + " request from " + realIP + " to " + path)
	}
}

func normalizeClientIPForLog(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ip
	}

	if i := strings.IndexByte(ip, ','); i >= 0 {
		ip = strings.TrimSpace(ip[:i])
	}

	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	} else {
		ip = strings.TrimPrefix(ip, "[")
		ip = strings.TrimSuffix(ip, "]")
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}

	addr = addr.Unmap()
	if addr == netip.IPv6Loopback() {
		return "127.0.0.1"
	}

	return addr.String()
}

// handleDashboard renders the main dashboard page.
func (s *Server) handleDashboard(c *gin.Context) {
	ctx := c.Request.Context()
	c.HTML(http.StatusOK, "dashboard.html", s.getDashboardData(ctx))
}

func (s *Server) getDashboardData(ctx context.Context) gin.H {
	data := s.getDashboardSummaryData(ctx)
	for k, v := range s.getDashboardDownloadsData(ctx) {
		data[k] = v
	}
	data["Page"] = "dashboard"
	return data
}

func (s *Server) getDashboardSummaryData(ctx context.Context) gin.H {
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

	lastSync, _ := s.db.GetLastSync(ctx)

	plexURL, plexToken := s.getPlexSettings(ctx)
	plexSectionID, _ := s.db.GetSetting(ctx, "plex_section_id")
	plexSectionTitle, _ := s.db.GetSetting(ctx, "plex_section_title")

	plexConfigured := strings.TrimSpace(plexURL) != "" && strings.TrimSpace(plexToken) != ""
	plexSectionConfigured := strings.TrimSpace(plexSectionID) != ""

	plexLibraryItems := 0
	plexLibraryItemsAvailable := false
	plexCoverage := 0
	plexCoverageAvailable := false
	if plexConfigured && plexSectionConfigured {
		items, err := s.getCachedPlexSectionItemCount(ctx, plexURL, plexToken, strings.TrimSpace(plexSectionID))
		if err != nil {
			webLog.Debug().Err(err).Msg("failed to fetch Plex section item count for dashboard")
		} else {
			plexLibraryItems = items
			plexLibraryItemsAvailable = true
			if completeBooks > 0 {
				coverage := int(math.Round((float64(plexLibraryItems) / float64(completeBooks)) * 100))
				if coverage < 0 {
					coverage = 0
				}
				plexCoverage = coverage
				plexCoverageAvailable = true
			}
		}
	}

	return gin.H{
		"TotalBooks":      totalBooks,
		"CompleteBooks":   completeBooks,
		"NewBooks":        newBooks,
		"ActiveDL":        len(activeDownloads),
		"PendingDL":       len(pendingDownloads),
		"FailedDL":        len(failedDownloads),
		"LastSync":        lastSync,
		"PlexConfigured":  plexConfigured,
		"PlexSection":     strings.TrimSpace(plexSectionTitle),
		"PlexItems":       plexLibraryItems,
		"PlexItemsSet":    plexLibraryItemsAvailable,
		"PlexCoverage":    plexCoverage,
		"PlexCoverageSet": plexCoverageAvailable,
	}
}

func (s *Server) getDashboardDownloadsData(ctx context.Context) gin.H {
	failedStatus := database.DownloadStatusFailed
	failedDownloads, _ := s.db.ListDownloads(ctx, &failedStatus)
	failedRecent := failedDownloads
	if len(failedRecent) > 10 {
		failedRecent = failedRecent[:10]
	}

	completeStatus := database.DownloadStatusComplete
	completeDownloads, _ := s.db.ListDownloads(ctx, &completeStatus)
	if len(completeDownloads) > 10 {
		completeDownloads = completeDownloads[:10]
	}

	rowsForTitles := make([]database.DownloadQueue, 0, len(failedRecent)+len(completeDownloads))
	rowsForTitles = append(rowsForTitles, failedRecent...)
	rowsForTitles = append(rowsForTitles, completeDownloads...)

	return gin.H{
		"FailedDL":        len(failedDownloads),
		"FailedDownloads": failedRecent,
		"DoneDownloads":   completeDownloads,
		"DownloadTitles":  s.getDownloadTitles(ctx, rowsForTitles),
	}
}

func (s *Server) getCachedPlexSectionItemCount(ctx context.Context, plexURL, plexToken, sectionID string) (int, error) {
	key := plexURL + "|" + sectionID

	s.plexCountCache.mu.Lock()
	if s.plexCountCache.key == key && s.plexCountCache.ok && time.Since(s.plexCountCache.fetchedAt) < 30*time.Second {
		count := s.plexCountCache.count
		s.plexCountCache.mu.Unlock()
		return count, nil
	}
	s.plexCountCache.mu.Unlock()

	plexCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	items, err := s.plexSectionItemCount(plexCtx, plexURL, plexToken, sectionID)
	if err != nil {
		return 0, err
	}

	s.plexCountCache.mu.Lock()
	s.plexCountCache.key = key
	s.plexCountCache.count = items
	s.plexCountCache.fetchedAt = time.Now()
	s.plexCountCache.ok = true
	s.plexCountCache.mu.Unlock()

	return items, nil
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
	c.HTML(http.StatusOK, "dashboard_summary.html", s.getDashboardSummaryData(ctx))
}

// handleDashboardDownloads renders dashboard done/failed download tables for HTMX polling.
func (s *Server) handleDashboardDownloads(c *gin.Context) {
	ctx := c.Request.Context()
	c.HTML(http.StatusOK, "dashboard_downloads.html", s.getDashboardDownloadsData(ctx))
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
		"Page":             "pipeline",
	})
}

// handleSettings renders the settings page.
func (s *Server) handleSettings(c *gin.Context) {
	data := s.settingsPageData(c.Request.Context())
	c.HTML(http.StatusOK, "settings.html", data)
}

func (s *Server) settingsPageData(ctx context.Context) gin.H {
	authData := s.authBaseData(ctx)

	syncSchedule, _ := s.db.GetSetting(ctx, "sync_schedule")
	syncEnabled := s.settingBool(ctx, "sync_enabled", true)
	syncMode := s.settingString(ctx, "sync_mode", "full")
	outputFormat, _ := s.db.GetSetting(ctx, "output_format")
	if outputFormat == "" {
		outputFormat = "m4b"
	}
	plexSectionPath, _ := s.db.GetSetting(ctx, "plex_section_path")

	// Auto-fetch from Plex API if we have a section ID but no saved path.
	if plexSectionPath == "" {
		if sectionID, _ := s.db.GetSetting(ctx, "plex_section_id"); sectionID != "" {
			plexURL, plexToken := s.getPlexSettings(ctx)
			if plexURL != "" && plexToken != "" {
				if fetched, err := s.plexSectionLocation(ctx, plexURL, plexToken, sectionID); err == nil && fetched != "" {
					plexSectionPath = fetched
					_ = s.db.SetSetting(ctx, "plex_section_path", fetched)
				} else if err != nil {
					webLog.Debug().Err(err).Str("section_id", sectionID).Msg("plex section path not available from API")
				}
			}
		}
	}

	embedCover := s.settingBool(ctx, "embed_cover", true)
	chapterFile := s.settingBool(ctx, "chapter_file", true)
	plexMatchFile := s.settingBool(ctx, "plexmatch_file", true)
	downloadConcurrency := s.settingInt(ctx, "download_concurrency", 0)
	decryptConcurrency := s.settingInt(ctx, "decrypt_concurrency", 0)
	processConcurrency := s.settingInt(ctx, "process_concurrency", 0)

	logLevel := logging.GetLevel()

	hostPath := detectHostMountPath(s.audiobooksPath)

	devices, _ := s.db.ListDevices(ctx)

	data := gin.H{
		"SyncSchedule":         syncSchedule,
		"SyncEnabled":          syncEnabled,
		"SyncMode":             syncMode,
		"OutputFormat":         outputFormat,
		"NativeAudiobooksPath": hostPath,
		"PlexSectionPath":      plexSectionPath,
		"EmbedCover":           embedCover,
		"ChapterFile":          chapterFile,
		"PlexMatchFile":        plexMatchFile,
		"DownloadConcurrency":  downloadConcurrency,
		"DecryptConcurrency":   decryptConcurrency,
		"ProcessConcurrency":   processConcurrency,
		"LogLevel":             logLevel,
		"Devices":              devices,
		"Page":                 "settings",
		"AudiobooksPath":       s.audiobooksPath,
		"DownloadsPath":        s.downloadsPath,
		"ConfigPath":           s.configPath,
	}

	for k, v := range authData {
		data[k] = v
	}

	data["Page"] = "settings"
	return data
}

// settingBool reads a boolean setting from DB, returning the given default
// when the key is absent (empty string).
func (s *Server) settingBool(ctx context.Context, key string, defaultVal bool) bool {
	v, _ := s.db.GetSetting(ctx, key)
	if v == "" {
		return defaultVal
	}
	return v == "true" || v == "1"
}

func (s *Server) settingString(ctx context.Context, key, defaultVal string) string {
	v, _ := s.db.GetSetting(ctx, key)
	v = strings.TrimSpace(v)
	if v == "" {
		return defaultVal
	}
	return v
}

func (s *Server) settingInt(ctx context.Context, key string, defaultVal int) int {
	v, _ := s.db.GetSetting(ctx, key)
	v = strings.TrimSpace(v)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

// detectHostMountPath tries to determine the host-side bind mount source for
// a container path by parsing /proc/self/mountinfo. Returns "" when the host
// path cannot be determined (e.g. running natively outside Docker).
func detectHostMountPath(containerPath string) string {
	// If not running in a container, there is no host vs container distinction.
	if _, err := os.Stat("/.dockerenv"); os.IsNotExist(err) {
		return ""
	}

	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	defer f.Close()

	// mountinfo format:  mount_id parent_id major:minor root mount_point ...
	// For bind mounts, "root" (field 3, 0-indexed) is the host path on the
	// underlying filesystem, and "mount_point" (field 4) is the container path.
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mountPoint := fields[4]
		if mountPoint != containerPath {
			continue
		}
		root := fields[3]
		// root="/" is common with ZFS datasets, BTRFS subvolumes, and
		// named Docker volumes where the filesystem root IS the mount.
		// In that case, look for a subvol= option or the mount source
		// after the "-" separator.
		if root != "/" {
			return root
		}

		// Find the "-" separator to reach fs_type and source.
		dashIdx := -1
		for i, f := range fields {
			if f == "-" {
				dashIdx = i
				break
			}
		}
		if dashIdx >= 0 && len(fields) > dashIdx+3 {
			superOpts := fields[dashIdx+3]
			// BTRFS: look for subvol= in super options.
			for _, opt := range strings.Split(superOpts, ",") {
				if strings.HasPrefix(opt, "subvol=") {
					return strings.TrimPrefix(opt, "subvol=")
				}
			}
		}

		// Cannot determine a meaningful host path.
		return ""
	}
	return ""
}

// handleSyncTrigger triggers a full sync (legacy endpoint, backward compatible).
func (s *Server) handleSyncTrigger(c *gin.Context) {
	s.triggerSync(c, library.SyncModeFull)
}

// handleQuickSyncTrigger triggers a quick sync (Audible library update only).
func (s *Server) handleQuickSyncTrigger(c *gin.Context) {
	s.triggerSync(c, library.SyncModeQuick)
}

// handleFullSyncTrigger triggers a full sync (Audible + filesystem + Plex).
func (s *Server) handleFullSyncTrigger(c *gin.Context) {
	s.triggerSync(c, library.SyncModeFull)
}

// handleSyncRetry re-runs the last sync with the same mode.
func (s *Server) handleSyncRetry(c *gin.Context) {
	mode := s.sync.LastMode()
	if mode == "" {
		mode = library.SyncModeFull
	}
	s.triggerSync(c, mode)
}

func (s *Server) triggerSync(c *gin.Context, mode library.SyncMode) {
	if !s.audible.IsAuthenticated() {
		msg := "Not authenticated — please sign in on the Settings page first."
		if c.GetHeader("HX-Request") == "true" {
			c.HTML(http.StatusOK, "sync_status.html", s.syncStatusData(library.SyncProgress{
				Status:  "failed",
				Message: msg,
				Error:   msg,
			}))
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": msg})
		return
	}

	progress := s.sync.GetProgress()
	if progress.Running {
		if c.GetHeader("HX-Request") == "true" {
			c.HTML(http.StatusOK, "sync_status.html", s.syncStatusData(progress))
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "sync already running"})
		return
	}

	go func() {
		var added int
		var err error
		switch mode {
		case library.SyncModeQuick:
			added, err = s.sync.QuickSync(context.Background())
		default:
			added, err = s.sync.FullSync(context.Background())
		}
		if err != nil {
			if errors.Is(err, library.ErrSyncInProgress) {
				return
			}
			webLog.Error().Err(err).Str("mode", string(mode)).Msg("manual sync failed")
			return
		}
		webLog.Info().Int("added", added).Str("mode", string(mode)).Msg("manual sync complete")
	}()

	if c.GetHeader("HX-Request") == "true" {
		started := s.sync.GetProgress()
		c.HTML(http.StatusOK, "sync_status.html", s.syncStatusData(started))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "started", "mode": string(mode)})
}

// syncStatusData converts a SyncProgress into template data.
func (s *Server) syncStatusData(progress library.SyncProgress) gin.H {
	phases := progress.Phases
	if len(phases) == 0 {
		phases = library.DefaultFullPhases()
	}
	data := gin.H{
		"Running":      progress.Running,
		"Mode":         string(progress.Mode),
		"Status":       progress.Status,
		"Message":      progress.Message,
		"Error":        progress.Error,
		"BooksFound":   progress.BooksFound,
		"BooksScanned": progress.BooksScanned,
		"BooksAdded":   progress.BooksAdded,
		"FilesFound":   progress.FilesFound,
		"PlexItems":    progress.PlexItems,
		"PlexScanned":  progress.PlexScanned,
		"Percent":      progress.Percent(),
		"Phases":       phases,
		"CurrentPhase": string(progress.CurrentPhase),
	}
	return data
}

// handleRunPhase triggers a single sync phase.
func (s *Server) handleRunPhase(c *gin.Context) {
	phaseName := c.Param("phase")
	phase := library.SyncPhase(phaseName)

	// Validate phase name
	valid := false
	for _, p := range library.DefaultFullPhases() {
		if p.Name == phase {
			valid = true
			break
		}
	}
	if !valid {
		if c.GetHeader("HX-Request") == "true" {
			c.HTML(http.StatusOK, "sync_status.html", s.syncStatusData(library.SyncProgress{
				Status: "failed", Message: "Unknown phase", Error: "Unknown phase: " + phaseName,
			}))
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid phase"})
		return
	}

	// Audible sync requires authentication
	if phase == library.PhaseAudibleSync && !s.audible.IsAuthenticated() {
		msg := "Not authenticated \u2014 please sign in on the Settings page first."
		if c.GetHeader("HX-Request") == "true" {
			c.HTML(http.StatusOK, "sync_status.html", s.syncStatusData(library.SyncProgress{
				Status: "failed", Message: msg, Error: msg,
			}))
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": msg})
		return
	}

	progress := s.sync.GetProgress()
	if progress.Running {
		if c.GetHeader("HX-Request") == "true" {
			c.HTML(http.StatusOK, "sync_status.html", s.syncStatusData(progress))
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "sync already running"})
		return
	}

	go func() {
		err := s.sync.RunPhase(context.Background(), phase)
		if err != nil {
			if errors.Is(err, library.ErrSyncInProgress) {
				return
			}
			webLog.Error().Err(err).Str("phase", phaseName).Msg("single phase run failed")
			return
		}
		webLog.Info().Str("phase", phaseName).Msg("single phase run complete")
	}()

	if c.GetHeader("HX-Request") == "true" {
		started := s.sync.GetProgress()
		c.HTML(http.StatusOK, "sync_status.html", s.syncStatusData(started))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "started", "phase": phaseName})
}

// handleSyncStatus renders sync progress for HTMX polling.
func (s *Server) handleSyncStatus(c *gin.Context) {
	progress := s.sync.GetProgress()
	c.HTML(http.StatusOK, "sync_status.html", s.syncStatusData(progress))
}

// plexQueryForSync is the callback used by SyncService to query Plex item count.
func (s *Server) plexQueryForSync(ctx context.Context) (int, error) {
	plexURL, plexToken := s.getPlexSettings(ctx)
	if plexURL == "" || plexToken == "" {
		return 0, fmt.Errorf("Plex not configured")
	}
	sectionID, _ := s.db.GetSetting(ctx, "plex_section_id")
	sectionID = strings.TrimSpace(sectionID)
	if sectionID == "" {
		return 0, fmt.Errorf("Plex library section not configured")
	}
	return s.plexSectionItemCount(ctx, plexURL, plexToken, sectionID)
}

// plexTriggerScanForSync is the callback used by SyncService to trigger a full Plex scan.
func (s *Server) plexTriggerScanForSync(ctx context.Context) error {
	plexURL, plexToken := s.getPlexSettings(ctx)
	if plexURL == "" || plexToken == "" {
		return fmt.Errorf("Plex not configured")
	}
	sectionID, _ := s.db.GetSetting(ctx, "plex_section_id")
	sectionID = strings.TrimSpace(sectionID)
	if sectionID == "" {
		return fmt.Errorf("Plex library section not configured")
	}
	// Empty path triggers a full section scan, force=false for routine syncs
	return s.plexTriggerSectionScan(ctx, plexURL, plexToken, sectionID, "", false)
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

// handlePipelineState returns the current worker-pool and waiting-item snapshot.
func (s *Server) handlePipelineState(c *gin.Context) {
	c.JSON(http.StatusOK, s.downloads.PipelineSnapshot(c.Request.Context()))
}

// handleSSE streams pipeline and sync events via Server-Sent Events.
func (s *Server) handleSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	dlID, dlEvents := s.downloads.Subscribe()
	defer s.downloads.Unsubscribe(dlID)

	syncID, syncEvents := s.sync.Subscribe()
	defer s.sync.Unsubscribe(syncID)

	ctx := c.Request.Context()
	poolTicker := time.NewTicker(3 * time.Second)
	defer poolTicker.Stop()

	// Prime the client with a full state snapshot on connect/reconnect.
	c.SSEvent("pipeline", s.downloads.PipelineSnapshot(ctx))

	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case <-poolTicker.C:
			c.SSEvent("pipeline", s.downloads.PipelineSnapshot(ctx))
			return true
		case evt, ok := <-dlEvents:
			if !ok {
				return false
			}
			c.SSEvent("pipeline", evt)
			return true
		case evt, ok := <-syncEvents:
			if !ok {
				return false
			}
			c.SSEvent("sync", evt)
			return true
		}
	})
}

// handleSaveSettings saves settings from the settings form.
func (s *Server) handleSaveSettings(c *gin.Context) {
	ctx := c.Request.Context()

	if _, ok := c.GetPostForm("sync_schedule_sent"); ok {
		schedule := strings.TrimSpace(c.PostForm("sync_schedule"))
		_ = s.db.SetSetting(ctx, "sync_schedule", schedule)
	}
	if _, ok := c.GetPostForm("sync_enabled_sent"); ok {
		enabled := c.PostForm("sync_enabled") == "true"
		_ = s.db.SetSetting(ctx, "sync_enabled", strconv.FormatBool(enabled))
	}
	if mode := strings.TrimSpace(c.PostForm("sync_mode")); mode != "" {
		_ = s.db.SetSetting(ctx, "sync_mode", mode)
	}
	if format := c.PostForm("output_format"); format != "" {
		_ = s.db.SetSetting(ctx, "output_format", format)
	}
	if _, ok := c.GetPostForm("download_concurrency"); ok {
		_ = s.db.SetSetting(ctx, "download_concurrency", strings.TrimSpace(c.PostForm("download_concurrency")))
	}
	if _, ok := c.GetPostForm("decrypt_concurrency"); ok {
		_ = s.db.SetSetting(ctx, "decrypt_concurrency", strings.TrimSpace(c.PostForm("decrypt_concurrency")))
	}
	if _, ok := c.GetPostForm("process_concurrency"); ok {
		_ = s.db.SetSetting(ctx, "process_concurrency", strings.TrimSpace(c.PostForm("process_concurrency")))
	}

	// Boolean toggles: the hidden *_sent field tells us the field was present
	// in the form, so unchecked = false rather than absent.
	if _, ok := c.GetPostForm("embed_cover_sent"); ok {
		v := c.PostForm("embed_cover") == "true"
		_ = s.db.SetSetting(ctx, "embed_cover", fmt.Sprintf("%t", v))
		s.downloads.SetEmbedCover(v)
		s.organizer.SetEmbedCover(v)
	}
	if _, ok := c.GetPostForm("chapter_file_sent"); ok {
		v := c.PostForm("chapter_file") == "true"
		_ = s.db.SetSetting(ctx, "chapter_file", fmt.Sprintf("%t", v))
		s.organizer.SetChapterFile(v)
	}
	if _, ok := c.GetPostForm("plexmatch_file_sent"); ok {
		v := c.PostForm("plexmatch_file") == "true"
		_ = s.db.SetSetting(ctx, "plexmatch_file", fmt.Sprintf("%t", v))
		s.organizer.SetPlexMatchFile(v)
	}

	if level := c.PostForm("log_level"); level != "" {
		_ = s.db.SetSetting(ctx, "log_level", level)
		logging.SetLevel(level)
	}

	if c.GetHeader("HX-Request") == "true" {
		c.HTML(http.StatusOK, "settings_saved.html", gin.H{"Message": "Settings saved"})
		return
	}
	c.Redirect(http.StatusSeeOther, "/settings")
}

// handleDBBackup streams a SQLite database backup as a downloadable file.
// For PostgreSQL this is not supported.
func (s *Server) handleDBBackup(c *gin.Context) {
	dbPath := filepath.Join(s.configPath, "audible.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Database backup is only available for SQLite"})
		return
	}

	ts := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("audible-backup-%s.db", ts)

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("Content-Type", "application/x-sqlite3")
	c.File(dbPath)
}

// handleFactoryReset wipes all data from the database and re-runs migrations.
func (s *Server) handleFactoryReset(c *gin.Context) {
	ctx := c.Request.Context()

	// Fully stop the pipeline — cancel in-flight downloads/decrypts and wait for workers to exit.
	webLog.Warn().Msg("factory reset: stopping all pipeline workers")
	s.downloads.StopAndWait()

	if err := s.db.Reset(ctx); err != nil {
		webLog.Error().Err(err).Msg("factory reset failed")
		s.downloads.Start(context.Background())
		if c.GetHeader("HX-Request") == "true" {
			c.HTML(http.StatusOK, "settings_saved.html", gin.H{"Message": "Reset failed: " + err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Purge the downloads (cache) folder contents.
	s.purgeDirectory(s.downloadsPath)

	// Remove credentials file so the user must re-authenticate.
	if err := os.Remove(s.credPath); err != nil && !os.IsNotExist(err) {
		webLog.Warn().Err(err).Str("path", s.credPath).Msg("factory reset: failed to remove credentials")
	} else {
		webLog.Info().Msg("factory reset: credentials file removed")
	}
	s.audible.SetCredentials(nil)

	// Re-run migrations to ensure schema is intact (idempotent).
	if err := s.db.Migrate(); err != nil {
		webLog.Error().Err(err).Msg("post-reset migration failed")
	}

	// Restart the download pipeline with a fresh context.
	s.downloads.Start(context.Background())
	webLog.Info().Msg("factory reset complete — database wiped, downloads cleared, credentials removed, pipeline restarted")

	if c.GetHeader("HX-Request") == "true" {
		c.Header("HX-Redirect", "/settings")
		c.HTML(http.StatusOK, "settings_saved.html", gin.H{"Message": "Factory reset complete. Redirecting…"})
		return
	}
	c.Redirect(http.StatusSeeOther, "/settings")
}

// purgeDirectory removes all files and subdirectories inside dir, but keeps dir itself.
func (s *Server) purgeDirectory(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		webLog.Warn().Err(err).Str("path", dir).Msg("factory reset: failed to read directory")
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			webLog.Warn().Err(err).Str("path", p).Msg("factory reset: failed to remove")
		}
	}
	webLog.Info().Str("path", dir).Msg("factory reset: directory purged")
}

func (s *Server) authBaseData(ctx context.Context) gin.H {
	plexURL, _ := s.db.GetSetting(ctx, "plex_url")
	plexToken, _ := s.db.GetSetting(ctx, "plex_token")
	plexSectionID, _ := s.db.GetSetting(ctx, "plex_section_id")
	plexSectionTitle, _ := s.db.GetSetting(ctx, "plex_section_title")

	plexConfigured := plexURL != "" && plexToken != ""
	plexSectionConfigured := strings.TrimSpace(plexSectionID) != ""

	data := gin.H{
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
	data := s.settingsPageData(c.Request.Context())
	data["FocusSection"] = "auth"
	for k, v := range extra {
		data[k] = v
	}
	data["Page"] = "settings"
	c.HTML(status, "settings.html", data)
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

// DiagnosticItem represents a book with its status across database, disk, and Plex.
type DiagnosticItem struct {
	ASIN          string `json:"asin"`
	Title         string `json:"title"`
	Author        string `json:"author"`
	Region        string `json:"region,omitempty"`
	Status        string `json:"status"`
	InDatabase    bool   `json:"in_database"`
	OnDisk        bool   `json:"on_disk"`
	InPlex        bool   `json:"in_plex"`
	FilePath      string `json:"file_path,omitempty"`
	FileExists    bool   `json:"file_exists"`
	PlexTitle     string `json:"plex_title,omitempty"` // Matching title found in Plex
	Issue         string `json:"issue,omitempty"`      // Description of what's wrong
	CanRedownload bool   `json:"can_redownload"`
}

// DiagnosticsResponse contains the full comparison data.
type DiagnosticsResponse struct {
	TotalBooks      int              `json:"total_books"`
	CompleteBooks   int              `json:"complete_books"`
	PlexItems       int              `json:"plex_items"`
	FilesOnDisk     int              `json:"files_on_disk"`
	MissingFromPlex int              `json:"missing_from_plex"`
	MissingFromDisk int              `json:"missing_from_disk"`
	RegionIssues    int              `json:"region_issues"`
	UserMarketplace string           `json:"user_marketplace"`
	Items           []DiagnosticItem `json:"items"`
}

// handleDiagnostics renders the diagnostics page.
func (s *Server) handleDiagnostics(c *gin.Context) {
	// Get user's marketplace/region from audible client credentials
	marketplace := "us"
	if creds := s.audible.GetCredentials(); creds != nil && creds.Marketplace != "" {
		marketplace = creds.Marketplace
	}

	c.HTML(http.StatusOK, "diagnostics.html", gin.H{
		"Page":            "diagnostics",
		"UserMarketplace": marketplace,
	})
}

// handleDiagnosticsCompare returns comparison data between database, disk, and Plex.
func (s *Server) handleDiagnosticsCompare(c *gin.Context) {
	ctx := c.Request.Context()

	// Get user's marketplace/region
	marketplace := "us"
	if creds := s.audible.GetCredentials(); creds != nil && creds.Marketplace != "" {
		marketplace = creds.Marketplace
	}

	// Get all books from database
	books, totalCount, err := s.db.ListBooks(ctx, database.BookFilter{Limit: 10000, Offset: 0})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load books: " + err.Error()})
		return
	}

	// Get complete books count
	completeStatus := database.BookStatusComplete
	_, completeCount, _ := s.db.ListBooks(ctx, database.BookFilter{Status: &completeStatus, Limit: 1})

	// Get Plex items if configured
	plexURL, plexToken := s.getPlexSettings(ctx)
	sectionID, _ := s.db.GetSetting(ctx, "plex_section_id")
	sectionID = strings.TrimSpace(sectionID)

	var plexItems []PlexItem
	if plexURL != "" && plexToken != "" && sectionID != "" {
		items, err := s.plexListSectionItems(ctx, plexURL, plexToken, sectionID, 10000)
		if err != nil {
			webLog.Warn().Err(err).Msg("failed to get Plex items for diagnostics")
		} else {
			plexItems = items
		}
	}

	// Build index of Plex items by title (normalized for comparison)
	plexTitleIndex := make(map[string]PlexItem)
	for _, item := range plexItems {
		normalizedTitle := normalizeTitle(item.Title)
		plexTitleIndex[normalizedTitle] = item
	}

	// Process each book to determine its status
	items := make([]DiagnosticItem, 0, len(books))
	missingFromPlex := 0
	missingFromDisk := 0
	regionIssues := 0
	filesOnDisk := 0

	for _, book := range books {
		// Only process complete books for diagnostics
		if book.Status != database.BookStatusComplete {
			continue
		}

		item := DiagnosticItem{
			ASIN:          book.ASIN,
			Title:         book.Title,
			Author:        book.Author,
			Status:        string(book.Status),
			InDatabase:    true,
			FilePath:      book.FilePath,
			CanRedownload: true, // All complete books can be redownloaded
		}

		// Check if file exists on disk
		if book.FilePath != "" {
			if _, err := os.Stat(book.FilePath); err == nil {
				item.OnDisk = true
				item.FileExists = true
				filesOnDisk++

				// Extract region from file path if present
				extractedRegion := extractRegionFromPath(book.FilePath)
				item.Region = extractedRegion

				// Check for region mismatch
				if extractedRegion != "" && extractedRegion != marketplace {
					item.Issue = fmt.Sprintf("Region mismatch: file has [%s] but account is [%s]", extractedRegion, marketplace)
					regionIssues++
				}
			} else {
				item.OnDisk = false
				item.FileExists = false
				item.Issue = "File missing from disk: " + book.FilePath
				missingFromDisk++
			}
		} else {
			item.Issue = "No file path recorded"
			missingFromDisk++
		}

		// Check if in Plex by title matching
		normalizedTitle := normalizeTitle(book.Title)
		if plexItem, found := plexTitleIndex[normalizedTitle]; found {
			item.InPlex = true
			item.PlexTitle = plexItem.Title
		} else {
			// Try fuzzy match - check if our title is contained in any Plex title or vice versa
			authorTitleKey := normalizeTitle(book.Author + " " + book.Title)
			for plexNormTitle, plexItem := range plexTitleIndex {
				if strings.Contains(plexNormTitle, normalizedTitle) ||
					strings.Contains(normalizedTitle, plexNormTitle) ||
					strings.Contains(plexNormTitle, authorTitleKey) {
					item.InPlex = true
					item.PlexTitle = plexItem.Title
					break
				}
			}
		}

		if !item.InPlex && item.OnDisk {
			if item.Issue != "" {
				item.Issue += "; "
			}
			item.Issue += "Not found in Plex library"
			missingFromPlex++
		}

		// Only include items with issues
		if item.Issue != "" || !item.InPlex || !item.OnDisk {
			items = append(items, item)
		}
	}

	response := DiagnosticsResponse{
		TotalBooks:      totalCount,
		CompleteBooks:   completeCount,
		PlexItems:       len(plexItems),
		FilesOnDisk:     filesOnDisk,
		MissingFromPlex: missingFromPlex,
		MissingFromDisk: missingFromDisk,
		RegionIssues:    regionIssues,
		UserMarketplace: marketplace,
		Items:           items,
	}

	c.JSON(http.StatusOK, response)
}

// handleDiagnosticsPlexItems returns all Plex library items.
func (s *Server) handleDiagnosticsPlexItems(c *gin.Context) {
	ctx := c.Request.Context()

	plexURL, plexToken := s.getPlexSettings(ctx)
	sectionID, _ := s.db.GetSetting(ctx, "plex_section_id")
	sectionID = strings.TrimSpace(sectionID)

	if plexURL == "" || plexToken == "" || sectionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Plex is not configured"})
		return
	}

	items, err := s.plexListSectionItems(ctx, plexURL, plexToken, sectionID, 10000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list Plex items: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": items, "count": len(items)})
}

// handleRedownload deletes the old file/folder and re-queues the book for download.
func (s *Server) handleRedownload(c *gin.Context) {
	ctx := c.Request.Context()
	asin := strings.TrimSpace(c.Param("asin"))
	if asin == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing ASIN"})
		return
	}

	// Get the book from database
	book, err := s.db.GetBookByASIN(ctx, asin)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "book not found: " + err.Error()})
		return
	}

	// Delete the old file and folder if they exist
	if book.FilePath != "" {
		// First remove the file itself
		if err := os.Remove(book.FilePath); err != nil && !os.IsNotExist(err) {
			webLog.Warn().Err(err).Str("path", book.FilePath).Msg("failed to remove old file")
		}

		// Then check if the parent folder is empty and remove it
		parentDir := filepath.Dir(book.FilePath)
		if parentDir != "" && parentDir != s.audiobooksPath {
			entries, err := os.ReadDir(parentDir)
			if err == nil && len(entries) == 0 {
				// Directory is empty, remove it
				if err := os.Remove(parentDir); err != nil {
					webLog.Warn().Err(err).Str("path", parentDir).Msg("failed to remove empty directory")
				} else {
					webLog.Info().Str("path", parentDir).Msg("removed empty book directory")
				}
			}
		}
	}

	// Reset book status to "new" so it can be re-downloaded
	book.Status = database.BookStatusNew
	book.FilePath = ""
	book.FileSize = 0
	if err := s.db.UpsertBook(ctx, book); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset book: " + err.Error()})
		return
	}

	// Queue the book for download (high priority for redownloads)
	if _, err := s.downloads.QueueBook(ctx, book.ID, book.ASIN, 100); err != nil {
		webLog.Warn().Err(err).Str("asin", asin).Msg("failed to queue book for redownload")
		// Don't fail - the book is reset and can be queued manually
	}

	webLog.Info().Str("asin", asin).Str("title", book.Title).Msg("book reset for redownload")

	if c.GetHeader("HX-Request") == "true" {
		c.HTML(http.StatusOK, "settings_saved.html", gin.H{
			"Message": fmt.Sprintf("Book '%s' has been queued for redownload", book.Title),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": fmt.Sprintf("Book '%s' has been queued for redownload", book.Title),
	})
}

// normalizeTitle normalizes a title for comparison (removes special chars, lowercase).
func normalizeTitle(title string) string {
	// Convert to lowercase
	title = strings.ToLower(title)

	// Remove common punctuation and normalize whitespace
	replacer := strings.NewReplacer(
		":", "",
		"-", " ",
		"_", " ",
		".", "",
		",", "",
		"'", "",
		"\"", "",
		"(", "",
		")", "",
		"[", "",
		"]", "",
	)
	title = replacer.Replace(title)

	// Collapse multiple spaces to single space
	title = strings.Join(strings.Fields(title), " ")

	return title
}

// extractRegionFromPath extracts the region code from a file path like "[us]" or "[uk]".
func extractRegionFromPath(path string) string {
	// Look for [region] pattern in the path
	start := strings.LastIndex(path, "[")
	end := strings.LastIndex(path, "]")
	if start != -1 && end != -1 && end > start && end-start <= 4 {
		region := path[start+1 : end]
		// Validate it looks like a region code (2-3 lowercase letters)
		region = strings.ToLower(region)
		if len(region) >= 2 && len(region) <= 3 {
			return region
		}
	}
	return ""
}
