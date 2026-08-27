// Package logging provides a structured logging abstraction for the application.
//
// It wraps zerolog to provide component-scoped loggers with consistent field naming.
// Every subsystem should create its own logger via logging.Component("name") to make
// it easy to filter and trace log output by component.
//
// Log levels:
//   - Trace: Very fine-grained, per-iteration details (e.g., each chunk written)
//   - Debug: Diagnostic info useful during development (e.g., ffmpeg args, SQL queries)
//   - Info:  Normal operational events (e.g., sync started, download complete)
//   - Warn:  Recoverable issues that deserve attention (e.g., missing optional metadata)
//   - Error: Failures that affect a single operation but not the whole system
//   - Fatal: Unrecoverable errors that prevent the application from starting
package logging

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Logger is a component-scoped structured logger.
//
// Component loggers are often created at package init time before Init runs.
// To ensure output format and level still follow runtime config, each event
// call builds a logger from current global settings.
type Logger struct {
	component string
	fields    map[string]string
}

// FileConfig controls optional rotating file logs.
type FileConfig struct {
	Enabled    bool
	Path       string
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   bool
}

// LogAvailability describes the available time window across all known logs.
type LogAvailability struct {
	Source   string     `json:"source"`
	Earliest *time.Time `json:"earliest,omitempty"`
	Latest   *time.Time `json:"latest,omitempty"`
}

func defaultFileConfig() FileConfig {
	return FileConfig{
		Enabled:    true,
		Path:       "/config/logs/audplexus.log",
		MaxSizeMB:  25,
		MaxBackups: 5,
		MaxAgeDays: 14,
		Compress:   true,
	}
}

var (
	globalLevel   = zerolog.InfoLevel
	useJSONOutput = true
	levelMu       sync.RWMutex
	fileConfig    = defaultFileConfig()
	fileWriter    io.Writer

	// ringBuf is a small in-memory tail of recent log lines, exposed via
	// the web UI's diagnostics tab. Capped to 1024 entries; older lines
	// drop off the back. Threads through io.MultiWriter so all log output
	// also still goes to stderr — this is additive, not a replacement.
	ringBuf = newRingBuffer(1024)
)

const maxDiagnosticsExportLines = 20000

// RingEntry is a single tail-buffer log line as captured by the
// in-memory ring buffer. Time is the time the line was written.
type RingEntry struct {
	Time time.Time `json:"time"`
	Line string    `json:"line"`
}

// ParsedEntry is a structured representation of a log line.
type ParsedEntry struct {
	Time      time.Time         `json:"time"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Component string            `json:"component,omitempty"`
	RawLine   string            `json:"raw_line"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// ParseLogLine extracts structured data from a raw log line.
// Handles both JSON format (zerolog) and ConsoleWriter format.
func ParseLogLine(rawLine string) ParsedEntry {
	entry := ParsedEntry{RawLine: rawLine}

	// Try JSON parsing first
	var jsonData map[string]interface{}
	if err := json.Unmarshal([]byte(rawLine), &jsonData); err == nil {
		// Extract standard fields
		if ts, ok := jsonData["time"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				entry.Time = t
			}
		}
		if level, ok := jsonData["level"].(string); ok {
			entry.Level = level
		}
		if msg, ok := jsonData["message"].(string); ok {
			entry.Message = msg
		}
		if comp, ok := jsonData["component"].(string); ok {
			entry.Component = comp
		}

		// Collect custom fields
		fields := make(map[string]string)
		for k, v := range jsonData {
			if k != "time" && k != "level" && k != "message" && k != "component" {
				if s, ok := v.(string); ok {
					fields[k] = s
				} else {
					// Convert non-string values to string
					fields[k] = fmt.Sprintf("%v", v)
				}
			}
		}
		if len(fields) > 0 {
			entry.Fields = fields
		}
	} else {
		// ConsoleWriter format fallback: extract what we can
		entry.Level = detectLevel(rawLine)
		entry.Message = rawLine
	}

	if entry.Level == "" {
		entry.Level = "info"
	}
	return entry
}

