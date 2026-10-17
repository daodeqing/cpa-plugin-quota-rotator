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
	RoutingStrategy   string   `yaml:"routing_strategy" json:"routing_strategy"`
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
		RoutingStrategy:   "auto",
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
	if err != nil || d < 5*time.Second {
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

// GetRoutingStrategy returns the configured routing strategy or default "auto".
func (c *Config) GetRoutingStrategy() string {
	if c == nil || c.RoutingStrategy == "" {
		if len(c.PinnedAccounts) > 0 {
			return "custom"
		}
		return "auto"
	}
	s := strings.ToLower(strings.TrimSpace(c.RoutingStrategy))
	switch s {
	case "quota_high_first", "quota_low_first", "plan_high_first", "plan_low_first", "expiry_soon_first", "custom":
		return s
	default:
		if len(c.PinnedAccounts) > 0 {
			return "custom"
		}
		return "auto"
	}
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

// ScopedPinnedKey 将固定规则绑定供应商和稳定凭证 ID，邮箱只作为展示信息。
func ScopedPinnedKey(provider, identifier string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "|" + strings.ToLower(strings.TrimSpace(identifier))
}

// MatchPinnedAccountForProvider 解析带供应商的规则；裸邮箱需先由完整凭证列表消歧。
func (c *Config) MatchPinnedAccountForProvider(provider string, identifiers ...string) (bool, *int) {
	if c == nil {
		return false, nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, spec := range c.PinnedAccounts {
		pattern, priority := ParsePinnedSpec(spec)
		if scopedProvider, id, scoped := strings.Cut(pattern, "|"); scoped {
			if scopedProvider != provider {
				continue
			}
			pattern = id
		}
		if strings.Contains(pattern, "@") && !strings.HasSuffix(pattern, ".json") {
			// 调度阶段没有完整凭证清单，带供应商的邮箱也不能代表唯一凭证。
			continue
		}
		if matchesPinnedIdentifier(pattern, identifiers...) {
			return true, priority
		}
	}
	return false, nil
}

func matchesPinnedIdentifier(pattern string, identifiers ...string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	stem := strings.TrimSuffix(filepath.Base(pattern), ".json")
	for _, id := range identifiers {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		if id == pattern || strings.TrimSuffix(filepath.Base(id), ".json") == stem {
			return true
		}
	}
	return false
}
