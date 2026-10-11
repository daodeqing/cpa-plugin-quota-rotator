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
		WeeklyResetAt:          now.Add(5 * 24 * time.Hour),
		IsHealthy:              true,
	})
	// Account B: 100% weekly quota
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-B",
		Provider:               "codex",
		WeeklyRemainingPercent: 100.0,
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
		WeeklyResetAt:          now.Add(3 * time.Hour),
		IsHealthy:              true,
	})
	// Account B: 99% remaining, resets in 4 days
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-B",
		Provider:               "codex",
		WeeklyRemainingPercent: 99.0,
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
		WeeklyResetAt:          now.Add(3 * time.Hour),
		IsHealthy:              true,
	})
	cache.Set(&quota.AccountQuotaInfo{
		AuthID:                 "acc-B",
		Provider:               "codex",
		WeeklyRemainingPercent: 90.0,
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
		WeeklyResetAt:          now.Add(7 * 24 * time.Hour),
		IsHealthy:              true,
	})

	// Pick should immediately recover back to Stage 1, prioritizing 100% account (acc-B)
	respAfter, stageAfter, idAfter := rot.Pick(req, now)
	if !respAfter.Handled || respAfter.AuthID != "acc-B" || stageAfter != StageFullCapacity {
		t.Fatalf("Expected recovery to Stage 1 acc-B, got %v (%s)", idAfter, stageAfter)
	}
}