func detectLevel(s string) string {
	t := strings.ToLower(s)
	if strings.Contains(t, "\"level\":\"error\"") || strings.Contains(t, " err ") || strings.Contains(t, "error") {
		return "error"
	}
	if strings.Contains(t, "\"level\":\"warn\"") || strings.Contains(t, " wrn ") || strings.Contains(t, "warn") {
		return "warn"
	}
	if strings.Contains(t, "\"level\":\"debug\"") || strings.Contains(t, " dbg ") || strings.Contains(t, "debug") {
		return "debug"
	}
	return "info"
}

type ringBuffer struct {
	mu      sync.RWMutex
	entries []RingEntry
	head    int
	cap     int
	count   int
	subs    map[int]chan RingEntry
	nextSub int
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{entries: make([]RingEntry, capacity), cap: capacity, subs: make(map[int]chan RingEntry)}
}

// Write implements io.Writer. Splits on newlines so multi-line writes
// (rare from zerolog, but possible from ConsoleWriter) land as separate
// entries. Empty trailing tokens are dropped.
func (rb *ringBuffer) Write(p []byte) (int, error) {
	now := time.Now()
	lines := strings.Split(strings.TrimRight(string(p), "\n"), "\n")
	rb.mu.Lock()
	for _, l := range lines {
		if l == "" {
			continue
		}
		entry := RingEntry{Time: now, Line: l}
		rb.entries[rb.head] = entry
		rb.head = (rb.head + 1) % rb.cap
		if rb.count < rb.cap {
			rb.count++
		}
		for _, ch := range rb.subs {
			select {
			case ch <- entry:
			default:
				// Keep stream non-blocking if a subscriber is slow.
			}
		}
	}
	rb.mu.Unlock()
	return len(p), nil
}

func (rb *ringBuffer) Subscribe() (int, <-chan RingEntry) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	id := rb.nextSub
	rb.nextSub++
	ch := make(chan RingEntry, 256)
	rb.subs[id] = ch
	return id, ch
}

func (rb *ringBuffer) Unsubscribe(id int) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	ch, ok := rb.subs[id]
	if !ok {
		return
	}
	delete(rb.subs, id)
	close(ch)
}

// Snapshot returns the most recent n entries (oldest first). n=0 returns
// everything currently in the buffer.
func (rb *ringBuffer) Snapshot(n int) []RingEntry {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	if rb.count == 0 {
		return nil
	}
	if n <= 0 || n > rb.count {
		n = rb.count
	}
	out := make([]RingEntry, 0, n)
	// Oldest valid entry is (head - count) mod cap.
	start := (rb.head - rb.count + rb.cap) % rb.cap
	skip := rb.count - n
	for i := 0; i < rb.count; i++ {
		if i < skip {
			continue
		}
		out = append(out, rb.entries[(start+i)%rb.cap])
	}
	return out
}

// TailLogs returns the most recent n lines captured by the in-memory
// ring buffer. n=0 returns everything currently buffered (up to 1024).
// Safe for concurrent use.
func TailLogs(n int) []RingEntry {
	return ringBuf.Snapshot(n)
}

// ExportLogs returns sanitized log entries for diagnostics export.
// It prefers rotating file logs when enabled, and falls back to in-memory
// tail entries when file logs are unavailable.
func ExportLogs(since, until *time.Time, maxLines int) ([]ParsedEntry, string, error) {
	if maxLines <= 0 {
		maxLines = 1000
	}
	if maxLines > maxDiagnosticsExportLines {
		maxLines = maxDiagnosticsExportLines
	}

	levelMu.RLock()
	cfg := fileConfig
	levelMu.RUnlock()

	if cfg.Enabled {
		entries, err := readParsedEntriesFromFiles(cfg, since, until, maxLines)
		if err == nil && len(entries) > 0 {
			return entries, "file", nil
		}
		if err != nil {
			return readParsedEntriesFromMemory(since, until, maxLines), "memory", err
		}
	}

	return readParsedEntriesFromMemory(since, until, maxLines), "memory", nil
}

