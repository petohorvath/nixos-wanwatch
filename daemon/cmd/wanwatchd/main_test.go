package main

import (
	"flag"
	"log/slog"
	"strings"
	"testing"
)

func TestParseFlagsDefaults(t *testing.T) {
	t.Parallel()
	f, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if f.configPath != "/etc/wanwatch/config.json" {
		t.Errorf("configPath = %q, want default", f.configPath)
	}
	if f.logLevel != "" {
		t.Errorf("logLevel = %q, want empty (use config global.logLevel)", f.logLevel)
	}
}

func TestParseFlagsOverrides(t *testing.T) {
	t.Parallel()
	f, err := parseFlags([]string{"-config", "/tmp/wan.json", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if f.configPath != "/tmp/wan.json" {
		t.Errorf("configPath = %q", f.configPath)
	}
	if f.logLevel != "debug" {
		t.Errorf("logLevel = %q", f.logLevel)
	}
}

func TestParseFlagsHelp(t *testing.T) {
	t.Parallel()
	// -help triggers flag.ErrHelp which run() treats as a clean
	// exit. parseFlags returns the wrapped error untouched.
	_, err := parseFlags([]string{"-help"})
	if err == nil {
		t.Fatal("parseFlags(-help) = nil, want flag.ErrHelp")
	}
	if !strings.Contains(err.Error(), flag.ErrHelp.Error()) {
		t.Errorf("err = %v, want wrapping flag.ErrHelp", err)
	}
}

func TestParseLogLevelKnown(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"":      slog.LevelInfo, // empty → info, so config.global default lands here
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for name, want := range cases {
		got, err := parseLogLevel(name)
		if err != nil {
			t.Errorf("parseLogLevel(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParseLogLevelRejectsTypo(t *testing.T) {
	t.Parallel()
	// A typo in config.global.logLevel must fail loudly — silent
	// fallback would hide the daemon's actual visibility.
	if _, err := parseLogLevel("verbose"); err == nil {
		t.Error("parseLogLevel(verbose) = nil, want error")
	}
}
