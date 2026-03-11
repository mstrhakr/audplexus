package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/mstrhakr/audible-plex-downloader/internal/audio"
	"github.com/mstrhakr/audible-plex-downloader/internal/audnexus"
	"github.com/mstrhakr/audible-plex-downloader/internal/config"
	"github.com/mstrhakr/audible-plex-downloader/internal/database"
	"github.com/mstrhakr/audible-plex-downloader/internal/library"
	"github.com/mstrhakr/audible-plex-downloader/internal/logging"
	"github.com/mstrhakr/audible-plex-downloader/internal/organizer"
	"github.com/mstrhakr/audible-plex-downloader/internal/scheduler"
	"github.com/mstrhakr/audible-plex-downloader/internal/web"
	audible "github.com/mstrhakr/go-audible"
)

var log = logging.Component("main")

func main() {
	// Load configuration
	configPath := filepath.Join(getConfigDir(), "config.yaml")
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: failed to load config: %v\n", err)
		os.Exit(1)
	}
	cfg.LoadFromEnv()

	// Initialize logging (must happen after config is loaded)
	logging.Init(cfg.Log.Level, cfg.Log.JSON)
	log = logging.Component("main")

	log.Info().
		Int("port", cfg.Server.Port).
		Str("db_type", cfg.Database.Type).
		Str("output_format", cfg.Output.Format).
		Str("log_level", cfg.Log.Level).
		Msg("starting audible-plex-downloader")

	// Initialize database
	db, err := initDatabase(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize database")
	}
	defer db.Close()
	log.Debug().Str("type", cfg.Database.Type).Msg("database connection established")

	if err := db.Migrate(); err != nil {
		log.Fatal().Err(err).Msg("failed to run migrations")
	}
	log.Info().Msg("database migrations complete")

	// Apply runtime-configurable settings stored in DB (override config file defaults).
	applyDBSettings(db, cfg)

	// Ensure directories exist
	for _, dir := range []string{cfg.Paths.Audiobooks, cfg.Paths.Downloads, cfg.Paths.Config} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			log.Fatal().Err(err).Str("path", dir).Msg("failed to create directory")
		}
		log.Debug().Str("path", dir).Msg("directory ready")
	}

	// Initialize Audible client - first try to get marketplace from existing credentials
	credPath := filepath.Join(cfg.Paths.Config, "credentials.json")
	marketplace, region := detectMarketplace(credPath)
	audibleClient := audible.NewClient(marketplace)
	if err := loadCredentials(audibleClient, credPath); err != nil {
		log.Warn().Err(err).Msg("no audible credentials loaded — authenticate via the web UI")
	} else {
		// Update marketplace from loaded credentials if available
		if creds := audibleClient.GetCredentials(); creds != nil && creds.Marketplace != "" {
			if mp, ok := audible.GetMarketplace(creds.Marketplace); ok {
				audibleClient.SetMarketplace(mp)
				region = mp.CountryCode
				log.Info().Str("marketplace", creds.Marketplace).Msg("using marketplace from credentials")
			}
		}
		log.Info().Msg("audible credentials loaded")
	}

	// Initialize FFmpeg (auto-downloads if not on system PATH)
	ffmpeg, err := audio.NewFFmpeg(cfg.Paths.Config)
	if err != nil {
		log.Warn().Err(err).Msg("ffmpeg not available — audio processing will fail")
	} else {
		log.Info().Msg("ffmpeg initialized")
	}

	// Initialize services
	syncSvc := library.NewSyncService(db, audibleClient, cfg.Paths.Audiobooks)
	anClient := audnexus.NewClientWithRegion(region)
	log.Info().Str("region", region).Msg("audnexus client initialized with region")
	org := organizer.NewPlexOrganizer(db, ffmpeg, cfg.Paths.Audiobooks, cfg.Output.EmbedCover, cfg.Output.ChapterFile)
	dlMgr := library.NewDownloadManager(
		db,
		audibleClient,
		ffmpeg,
		anClient,
		org,
		cfg.Paths.Audiobooks,
		cfg.Paths.Downloads,
		cfg.Output.Format,
		cfg.Output.EmbedCover,
		cfg.Download.DownloadConcurrency,
		cfg.Download.DecryptConcurrency,
		cfg.Download.ProcessConcurrency,
	)

	// Start download manager
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dlMgr.Start(ctx)
	defer dlMgr.Stop()
	log.Info().Msg("download manager started")

	// Start scheduler
	sched := scheduler.New(syncSvc, dlMgr)
	if cfg.Sync.Mode != "" {
		sched.SetSyncMode(cfg.Sync.Mode)
	}
	if cfg.Sync.Enabled && cfg.Sync.Schedule != "" {
		if err := sched.SetSyncSchedule(cfg.Sync.Schedule); err != nil {
			log.Error().Err(err).Str("schedule", cfg.Sync.Schedule).Msg("invalid sync schedule")
		}
	}
	sched.Start()
	defer sched.Stop()
	log.Info().Bool("enabled", cfg.Sync.Enabled).Str("schedule", cfg.Sync.Schedule).Msg("scheduler started")

	// Start web server
	webServer := web.NewServer(db, syncSvc, dlMgr, anClient, org, audibleClient, credPath, cfg.Server.Port, cfg.Paths.Audiobooks, cfg.Paths.Downloads, cfg.Paths.Config)
	go func() {
		if err := webServer.Start(); err != nil {
			log.Fatal().Err(err).Msg("web server failed")
		}
	}()
	log.Info().Int("port", cfg.Server.Port).Msg("web server started")

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Info().Str("signal", sig.String()).Msg("shutting down")
}