// GetFileConfig returns the active rotating file log configuration.
func GetFileConfig() FileConfig {
	levelMu.RLock()
	defer levelMu.RUnlock()
	return fileConfig
}

// GetLogAvailability reports the currently available log time window.
func GetLogAvailability() (LogAvailability, error) {
	levelMu.RLock()
	cfg := fileConfig
	levelMu.RUnlock()

	var sourceParts []string
	var mergedEarliest *time.Time
	var mergedLatest *time.Time
	var fileErr error

	if cfg.Enabled {
		e, l, err := boundsFromFiles(cfg)
		if err != nil {
			fileErr = err
		} else if e != nil && l != nil {
			sourceParts = append(sourceParts, "file")
			mergedEarliest, mergedLatest = mergeBounds(mergedEarliest, mergedLatest, e, l)
		}
	}

	e, l := boundsFromMemory()
	if e != nil && l != nil {
		sourceParts = append(sourceParts, "memory")
		mergedEarliest, mergedLatest = mergeBounds(mergedEarliest, mergedLatest, e, l)
	}

	source := "none"
	if len(sourceParts) == 1 {
		source = sourceParts[0]
	} else if len(sourceParts) > 1 {
		source = "mixed"
	}

	return LogAvailability{
		Source:   source,
		Earliest: mergedEarliest,
		Latest:   mergedLatest,
	}, fileErr
}

// SubscribeLogs opens a live feed of new in-memory log entries.
// The returned id must be passed to UnsubscribeLogs when done.
func SubscribeLogs() (int, <-chan RingEntry) {
	return ringBuf.Subscribe()
}

// UnsubscribeLogs closes a previously opened live log feed.
func UnsubscribeLogs(id int) {
	ringBuf.Unsubscribe(id)
}

// Init configures the global logging defaults. Call once at startup.
func Init(level string, jsonOutput bool, cfg FileConfig) {
	normalized := normalizeFileConfig(cfg)
	writer, err := buildFileWriter(normalized)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: file logging disabled (%v)\n", err)
		normalized.Enabled = false
		writer = nil
	}

	levelMu.Lock()
	globalLevel = parseLevel(level)
	useJSONOutput = jsonOutput
	fileConfig = normalized
	fileWriter = writer
	levelMu.Unlock()

	zerolog.SetGlobalLevel(globalLevel)
	zerolog.TimeFieldFormat = time.RFC3339

	zl := zerolog.New(outputWriter()).With().Timestamp().Logger().Level(globalLevel)
	setGlobalLogger(zl)
}

// SetLevel changes the log level at runtime. Safe for concurrent use.
func SetLevel(level string) {
	levelMu.Lock()
	globalLevel = parseLevel(level)
	levelMu.Unlock()

	zerolog.SetGlobalLevel(globalLevel)
}

// GetLevel returns the current log level name.
func GetLevel() string {
	levelMu.RLock()
	defer levelMu.RUnlock()
	return globalLevel.String()
}

func setGlobalLogger(zl zerolog.Logger) {
	zlog.Logger = zl
}

func outputWriter() io.Writer {
	levelMu.RLock()
	jsonOutput := useJSONOutput
	activeFileWriter := fileWriter
	levelMu.RUnlock()

	// io.MultiWriter tees every line to the ring buffer in addition to
	// stderr / ConsoleWriter, so /api/diagnostics/logs/tail can serve a
	// recent-history snapshot without us having to scrape the docker log.
	writers := []io.Writer{ringBuf}
	if jsonOutput {
		writers = append(writers, os.Stderr)
	} else {
		writers = append(writers, zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: "15:04:05",
		})
	}
	if activeFileWriter != nil {
		writers = append(writers, activeFileWriter)
	}
	return io.MultiWriter(writers...)
}

