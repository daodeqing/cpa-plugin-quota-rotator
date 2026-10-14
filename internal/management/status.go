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
	RawLabel                    string  `json:"raw_label"`
	MaskedLabel                 string  `json:"masked_label"`
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
	RoutingStrategy   string            `json:"routing_strategy"`
	ActiveStages      map[string]string `json:"active_stages"`
	LastPickedIDs     map[string]string `json:"last_picked_ids"`
	Accounts          []AccountView     `json:"accounts"`
}

const maskPattern = "***"

func keepPrefix(value string, visible int) string {
	if visible > len(value) {
		return value
	}
	return value[:visible]
}

// MaskEmail masks email addresses consistent with EasyCLIProxyAPI privacy mode.
func MaskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return MaskIdentifier(email)
	}
	local := email[:at]
	domain := email[at+1:]
	dot := strings.LastIndex(domain, ".")
	host := domain
	suffix := ""
	if dot > 0 {
		host = domain[:dot]
		suffix = domain[dot:]
	}
	localVis := 1
	if len(local) > 4 {
		localVis = 2
	}
	return fmt.Sprintf("%s%s@%s%s%s", keepPrefix(local, localVis), maskPattern, keepPrefix(host, 1), maskPattern, suffix)
}

// MaskIdentifier masks opaque account identifiers or emails for privacy mode.
func MaskIdentifier(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return value
	}
	if strings.Contains(text, "@") {
		return MaskEmail(text)
	}
	if len(text) <= 4 {
		return maskPattern
	}
	visible := 1
	if len(text) > 10 {
		visible = 2
	}
	return fmt.Sprintf("%s%s%s", text[:visible], maskPattern, text[len(text)-visible:])
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

		rawLabel := acc.Email
		if rawLabel == "" {
			rawLabel = acc.FileName
		}
		if rawLabel == "" {
			rawLabel = acc.AuthID
		}
		maskedLabel := MaskIdentifier(rawLabel)

		views = append(views, AccountView{
			AuthID:                      acc.AuthID,
			AuthIndex:                   acc.AuthIndex,
			FileName:                    acc.FileName,
			Email:                       acc.Email,
			RawLabel:                    rawLabel,
			MaskedLabel:                 maskedLabel,
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
		RoutingStrategy:   cfg.GetRoutingStrategy(),
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

// RenderStatusPageHTML renders an interactive HTML dashboard for /v0/resource/plugins/quota-rotator/status.
func strategyDisplayLabel(strategy string) string {
	switch strategy {
	case "quota_high_first":
		return "优先高配额"
	case "quota_low_first":
		return "优先低配额"
	case "plan_high_first":
		return "优先高订阅"
	case "plan_low_first":
		return "优先低订阅"
	case "expiry_soon_first":
		return "优先近到期"
	case "custom":
		return "自定义"
	default:
		return "自动（推荐）"
	}
}

// RenderStatusPageHTML renders an interactive HTML dashboard for /v0/resource/plugins/quota-rotator/status.
func RenderStatusPageHTML(status StatusResponse, defaultPrivacy bool) []byte {
	currentStrategy := status.RoutingStrategy
	if currentStrategy == "" {
		if len(status.PinnedAccounts) > 0 {
			currentStrategy = "custom"
		} else {
			currentStrategy = "auto"
		}
	}
	strategyLabel := strategyDisplayLabel(currentStrategy)


	var buf bytes.Buffer
	buf.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>智能配额轮换调度器 (Smart Quota Rotator)</title>
<style>
:root {
  --bg-main: #0b0f19;
  --bg-card: #151e2e;
  --bg-card-subtle: #1e293b;
  --border-color: #334155;
  --text-primary: #f8fafc;
  --text-secondary: #94a3b8;
  --accent-blue: #3b82f6;
  --accent-cyan: #38bdf8;
  --accent-green: #10b981;
  --accent-amber: #f59e0b;
  --accent-red: #ef4444;
}
* { box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", sans-serif;
  margin: 0; padding: 20px;
  background: var(--bg-main); color: var(--text-primary);
  line-height: 1.5; font-size: 13px;
}
.card {
  background: var(--bg-card);
  border: 1px solid var(--border-color);
  border-radius: 12px;
  padding: 18px 20px;
  margin-bottom: 18px;
  box-shadow: 0 4px 14px rgba(0,0,0,0.3);
}
.header-row {
  display: flex; justify-content: space-between; align-items: flex-start;
  flex-wrap: wrap; gap: 14px;
}
h1 {
  margin: 0 0 6px; font-size: 19px; font-weight: 700;
  color: #fff; display: flex; align-items: center; gap: 8px;
}
.desc { color: var(--text-secondary); font-size: 13px; margin-bottom: 14px; }
.header-actions {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
}
.btn {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 7px 14px; border-radius: 8px; font-size: 13px; font-weight: 600;
  cursor: pointer; border: 1px solid transparent; transition: all 0.15s ease;
  text-decoration: none;
}
.btn-secondary {
  background: #1e293b; color: #cbd5e1; border-color: #475569;
}
.btn-secondary:hover { background: #334155; color: #fff; border-color: #64748b; }
.btn-icon {
  padding: 7px 10px; background: #1e293b; color: #cbd5e1; border: 1px solid #475569; border-radius: 8px;
}
.btn-icon:hover { background: #334155; color: #fff; }
.btn-active { background: #065f46; color: #a7f3d0; border-color: #059669; }

/* Custom Strategy Dropdown matching user screenshot */
.strategy-dropdown-container {
  position: relative;
  display: inline-block;
}
.strategy-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 16px;
  background: #1e293b;
  color: #f8fafc;
  border: 1.5px solid var(--accent-blue);
  border-radius: 9999px;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.2s ease;
  box-shadow: 0 0 0 1px rgba(59, 130, 246, 0.2);
}
.strategy-btn:hover {
  background: #253347;
  border-color: #60a5fa;
  box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.3);
}
.caret-icon {
  font-size: 11px;
  transition: transform 0.2s ease;
  color: #60a5fa;
}
.strategy-btn.open .caret-icon {
  transform: rotate(180deg);
}
.strategy-menu {
  display: none;
  position: absolute;
  right: 0;
  top: calc(100% + 8px);
  z-index: 1000;
  background: #ffffff;
  color: #0f172a;
  border: 1px solid #cbd5e1;
  border-radius: 12px;
  box-shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.25), 0 8px 10px -6px rgba(0, 0, 0, 0.15);
  min-width: 160px;
  padding: 6px 0;
  animation: popIn 0.15s ease-out;
}
.strategy-menu.show {
  display: block;
}
@keyframes popIn {
  from { opacity: 0; transform: translateY(-6px); }
  to { opacity: 1; transform: translateY(0); }
}
.strategy-item {
  padding: 9px 18px;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  color: #1e293b;
  transition: background 0.12s, color 0.12s;
}
.strategy-item:hover {
  background: #f1f5f9;
  color: var(--accent-blue);
}
.strategy-item.active {
  background: #eff6ff;
  color: var(--accent-blue);
  font-weight: 700;
  position: relative;
}
.strategy-item.active::after {
  content: "✓";
  position: absolute;
  right: 14px;
  font-weight: bold;
}

.top-controls {
  display: flex; align-items: center; justify-content: space-between;
  flex-wrap: wrap; gap: 14px;
  background: var(--bg-card-subtle);
  border: 1px solid #3b4c63;
  border-radius: 10px;
  padding: 12px 16px;
  margin-top: 10px;
}
.control-item { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.control-label { font-weight: 600; color: #e2e8f0; font-size: 13px; }
.select-styled {
  background: #0f172a; color: #f8fafc;
  border: 1px solid #475569; border-radius: 6px;
  padding: 6px 12px; font-size: 13px; font-weight: 500;
  cursor: pointer; outline: none; transition: all 0.15s ease;
}
.select-styled:hover, .select-styled:focus {
  border-color: var(--accent-cyan);
  box-shadow: 0 0 0 2px rgba(56, 189, 248, 0.2);
}
.badge {
  display: inline-flex; align-items: center; padding: 3px 8px;
  border-radius: 999px; font-size: 11px; font-weight: 600;
  background: #334155; color: #cbd5e1;
}
.badge.ok { background: #064e3b; color: #6ee7b7; border: 1px solid #059669; }
.badge.pin { background: #78350f; color: #fde68a; border: 1px solid #d97706; }
.badge.warn { background: #7f1d1d; color: #fecaca; border: 1px solid #dc2626; }
.badge.info { background: #0c4a6e; color: #7dd3fc; border: 1px solid #0284c7; }
.meta-badges { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th, td { text-align: left; padding: 11px 12px; border-bottom: 1px solid #283548; vertical-align: middle; }
th { color: #94a3b8; font-weight: 600; font-size: 12px; background: rgba(15,23,42,0.4); }
tr:hover td { background: rgba(30, 41, 59, 0.4); }
code {
  background: #0f172a; padding: 3px 7px; border-radius: 4px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px; color: #e2e8f0; border: 1px solid #1e293b;
}
.priority-select {
  background: #0f172a; color: #f1f5f9; border: 1px solid #475569;
  border-radius: 6px; padding: 5px 8px; font-size: 12px; font-weight: 600;
  cursor: pointer; outline: none; transition: border-color 0.15s;
}
.priority-select.is-pinned {
  border-color: #f59e0b; color: #fef3c7; background: #292524;
}
.priority-select:focus { border-color: var(--accent-cyan); }
.account-label { font-weight: 500; color: #f8fafc; font-family: ui-monospace, monospace; }
.toast {
  position: fixed; top: 20px; right: 24px; z-index: 9999;
  background: #1e293b; color: #fff; padding: 10px 18px; border-radius: 8px;
  border: 1px solid var(--accent-cyan); box-shadow: 0 8px 24px rgba(0,0,0,0.5);
  font-size: 13px; font-weight: 600; transform: translateY(-30px);
  opacity: 0; pointer-events: none; transition: all 0.25s ease;
}
.toast.show { transform: translateY(0); opacity: 1; pointer-events: auto; }
.toast.success { border-color: var(--accent-green); background: #064e3b; color: #ecfdf5; }
.toast.error { border-color: var(--accent-red); background: #7f1d1d; color: #fef2f2; }
</style>
</head>
<body>
<div id="toast" class="toast"></div>

<div class="card">
  <div class="header-row">
    <div>
      <h1><span>⚡</span> 智能配额轮换调度器 (Smart Quota Rotator v1.2.3)</h1>
      <div class="desc">两阶段自动轮换：满额 (100%) 优先轮询；全员 ≤99% 时按周限刷新倒计时最短优先消耗；支持多号并行防打爆与固定指定账号优先级。</div>
    </div>
    <div class="header-actions">
      <button class="btn btn-secondary" onclick="window.open('https://github.com/daodeqing/cpa-plugin-quota-rotator', '_blank')">↗ 查看全部功能</button>
      <button class="btn btn-icon" onclick="location.reload()" title="刷新数据">🔄</button>
      
      <!-- Strategy Dropdown matching user screenshot -->
      <div class="strategy-dropdown-container">
        <button type="button" class="strategy-btn" id="strategyBtn" onclick="toggleStrategyMenu(event)">
          <span id="strategyBtnLabel">` + html.EscapeString(strategyLabel) + `
          <span class="caret-icon">▲</span>
        </button>
        <div class="strategy-menu" id="strategyMenu">`)


	strategyOptions := []struct {
		Val   string
		Label string
	}{
		{"auto", "自动（推荐）"},
		{"quota_high_first", "优先高配额"},
		{"quota_low_first", "优先低配额"},
		{"plan_high_first", "优先高订阅"},
		{"plan_low_first", "优先低订阅"},
		{"expiry_soon_first", "优先近到期"},
		{"custom", "自定义"},
	}

	for _, opt := range strategyOptions {
		activeClass := ""
		if opt.Val == currentStrategy {
			activeClass = " active"
		}
		buf.WriteString(fmt.Sprintf(`<div class="strategy-item%s" data-val="%s" onclick="selectStrategy('%s')">%s</div>`, activeClass, opt.Val, opt.Val, opt.Label))
	}

	buf.WriteString(`        </div>
      </div>
    </div>
  </div>

  <div class="top-controls">
    <div class="control-item">
      <label class="control-label" for="parallelSelect">⚡ 并行临期账号数 (parallel_count):</label>
      <select id="parallelSelect" class="select-styled" onchange="onParallelChange(this.value)">`)

	for _, countOpt := range []int{1, 2, 3, 4, 5, 6, 8} {
		selected := ""
		if countOpt == status.ParallelCount {
			selected = " selected"
		}
		labelExtra := ""
		if countOpt == 1 {
			labelExtra = " (单号串行，极限榨干)"
		} else if countOpt == 2 {
			labelExtra = " (双号并行，推荐防打爆)"
		} else if countOpt == 3 {
			labelExtra = " (三号并行，高并发分流)"
		}
		buf.WriteString(fmt.Sprintf(`<option value="%d"%s>%d 个账号%s</option>`, countOpt, selected, countOpt, labelExtra))
	}

	privacyBtnClass := "btn btn-secondary"
	privacyText := "已关闭"
	if defaultPrivacy {
		privacyBtnClass = "btn btn-active"
		privacyText = "已开启"
	}

	buf.WriteString(fmt.Sprintf(`</select>
    </div>
    <div class="control-item">
      <button id="privacyBtn" class="%s" onclick="togglePrivacy()">👁️ 隐私模式: <span id="privacyStatus">%s</span></button>
      <button class="btn btn-secondary" onclick="location.reload()">🔄 刷新列表</button>
    </div>
  </div>

  <div class="meta-badges">`, privacyBtnClass, privacyText))

	if status.Enabled {
		buf.WriteString(`<span class="badge ok">● 运行中 (Enabled)</span>`)
	} else {
		buf.WriteString(`<span class="badge warn">● 已停用 (Disabled)</span>`)
	}
	buf.WriteString(fmt.Sprintf(`<span class="badge info">当前策略: %s</span>`, html.EscapeString(strategyLabel)))
	buf.WriteString(fmt.Sprintf(`<span class="badge info">并行临期数: %d</span>`, status.ParallelCount))
	buf.WriteString(fmt.Sprintf(`<span class="badge">自动刷新间隔: %s</span>`, html.EscapeString(status.RefreshInterval)))
	pinnedCount := len(status.PinnedAccounts)
	if pinnedCount > 0 {
		buf.WriteString(fmt.Sprintf(`<span class="badge pin">已固定优先级: %d 个账号</span>`, pinnedCount))
	}
	buf.WriteString(`</div>
</div>

<div class="card">
  <table>
    <thead>
      <tr>
        <th>渠道</th>
        <th>账号 / 凭证文件</th>
        <th>当前优先级</th>
        <th>调度模式 / 固定优先级</th>
        <th>周限剩余</th>
        <th>距离周限重置</th>
        <th>5h 短窗剩余</th>
        <th>状态</th>
      </tr>
    </thead>
    <tbody>`)

	for _, acc := range status.Accounts {
		initialLabel := acc.RawLabel
		if defaultPrivacy {
			initialLabel = acc.MaskedLabel
		}

		selectClass := "priority-select"
		if acc.IsPinned {
			selectClass += " is-pinned"
		}

		priorityTag := ""
		if acc.IsPinned {
			priorityTag = fmt.Sprintf(`<span class="badge pin">📌 优先级 %d (固定)</span>`, acc.ComputedPriority)
		} else {
			priorityTag = fmt.Sprintf(`<span class="badge ok">🔄 优先级 %d (动态)</span>`, acc.ComputedPriority)
		}

		stateText := `<span class="badge ok">可用</span>`
		if acc.Disabled {
			stateText = `<span class="badge warn">已停用</span>`
		} else if acc.IsBypassed {
			stateText = `<span class="badge warn">0% 避让中</span>`
		}

		// Options for priority selector
		var optBuf bytes.Buffer
		autoSelected := ""
		if !acc.IsPinned {
			autoSelected = " selected"
		}
		optBuf.WriteString(fmt.Sprintf(`<option value="auto"%s>🔄 自动（推荐）</option>`, autoSelected))
		for p := 10; p >= 1; p-- {
			sel := ""
			if acc.IsPinned && acc.PinnedPriority != nil && *acc.PinnedPriority == p {
				sel = " selected"
			}
			label := fmt.Sprintf("%d", p)
			if p == 10 {
				label += " (最高)"
			} else if p == 1 {
				label += " (最低)"
			}
			optBuf.WriteString(fmt.Sprintf(`<option value="%d"%s>📌 固定优先级: %s</option>`, p, sel, label))
		}

		targetAccount := acc.Email
		if targetAccount == "" {
			targetAccount = acc.FileName
		}
		if targetAccount == "" {
			targetAccount = acc.AuthID
		}

		buf.WriteString(fmt.Sprintf(`<tr>
  <td><code>%s</code></td>
  <td><span class="account-label" data-raw="%s" data-masked="%s">%s</span></td>
  <td>%s</td>
  <td><select class="%s" data-account="%s" onchange="onPriorityChange(this)">%s</select></td>
  <td><strong>%.1f%%</strong></td>
  <td>%s</td>
  <td>%.1f%%</td>
  <td>%s</td>
</tr>`,
			html.EscapeString(acc.Provider),
			html.EscapeString(acc.RawLabel),
			html.EscapeString(acc.MaskedLabel),
			html.EscapeString(initialLabel),
			priorityTag,
			selectClass,
			html.EscapeString(targetAccount),
			optBuf.String(),
			acc.WeeklyRemainingPercent,
			html.EscapeString(formatCountdownHuman(acc.WeeklyResetCountdownSeconds)),
			acc.ShortWindowRemainingPercent,
			stateText,
		))
	}

	buf.WriteString(fmt.Sprintf(`    </tbody>
  </table>
</div>

<script>
let privacyEnabled = %t;
try {
  const localVal = localStorage.getItem('cpa_rotator_privacy');
  if (localVal !== null) {
    privacyEnabled = localVal === '1';
  }
} catch (e) {}

function applyPrivacy(enabled) {
  privacyEnabled = enabled;
  document.querySelectorAll('.account-label').forEach(el => {
    el.textContent = enabled ? el.getAttribute('data-masked') : el.getAttribute('data-raw');
  });
  const statusSpan = document.getElementById('privacyStatus');
  if (statusSpan) {
    statusSpan.textContent = enabled ? '已开启' : '已关闭';
  }
  const btn = document.getElementById('privacyBtn');
  if (btn) {
    btn.className = 'btn ' + (enabled ? 'btn-active' : 'btn-secondary');
  }
  try {
    localStorage.setItem('cpa_rotator_privacy', enabled ? '1' : '0');
  } catch (e) {}
}

function togglePrivacy() {
  applyPrivacy(!privacyEnabled);
}

// Initial application on load to respect localStorage preference if present
applyPrivacy(privacyEnabled);

function showToast(msg, type = 'info') {
  const el = document.getElementById('toast');
  if (!el) return;
  el.textContent = msg;
  el.className = 'toast show ' + type;
  setTimeout(() => {
    el.className = 'toast ' + type;
  }, 2500);
}

// Dropdown toggle
function toggleStrategyMenu(e) {
  e.stopPropagation();
  const menu = document.getElementById('strategyMenu');
  const btn = document.getElementById('strategyBtn');
  if (!menu || !btn) return;
  const isShow = menu.classList.contains('show');
  if (isShow) {
    menu.classList.remove('show');
    btn.classList.remove('open');
  } else {
    menu.classList.add('show');
    btn.classList.add('open');
  }
}

document.addEventListener('click', (e) => {
  const menu = document.getElementById('strategyMenu');
  const btn = document.getElementById('strategyBtn');
  if (menu && menu.classList.contains('show')) {
    if (!menu.contains(e.target) && !btn.contains(e.target)) {
      menu.classList.remove('show');
      btn.classList.remove('open');
    }
  }
});

function selectStrategy(val) {
  const menu = document.getElementById('strategyMenu');
  const btn = document.getElementById('strategyBtn');
  if (menu) menu.classList.remove('show');
  if (btn) btn.classList.remove('open');

  showToast('正在切换调度策略...');
  fetch(window.location.pathname + '?action=set_strategy&strategy=' + encodeURIComponent(val) + '&format=json')
    .then(r => r.json())
    .then(res => {
      showToast('调度策略已切换成功！', 'success');
      setTimeout(() => location.reload(), 500);
    })
    .catch(err => {
      showToast('切换失败: ' + err, 'error');
    });
}

function onParallelChange(val) {
  const count = parseInt(val, 10);
  if (!count || count < 1) return;
  showToast('正在更新并行账号数...');
  fetch(window.location.pathname + '?action=set_parallel&count=' + encodeURIComponent(count) + '&format=json')
    .then(r => r.json())
    .then(data => {
      showToast('并行账号数已更新为 ' + count + '！', 'success');
      setTimeout(() => location.reload(), 500);
    })
    .catch(err => {
      showToast('更新失败: ' + err, 'error');
    });
}

function onPriorityChange(sel) {
  const account = sel.getAttribute('data-account');
  const val = sel.value;
  showToast('正在保存账号优先级...');
  let url = window.location.pathname + '?format=json';
  if (val === 'auto') {
    url += '&action=unpin&account=' + encodeURIComponent(account);
  } else {
    url += '&action=pin&account=' + encodeURIComponent(account) + '&priority=' + encodeURIComponent(val);
  }
  fetch(url)
    .then(r => r.json())
    .then(data => {
      showToast(val === 'auto' ? '账号已恢复自动轮换！' : '账号已成功固定优先级为 ' + val + '！', 'success');
      setTimeout(() => location.reload(), 500);
    })
    .catch(err => {
      showToast('设置失败: ' + err, 'error');
    });
}
</script>
</body>
</html>`, defaultPrivacy))

	return buf.Bytes()
}
