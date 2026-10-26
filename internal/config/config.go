// Package config loads the YAML configuration. Unknown keys are rejected so a
// typo never silently falls back to a default.
package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Liquidsoap Liquidsoap `yaml:"liquidsoap"`
	Neighbeats Neighbeats `yaml:"neighbeats"`
	Director   Director   `yaml:"director"`
	Data       string     `yaml:"data"`
}

type Liquidsoap struct {
	// ControlURL is the internal harbor (never exposed through Traefik).
	ControlURL string        `yaml:"control_url"`
	Timeout    time.Duration `yaml:"timeout"`
}

type Neighbeats struct {
	Socket  string        `yaml:"socket"`
	User    string        `yaml:"user"`
	Timeout time.Duration `yaml:"timeout"`
}

type Director struct {
	// Mode is the neighbeats taste mode: comfort, explore, rediscover, none.
	Mode          string        `yaml:"mode"`
	LegMin        int           `yaml:"leg_min"`
	LegMax        int           `yaml:"leg_max"`
	MaxMinutes    int           `yaml:"max_minutes"`
	LookAhead     time.Duration `yaml:"look_ahead"`
	RepeatWindow  time.Duration `yaml:"repeat_window"`
	PollInterval  time.Duration `yaml:"poll_interval"`
	AssumedTrack  time.Duration `yaml:"assumed_track"`
	PushWhenBelow int           `yaml:"push_when_below"`
	SeedPaths     []string      `yaml:"seed_paths"`
}

// Default returns the configuration used for every key a file leaves out.
func Default() Config {
	return Config{
		Liquidsoap: Liquidsoap{ControlURL: "http://liquidsoap:8005", Timeout: 5 * time.Second},
		Neighbeats: Neighbeats{Socket: "/run/neighbeats/neighbeats.sock", Timeout: 30 * time.Second},
		Director: Director{
			Mode:          "comfort",
			LegMin:        8,
			LegMax:        15,
			MaxMinutes:    10,
			LookAhead:     20 * time.Minute,
			RepeatWindow:  8 * time.Hour,
			PollInterval:  2 * time.Second,
			AssumedTrack:  4 * time.Minute,
			PushWhenBelow: 2,
		},
		Data: "/data",
	}
}

// Load reads path over the defaults. An empty path returns the defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, cfg.Validate()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && err.Error() != "EOF" {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	d := c.Director
	switch d.Mode {
	case "comfort", "explore", "rediscover", "none":
	default:
		return fmt.Errorf("config: director.mode %q: want comfort, explore, rediscover or none", d.Mode)
	}
	if d.LegMin < 1 || d.LegMax < d.LegMin || d.LegMax > 200 {
		return fmt.Errorf("config: director.leg_min/leg_max %d/%d: want 1 <= min <= max <= 200", d.LegMin, d.LegMax)
	}
	if d.LookAhead <= 0 || d.PollInterval <= 0 || d.AssumedTrack <= 0 {
		return fmt.Errorf("config: director durations must be positive")
	}
	if d.PushWhenBelow < 1 {
		return fmt.Errorf("config: director.push_when_below must be >= 1")
	}
	if c.Liquidsoap.ControlURL == "" {
		return fmt.Errorf("config: liquidsoap.control_url is empty")
	}
	return nil
}
