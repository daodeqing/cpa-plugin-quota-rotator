package management

import (
	"bytes"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/quota"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/scheduler"
)

type AccountView struct {
	AuthID                      string  `json:"auth_id"`
	AuthIndex                   string  `json:"auth_index,omitempty"`
	FileName                    string  `json:"file_name,omitempty"`
	Email                       string  `json:"email,omitempty"`
	Provider                    string  `json:"provider"`
	WeeklyRemainingPercent      float64 `json:"weekly_remaining_percent"`
	WeeklyResetAt               string  `json:"weekly_reset_at"`
	WeeklyResetCountdownSeconds int64   `json:"weekly_reset_countdown_seconds"`
	ShortWindowRemainingPercent float64 `json:"short_window_remaining_percent"`
	IsHealthy                   bool    `json:"is_healthy"`
	Disabled                    bool    `json:"disabled,omitempty"`
	IsBypassed                  bool    `json:"is_bypassed"`
	IsPinned                    bool    `json:"is_pinned"`
	PinnedPriority              *int    `json:"pinned_priority,omitempty"`
	ComputedPriority            int     `json:"computed_priority"`
	EligibleStage               string  `json:"eligible_stage"`
}

type StatusResponse struct {
	PluginID          string            `json:"plugin_id"`
	Enabled           bool              `json:"enabled"`
	StrictShortWindow bool              `json:"strict_short_window"`
	RefreshInterval   string            `json:"refresh_interval"`
	Providers         []string          `json:"providers"`
	ParallelCount     int               `json:"parallel_count"`
	PinnedAccounts    []string          `json:"pinned_accounts"`
	ActiveStages      map[string]string `json:"active_stages"`
	LastPickedIDs     map[string]string `json:"last_picked_ids"`
	Accounts          []AccountView     `json:"accounts"`
}

// BuildStatusResponse constructs the status payload for management queries,
// deduplicating AuthID/AuthIndex aliases and sorting by priority descending.
func BuildStatusResponse(cfg config.Config, rot *scheduler.Rotator, cache *quota.Cache, now time.Time) StatusResponse {
	stages, picks := rot.GetStateSnapshot()
	accounts := cache.GetAll()

	seen := make(map[string]bool, len(accounts))
	views := make([]AccountView, 0, len(accounts))

	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		if acc.FileName != "" && acc.AuthID == acc.AuthIndex && acc.FileName != acc.AuthIndex {
			if _, ok := cache.Get(acc.FileName); ok {
				continue
			}
		}
		dedupeKey := acc.Provider + ":" + acc.FileName + ":" + acc.AuthID
		if seen[dedupeKey] {
			continue
		}
		seen[dedupeKey] = true

		bypassed := acc.IsBypassed(cfg.StrictShortWindow)
		stage := "none"
		if acc.IsPinned {
			stage = scheduler.StagePinned
		} else if !bypassed {
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
			AuthIndex:                   acc.AuthIndex,
			FileName:                    acc.FileName,
			Email:                       acc.Email,
			Provider:                    acc.Provider,
			WeeklyRemainingPercent:      acc.WeeklyRemainingPercent,
			WeeklyResetAt:               resetStr,
			WeeklyResetCountdownSeconds: countdownSec,
			ShortWindowRemainingPercent: acc.ShortWindowRemainingPercent,
			IsHealthy:                   acc.IsHealthy,
			Disabled:                    acc.Disabled,
			IsBypassed:                  bypassed,
			IsPinned:                    acc.IsPinned,
			PinnedPriority:              acc.PinnedPriority,
			ComputedPriority:            acc.ComputedPriority,
			EligibleStage:               stage,
		})
	}

	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Provider != views[j].Provider {
			return views[i].Provider < views[j].Provider
		}
		if views[i].ComputedPriority != views[j].ComputedPriority {
			return views[i].ComputedPriority > views[j].ComputedPriority
		}
		if views[i].WeeklyResetCountdownSeconds != views[j].WeeklyResetCountdownSeconds {
			return views[i].WeeklyResetCountdownSeconds < views[j].WeeklyResetCountdownSeconds
		}
		return views[i].AuthID < views[j].AuthID
	})

	pinnedList := cfg.PinnedAccounts
	if pinnedList == nil {
		pinnedList = []string{}
	}

	return StatusResponse{
		PluginID:          "quota-rotator",
		Enabled:           cfg.Enabled,
		StrictShortWindow: cfg.StrictShortWindow,
		RefreshInterval:   cfg.RefreshInterval,
		Providers:         cfg.Providers,
		ParallelCount:     cfg.GetParallelCount(),
		PinnedAccounts:    pinnedList,
		ActiveStages:      stages,
		LastPickedIDs:     picks,
		Accounts:          views,
	}
}

