package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Server.Port != 8080 {
		t.Fatalf("Server.Port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Database.Type != "sqlite" {
		t.Fatalf("Database.Type = %q, want sqlite", cfg.Database.Type)
	}
	if cfg.Paths.Config != "/config" {
		t.Fatalf("Paths.Config = %q, want /config", cfg.Paths.Config)
	}
	if cfg.Output.Format != "m4b" {
		t.Fatalf("Output.Format = %q, want m4b", cfg.Output.Format)
	}
	if !cfg.Sync.Enabled {
		t.Fatalf("Sync.Enabled = false, want true")
	}
	if !cfg.Log.FileEnabled {
		t.Fatalf("Log.FileEnabled = false, want true")
	}
	if cfg.Log.FilePath != "/config/logs/audplexus.log" {
		t.Fatalf("Log.FilePath = %q, want /config/logs/audplexus.log", cfg.Log.FilePath)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	cfg, err := Load(missingPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Fatalf("Server.Port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Database.Path != "/config/audible.db" {
		t.Fatalf("Database.Path = %q, want /config/audible.db", cfg.Database.Path)
	}
}

func TestLoadParsesAndMergesWithDefaults(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")

	data := []byte(`server:
  port: 9090
database:
  type: postgres
  dsn: postgres://user:pw@db:5432/app
log:
  level: debug
  json: true
output:
  format: mp3
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Fatalf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Database.Type != "postgres" {
		t.Fatalf("Database.Type = %q, want postgres", cfg.Database.Type)
	}
	if cfg.Database.DSN == "" {
		t.Fatalf("Database.DSN = empty, want value")
	}
	if cfg.Log.Level != "debug" || !cfg.Log.JSON {
		t.Fatalf("Log = %+v, want level=debug json=true", cfg.Log)
	}
	if cfg.Output.Format != "mp3" {
		t.Fatalf("Output.Format = %q, want mp3", cfg.Output.Format)
	}

	// Unspecified fields should still come from defaults.
	if cfg.Paths.Audiobooks != "/audiobooks" {
		t.Fatalf("Paths.Audiobooks = %q, want /audiobooks", cfg.Paths.Audiobooks)
	}
}

func TestLoadInvalidYAMLReturnsError(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.yaml")
	if err := os.WriteFile(path, []byte("server: ["), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load() error = nil, want parse error")
	}
}

func TestLoadFromEnvOverrides(t *testing.T) {
	cfg := DefaultConfig()

	t.Setenv("PORT", "7070")
	t.Setenv("DATABASE_TYPE", "postgres")
	t.Setenv("DATABASE_PATH", "/tmp/custom.db")
	t.Setenv("DATABASE_DSN", "postgres://db")
	t.Setenv("AUDIOBOOKS_PATH", "/mnt/books")
	t.Setenv("DOWNLOADS_PATH", "/mnt/dl")
	t.Setenv("CONFIG_PATH", "/mnt/cfg")
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("LOG_JSON", "true")
	t.Setenv("LOG_FILE_ENABLED", "false")
	t.Setenv("LOG_FILE_PATH", "/tmp/audplexus.log")
	t.Setenv("LOG_FILE_MAX_SIZE_MB", "42")
	t.Setenv("LOG_FILE_MAX_BACKUPS", "9")
	t.Setenv("LOG_FILE_MAX_AGE_DAYS", "30")
	t.Setenv("LOG_FILE_COMPRESS", "false")
	t.Setenv("OUTPUT_FORMAT", "mp3")
	t.Setenv("DOWNLOAD_DOWNLOAD_CONCURRENCY", "5")
	t.Setenv("DECRYPT_CONCURRENCY", "6")
	t.Setenv("PROCESS_CONCURRENCY", "7")
	t.Setenv("PLEX_URL", "http://plex")
	t.Setenv("PLEX_TOKEN", "token")
	t.Setenv("SYNC_SCHEDULE", "0 * * * *")
	t.Setenv("SYNC_ENABLED", "false")
	t.Setenv("SYNC_MODE", "quick")
	t.Setenv("SYNC_AUTO_QUEUE_NEW", "1")

	cfg.LoadFromEnv()

	if cfg.Server.Port != 7070 {
		t.Fatalf("Server.Port = %d, want 7070", cfg.Server.Port)
	}
	if cfg.Database.Type != "postgres" || cfg.Database.Path != "/tmp/custom.db" || cfg.Database.DSN != "postgres://db" {
		t.Fatalf("Database overrides failed: %+v", cfg.Database)
	}
	if cfg.Paths.Audiobooks != "/mnt/books" || cfg.Paths.Downloads != "/mnt/dl" || cfg.Paths.Config != "/mnt/cfg" {
		t.Fatalf("Paths overrides failed: %+v", cfg.Paths)
	}
	if cfg.Log.Level != "warn" || !cfg.Log.JSON {
		t.Fatalf("Log overrides failed: %+v", cfg.Log)
	}
	if cfg.Log.FileEnabled {
		t.Fatalf("Log.FileEnabled = true, want false")
	}
	if cfg.Log.FilePath != "/tmp/audplexus.log" {
		t.Fatalf("Log.FilePath = %q, want /tmp/audplexus.log", cfg.Log.FilePath)
	}
	if cfg.Log.FileMaxSizeMB != 42 || cfg.Log.FileMaxBackups != 9 || cfg.Log.FileMaxAgeDays != 30 || cfg.Log.FileCompress {
		t.Fatalf("Log file overrides failed: %+v", cfg.Log)
	}
	if cfg.Output.Format != "mp3" {
		t.Fatalf("Output.Format = %q, want mp3", cfg.Output.Format)
	}
	if cfg.Download.DownloadConcurrency != 5 || cfg.Download.DecryptConcurrency != 6 || cfg.Download.ProcessConcurrency != 7 {
		t.Fatalf("Download concurrency overrides failed: %+v", cfg.Download)
	}
	if cfg.Plex.URL != "http://plex" || cfg.Plex.Token != "token" {
		t.Fatalf("Plex overrides failed: %+v", cfg.Plex)
	}
	if cfg.Sync.Schedule != "0 * * * *" || cfg.Sync.Enabled || cfg.Sync.Mode != "quick" || !cfg.Sync.AutoQueueNew {
		t.Fatalf("Sync overrides failed: %+v", cfg.Sync)
	}
}