func initDatabase(cfg *config.Config) (database.Database, error) {
	switch cfg.Database.Type {
	case "sqlite":
		dbPath := cfg.Database.Path
		if dbPath == "" {
			dbPath = filepath.Join(cfg.Paths.Config, "audible.db")
		}
		if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
		return database.NewSQLite(dbPath)
	case "postgres":
		if cfg.Database.DSN == "" {
			return nil, fmt.Errorf("postgres DSN is required")
		}
		return database.NewPostgres(cfg.Database.DSN)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Database.Type)
	}
}

func loadCredentials(client *audible.Client, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var creds audible.Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return fmt.Errorf("parse credentials: %w", err)
	}
	client.SetCredentials(&creds)
	return nil
}

func getConfigDir() string {
	if v := os.Getenv("CONFIG_PATH"); v != "" {
		return v
	}
	return "/config"
}

// applyDBSettings reads runtime-configurable values that the user may have
// changed via the web UI and applies them so they survive restarts.
func applyDBSettings(db database.Database, cfg *config.Config) {
	ctx := context.Background()

	// Config file/env provide defaults; persisted DB settings are user-facing
	// runtime preferences that override defaults when present.
	cfg.Output.Format = resolveStringSetting(ctx, db, "output_format", cfg.Output.Format)
	cfg.Output.EmbedCover = resolveBoolSetting(ctx, db, "embed_cover", cfg.Output.EmbedCover)
	cfg.Output.ChapterFile = resolveBoolSetting(ctx, db, "chapter_file", cfg.Output.ChapterFile)

	cfg.Sync.Schedule = resolveStringSetting(ctx, db, "sync_schedule", cfg.Sync.Schedule)
	cfg.Sync.Enabled = resolveBoolSetting(ctx, db, "sync_enabled", cfg.Sync.Enabled)
	cfg.Sync.Mode = resolveStringSetting(ctx, db, "sync_mode", cfg.Sync.Mode)

	cfg.Download.DownloadConcurrency = resolveIntSetting(ctx, db, "download_concurrency", cfg.Download.DownloadConcurrency)
	cfg.Download.DecryptConcurrency = resolveIntSetting(ctx, db, "decrypt_concurrency", cfg.Download.DecryptConcurrency)
	cfg.Download.ProcessConcurrency = resolveIntSetting(ctx, db, "process_concurrency", cfg.Download.ProcessConcurrency)

	cfg.Log.Level = resolveStringSetting(ctx, db, "log_level", cfg.Log.Level)
	logging.SetLevel(cfg.Log.Level)

	// Seed Plex values from config/env once if DB has not been configured yet.
	_ = resolveStringSetting(ctx, db, "plex_url", cfg.Plex.URL)
	_ = resolveStringSetting(ctx, db, "plex_token", cfg.Plex.Token)
}

func resolveStringSetting(ctx context.Context, db database.Database, key, fallback string) string {
	v, _ := db.GetSetting(ctx, key)
	v = trim(v)
	if v != "" {
		return v
	}
	fallback = trim(fallback)
	if fallback != "" {
		_ = db.SetSetting(ctx, key, fallback)
	}
	return fallback
}

func resolveBoolSetting(ctx context.Context, db database.Database, key string, fallback bool) bool {
	v, _ := db.GetSetting(ctx, key)
	v = trim(v)
	if v == "" {
		_ = db.SetSetting(ctx, key, strconv.FormatBool(fallback))
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func resolveIntSetting(ctx context.Context, db database.Database, key string, fallback int) int {
	v, _ := db.GetSetting(ctx, key)
	v = trim(v)
	if v == "" {
		_ = db.SetSetting(ctx, key, strconv.Itoa(fallback))
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func trim(s string) string {
	return strings.TrimSpace(s)
}

// detectMarketplace reads credentials file to get marketplace before client initialization.
// Returns US marketplace as default if credentials cannot be read.
func detectMarketplace(credPath string) (audible.Marketplace, string) {
	data, err := os.ReadFile(credPath)
	if err != nil {
		return audible.MarketplaceUS, "us"
	}

	var creds struct {
		Marketplace string `json:"marketplace"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return audible.MarketplaceUS, "us"
	}

	if creds.Marketplace != "" {
		if mp, ok := audible.GetMarketplace(creds.Marketplace); ok {
			return mp, mp.CountryCode
		}
	}

	return audible.MarketplaceUS, "us"
}
