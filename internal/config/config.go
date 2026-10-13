package config

import (
	"path/filepath"
	"strconv"
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
	PinnedAccounts    []string `yaml:"pinned_accounts" json:"pinned_accounts"`
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
		PinnedAccounts:    []string{},
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

// ParsePinnedSpec parses an entry from pinned_accounts such as "user@gmail.com=10"
// or "antigravity-user@gmail.com.json" into a normalized identifier and optional priority.
func ParsePinnedSpec(spec string) (pattern string, fixedPriority *int) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return "", nil
	}
	for _, sep := range []string{"=", ":"} {
		if idx := strings.LastIndex(s, sep); idx > 0 && idx < len(s)-1 {
			right := strings.TrimSpace(s[idx+1:])
			if val, err := strconv.Atoi(right); err == nil {
				left := strings.ToLower(strings.TrimSpace(s[:idx]))
				return left, &val
			}
		}
	}
	return strings.ToLower(s), nil
}

// MatchPinnedAccount checks if any of the given account identifiers (ID, filename, email, auth_index)
// matches a configured pinned_accounts entry.
func (c *Config) MatchPinnedAccount(identifiers ...string) (pinned bool, fixedPriority *int) {
	if c == nil || len(c.PinnedAccounts) == 0 {
		return false, nil
	}
	normIDs := make([]string, 0, len(identifiers)*2)
	for _, raw := range identifiers {
		v := strings.ToLower(strings.TrimSpace(raw))
		if v == "" {
			continue
		}
		normIDs = append(normIDs, v)
		base := filepath.Base(v)
		if base != v {
			normIDs = append(normIDs, base)
		}
		trimmed := strings.TrimSuffix(base, ".json")
		if trimmed != base {
			normIDs = append(normIDs, trimmed)
		}
	}
	if len(normIDs) == 0 {
		return false, nil
	}
	for _, rawSpec := range c.PinnedAccounts {
		pat, prio := ParsePinnedSpec(rawSpec)
		if pat == "" {
			continue
		}
		patBase := strings.TrimSuffix(filepath.Base(pat), ".json")
		for _, id := range normIDs {
			if id == pat || id == patBase || (strings.Contains(pat, "@") && strings.Contains(id, pat)) {
				return true, prio
			}
		}
	}
	return false, nil
}