func formatCountdownHuman(seconds int64) string {
	if seconds <= 0 {
		return "即将重置"
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	mins := (seconds % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%d天 %d小时 %d分", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%d小时 %d分", hours, mins)
	}
	return fmt.Sprintf("%d分钟", mins)
}

// RenderStatusPageHTML renders a read-only HTML dashboard for /v0/resource/plugins/quota-rotator/status.
func RenderStatusPageHTML(status StatusResponse) []byte {
	var buf bytes.Buffer
	buf.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>智能配额轮换看板</title>`)
	buf.WriteString(`<style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;margin:0;padding:24px;background:#0f172a;color:#e2e8f0;line-height:1.5}
.card{background:#1e293b;border:1px solid #334155;border-radius:12px;padding:20px;margin-bottom:20px}
h1{margin:0 0 8px;font-size:20px;color:#f8fafc}
.meta{display:flex;flex-wrap:wrap;gap:12px;margin-top:12px}
.badge{display:inline-flex;align-items:center;padding:4px 10px;border-radius:999px;font-size:12px;font-weight:600;background:#334155;color:#f1f5f9}
.badge.ok{background:#065f46;color:#a7f3d0}
.badge.pin{background:#92400e;color:#fde68a}
.badge.warn{background:#7f1d1d;color:#fecaca}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{text-align:left;padding:10px 12px;border-bottom:1px solid #334155}
th{color:#94a3b8;font-weight:600}
code{background:#0f172a;padding:2px 6px;border-radius:4px;font-family:monospace}
</style></head><body>`)
	buf.WriteString(`<div class="card"><h1>智能配额轮换调度器 (Smart Quota Rotator v1.2.1)</h1>`)
	buf.WriteString(`<div>规则：满额（100%）优先轮询；全员 ≤99% 时按周限刷新倒计时最短优先消耗；支持固定指定账号优先级。</div>`)
	buf.WriteString(`<div class="meta">`)
	if status.Enabled {
		buf.WriteString(`<span class="badge ok">运行中 (Enabled)</span>`)
	} else {
		buf.WriteString(`<span class="badge warn">已停用 (Disabled)</span>`)
	}
	buf.WriteString(fmt.Sprintf(`<span class="badge">并行临期账号数 (parallel_count): %d</span>`, status.ParallelCount))
	buf.WriteString(fmt.Sprintf(`<span class="badge">自动刷新间隔: %s</span>`, html.EscapeString(status.RefreshInterval)))
	if len(status.PinnedAccounts) > 0 {
		buf.WriteString(fmt.Sprintf(`<span class="badge pin">已固定优先级账号: %s</span>`, html.EscapeString(strings.Join(status.PinnedAccounts, ", "))))
	}
	buf.WriteString(`</div></div>`)

	buf.WriteString(`<div class="card"><table><thead><tr><th>渠道</th><th>账号 / 凭证文件</th><th>当前优先级</th><th>模式</th><th>周限剩余</th><th>距离周限重置</th><th>5h 短窗剩余</th><th>状态</th></tr></thead><tbody>`)
	for _, acc := range status.Accounts {
		label := acc.Email
		if label == "" {
			label = acc.FileName
		}
		if label == "" {
			label = acc.AuthID
		}
		modeBadge := `<span class="badge ok">自动轮换</span>`
		if acc.IsPinned {
			modeBadge = `<span class="badge pin">已固定优先级</span>`
		}
		stateText := "可用"
		if acc.Disabled {
			stateText = "已停用"
		} else if acc.IsBypassed {
			stateText = "0% 避让中"
		}
		buf.WriteString(fmt.Sprintf(`<tr><td><code>%s</code></td><td>%s</td><td><strong>优先级 %d</strong></td><td>%s</td><td>%.1f%%</td><td>%s</td><td>%.1f%%</td><td>%s</td></tr>`,
			html.EscapeString(acc.Provider),
			html.EscapeString(label),
			acc.ComputedPriority,
			modeBadge,
			acc.WeeklyRemainingPercent,
			html.EscapeString(formatCountdownHuman(acc.WeeklyResetCountdownSeconds)),
			acc.ShortWindowRemainingPercent,
			html.EscapeString(stateText),
		))
	}
	buf.WriteString(`</tbody></table></div></body></html>`)
	return buf.Bytes()
}
