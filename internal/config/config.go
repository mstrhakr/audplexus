package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Paths    PathsConfig    `yaml:"paths"`
	Output   OutputConfig   `yaml:"output"`
	Download DownloadConfig `yaml:"download"`
	Sync     SyncConfig     `yaml:"sync"`
	Plex     PlexConfig     `yaml:"plex"`
	Log      LogConfig      `yaml:"log"`
}

type LogConfig struct {
	Level string `yaml:"level"` // trace, debug, info, warn, error, fatal
	JSON  bool   `yaml:"json"`  // true for structured JSON, false for console
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type DatabaseConfig struct {
	Type string `yaml:"type"` // "sqlite" or "postgres"
	Path string `yaml:"path"` // SQLite file path
	DSN  string `yaml:"dsn"`  // PostgreSQL connection string
}

type PathsConfig struct {
	Audiobooks string `yaml:"audiobooks"`
	Downloads  string `yaml:"downloads"`
	Config     string `yaml:"config"`
}

type OutputConfig struct {
	Format        string `yaml:"format"` // "m4b" or "mp3"
	EmbedCover    bool   `yaml:"embed_cover"`
	ChapterFile   bool   `yaml:"chapter_file"`
	PlexMatchFile bool   `yaml:"plexmatch_file"` // write .plexmatch hint files for perfect Plex scanning
}

type DownloadConfig struct {
	DownloadConcurrency int `yaml:"download_concurrency"` // 0 = auto-detect, downloads happen in parallel
	DecryptConcurrency  int `yaml:"decrypt_concurrency"`  // 0 = auto-detect, decryption is CPU-intensive
	ProcessConcurrency  int `yaml:"process_concurrency"`  // 0 = auto-detect, metadata enrichment and organization
}

type SyncConfig struct {
	Schedule     string `yaml:"schedule"`
	Enabled      bool   `yaml:"enabled"`
	Mode         string `yaml:"mode"`           // "quick" or "full" — default "full" for scheduled syncs
	AutoQueueNew bool   `yaml:"auto_queue_new"` // queue all newly-discovered books after sync
}

type PlexConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8080,
		},
		Database: DatabaseConfig{
			Type: "sqlite",
			Path: "/config/audible.db",
		},
		Paths: PathsConfig{
			Audiobooks: "/audiobooks",
			Downloads:  "/downloads",
			Config:     "/config",
		},
		Output: OutputConfig{
			Format:        "m4b",
			EmbedCover:    true,
			ChapterFile:   true,
			PlexMatchFile: true,
		},
		Sync: SyncConfig{
			Schedule:     "0 */6 * * *",
			Enabled:      true,
			Mode:         "full",
			AutoQueueNew: false,
		},
		Log: LogConfig{
			Level: "info",
			JSON:  false,
		},
	}
}

// Load reads a YAML config file and merges it with defaults.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // Use defaults if no config file
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

// LoadFromEnv applies environment variable overrides.
func (c *Config) LoadFromEnv() {
	if v := os.Getenv("PORT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Server.Port)
	}
	if v := os.Getenv("DATABASE_TYPE"); v != "" {
		c.Database.Type = v
	}
	if v := os.Getenv("DATABASE_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("DATABASE_DSN"); v != "" {
		c.Database.DSN = v
	}
	if v := os.Getenv("AUDIOBOOKS_PATH"); v != "" {
		c.Paths.Audiobooks = v
	}
	if v := os.Getenv("DOWNLOADS_PATH"); v != "" {
		c.Paths.Downloads = v
	}
	if v := os.Getenv("CONFIG_PATH"); v != "" {
		c.Paths.Config = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	if v := os.Getenv("LOG_JSON"); v != "" {
		switch v {
		case "1", "true", "TRUE", "True":
			c.Log.JSON = true
		case "0", "false", "FALSE", "False":
			c.Log.JSON = false
		}
	}
	if v := os.Getenv("OUTPUT_FORMAT"); v != "" {
		c.Output.Format = v
	}
	if v := firstEnv("DOWNLOAD_CONCURRENCY", "DOWNLOAD_DOWNLOAD_CONCURRENCY"); v != "" {
		fmt.Sscanf(v, "%d", &c.Download.DownloadConcurrency)
	}
	if v := firstEnv("DECRYPT_CONCURRENCY", "DOWNLOAD_DECRYPT_CONCURRENCY"); v != "" {
		fmt.Sscanf(v, "%d", &c.Download.DecryptConcurrency)
	}
	if v := firstEnv("PROCESS_CONCURRENCY", "DOWNLOAD_PROCESS_CONCURRENCY"); v != "" {
		fmt.Sscanf(v, "%d", &c.Download.ProcessConcurrency)
	}
	if v := os.Getenv("PLEX_URL"); v != "" {
		c.Plex.URL = v
	}
	if v := os.Getenv("PLEX_TOKEN"); v != "" {
		c.Plex.Token = v
	}
	if v := os.Getenv("SYNC_SCHEDULE"); v != "" {
		c.Sync.Schedule = v
	}
	if v := os.Getenv("SYNC_ENABLED"); v != "" {
		switch v {
		case "1", "true", "TRUE", "True":
			c.Sync.Enabled = true
		case "0", "false", "FALSE", "False":
			c.Sync.Enabled = false
		}
	}
	if v := os.Getenv("SYNC_MODE"); v != "" {
		c.Sync.Mode = v
	}
	if v := os.Getenv("SYNC_AUTO_QUEUE_NEW"); v != "" {
		switch v {
		case "1", "true", "TRUE", "True":
			c.Sync.AutoQueueNew = true
		case "0", "false", "FALSE", "False":
			c.Sync.AutoQueueNew = false
		}
	}
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

