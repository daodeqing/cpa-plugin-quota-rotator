package management

import (
	"time"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/quota"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/scheduler"
)

type AccountView struct {
	AuthID                      string  `json:"auth_id"`
	Provider                    string  `json:"provider"`
	WeeklyRemainingPercent      float64 `json:"weekly_remaining_percent"`
	WeeklyResetAt               string  `json:"weekly_reset_at"`
	WeeklyResetCountdownSeconds int64   `json:"weekly_reset_countdown_seconds"`
	ShortWindowRemainingPercent float64 `json:"short_window_remaining_percent"`
	IsHealthy                   bool    `json:"is_healthy"`
	IsBypassed                  bool    `json:"is_bypassed"`
	EligibleStage               string  `json:"eligible_stage"`
}

type StatusResponse struct {
	PluginID          string            `json:"plugin_id"`
	Enabled           bool              `json:"enabled"`
	StrictShortWindow bool              `json:"strict_short_window"`
	RefreshInterval   string            `json:"refresh_interval"`
	Providers         []string          `json:"providers"`
	ActiveStages      map[string]string `json:"active_stages"`
	LastPickedIDs     map[string]string `json:"last_picked_ids"`
	Accounts          []AccountView     `json:"accounts"`
}

// BuildStatusResponse constructs the status payload for management queries.
func BuildStatusResponse(cfg config.Config, rot *scheduler.Rotator, cache *quota.Cache, now time.Time) StatusResponse {
	stages, picks := rot.GetStateSnapshot()
	accounts := cache.GetAll()
	views := make([]AccountView, 0, len(accounts))

	for _, acc := range accounts {
		bypassed := acc.IsBypassed(cfg.StrictShortWindow)
		stage := "none"
		if !bypassed {
			if acc.IsStageOneEligible() {
				stage = scheduler.StageFullCapacity
			} else {
				stage = scheduler.StageResetSoon
			}
		}
		countdownSec := int64(acc.ResetCountdown(now).Seconds())
		resetStr := ""
		if !acc.WeeklyResetAt.IsZero() {
			resetStr = acc.WeeklyResetAt.UTC().Format(time.RFC3339)
		}

		views = append(views, AccountView{
			AuthID:                      acc.AuthID,
			Provider:                    acc.Provider,
			WeeklyRemainingPercent:      acc.WeeklyRemainingPercent,
			WeeklyResetAt:               resetStr,
			WeeklyResetCountdownSeconds: countdownSec,
			ShortWindowRemainingPercent: acc.ShortWindowRemainingPercent,
			IsHealthy:                   acc.IsHealthy,
			IsBypassed:                  bypassed,
			EligibleStage:               stage,
		})
	}

	return StatusResponse{
		PluginID:          "quota-rotator",
		Enabled:           cfg.Enabled,
		StrictShortWindow: cfg.StrictShortWindow,
		RefreshInterval:   cfg.RefreshInterval,
		Providers:         cfg.Providers,
		ActiveStages:      stages,
		LastPickedIDs:     picks,
		Accounts:          views,
	}
}
