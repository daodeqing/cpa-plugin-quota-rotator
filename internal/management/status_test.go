package management

import (
	"strings"
	"testing"
	"time"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/quota"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/scheduler"
)

func TestFailedQuotaAccountIsVisibleWithUnknownValues(t *testing.T) {
	cfg := config.DefaultConfig()
	cache := quota.NewCache()
	cache.Set(&quota.AccountQuotaInfo{AuthID: "account.json", FileName: "account.json", Provider: "antigravity", QuotaError: "查询失败 <script>alert(1)</script>"})
	status := BuildStatusResponse(cfg, scheduler.NewRotator(cfg, cache, nil), cache, time.Now())
	if len(status.Accounts) != 1 || status.Accounts[0].QuotaError == "" || status.Accounts[0].QuotaStale {
		t.Fatalf("failed account missing: %+v", status.Accounts)
	}
	page := string(RenderStatusPageHTML(status, true))
	if !strings.Contains(page, "查询失败") || !strings.Contains(page, "<strong>—</strong>") || strings.Contains(page, "<strong>0.0%</strong>") {
		t.Fatal("unknown quota rendered as a real quota")
	}
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Fatal("unescaped quota error")
	}
}

func TestFailedQuotaMarksLastSuccessfulSnapshot(t *testing.T) {
	cfg := config.DefaultConfig()
	cache := quota.NewCache()
	now := time.Now()
	cache.Set(&quota.AccountQuotaInfo{AuthID: "account.json", Provider: "antigravity", WeeklyRemainingPercent: 72, ShortWindowRemainingPercent: 43, WeeklyResetAt: now.Add(time.Hour), ObservedAt: now.Add(-time.Minute), QuotaError: "配额查询失败"})
	status := BuildStatusResponse(cfg, scheduler.NewRotator(cfg, cache, nil), cache, now)
	page := string(RenderStatusPageHTML(status, false))
	if !status.Accounts[0].QuotaStale || !strings.Contains(page, "上次数据") || !strings.Contains(page, "72.0%") {
		t.Fatal("stale snapshot not labelled")
	}
}

func TestPinControlsUseProviderAndUniqueCredentialInsteadOfEmail(t *testing.T) {
	status := StatusResponse{Accounts: []AccountView{
		{Provider: "antigravity", Email: "shared@example.com", AuthID: "ag-file.json", AuthIndex: "ag-index", RawLabel: "shared@example.com", MaskedLabel: "s***@e***.com"},
		{Provider: "codex", Email: "shared@example.com", AuthID: "codex-file.json", AuthIndex: "codex-index", RawLabel: "shared@example.com", MaskedLabel: "s***@e***.com"},
	}}
	page := string(RenderStatusPageHTML(status, true))
	for _, attributes := range []string{
		`data-account="ag-index" data-provider="antigravity"`,
		`data-account="codex-index" data-provider="codex"`,
	} {
		if !strings.Contains(page, attributes) {
			t.Fatalf("missing isolated control: %s", attributes)
		}
	}
	if strings.Contains(page, `data-account="shared@example.com"`) {
		t.Fatal("control still submits an ambiguous email")
	}
	if !strings.Contains(page, "!r.ok || data.error") {
		t.Fatal("failed save still reported as success")
	}
}
