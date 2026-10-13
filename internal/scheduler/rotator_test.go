package scheduler

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/quota"
)

func TestRotator_Scenario1_FullCapacityRoundRobin(t *testing.T) {
	cfg := config.DefaultConfig()
	cache := quota.NewCache()
	now := time.Now()

	// Account A: 100% weekly quota
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-A",
		Provider:               "codex",
		WeeklyRemainingPercent: 100.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:          now.Add(5 * 24 * time.Hour),
		IsHealthy:              true,
	})
	// Account B: 100% weekly quota
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-B",
		Provider:               "codex",
		WeeklyRemainingPercent: 100.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:          now.Add(2 * 24 * time.Hour),
		IsHealthy:              true,
	})

	rot := NewRotator(cfg, cache, nil)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-A", Priority: 1},
			{ID: "acc-B", Priority: 1},
		},
	}

	// 1st request -> acc-A
	resp1, stage1, id1 := rot.Pick(req, now)
	if !resp1.Handled || resp1.AuthID != "acc-A" || stage1 != StageFullCapacity {
		t.Fatalf("Pick 1 expected acc-A in Stage 1, got %v (%s)", id1, stage1)
	}

	// 2nd request -> acc-B (Round-Robin)
	resp2, stage2, id2 := rot.Pick(req, now)
	if !resp2.Handled || resp2.AuthID != "acc-B" || stage2 != StageFullCapacity {
		t.Fatalf("Pick 2 expected acc-B in Stage 1, got %v (%s)", id2, stage2)
	}

	// 3rd request -> acc-A (Round-Robin cycles back)
	resp3, _, id3 := rot.Pick(req, now)
	if !resp3.Handled || resp3.AuthID != "acc-A" {
		t.Fatalf("Pick 3 expected acc-A in Stage 1, got %v", id3)
	}
}

func TestRotator_Scenario2_ResetSoonPriorityAt99Percent(t *testing.T) {
	cfg := config.DefaultConfig()
	cache := quota.NewCache()
	now := time.Now()

	// Both accounts are at 99% or below
	// Account A: 99% remaining, resets in 3 hours
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-A",
		Provider:               "codex",
		WeeklyRemainingPercent: 99.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:          now.Add(3 * time.Hour),
		IsHealthy:              true,
	})
	// Account B: 99% remaining, resets in 4 days
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-B",
		Provider:               "codex",
		WeeklyRemainingPercent: 99.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:          now.Add(4 * 24 * time.Hour),
		IsHealthy:              true,
	})

	rot := NewRotator(cfg, cache, nil)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-B", Priority: 1},
			{ID: "acc-A", Priority: 1},
		},
	}

	// Since both are <= 99%, system switches to Stage 2: Reset-Soon First
	// acc-A has countdown 3h vs acc-B 4 days -> acc-A must be chosen
	for i := 0; i < 5; i++ {
		resp, stage, id := rot.Pick(req, now)
		if !resp.Handled || resp.AuthID != "acc-A" || stage != StageResetSoon {
			t.Fatalf("Pick %d expected acc-A (3h countdown), got %v (%s)", i, id, stage)
		}
	}
}

func TestRotator_Scenario3_BypassOnZeroQuotaOrShortWindow(t *testing.T) {
	cfg := config.DefaultConfig()
	cache := quota.NewCache()
	now := time.Now()

	// Account A: resets in 1 hour (shortest countdown), but 5h short window exhausted (0%)
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-A",
		Provider:                    "codex",
		WeeklyRemainingPercent:      90.0,
		ShortWindowRemainingPercent: 0.0,
		WeeklyResetAt:               now.Add(1 * time.Hour),
		IsHealthy:                   true,
	})
	// Account B: resets in 20 hours, healthy short window
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-B",
		Provider:                    "codex",
		WeeklyRemainingPercent:      85.0,
		ShortWindowRemainingPercent: 80.0,
		WeeklyResetAt:               now.Add(20 * time.Hour),
		IsHealthy:                   true,
	})

	rot := NewRotator(cfg, cache, nil)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-A", Priority: 1},
			{ID: "acc-B", Priority: 1},
		},
	}

	// acc-A is bypassed due to 0% short window -> traffic routes to acc-B
	resp, stage, id := rot.Pick(req, now)
	if !resp.Handled || resp.AuthID != "acc-B" || stage != StageResetSoon {
		t.Fatalf("Expected acc-B after acc-A bypass, got %v (%s)", id, stage)
	}
}

