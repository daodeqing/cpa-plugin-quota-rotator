package scheduler

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/quota"
)

const (
	StageFullCapacity = "stage_1_full"       // Stage 1: WeeklyRemaining == 100%, Round-Robin
	StageResetSoon    = "stage_2_reset_soon"  // Stage 2: WeeklyRemaining <= 99%, Reset-Soon First
	StageNone         = "none"
)

// CandidateWithQuota wraps candidate with its quota snapshot.
type CandidateWithQuota struct {
	Candidate pluginapi.SchedulerAuthCandidate
	Quota     *quota.AccountQuotaInfo
	Countdown time.Duration
}

// Inspector reports runtime health / cooldown of credentials.
type Inspector interface {
	IsUnavailable(authID string, now time.Time) (unavailable bool, reason string)
}

// Rotator implements the two-stage Quota Priority Rotator scheduler.
type Rotator struct {
	mu            sync.Mutex
	cfg           config.Config
	cache         *quota.Cache
	inspector     Inspector
	rrIndices     map[string]int
	lastStages    map[string]string
	lastPickedIDs map[string]string
}

// NewRotator creates a new Rotator instance.
func NewRotator(cfg config.Config, cache *quota.Cache, inspector Inspector) *Rotator {
	if cache == nil {
		cache = quota.NewCache()
	}
	return &Rotator{
		cfg:           cfg,
		cache:         cache,
		inspector:     inspector,
		rrIndices:     make(map[string]int),
		lastStages:    make(map[string]string),
		lastPickedIDs: make(map[string]string),
	}
}

// Configure updates the rotator configuration.
func (r *Rotator) Configure(cfg config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
}

// Pick executes the two-stage routing decision tree.
func (r *Rotator) Pick(req pluginapi.SchedulerPickRequest, now time.Time) (pluginapi.SchedulerPickResponse, string, string) {
	unhandled := pluginapi.SchedulerPickResponse{Handled: false}

	provider := resolveProvider(req)
	if provider == "" || len(req.Candidates) == 0 {
		return unhandled, StageNone, ""
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.cfg.ShouldHandleProvider(provider) {
		return unhandled, StageNone, ""
	}

	routeKey := provider + ":" + strings.ToLower(strings.TrimSpace(req.Model))

	// Step 1: Health Guard & Quota Resolution
	var active []CandidateWithQuota
	for _, cand := range req.Candidates {
		// Check host inspector for cooldown or disabled state
		if r.inspector != nil {
			if unavail, _ := r.inspector.IsUnavailable(cand.ID, now); unavail {
				continue
			}
		}

		// Retrieve quota from cache or generate default 100% full capacity
		q, found := r.cache.Get(cand.ID)
		if !found {
			q = &quota.AccountQuotaInfo{
				AuthID:                      cand.ID,
				Provider:                    provider,
				WeeklyRemainingPercent:      100.0,
				ShortWindowRemainingPercent: 100.0,
				IsHealthy:                   true,
				ObservedAt:                  now,
			}
		}

		// Check bypass: 0% weekly, or 0% short window
		if q.IsBypassed(r.cfg.StrictShortWindow) {
			continue
		}

		active = append(active, CandidateWithQuota{
			Candidate: cand,
			Quota:     q,
			Countdown: q.ResetCountdown(now),
		})
	}

	if len(active) == 0 {
		r.lastStages[routeKey] = StageNone
		r.lastPickedIDs[routeKey] = ""
		return unhandled, StageNone, ""
	}

	// Step 2: Check Stage 1 (100% Full Capacity)
	var stageOneCandidates []CandidateWithQuota
	for _, item := range active {
		if item.Quota.IsStageOneEligible() {
			stageOneCandidates = append(stageOneCandidates, item)
		}
	}

	if len(stageOneCandidates) > 0 {
		// --- STAGE 1 ACTIVE (100% 满额轮询) ---
		idx := r.rrIndices[routeKey]
		if idx >= len(stageOneCandidates) {
			idx = 0
		}
		picked := stageOneCandidates[idx]
		r.rrIndices[routeKey] = (idx + 1) % len(stageOneCandidates)
		r.lastStages[routeKey] = StageFullCapacity
		r.lastPickedIDs[routeKey] = picked.Candidate.ID

		return pluginapi.SchedulerPickResponse{
			AuthID:  picked.Candidate.ID,
			Handled: true,
		}, StageFullCapacity, picked.Candidate.ID
	}

	// Step 3: Stage 2 (<=99% 即将刷新优先 Reset-Soon First)
	// All healthy active candidates have weekly quota <= 99% (and > 0%)
	sort.SliceStable(active, func(i, j int) bool {
		// 1. Shorter countdown to weekly reset has absolute priority
		if active[i].Countdown != active[j].Countdown {
			return active[i].Countdown < active[j].Countdown
		}
		// 2. Tie breaker: Higher remaining percentage
		if active[i].Quota.WeeklyRemainingPercent != active[j].Quota.WeeklyRemainingPercent {
			return active[i].Quota.WeeklyRemainingPercent > active[j].Quota.WeeklyRemainingPercent
		}
		// 3. Tie breaker: Lower ID for stable order
		return active[i].Candidate.ID < active[j].Candidate.ID
	})

	picked := active[0]
	r.lastStages[routeKey] = StageResetSoon
	r.lastPickedIDs[routeKey] = picked.Candidate.ID

	return pluginapi.SchedulerPickResponse{
		AuthID:  picked.Candidate.ID,
		Handled: true,
	}, StageResetSoon, picked.Candidate.ID
}

// GetStateSnapshot returns current routing status for diagnostics.
func (r *Rotator) GetStateSnapshot() (map[string]string, map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stages := make(map[string]string, len(r.lastStages))
	picks := make(map[string]string, len(r.lastPickedIDs))
	for k, v := range r.lastStages {
		stages[k] = v
	}
	for k, v := range r.lastPickedIDs {
		picks[k] = v
	}
	return stages, picks
}

func resolveProvider(req pluginapi.SchedulerPickRequest) string {
	if p := strings.ToLower(strings.TrimSpace(req.Provider)); p != "" {
		return p
	}
	if len(req.Providers) == 1 {
		return strings.ToLower(strings.TrimSpace(req.Providers[0]))
	}
	return ""
}