func normalizeFileConfig(cfg FileConfig) FileConfig {
	def := defaultFileConfig()
	if strings.TrimSpace(cfg.Path) == "" {
		cfg.Path = def.Path
	}
	if cfg.MaxSizeMB <= 0 {
		cfg.MaxSizeMB = def.MaxSizeMB
	}
	if cfg.MaxBackups <= 0 {
		cfg.MaxBackups = def.MaxBackups
	}
	if cfg.MaxAgeDays <= 0 {
		cfg.MaxAgeDays = def.MaxAgeDays
	}
	return cfg
}

func buildFileWriter(cfg FileConfig) (io.Writer, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	logPath := strings.TrimSpace(cfg.Path)
	if logPath == "" {
		return nil, fmt.Errorf("log file path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	return &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
	}, nil
}

type logFileMeta struct {
	path    string
	modTime time.Time
}

func listLogFiles(cfg FileConfig) ([]logFileMeta, error) {
	logPath := strings.TrimSpace(cfg.Path)
	if logPath == "" {
		return nil, nil
	}
	dir := filepath.Dir(logPath)
	base := filepath.Base(logPath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	files := make([]logFileMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name != base && !strings.HasPrefix(name, base+"-") && !strings.HasPrefix(name, base+".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, logFileMeta{path: filepath.Join(dir, name), modTime: info.ModTime()})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})
	return files, nil
}

func withinWindow(t time.Time, since, until *time.Time) bool {
	if since != nil && t.Before(*since) {
		return false
	}
	if until != nil && t.After(*until) {
		return false
	}
	return true
}

func clampParsedEntries(entries []ParsedEntry, maxLines int) []ParsedEntry {
	if maxLines <= 0 || len(entries) <= maxLines {
		return entries
	}
	return entries[len(entries)-maxLines:]
}

func readParsedEntriesFromFiles(cfg FileConfig, since, until *time.Time, maxLines int) ([]ParsedEntry, error) {
	files, err := listLogFiles(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]ParsedEntry, 0, maxLines)
	for _, file := range files {
		f, err := os.Open(file.path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 2*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			entry := ParseLogLine(line)
			if entry.Time.IsZero() {
				entry.Time = file.modTime.UTC()
			}
			if !entry.Time.IsZero() && !withinWindow(entry.Time, since, until) {
				continue
			}
			out = append(out, entry)
			if maxLines > 0 && len(out) > maxLines {
				out = out[len(out)-maxLines:]
			}
		}
		_ = f.Close()
		if scanErr := scanner.Err(); scanErr != nil {
			continue
		}
	}
	return clampParsedEntries(out, maxLines), nil
}

func readParsedEntriesFromMemory(since, until *time.Time, maxLines int) []ParsedEntry {
	raw := TailLogs(0)
	out := make([]ParsedEntry, 0, len(raw))
	for _, r := range raw {
		entry := ParseLogLine(r.Line)
		if entry.Time.IsZero() {
			entry.Time = r.Time.UTC()
		}
		if !withinWindow(entry.Time, since, until) {
			continue
		}
		out = append(out, entry)
	}
	return clampParsedEntries(out, maxLines)
}

func mergeBounds(curEarliest, curLatest, earliest, latest *time.Time) (*time.Time, *time.Time) {
	outEarliest := curEarliest
	outLatest := curLatest
	if earliest != nil {
		if outEarliest == nil || earliest.Before(*outEarliest) {
			t := earliest.UTC()
			outEarliest = &t
		}
	}
	if latest != nil {
		if outLatest == nil || latest.After(*outLatest) {
			t := latest.UTC()
			outLatest = &t
		}
	}
	return outEarliest, outLatest
}

func boundsFromMemory() (*time.Time, *time.Time) {
	raw := TailLogs(0)
	if len(raw) == 0 {
		return nil, nil
	}
	first := raw[0].Time.UTC()
	last := raw[len(raw)-1].Time.UTC()
	return &first, &last
}

func boundsFromFiles(cfg FileConfig) (*time.Time, *time.Time, error) {
	files, err := listLogFiles(cfg)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, nil
	}

	var earliest *time.Time
	var latest *time.Time
	for _, file := range files {
		f, err := os.Open(file.path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 2*1024*1024)
		for scanner.Scan() {
			entry := ParseLogLine(scanner.Text())
			ts := entry.Time
			if ts.IsZero() {
				continue
			}
			ts = ts.UTC()
			if earliest == nil || ts.Before(*earliest) {
				t := ts
				earliest = &t
			}
			if latest == nil || ts.After(*latest) {
				t := ts
				latest = &t
			}
		}
		_ = f.Close()
	}

	// Fallback when we cannot parse timestamps from file lines (e.g. console format).
	if earliest == nil || latest == nil {
		first := files[0].modTime.UTC()
		last := files[len(files)-1].modTime.UTC()
		earliest = &first
		latest = &last
	}
	return earliest, latest, nil
}

func (l *Logger) build() zerolog.Logger {
	levelMu.RLock()
	level := globalLevel
	levelMu.RUnlock()

	ctx := zerolog.New(outputWriter()).With().Timestamp()
	if l.component != "" {
		ctx = ctx.Str("component", l.component)
	}
	for k, v := range l.fields {
		ctx = ctx.Str(k, v)
	}
	return ctx.Logger().Level(level)
}

// Component creates a logger scoped to a named component.
// The component name appears in every log line for easy filtering.
//
//	log := logging.Component("sync")
//	log.Info().Str("asin", asin).Msg("book synced")
func Component(name string) *Logger {
	return &Logger{component: name, fields: map[string]string{}}
}

// GetZerolog returns the underlying zerolog.Logger for advanced use cases.
func (l *Logger) GetZerolog() zerolog.Logger {
	return l.build()
}

// --- Trace ---

func (l *Logger) Trace() *zerolog.Event {
	zl := l.build()
	return zl.Trace()
}

// --- Debug ---

func (l *Logger) Debug() *zerolog.Event {
	zl := l.build()
	return zl.Debug()
}

// --- Info ---

func (l *Logger) Info() *zerolog.Event {
	zl := l.build()
	return zl.Info()
}

// --- Warn ---

func (l *Logger) Warn() *zerolog.Event {
	zl := l.build()
	return zl.Warn()
}

// --- Error ---

func (l *Logger) Error() *zerolog.Event {
	zl := l.build()
	return zl.Error()
}

// --- Fatal ---

func (l *Logger) Fatal() *zerolog.Event {
	zl := l.build()
	return zl.Fatal()
}

// --- Convenience methods for common patterns ---

// Err returns an error-level event with the error already attached.
func (l *Logger) Err(err error) *zerolog.Event {
	zl := l.build()
	return zl.Error().Err(err)
}

// WithField returns a new Logger with an additional field baked in.
func (l *Logger) WithField(key, value string) *Logger {
	fields := make(map[string]string, len(l.fields)+1)
	for k, v := range l.fields {
		fields[k] = v
	}
	fields[key] = value
	return &Logger{component: l.component, fields: fields}
}

// WithFields returns a new Logger with additional fields baked in.
func (l *Logger) WithFields(fields map[string]string) *Logger {
	merged := make(map[string]string, len(l.fields)+len(fields))
	for k, v := range l.fields {
		merged[k] = v
	}
	for k, v := range fields {
		merged[k] = v
	}
	return &Logger{component: l.component, fields: merged}
}

func parseLevel(level string) zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "fatal":
		return zerolog.FatalLevel
	default:
		return zerolog.InfoLevel
	}
}