func TestRotator_Scenario4_RecoveryToStageOneWhenAccountResets(t *testing.T) {
	cfg := config.DefaultConfig()
	cache := quota.NewCache()
	now := time.Now()

	// Initial: Account A is 95% (3h reset), Account B is 90% (20h reset) -> Stage 2
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-A",
		Provider:               "codex",
		WeeklyRemainingPercent: 95.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:          now.Add(3 * time.Hour),
		IsHealthy:              true,
	})
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-B",
		Provider:               "codex",
		WeeklyRemainingPercent: 90.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:          now.Add(20 * time.Hour),
		IsHealthy:              true,
	})

	rot := NewRotator(cfg, cache, nil)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-A", Priority: 1},
			{ID: "acc-B", Priority: 1},
		},
	}

	// Verify it starts in Stage 2
	_, stageBefore, idBefore := rot.Pick(req, now)
	if stageBefore != StageResetSoon || idBefore != "acc-A" {
		t.Fatalf("Expected Stage 2 acc-A, got %s %s", stageBefore, idBefore)
	}

	// Now Account B weekly quota resets back to 100%!
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-B",
		Provider:               "codex",
		WeeklyRemainingPercent: 100.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:          now.Add(7 * 24 * time.Hour),
		IsHealthy:              true,
	})

	// Pick should immediately recover back to Stage 1, prioritizing 100% account (acc-B)
	respAfter, stageAfter, idAfter := rot.Pick(req, now)
	if !respAfter.Handled || respAfter.AuthID != "acc-B" || stageAfter != StageFullCapacity {
		t.Fatalf("Expected recovery to Stage 1 acc-B, got %v (%s)", idAfter, stageAfter)
	}
}

func TestRotator_Scenario5_ParallelResetSoonAccounts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ParallelCount = 2 // Allow top 2 reset-soon accounts to consume in parallel
	cache := quota.NewCache()
	now := time.Now()

	// 3 accounts at 99% weekly quota:
	// acc-A: 2h countdown
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-A",
		Provider:                    "codex",
		WeeklyRemainingPercent:      99.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:               now.Add(2 * time.Hour),
		IsHealthy:                   true,
	})
	// acc-B: 4h countdown
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-B",
		Provider:                    "codex",
		WeeklyRemainingPercent:      99.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:               now.Add(4 * time.Hour),
		IsHealthy:                   true,
	})
	// acc-C: 24h countdown
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-C",
		Provider:                    "codex",
		WeeklyRemainingPercent:      99.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:               now.Add(24 * time.Hour),
		IsHealthy:                   true,
	})

	rot := NewRotator(cfg, cache, nil)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-A", Priority: 1},
			{ID: "acc-B", Priority: 1},
			{ID: "acc-C", Priority: 1},
		},
	}

	// 1st request -> acc-A (shortest countdown)
	resp1, stage1, id1 := rot.Pick(req, now)
	if !resp1.Handled || resp1.AuthID != "acc-A" || stage1 != StageResetSoon {
		t.Fatalf("Pick 1 expected acc-A, got %s (%s)", id1, stage1)
	}

	// 2nd request -> acc-B (parallel top-2 round-robin)
	resp2, stage2, id2 := rot.Pick(req, now)
	if !resp2.Handled || resp2.AuthID != "acc-B" || stage2 != StageResetSoon {
		t.Fatalf("Pick 2 expected acc-B, got %s (%s)", id2, stage2)
	}

	// 3rd request -> acc-A (cycles back in top-2)
	resp3, _, id3 := rot.Pick(req, now)
	if !resp3.Handled || resp3.AuthID != "acc-A" {
		t.Fatalf("Pick 3 expected acc-A, got %s", id3)
	}

	// 4th request -> acc-B (acc-C is untouched)
	resp4, _, id4 := rot.Pick(req, now)
	if !resp4.Handled || resp4.AuthID != "acc-B" {
		t.Fatalf("Pick 4 expected acc-B, got %s", id4)
	}

	// Now acc-A short window exhausts (0%)!
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-A",
		Provider:                    "codex",
		WeeklyRemainingPercent:      95.0,
		ShortWindowRemainingPercent: 0.0,
		WeeklyResetAt:               now.Add(2 * time.Hour),
		IsHealthy:                   true,
	})

	// 5th request: acc-A is bypassed! New top-2 are acc-B and acc-C.
	// Current round-robin index picks acc-B
	resp5, _, id5 := rot.Pick(req, now)
	if !resp5.Handled || resp5.AuthID != "acc-B" {
		t.Fatalf("Pick 5 expected acc-B, got %s", id5)
	}

	// 6th request: round-robin within (B, C) -> picks acc-C!
	resp6, _, id6 := rot.Pick(req, now)
	if !resp6.Handled || resp6.AuthID != "acc-C" {
		t.Fatalf("Pick 6 expected acc-C, got %s", id6)
	}
}

