package scheduler

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	StagePinned       = "stage_0_pinned"     // Pinned account has higher fixed priority than auto-rotated pool
	StageFullCapacity = "stage_1_full"       // Stage 1: WeeklyRemaining == 100%, Round-Robin
	StageResetSoon    = "stage_2_reset_soon" // Stage 2: WeeklyRemaining <= 99%, Reset-Soon First
	StageNone         = "none"
)

// CandidateWithQuota wraps candidate with its quota snapshot.
type CandidateWithQuota struct {
	Candidate      pluginapi.SchedulerAuthCandidate
	Quota          *quota.AccountQuotaInfo
	Countdown      time.Duration
	IsPinned       bool
	PinnedPriority int
	AutoPriority   int
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

// Pick executes the two-stage routing decision tree with pinned priority support.
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
	var autoActive []CandidateWithQuota
	var pinnedActive []CandidateWithQuota

	for _, cand := range req.Candidates {
		if r.inspector != nil {
			if unavail, _ := r.inspector.IsUnavailable(cand.ID, now); unavail {
				continue
			}
		}

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

		if q.IsBypassed(r.cfg.StrictShortWindow) {
			continue
		}

		isPinned, fixedPrio := r.resolveCandidatePin(cand, q)
		autoPrio := q.ComputedPriority
		if autoPrio <= 0 {
			autoPrio = cand.Priority
		}

		item := CandidateWithQuota{
			Candidate:      cand,
			Quota:          q,
			Countdown:      q.ResetCountdown(now),
			IsPinned:       isPinned,
			PinnedPriority: fixedPrio,
			AutoPriority:   autoPrio,
		}

		if isPinned {
			pinnedActive = append(pinnedActive, item)
		} else {
			autoActive = append(autoActive, item)
		}
	}

	if len(autoActive) == 0 && len(pinnedActive) == 0 {
		r.lastStages[routeKey] = StageNone
		r.lastPickedIDs[routeKey] = ""
		return unhandled, StageNone, ""
	}

	// Determine the highest auto-rotated priority tier when autoActive is non-empty
	maxAutoPriority := 0
	for _, item := range autoActive {
		if item.AutoPriority > maxAutoPriority {
			maxAutoPriority = item.AutoPriority
		}
	}
	if maxAutoPriority <= 1 && len(autoActive) > 0 {
		maxAutoPriority = len(autoActive) + 1
	}

	// Check if any pinned account has a strictly higher fixed priority than the active auto-rotated pool
	// (or if only pinned accounts are available).
	if len(pinnedActive) > 0 {
		sort.SliceStable(pinnedActive, func(i, j int) bool {
			if pinnedActive[i].PinnedPriority != pinnedActive[j].PinnedPriority {
				return pinnedActive[i].PinnedPriority > pinnedActive[j].PinnedPriority
			}
			if pinnedActive[i].Countdown != pinnedActive[j].Countdown {
				return pinnedActive[i].Countdown < pinnedActive[j].Countdown
			}
			return pinnedActive[i].Candidate.ID < pinnedActive[j].Candidate.ID
		})
		topPinnedPrio := pinnedActive[0].PinnedPriority
		if len(autoActive) == 0 || topPinnedPrio > maxAutoPriority {
			var topPinned []CandidateWithQuota
			for _, p := range pinnedActive {
				if p.PinnedPriority == topPinnedPrio {
					topPinned = append(topPinned, p)
				}
			}
			idx := r.rrIndices[routeKey]
			if idx >= len(topPinned) {
				idx = 0
			}
			picked := topPinned[idx]
			r.rrIndices[routeKey] = (idx + 1) % len(topPinned)
			r.lastStages[routeKey] = StagePinned
			r.lastPickedIDs[routeKey] = picked.Candidate.ID
			return pluginapi.SchedulerPickResponse{
				AuthID:  picked.Candidate.ID,
				Handled: true,
			}, StagePinned, picked.Candidate.ID
		}
	}

	// Include any pinned accounts whose fixed priority equals maxAutoPriority
	active := make([]CandidateWithQuota, 0, len(autoActive)+len(pinnedActive))
	active = append(active, autoActive...)
	for _, p := range pinnedActive {
		if p.PinnedPriority == maxAutoPriority {
			active = append(active, p)
		}
	}

	// Step 2: Check Stage 1 (100% Full Capacity)
	var stageOneCandidates []CandidateWithQuota
	for _, item := range active {
		if item.Quota.IsStageOneEligible() {
			stageOneCandidates = append(stageOneCandidates, item)
		}
	}

	if len(stageOneCandidates) > 0 {
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

	// Step 3: Stage 2 (<=99% Reset-Soon First)
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].Countdown != active[j].Countdown {
			return active[i].Countdown < active[j].Countdown
		}
		if active[i].Quota.WeeklyRemainingPercent != active[j].Quota.WeeklyRemainingPercent {
			return active[i].Quota.WeeklyRemainingPercent > active[j].Quota.WeeklyRemainingPercent
		}
		return active[i].Candidate.ID < active[j].Candidate.ID
	})

	parallelCount := r.cfg.GetParallelCount()
	if parallelCount > len(active) {
		parallelCount = len(active)
	}

	topCandidates := active[:parallelCount]

	idx := r.rrIndices[routeKey]
	if idx >= len(topCandidates) {
		idx = 0
	}
	picked := topCandidates[idx]
	r.rrIndices[routeKey] = (idx + 1) % len(topCandidates)

	r.lastStages[routeKey] = StageResetSoon
	r.lastPickedIDs[routeKey] = picked.Candidate.ID

	return pluginapi.SchedulerPickResponse{
		AuthID:  picked.Candidate.ID,
		Handled: true,
	}, StageResetSoon, picked.Candidate.ID
}

func (r *Rotator) resolveCandidatePin(cand pluginapi.SchedulerAuthCandidate, q *quota.AccountQuotaInfo) (bool, int) {
	email := ""
	name := ""
	if cand.Attributes != nil {
		email = cand.Attributes["email"]
		name = cand.Attributes["name"]
	}
	if cand.Metadata != nil {
		if email == "" {
			if s, ok := cand.Metadata["email"].(string); ok {
				email = s
			}
		}
		if b, ok := cand.Metadata["priority_pinned"].(bool); ok && b {
			return true, cand.Priority
		}
		if s, ok := cand.Metadata["priority_pinned"].(string); ok && strings.EqualFold(strings.TrimSpace(s), "true") {
			return true, cand.Priority
		}
	}
	if q != nil {
		if email == "" {
			email = q.Email
		}
		if name == "" {
			name = q.FileName
		}
	}
	if pinned, fixed := r.cfg.MatchPinnedAccount(cand.ID, email, name); pinned {
		if fixed != nil {
			return true, *fixed
		}
		return true, cand.Priority
	}
	if q != nil && q.IsPinned {
		if q.PinnedPriority != nil {
			return true, *q.PinnedPriority
		}
		return true, cand.Priority
	}
	if cand.Attributes != nil {
		if p := strings.TrimSpace(cand.Attributes["priority_pinned"]); strings.EqualFold(p, "true") {
			if rawPrio := strings.TrimSpace(cand.Attributes["priority"]); rawPrio != "" {
				if parsed, err := strconv.Atoi(rawPrio); err == nil {
					return true, parsed
				}
			}
			return true, cand.Priority
		}
	}
	return false, cand.Priority
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
