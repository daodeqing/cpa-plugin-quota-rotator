package quota

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseCodexHeaders parses Codex quota metrics from HTTP response headers.
func ParseCodexHeaders(headers map[string][]string, now time.Time) (*AccountQuotaInfo, bool) {
	if len(headers) == 0 {
		return nil, false
	}
	info := &AccountQuotaInfo{
		Provider:                    "codex",
		WeeklyRemainingPercent:      100.0,
		ShortWindowRemainingPercent: 100.0,
		IsHealthy:                   true,
		ObservedAt:                  now.UTC(),
	}
	hasData := false

	// Secondary window = weekly limit
	if val := getFirstHeader(headers, "X-Codex-Secondary-Used-Percent"); val != "" {
		if used, ok := toFloat(val); ok {
			info.WeeklyRemainingPercent = math.Max(0, math.Min(100, 100.0-used))
			hasData = true
		}
	}
	if val := getFirstHeader(headers, "X-Codex-Secondary-Reset-At"); val != "" {
		if t, ok := parseTime(val); ok && t != nil {
			info.WeeklyResetAt = *t
			hasData = true
		}
	} else if val := getFirstHeader(headers, "X-Codex-Secondary-Reset-After-Seconds"); val != "" {
		if sec, ok := toFloat(val); ok && sec > 0 {
			info.WeeklyResetAt = now.UTC().Add(time.Duration(sec) * time.Second)
			hasData = true
		}
	}

	// Primary window = 5-hour short limit
	if val := getFirstHeader(headers, "X-Codex-Primary-Used-Percent"); val != "" {
		if used, ok := toFloat(val); ok {
			info.ShortWindowRemainingPercent = math.Max(0, math.Min(100, 100.0-used))
			hasData = true
		}
	}
	if val := getFirstHeader(headers, "X-Codex-Primary-Reset-After-Seconds"); val != "" {
		if sec, ok := toFloat(val); ok && sec > 0 {
			info.ShortWindowResetAt = now.UTC().Add(time.Duration(sec) * time.Second)
			hasData = true
		}
	}

	if !hasData {
		return nil, false
	}
	return info, true
}

// ParseCodexUsageJSON parses response body from /backend-api/wham/usage or /v1/usage.
func ParseCodexUsageJSON(raw []byte, now time.Time) (weeklyRem float64, weeklyReset *time.Time, shortRem float64, shortReset *time.Time, ok bool) {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return 0, nil, 0, nil, false
	}
	rateLimit, _ := root["rate_limit"].(map[string]any)
	if rateLimit == nil {
		return 0, nil, 0, nil, false
	}

	weeklyRem = 100.0
	shortRem = 100.0
	foundWeekly := false
	foundShort := false

	// Secondary window (weekly)
	if secWin, ok := rateLimit["secondary_window"].(map[string]any); ok {
		if rem, ok := quotaPercent(secWin); ok {
			weeklyRem = rem
			foundWeekly = true
		}
		weeklyReset = quotaResetAt(secWin, now)
	}

	// Primary window (short 5-hour)
	if priWin, ok := rateLimit["primary_window"].(map[string]any); ok {
		if rem, ok := quotaPercent(priWin); ok {
			shortRem = rem
			foundShort = true
		}
		shortReset = quotaResetAt(priWin, now)
	}

	return weeklyRem, weeklyReset, shortRem, shortReset, (foundWeekly || foundShort)
}

// ParseAntigravityQuotaJSON parses Google Code Assist retrieveUserQuotaSummary payload.
func ParseAntigravityQuotaJSON(raw []byte, group string, now time.Time) (weeklyRem float64, weeklyReset *time.Time, ok bool) {
	weeklyRem, weeklyReset, _, _, ok = ParseAntigravityQuotaDetailsJSON(raw, group, now)
	return weeklyRem, weeklyReset, ok
}