func TestRotator_Scenario6_PinnedAccountPriority(t *testing.T) {
	cfg := config.DefaultConfig()
	// Pin acc-C to high priority 20, and pin acc-backup to low priority 0
	cfg.PinnedAccounts = []string{"acc-C=20", "acc-backup=0"}
	cache := quota.NewCache()
	now := time.Now()

	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-A",
		Provider:                    "antigravity",
		WeeklyRemainingPercent:      60.0,
		ShortWindowRemainingPercent: 90.0,
		WeeklyResetAt:               now.Add(4 * time.Hour),
		IsHealthy:                   true,
		ComputedPriority:            4,
	})
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-C",
		Provider:                    "antigravity",
		WeeklyRemainingPercent:      95.0,
		ShortWindowRemainingPercent: 95.0,
		WeeklyResetAt:               now.Add(5 * 24 * time.Hour),
		IsHealthy:                   true,
	})
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-backup",
		Provider:                    "antigravity",
		WeeklyRemainingPercent:      100.0,
		ShortWindowRemainingPercent: 100.0,
		WeeklyResetAt:               now.Add(7 * 24 * time.Hour),
		IsHealthy:                   true,
	})

	rot := NewRotator(cfg, cache, nil)
	req := pluginapi.SchedulerPickRequest{
		Provider: "antigravity",
		Model:    "gemini-3.8-flash-high",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-A", Priority: 4},
			{ID: "acc-C", Priority: 20},
			{ID: "acc-backup", Priority: 0},
		},
	}

	// 1. Pinned high-priority acc-C (20 > 4) must be selected first
	resp1, stage1, id1 := rot.Pick(req, now)
	if !resp1.Handled || id1 != "acc-C" || stage1 != StagePinned {
		t.Fatalf("Expected pinned high-priority acc-C, got %s (%s)", id1, stage1)
	}

	// 2. When acc-C short window exhausts, acc-A (auto-rotated priority 4) is picked,
	// while acc-backup (pinned at 0 < 4) is still held back as reserve!
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                      "acc-C",
		Provider:                    "antigravity",
		WeeklyRemainingPercent:      95.0,
		ShortWindowRemainingPercent: 0.0,
		WeeklyResetAt:               now.Add(5 * 24 * time.Hour),
		IsHealthy:                   true,
	})

	resp2, stage2, id2 := rot.Pick(req, now)
	if !resp2.Handled || id2 != "acc-A" || stage2 != StageResetSoon {
		t.Fatalf("Expected auto-rotated acc-A while acc-backup is pinned at 0, got %s (%s)", id2, stage2)
	}
}
