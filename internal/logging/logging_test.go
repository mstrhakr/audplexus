package logging

import (
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want zerolog.Level
	}{
		{in: "trace", want: zerolog.TraceLevel},
		{in: "debug", want: zerolog.DebugLevel},
		{in: "info", want: zerolog.InfoLevel},
		{in: "warn", want: zerolog.WarnLevel},
		{in: "warning", want: zerolog.WarnLevel},
		{in: "error", want: zerolog.ErrorLevel},
		{in: "fatal", want: zerolog.FatalLevel},
		{in: " nonsense ", want: zerolog.InfoLevel},
	}

	for _, tc := range tests {
		if got := parseLevel(tc.in); got != tc.want {
			t.Fatalf("parseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestInitAndSetLevel(t *testing.T) {
	Init("debug", true, FileConfig{})
	if got := GetLevel(); got != "debug" {
		t.Fatalf("GetLevel() after Init = %q, want debug", got)
	}

	SetLevel("error")
	if got := GetLevel(); got != "error" {
		t.Fatalf("GetLevel() after SetLevel = %q, want error", got)
	}
}

func TestOutputWriterMode(t *testing.T) {
	// outputWriter() now returns an io.MultiWriter that tees to both
	// stderr (or ConsoleWriter) AND the in-memory ring buffer so the
	// diagnostics page can serve a recent-log tail. The test asserts the
	// mode-dependent behavior end-to-end: a JSON-mode write reaches the
	// ring buffer as raw JSON, while console mode reaches it as the
	// pretty-printed ConsoleWriter format. Both prove the writer is wired.
	// Reset state BEFORE calling outputWriter(): the MultiWriter closes
	// over the *current* ringBuf value, so swapping the package var
	// after construction would write to a stale buffer. Also reset the
	// zerolog global level — sibling tests (TestInitAndSetLevel) can
	// leave it at "error", which would filter our Info() writes before
	// they ever reach the writer.
	ringBuf = newRingBuffer(16)
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	useJSONOutput = true
	w := outputWriter()
	if w == nil {
		t.Fatalf("outputWriter() returned nil in json mode")
	}
	zl := zerolog.New(w).With().Timestamp().Logger()
	zl.Info().Str("k", "v").Msg("json-mode")
	jsonTail := ringBuf.Snapshot(0)
	if len(jsonTail) == 0 || !strings.Contains(jsonTail[len(jsonTail)-1].Line, `"k":"v"`) {
		t.Fatalf("json-mode writer did not reach ring buffer; tail=%v", jsonTail)
	}

	ringBuf = newRingBuffer(16)
	useJSONOutput = false
	w2 := outputWriter()
	if w2 == nil {
		t.Fatalf("outputWriter() returned nil in console mode")
	}
	zl2 := zerolog.New(w2).With().Timestamp().Logger()
	zl2.Info().Str("k", "v").Msg("console-mode")
	consoleTail := ringBuf.Snapshot(0)
	if len(consoleTail) == 0 || !strings.Contains(consoleTail[len(consoleTail)-1].Line, "console-mode") {
		t.Fatalf("console-mode writer did not reach ring buffer; tail=%v", consoleTail)
	}
}

func TestTailLogsRingBufferOrder(t *testing.T) {
	ringBuf = newRingBuffer(3)
	// Three writes, no eviction yet.
	_, _ = ringBuf.Write([]byte("a\n"))
	_, _ = ringBuf.Write([]byte("b\n"))
	_, _ = ringBuf.Write([]byte("c\n"))
	got := TailLogs(0)
	if len(got) != 3 || got[0].Line != "a" || got[2].Line != "c" {
		t.Fatalf("expected oldest-first [a b c], got %+v", got)
	}
	// Fourth write evicts the oldest.
	_, _ = ringBuf.Write([]byte("d\n"))
	got = TailLogs(0)
	if len(got) != 3 || got[0].Line != "b" || got[2].Line != "d" {
		t.Fatalf("expected [b c d] after eviction, got %+v", got)
	}
	// Limit n.
	got = TailLogs(2)
	if len(got) != 2 || got[0].Line != "c" || got[1].Line != "d" {
		t.Fatalf("expected last 2 = [c d], got %+v", got)
	}
}

func TestExportLogsClampsMaxLinesUpperBound(t *testing.T) {
	ringBuf = newRingBuffer(4)
	_, _ = ringBuf.Write([]byte("a\n"))
	_, _ = ringBuf.Write([]byte("b\n"))
	_, _ = ringBuf.Write([]byte("c\n"))
	_, _ = ringBuf.Write([]byte("d\n"))

	entries, source, err := ExportLogs(nil, nil, maxDiagnosticsExportLines+1000)
	if err != nil {
		t.Fatalf("ExportLogs returned unexpected error: %v", err)
	}
	if source != "memory" {
		t.Fatalf("source = %q, want memory", source)
	}
	if len(entries) != 4 {
		t.Fatalf("len(entries) = %d, want 4", len(entries))
	}
}

func TestExportLogsUsesDefaultWhenNonPositive(t *testing.T) {
	ringBuf = newRingBuffer(3)
	_, _ = ringBuf.Write([]byte("x\n"))
	_, _ = ringBuf.Write([]byte("y\n"))
	_, _ = ringBuf.Write([]byte("z\n"))

	entries, source, err := ExportLogs(nil, nil, 0)
	if err != nil {
		t.Fatalf("ExportLogs returned unexpected error: %v", err)
	}
	if source != "memory" {
		t.Fatalf("source = %q, want memory", source)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}
}

func TestWithFieldAndWithFieldsImmutability(t *testing.T) {
	base := Component("test")
	if len(base.fields) != 0 {
		t.Fatalf("base logger should start with 0 fields")
	}

	one := base.WithField("k1", "v1")
	if len(base.fields) != 0 {
		t.Fatalf("base logger was mutated by WithField")
	}
	if got := one.fields["k1"]; got != "v1" {
		t.Fatalf("WithField field k1 = %q, want v1", got)
	}

	two := one.WithFields(map[string]string{"k2": "v2", "k3": "v3"})
	if len(one.fields) != 1 {
		t.Fatalf("intermediate logger was mutated by WithFields")
	}
	if got := two.fields["k2"]; got != "v2" {
		t.Fatalf("WithFields field k2 = %q, want v2", got)
	}
	if got := two.fields["k3"]; got != "v3" {
		t.Fatalf("WithFields field k3 = %q, want v3", got)
	}
}

func TestLoggerEventMethods(t *testing.T) {
	Init("info", true, FileConfig{})
	l := Component("web").WithField("req_id", "abc")

	if l.GetZerolog().GetLevel() != zerolog.InfoLevel {
		t.Fatalf("GetZerolog level mismatch")
	}

	// Smoke test event builders to ensure methods are wired.
	l.Trace().Msg("trace")
	l.Debug().Msg("debug")
	l.Info().Msg("info")
	l.Warn().Msg("warn")
	l.Error().Msg("error")
	l.Err(errors.New("x")).Msg("err")
}
