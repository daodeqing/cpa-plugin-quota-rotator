package config

import (
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config defines the user-configurable options for the quota rotator plugin.
type Config struct {
	Enabled           bool     `yaml:"enabled" json:"enabled"`
	StrictShortWindow bool     `yaml:"strict_short_window" json:"strict_short_window"`
	RefreshInterval   string   `yaml:"refresh_interval" json:"refresh_interval"`
	Providers         []string `yaml:"providers" json:"providers"`
	AntigravityGroup  string   `yaml:"antigravity_group" json:"antigravity_group"`
	ParallelCount     int      `yaml:"parallel_count" json:"parallel_count"`
}

// DefaultConfig returns the recommended default plugin configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:           true,
		StrictShortWindow: true,
		RefreshInterval:   "10m",
		Providers:         []string{"codex", "antigravity"},
		AntigravityGroup:  "gemini",
		ParallelCount:     1,
	}
}

// Parse parses YAML configuration bytes and applies default fallbacks.
func Parse(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return DefaultConfig(), err
	}
	return cfg, nil
}

// GetRefreshDuration returns the parsed refresh interval or default 10m.
func (c *Config) GetRefreshDuration() time.Duration {
	if c == nil || c.RefreshInterval == "" {
		return 10 * time.Minute
	}
	d, err := time.ParseDuration(c.RefreshInterval)
	if err != nil || d < 1*time.Minute {
		return 10 * time.Minute
	}
	return d
}

// ShouldHandleProvider checks if a provider key should be scheduled by this plugin.
func (c *Config) ShouldHandleProvider(provider string) bool {
	if c == nil || !c.Enabled {
		return false
	}
	p := strings.ToLower(strings.TrimSpace(provider))
	if len(c.Providers) == 0 {
		return p == "codex" || p == "antigravity"
	}
	for _, item := range c.Providers {
		if strings.ToLower(strings.TrimSpace(item)) == p {
			return true
		}
	}
	return false
}

// GetParallelCount returns the configured parallel count, clamped to >= 1.
func (c *Config) GetParallelCount() int {
	if c == nil || c.ParallelCount <= 1 {
		return 1
	}
	return c.ParallelCount
}
