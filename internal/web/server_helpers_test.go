package web

import (
	"context"
	"strings"
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/mediaserver"
)

func TestNormalizeClientIPForLog(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: " 127.0.0.1 ", want: "127.0.0.1"},
		{in: "127.0.0.1:8080", want: "127.0.0.1"},
		{in: "[::1]:443", want: "127.0.0.1"},
		{in: "2001:db8::1", want: "2001:db8::1"},
		{in: "10.0.0.1, 192.168.1.10", want: "10.0.0.1"},
		{in: "not-an-ip", want: "not-an-ip"},
	}

	for _, tc := range tests {
		if got := normalizeClientIPForLog(tc.in); got != tc.want {
			t.Fatalf("normalizeClientIPForLog(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMediaServerLabel(t *testing.T) {
	if key, label := mediaServerLabel(mediaserver.TypeEmby); key != "emby" || label != "Emby" {
		t.Fatalf("mediaServerLabel(emby) = (%q,%q)", key, label)
	}
	if key, label := mediaServerLabel(mediaserver.TypePlex); key != "plex" || label != "Plex" {
		t.Fatalf("mediaServerLabel(plex) = (%q,%q)", key, label)
	}
	if key, label := mediaServerLabel(mediaserver.Type("unknown")); key != "plex" || label != "Plex" {
		t.Fatalf("mediaServerLabel(default) = (%q,%q)", key, label)
	}
}

func TestSettingsHelpers(t *testing.T) {
	stub := database.NewStubDB()
	s := &Server{db: stub}
	ctx := context.Background()

	if got := s.settingBool(ctx, "missing_bool", true); !got {
		t.Fatalf("settingBool missing default true should return true")
	}
	_ = stub.SetSetting(ctx, "bool_true", "1")
	_ = stub.SetSetting(ctx, "bool_false", "false")
	if !s.settingBool(ctx, "bool_true", false) {
		t.Fatalf("settingBool bool_true should be true")
	}
	if s.settingBool(ctx, "bool_false", true) {
		t.Fatalf("settingBool bool_false should be false")
	}

	if got := s.settingString(ctx, "missing_string", "fallback"); got != "fallback" {
		t.Fatalf("settingString missing = %q, want fallback", got)
	}
	_ = stub.SetSetting(ctx, "name", "  value  ")
	if got := s.settingString(ctx, "name", "fallback"); got != "value" {
		t.Fatalf("settingString trim = %q, want value", got)
	}

	if got := s.settingInt(ctx, "missing_int", 7); got != 7 {
		t.Fatalf("settingInt missing = %d, want 7", got)
	}
	_ = stub.SetSetting(ctx, "int_ok", "42")
	_ = stub.SetSetting(ctx, "int_bad", "abc")
	if got := s.settingInt(ctx, "int_ok", 0); got != 42 {
		t.Fatalf("settingInt int_ok = %d, want 42", got)
	}
	if got := s.settingInt(ctx, "int_bad", 9); got != 9 {
		t.Fatalf("settingInt int_bad = %d, want 9", got)
	}
}

func TestBuildDeleteMediaConfirm(t *testing.T) {
	withoutQueue := buildDeleteMediaConfirm("A Book", false)
	if withoutQueue != "Delete downloaded files for 'A Book' and reset status to New?" {
		t.Fatalf("unexpected confirm text without auto-queue: %q", withoutQueue)
	}

	withQueue := buildDeleteMediaConfirm("A Book", true)
	if withQueue != "Delete downloaded files for 'A Book' and reset status to New? Auto-queue is enabled and redownload will start immediately." {
		t.Fatalf("unexpected confirm text with auto-queue: %q", withQueue)
	}
}

func TestBuildLibraryBookActionsDeleteConfirm(t *testing.T) {
	books := []database.Book{{
		ID:       101,
		Title:    "Title",
		Status:   database.BookStatusComplete,
		FilePath: "/tmp/title.m4b",
	}}

	actionsNoQueue := buildLibraryBookActions(books, false)
	actionNoQueue, ok := actionsNoQueue[101]
	if !ok {
		t.Fatalf("expected action for book 101")
	}
	if !actionNoQueue.ShowDelete {
		t.Fatalf("expected ShowDelete true when file path exists")
	}
	if strings.Contains(actionNoQueue.DeleteConfirm, "Auto-queue is enabled") {
		t.Fatalf("confirm text unexpectedly mentions auto-queue when disabled: %q", actionNoQueue.DeleteConfirm)
	}

	actionsWithQueue := buildLibraryBookActions(books, true)
	actionWithQueue := actionsWithQueue[101]
	if !strings.Contains(actionWithQueue.DeleteConfirm, "Auto-queue is enabled and redownload will start immediately.") {
		t.Fatalf("confirm text should mention immediate redownload when enabled: %q", actionWithQueue.DeleteConfirm)
	}
}

func TestBuildLibraryBookActionsHideDeleteWhenNotComplete(t *testing.T) {
	books := []database.Book{{
		ID:       202,
		Title:    "Queued Book",
		Status:   database.BookStatusQueued,
		FilePath: "/tmp/queued-book.m4b",
	}}

	actions := buildLibraryBookActions(books, false)
	action, ok := actions[202]
	if !ok {
		t.Fatalf("expected action for book 202")
	}
	if !action.ShowCancel {
		t.Fatalf("expected ShowCancel true for queued book")
	}
	if action.ShowDelete {
		t.Fatalf("expected ShowDelete false for non-complete book")
	}
}

func TestBuildLibraryBookActionsHideCancelWhenComplete(t *testing.T) {
	books := []database.Book{{
		ID:       303,
		Title:    "Complete Book",
		Status:   database.BookStatusComplete,
		FilePath: "/tmp/complete-book.m4b",
	}}

	actions := buildLibraryBookActions(books, false)
	action, ok := actions[303]
	if !ok {
		t.Fatalf("expected action for book 303")
	}
	if action.ShowCancel {
		t.Fatalf("expected ShowCancel false for complete book")
	}
}
