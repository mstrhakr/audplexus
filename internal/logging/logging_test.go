package logging

import (
	"errors"
	"os"
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
	Init("debug", true)
	if got := GetLevel(); got != "debug" {
		t.Fatalf("GetLevel() after Init = %q, want debug", got)
	}

	SetLevel("error")
	if got := GetLevel(); got != "error" {
		t.Fatalf("GetLevel() after SetLevel = %q, want error", got)
	}
}

func TestOutputWriterMode(t *testing.T) {
	useJSONOutput = true
	w := outputWriter()
	if w != os.Stderr {
		t.Fatalf("outputWriter() in json mode should return stderr")
	}

	useJSONOutput = false
	if _, ok := outputWriter().(zerolog.ConsoleWriter); !ok {
		t.Fatalf("outputWriter() in console mode should return zerolog.ConsoleWriter")
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
	Init("info", true)
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
