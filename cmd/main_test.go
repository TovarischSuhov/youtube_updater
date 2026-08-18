package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"youtube-updater/config"
	"youtube-updater/state"
)

// TestContractSurface is a compile-time check of the public API shape.
func TestContractSurface(t *testing.T) {
	var _ func(string) error = SetupLogging
	var _ func(string) (bool, error) = EnsureConfig
	var _ func(string, string, string, string, string, bool) error = Run
	var _ func(string, string, string, string, string, string) error = RunAdd
	var _ func(string, string) error = RunRemove
	var _ func(string) error = RunList
	var _ func(string, string) error = RunStatus
}

func TestSetupLogging_AcceptsKnownLevels(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)
	for _, lvl := range []string{"debug", "info", "warn", "error", "DEBUG", "Info", "Warn"} {
		if err := SetupLogging(lvl); err != nil {
			t.Errorf("SetupLogging(%q) unexpected error: %v", lvl, err)
		}
	}
}

func TestSetupLogging_RejectsUnknownLevel(t *testing.T) {
	if err := SetupLogging("trace"); err == nil {
		t.Fatal("expected error for unknown level, got nil")
	}
}

func TestSetupLogging_ErrorLevelSuppressesInfo(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)
	out := captureStderr(t, func() {
		if err := SetupLogging("error"); err != nil {
			t.Fatal(err)
		}
		slog.Default().Info("should-be-suppressed")
	})
	if strings.Contains(out, "should-be-suppressed") {
		t.Errorf("info leaked at error level: %q", out)
	}
}

func TestSetupLogging_InfoLevelShowsInfo(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)
	out := captureStderr(t, func() {
		if err := SetupLogging("info"); err != nil {
			t.Fatal(err)
		}
		slog.Default().Info("should-appear")
	})
	if !strings.Contains(out, "should-appear") {
		t.Errorf("info missing at info level: %q", out)
	}
}

func TestEnsureConfig_CreatesWhenMissing(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)
	path := filepath.Join(t.TempDir(), "config.yaml")
	created, err := EnsureConfig(path)
	if err != nil {
		t.Fatalf("EnsureConfig error: %v", err)
	}
	if !created {
		t.Error("expected created=true for missing config")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file not created: %v", err)
	}
	m, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("created config not readable: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty config, got %d", len(m))
	}
}

func TestEnsureConfig_NoOpWhenPresent(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.SaveConfig(path, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureConfig(path)
	if err != nil {
		t.Fatalf("EnsureConfig error: %v", err)
	}
	if created {
		t.Error("expected created=false for existing config")
	}
	m, _ := config.LoadConfig(path)
	if len(m) != 1 || m[0].ChannelID != "UCa" {
		t.Errorf("existing config changed: %+v", m)
	}
}

func TestRun_ReturnsErrorOnMissingConfig(t *testing.T) {
	err := Run(
		"/nonexistent/config.yaml", "", "",
		filepath.Join(t.TempDir(), "state.json"),
		"http://localhost:8080", true,
	)
	if err == nil {
		t.Fatal("expected error for missing config, got nil")
	}
}

func TestRunList_PrintsPairs(t *testing.T) {
	path := writeConfig(t, []config.ChannelMapping{
		{ChannelID: "UCa", PlaylistID: "PLa", ChannelName: "Foo", PlaylistName: "Bar"},
	})
	out := captureStdout(t, func() {
		if err := RunList(path); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"UCa", "Foo", "PLa", "Bar"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestRunList_ShowsUnnamedForLegacyPairs(t *testing.T) {
	path := writeConfig(t, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}})
	out := captureStdout(t, func() {
		if err := RunList(path); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "<unnamed>") {
		t.Errorf("expected <unnamed> placeholder for legacy pair, got:\n%s", out)
	}
}

func TestRunRemove_RemovesPairAndSaves(t *testing.T) {
	path := writeConfig(t, []config.ChannelMapping{
		{ChannelID: "UCa", PlaylistID: "PLa"},
		{ChannelID: "UCb", PlaylistID: "PLb"},
	})
	if err := RunRemove(path, "UCa"); err != nil {
		t.Fatalf("RunRemove error: %v", err)
	}
	got, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChannelID != "UCb" {
		t.Fatalf("expected only UCb left after remove, got %+v", got)
	}
}

func TestRunRemove_AcceptsChannelURL(t *testing.T) {
	const ch = "UC1234567890abcdefghijkl"
	path := writeConfig(t, []config.ChannelMapping{
		{ChannelID: ch, PlaylistID: "PLa"},
		{ChannelID: "UCb", PlaylistID: "PLb"},
	})
	if err := RunRemove(path, "https://www.youtube.com/channel/"+ch); err != nil {
		t.Fatalf("RunRemove error: %v", err)
	}
	got, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChannelID != "UCb" {
		t.Fatalf("expected the /channel/ URL to remove %s, got %+v", ch, got)
	}
}

func TestRunRemove_RejectsHandleURL(t *testing.T) {
	path := writeConfig(t, []config.ChannelMapping{{ChannelID: "UC1234567890abcdefghijkl", PlaylistID: "PLa"}})
	if err := RunRemove(path, "https://www.youtube.com/@SomeHandle"); err == nil {
		t.Fatal("expected error for handle URL, got nil")
	}
	got, _ := config.LoadConfig(path)
	if len(got) != 1 {
		t.Fatalf("config should be unchanged after a rejected remove, got %+v", got)
	}
}

func TestRunStatus_PrintsSeededAndUnseeded(t *testing.T) {
	configPath := writeConfig(t, []config.ChannelMapping{
		{ChannelID: "UCa", ChannelName: "Foo", PlaylistID: "PLa", PlaylistName: "Bar"},
		{ChannelID: "UCb", ChannelName: "Baz", PlaylistID: "PLb", PlaylistName: "Qux"},
	})
	statePath := filepath.Join(t.TempDir(), "state.json")
	st, err := state.NewState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetLastSeen("UCa", "vid1", "2026-08-01T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := RunStatus(configPath, statePath); err != nil {
			t.Fatal(err)
		}
	})
	// Seeded channel: names, IDs, seeded flag, last-seen video id + publish time.
	for _, want := range []string{"Foo", "UCa", "Bar", "seeded:", "true", "vid1", "2026-08-01T12:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	// Unseeded channel: flagged as not synced.
	for _, want := range []string{"Baz", "UCb", "false", "not synced yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestRunStatus_MissingState_AllUnseeded(t *testing.T) {
	configPath := writeConfig(t, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}})
	statePath := filepath.Join(t.TempDir(), "does-not-exist.json")
	out := captureStdout(t, func() {
		if err := RunStatus(configPath, statePath); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "not synced yet") {
		t.Errorf("missing state should read as unseeded; got:\n%s", out)
	}
	if strings.Contains(out, "true") {
		t.Errorf("missing state should have no seeded channels; got:\n%s", out)
	}
}

func writeConfig(t *testing.T, mappings []config.ChannelMapping) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.SaveConfig(path, mappings); err != nil {
		t.Fatal(err)
	}
	return path
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String()
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String()
}
