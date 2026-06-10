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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
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

var (
	globalLevel   = zerolog.InfoLevel
	useJSONOutput = true
	levelMu       sync.RWMutex

	// ringBuf is a small in-memory tail of recent log lines, exposed via
	// the web UI's diagnostics tab. Capped to 1024 entries; older lines
	// drop off the back. Threads through io.MultiWriter so all log output
	// also still goes to stderr — this is additive, not a replacement.
	ringBuf = newRingBuffer(1024)
)

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
func Init(level string, jsonOutput bool) {
	levelMu.Lock()
	globalLevel = parseLevel(level)
	useJSONOutput = jsonOutput
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
	// io.MultiWriter tees every line to the ring buffer in addition to
	// stderr / ConsoleWriter, so /api/diagnostics/logs/tail can serve a
	// recent-history snapshot without us having to scrape the docker log.
	if useJSONOutput {
		return io.MultiWriter(os.Stderr, ringBuf)
	}
	return io.MultiWriter(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	}, ringBuf)
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
