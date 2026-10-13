package quota

import (
	"encoding/json"
	"math"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"testing"
	"time"
)

func TestParseAntigravityQuotaDetailsJSON_ShortResetWeeklyBucket(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-10-13T02:11:00Z")
	// Weekly bucket resets in ~5 hours (<= 8h), which previously got misclassified as 5h short window!
	raw := []byte(`{
		"groups": [
			{
				"displayName": "Gemini Models",
				"buckets": [
					{"window": "weekly", "remainingFraction": 0.608981, "resetTime": "2026-10-13T07:09:30Z"},
					{"window": "5h", "remainingFraction": 0.7490297, "resetTime": "2026-10-13T04:58:22Z"}
				]
			},
			{
				"displayName": "Claude and GPT models",
				"buckets": [
					{"window": "weekly", "remainingFraction": 1.0, "resetTime": "2026-10-20T02:11:26Z"},
					{"window": "5h", "remainingFraction": 1.0, "resetTime": "2026-10-13T07:11:26Z"}
				]
			}
		]
	}`)

	weeklyRem, weeklyReset, shortRem, shortReset, ok := ParseAntigravityQuotaDetailsJSON(raw, "gemini", now)
	if !ok {
		t.Fatalf("expected ParseAntigravityQuotaDetailsJSON to succeed")
	}
	if math.Abs(weeklyRem-60.8981) > 0.01 {
		t.Fatalf("expected weeklyRemaining ~60.8981%%, got %.4f%%", weeklyRem)
	}
	if weeklyReset == nil || weeklyReset.Format(time.RFC3339) != "2026-10-13T07:09:30Z" {
		t.Fatalf("expected weeklyReset 2026-10-13T07:09:30Z, got %v", weeklyReset)
	}
	if math.Abs(shortRem-74.90297) > 0.01 {
		t.Fatalf("expected shortRemaining ~74.90297%%, got %.4f%%", shortRem)
	}
	if shortReset == nil || shortReset.Format(time.RFC3339) != "2026-10-13T04:58:22Z" {
		t.Fatalf("expected shortReset 2026-10-13T04:58:22Z, got %v", shortReset)
	}
}

type mockHostCaller struct {
	files     []map[string]any
	savedDocs map[string]map[string]any
}

func (m *mockHostCaller) Call(method string, request []byte) ([]byte, error) {
	switch method {
	case "host.auth.list":
		return mustJSON(map[string]any{
			"ok":     true,
			"result": map[string]any{"files": m.files},
		}), nil
	case "host.auth.get":
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		_ = json.Unmarshal(request, &req)
		for _, f := range m.files {
			if f["auth_index"] == req.AuthIndex {
				name := f["name"].(string)
				doc := m.savedDocs[name]
				return mustJSON(map[string]any{
					"ok": true,
					"result": map[string]any{
						"auth_index": req.AuthIndex,
						"name":       name,
						"json":       doc,
					},
				}), nil
			}
		}
		return mustJSON(map[string]any{"ok": false}), nil
	case "host.auth.save":
		var req struct {
			Name string         `json:"name"`
			JSON map[string]any `json:"json"`
		}
		_ = json.Unmarshal(request, &req)
		m.savedDocs[req.Name] = req.JSON
		return mustJSON(map[string]any{
			"ok":     true,
			"result": map[string]any{"name": req.Name, "path": req.Name},
		}), nil
	default:
		return mustJSON(map[string]any{"ok": false}), nil
	}
}

func TestSyncPrioritiesToHost_AndPinAccount(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-10-13T02:00:00Z")
	cache := NewCache()

	caller := &mockHostCaller{
		files: []map[string]any{
			{"id": "acc1.json", "name": "acc1.json", "auth_index": "idx1", "provider": "antigravity", "email": "u1@gmail.com", "priority": 2},
			{"id": "acc2.json", "name": "acc2.json", "auth_index": "idx2", "provider": "antigravity", "email": "u2@gmail.com", "priority": 2},
			{"id": "acc3.json", "name": "acc3.json", "auth_index": "idx3", "provider": "antigravity", "email": "u3@gmail.com", "priority": 2},
			{"id": "acc4.json", "name": "acc4.json", "auth_index": "idx4", "provider": "antigravity", "email": "u4@gmail.com", "priority": 9},
		},
		savedDocs: map[string]map[string]any{
			"acc1.json": {"email": "u1@gmail.com", "priority": 2},
			"acc2.json": {"email": "u2@gmail.com", "priority": 2},
			"acc3.json": {"email": "u3@gmail.com", "priority": 2},
			"acc4.json": {"email": "u4@gmail.com", "priority": 9},
		},
	}

	// acc1: resets in 4h (shortest)
	cache.Set(&AccountQuotaInfo{AuthID: "acc1.json", Provider: "antigravity", WeeklyRemainingPercent: 60, ShortWindowRemainingPercent: 80, WeeklyResetAt: now.Add(4 * time.Hour), IsHealthy: true})
	// acc2: resets in 24h (2nd shortest)
	cache.Set(&AccountQuotaInfo{AuthID: "acc2.json", Provider: "antigravity", WeeklyRemainingPercent: 90, ShortWindowRemainingPercent: 90, WeeklyResetAt: now.Add(24 * time.Hour), IsHealthy: true})
	// acc3: 0% exhausted
	cache.Set(&AccountQuotaInfo{AuthID: "acc3.json", Provider: "antigravity", WeeklyRemainingPercent: 0, ShortWindowRemainingPercent: 80, WeeklyResetAt: now.Add(12 * time.Hour), IsHealthy: true})
	// acc4: pinned at priority 9
	cache.Set(&AccountQuotaInfo{AuthID: "acc4.json", Provider: "antigravity", WeeklyRemainingPercent: 98, ShortWindowRemainingPercent: 95, WeeklyResetAt: now.Add(100 * time.Hour), IsHealthy: true})

	cfg := config.DefaultConfig()
	cfg.ParallelCount = 1
	cfg.PinnedAccounts = []string{"u4@gmail.com"}

	updated, err := SyncPrioritiesToHost(caller, cache, cfg, now)
	if err != nil {
		t.Fatalf("SyncPrioritiesToHost failed: %v", err)
	}
	if updated == 0 {
		t.Fatalf("expected priorities to be updated")
	}

	// 2 healthy non-pinned accounts (acc1, acc2) -> topPriority = 3
	// acc1 (4h reset) -> 3, acc2 (24h reset) -> 2, acc3 (0% exhausted) -> 1, acc4 (pinned) -> stays 9
	if got := func() int { v, _ := parseDocInt(caller.savedDocs["acc1.json"]["priority"]); return v }(); got != 3 {
		t.Fatalf("expected acc1 priority 3, got %d", got)
	}
	if got := func() int { v, _ := parseDocInt(caller.savedDocs["acc2.json"]["priority"]); return v }(); got != 2 {
		t.Fatalf("expected acc2 priority 2, got %d", got)
	}
	if got := func() int { v, _ := parseDocInt(caller.savedDocs["acc3.json"]["priority"]); return v }(); got != 1 {
		t.Fatalf("expected acc3 priority 1, got %d", got)
	}
	if got := func() int { v, _ := parseDocInt(caller.savedDocs["acc4.json"]["priority"]); return v }(); got != 9 {
		t.Fatalf("expected pinned acc4 priority 9, got %d", got)
	}
}