// ParseAntigravityQuotaDetailsJSON separates the weekly and five-hour buckets
// returned by retrieveUserQuotaSummary.
func ParseAntigravityQuotaDetailsJSON(raw []byte, group string, now time.Time) (weeklyRem float64, weeklyReset *time.Time, shortRem float64, shortReset *time.Time, ok bool) {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return 0, nil, 0, nil, false
	}

	type candidate struct {
		window    string
		remaining float64
		resetAt   *time.Time
	}
	var candidates []candidate

	add := func(modelText, windowHint string, remainingRaw, resetRaw any) {
		if !matchesModelGroup(modelText, group) {
			return
		}
		remainingValue, ok := toFloat(remainingRaw)
		if !ok {
			return
		}
		if remainingValue <= 1.0 {
			remainingValue *= 100.0
		}
		resetAt, _ := parseTime(resetRaw)
		if resetAt == nil && remainingValue < 99.999 {
			return
		}
		candidates = append(candidates, candidate{
			window:    strings.ToLower(strings.TrimSpace(windowHint)),
			remaining: math.Max(0, math.Min(100, remainingValue)),
			resetAt:   resetAt,
		})
	}

	if models, ok := root["models"].(map[string]any); ok {
		for modelID, rawModel := range models {
			model, _ := rawModel.(map[string]any)
			quota, _ := model["quotaInfo"].(map[string]any)
			text := modelID + " " + strings.ToLower(firstString(model["modelProvider"]))
			if windows, ok := quota["windows"].([]any); ok && len(windows) > 0 {
				for _, rawWindow := range windows {
					window, _ := rawWindow.(map[string]any)
					winHint := firstString(window["window"])
					if winHint == "" {
						winHint = firstString(window["type"])
					}
					add(text, winHint, window["remainingFraction"], window["resetTime"])
				}
			} else if quota != nil {
				add(text, firstString(quota["window"]), quota["remainingFraction"], quota["resetTime"])
			}
		}
	}
	if buckets, ok := root["buckets"].([]any); ok {
		for _, rawBucket := range buckets {
			bucket, _ := rawBucket.(map[string]any)
			add(firstString(bucket["modelId"]), firstString(bucket["window"]), bucket["remainingFraction"], bucket["resetTime"])
		}
	}
	if groups, ok := root["groups"].([]any); ok {
		for _, rawGroup := range groups {
			quotaGroup, _ := rawGroup.(map[string]any)
			text := firstString(quotaGroup["displayName"]) + " " + firstString(quotaGroup["description"])
			buckets, _ := quotaGroup["buckets"].([]any)
			for _, rawBucket := range buckets {
				bucket, _ := rawBucket.(map[string]any)
				add(text, firstString(bucket["window"]), bucket["remainingFraction"], bucket["resetTime"])
			}
		}
	}

	if len(candidates) == 0 {
		return 0, nil, 0, nil, false
	}

	weeklyRem = 100.0
	shortRem = 100.0
	var weeklyCandidate, shortCandidate *candidate
	for i := range candidates {
		cand := &candidates[i]
		w := cand.window
		var isShort bool
		if w == "weekly" || strings.Contains(w, "week") || strings.Contains(w, "7d") {
			isShort = false
		} else if w == "5h" || strings.Contains(w, "5h") || strings.Contains(w, "hour") || strings.Contains(w, "short") || strings.Contains(w, "primary") {
			isShort = true
		} else {
			resetAfter := time.Duration(0)
			if cand.resetAt != nil {
				resetAfter = cand.resetAt.Sub(now)
			}
			isShort = resetAfter > 0 && resetAfter <= 8*time.Hour
		}

		if isShort {
			if shortCandidate == nil || cand.remaining < shortCandidate.remaining ||
				(cand.remaining == shortCandidate.remaining && cand.resetAt != nil && (shortCandidate.resetAt == nil || cand.resetAt.Before(*shortCandidate.resetAt))) {
				shortCandidate = cand
			}
			continue
		}
		if weeklyCandidate == nil || cand.remaining < weeklyCandidate.remaining ||
			(cand.remaining == weeklyCandidate.remaining && cand.resetAt != nil && (weeklyCandidate.resetAt == nil || cand.resetAt.Before(*weeklyCandidate.resetAt))) {
			weeklyCandidate = cand
		}
	}
	if weeklyCandidate != nil {
		weeklyRem, weeklyReset = weeklyCandidate.remaining, weeklyCandidate.resetAt
	}
	if shortCandidate != nil {
		shortRem, shortReset = shortCandidate.remaining, shortCandidate.resetAt
	}
	if weeklyCandidate == nil && shortCandidate == nil {
		return 0, nil, 0, nil, false
	}
	return weeklyRem, weeklyReset, shortRem, shortReset, true
}

func getFirstHeader(headers map[string][]string, key string) string {
	for k, vals := range headers {
		if strings.EqualFold(k, key) && len(vals) > 0 {
			return strings.TrimSpace(vals[0])
		}
	}
	return ""
}

func quotaPercent(window map[string]any) (float64, bool) {
	if reached, ok := toBool(window["limit_reached"]); ok && reached {
		return 0.0, true
	}
	if used, ok := toFloat(window["used_percent"]); ok {
		return math.Max(0, math.Min(100, 100.0-used)), true
	}
	rem, hasRem := toFloat(window["remaining"])
	lim, hasLim := toFloat(window["limit"])
	if hasRem && hasLim && lim > 0 {
		return math.Max(0, math.Min(100, (rem/lim)*100.0)), true
	}
	if hasRem && rem >= 0 && rem <= 100 {
		return rem, true
	}
	return 0.0, false
}

func quotaResetAt(window map[string]any, now time.Time) *time.Time {
	if val, ok := parseTime(window["reset_at"]); ok {
		return val
	}
	if sec, ok := toFloat(window["reset_after_seconds"]); ok && sec > 0 {
		val := now.UTC().Add(time.Duration(sec) * time.Second)
		return &val
	}
	return nil
}

func matchesModelGroup(text, group string) bool {
	t := strings.ToLower(text)
	g := strings.ToLower(group)
	if g == "claude_gpt" {
		return strings.Contains(t, "claude") || strings.Contains(t, "gpt") || strings.Contains(t, "openai")
	}
	return strings.Contains(t, "gemini") || g == "" || g == "all"
}

func parseTime(raw any) (*time.Time, bool) {
	if raw == nil {
		return nil, false
	}
	switch value := raw.(type) {
	case json.Number:
		integer, err := value.Int64()
		if err != nil {
			return nil, false
		}
		return unixTime(integer)
	case float64:
		return unixTime(int64(value))
	case string:
		v := strings.TrimSpace(value)
		if integer, err := strconv.ParseInt(v, 10, 64); err == nil {
			return unixTime(integer)
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, v); err == nil {
				parsed = parsed.UTC()
				return &parsed, true
			}
		}
	}
	return nil, false
}

func unixTime(value int64) (*time.Time, bool) {
	if value <= 0 {
		return nil, false
	}
	var parsed time.Time
	if value > 1_000_000_000_000 {
		parsed = time.UnixMilli(value).UTC()
	} else {
		parsed = time.Unix(value, 0).UTC()
	}
	return &parsed, true
}

func toFloat(raw any) (float64, bool) {
	if raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func toBool(raw any) (bool, bool) {
	if raw == nil {
		return false, false
	}
	switch value := raw.(type) {
	case bool:
		return value, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return false, false
	}
}

func firstString(val any) string {
	if s, ok := val.(string); ok {
		return s
	}
	return ""
}
