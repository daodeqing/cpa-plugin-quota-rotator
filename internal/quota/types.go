package quota

import (
	"time"
)

// AccountQuotaInfo represents normalized quota snapshot for a single account.
type AccountQuotaInfo struct {
	AuthID                      string    `json:"auth_id"`
	AuthIndex                   string    `json:"auth_index,omitempty"`
	FileName                    string    `json:"file_name,omitempty"`
	Email                       string    `json:"email,omitempty"`
	Provider                    string    `json:"provider"`
	WeeklyRemainingPercent      float64   `json:"weekly_remaining_percent"`       // 0.0 to 100.0
	WeeklyResetAt               time.Time `json:"weekly_reset_at"`
	ShortWindowRemainingPercent float64   `json:"short_window_remaining_percent"` // 5h window (0.0 to 100.0)
	ShortWindowResetAt          time.Time `json:"short_window_reset_at"`
	IsHealthy                   bool      `json:"is_healthy"`
	Disabled                    bool      `json:"disabled,omitempty"`
	IsPinned                    bool      `json:"is_pinned"`
	PinnedPriority              *int      `json:"pinned_priority,omitempty"`
	ComputedPriority            int       `json:"computed_priority"`
	StatusMessage               string    `json:"status_message,omitempty"`
	ObservedAt                  time.Time `json:"observed_at"`
}

// ResetCountdown returns remaining duration until weekly reset from reference time 'now'.
func (a *AccountQuotaInfo) ResetCountdown(now time.Time) time.Duration {
	if a == nil || a.WeeklyResetAt.IsZero() {
		return 7 * 24 * time.Hour
	}
	diff := a.WeeklyResetAt.Sub(now)
	if diff < 0 {
		return 0
	}
	return diff
}

// IsStageOneEligible returns true if weekly remaining quota is 100% (full capacity).
func (a *AccountQuotaInfo) IsStageOneEligible() bool {
	if a == nil {
		return false
	}
	// Consider 100% (or very close with floating precision >= 99.999) as full
	return a.WeeklyRemainingPercent >= 99.999
}

// IsBypassed returns true if the account must be avoided/downgraded.
func (a *AccountQuotaInfo) IsBypassed(strictShortWindow bool) bool {
	if a == nil {
		return true
	}
	if !a.IsHealthy {
		return true
	}
	// Weekly quota completely exhausted (0%)
	if a.WeeklyRemainingPercent <= 0.001 {
		return true
	}
	// Short window (5 hours) completely exhausted (0%)
	if strictShortWindow && a.ShortWindowRemainingPercent <= 0.001 {
		return true
	}
	return false
}
