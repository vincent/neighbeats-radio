package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadOverridesDefaults(t *testing.T) {
	cfg, err := Load(write(t, "director:\n  mode: explore\n  look_ahead: 30m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Director.Mode != "explore" || cfg.Director.LookAhead != 30*time.Minute {
		t.Fatalf("override lost: %+v", cfg.Director)
	}
	if cfg.Director.LegMax != 15 {
		t.Fatalf("default lost: %+v", cfg.Director)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	_, err := Load(write(t, "director:\n  mod: explore\n"))
	if err == nil || !strings.Contains(err.Error(), "mod") {
		t.Fatalf("want unknown key error, got %v", err)
	}
}

func TestLoadEmptyFileAndPath(t *testing.T) {
	if _, err := Load(write(t, "")); err != nil {
		t.Fatalf("empty file: %v", err)
	}
	if _, err := Load(""); err != nil {
		t.Fatalf("no path: %v", err)
	}
}

func TestValidate(t *testing.T) {
	for name, body := range map[string]string{
		"mode": "director:\n  mode: wild\n",
		"legs": "director:\n  leg_min: 20\n  leg_max: 10\n",
		"poll": "director:\n  poll_interval: 0s\n",
		"push": "director:\n  push_when_below: 0\n",
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
