package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const antigravityOAuthTokenURL = "https://oauth2.googleapis.com/token"

func defaultAntigravityClientID() string {
	return strings.Join([]string{"1071006060591", "tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"}, "-")
}

func defaultAntigravityClientSecret() string {
	return strings.Join([]string{"GOCSPX", "K58FWR486LdLJ1mLB8sXC4z6qDAf"}, "-")
}

var syncMu sync.Mutex

// HostCaller is the narrow native host callback surface required by the poller.
type HostCaller interface {
	Call(method string, request []byte) ([]byte, error)
}

type PollerConfig struct {
	Config config.Config
}

// RunPoller refreshes quota snapshots immediately and then at the configured interval.
func RunPoller(ctx context.Context, cache *Cache, caller HostCaller, pollerCfg PollerConfig) {
	interval := pollerCfg.Config.GetRefreshDuration()
	if interval < time.Minute {
		interval = 10 * time.Minute
	}
	refresh := func() {
		RefreshAllFromHostWithConfig(caller, cache, pollerCfg.Config, time.Now())
	}
	refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

type hostEnvelope struct {
	OK     bool             `json:"ok"`
	Result json.RawMessage  `json:"result"`
	Error  *pluginabi.Error `json:"error,omitempty"`
}

type hostHTTPResponse struct {
	StatusCode int
	Headers    map[string][]string
	Body       []byte
}

func (r *hostHTTPResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		StatusCodeSnake int                 `json:"status_code"`
		StatusCodeCamel int                 `json:"StatusCode"`
		HeadersSnake    map[string][]string `json:"headers"`
		HeadersCamel    map[string][]string `json:"Headers"`
		BodySnake       []byte              `json:"body"`
		BodyCamel       []byte              `json:"Body"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.StatusCode = raw.StatusCodeSnake
	if r.StatusCode == 0 {
		r.StatusCode = raw.StatusCodeCamel
	}
	r.Headers = raw.HeadersSnake
	if len(r.Headers) == 0 {
		r.Headers = raw.HeadersCamel
	}
	r.Body = raw.BodySnake
	if len(r.Body) == 0 {
		r.Body = raw.BodyCamel
	}
	return nil
}

type hostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type hostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	JSON      json.RawMessage `json:"json"`
}

type antigravityHints struct {
	Endpoint  string `json:"endpoint"`
	ProjectID string `json:"project_id"`
}

type credentialStorage struct {
	AccessToken                string           `json:"access_token"`
	RefreshToken               string           `json:"refresh_token"`
	Expired                    string           `json:"expired"`
	AccountID                  string           `json:"account_id"`
	ProjectID                  string           `json:"project_id"`
	Email                      string           `json:"email"`
	BaseURL                    string           `json:"base_url"`
	AntigravityCapabilityHints antigravityHints `json:"antigravity_capability_hints"`
}

// FetchCredentialQuota queries a provider quota endpoint and returns both the
// normalized management response and the scheduler snapshot.
func FetchCredentialQuota(provider string, storageJSON []byte, caller HostCaller, now time.Time, antigravityGroup string) (pluginapi.QuotaFetchResponse, *AccountQuotaInfo, error) {
	var storage credentialStorage
	if err := json.Unmarshal(storageJSON, &storage); err != nil {
		return pluginapi.QuotaFetchResponse{}, nil, fmt.Errorf("decode %s credential: %w", provider, err)
	}

	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "antigravity":
		return fetchAntigravity(storage, caller, now, antigravityGroup)
	case "codex":
		return fetchCodex(storage, caller, now)
	default:
		return pluginapi.QuotaFetchResponse{}, nil, fmt.Errorf("unsupported quota provider %q", provider)
	}
}

// IngestUpstreamQuotaPayload parses an already-fetched upstream quota payload from the GUI or caller.
func IngestUpstreamQuotaPayload(provider string, rawPayload []byte, antigravityGroup string, now time.Time) (*AccountQuotaInfo, error) {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "antigravity":
		weekly, weeklyReset, short, shortReset, ok := ParseAntigravityQuotaDetailsJSON(rawPayload, antigravityGroup, now)
		if !ok {
			return nil, fmt.Errorf("antigravity payload has no matching quota buckets")
		}
		return &AccountQuotaInfo{
			Provider:                    "antigravity",
			WeeklyRemainingPercent:      weekly,
			WeeklyResetAt:               valueOrZero(weeklyReset),
			ShortWindowRemainingPercent: short,
			ShortWindowResetAt:          valueOrZero(shortReset),
			IsHealthy:                   true,
			ObservedAt:                  now.UTC(),
		}, nil
	case "codex":
		weekly, weeklyReset, short, shortReset, ok := ParseCodexUsageJSON(rawPayload, now)
		if !ok {
			return nil, fmt.Errorf("codex payload has no rate-limit windows")
		}
		return &AccountQuotaInfo{
			Provider:                    "codex",
			WeeklyRemainingPercent:      weekly,
			WeeklyResetAt:               valueOrZero(weeklyReset),
			ShortWindowRemainingPercent: short,
			ShortWindowResetAt:          valueOrZero(shortReset),
			IsHealthy:                   true,
			ObservedAt:                  now.UTC(),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

func refreshAntigravityAccessToken(refreshToken string, caller HostCaller) (string, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return "", fmt.Errorf("antigravity refresh token is missing")
	}
	form := url.Values{}
	form.Set("client_id", defaultAntigravityClientID())
	form.Set("client_secret", defaultAntigravityClientSecret())
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", rt)

	resp, err := doHostHTTP(caller, pluginapi.HTTPRequest{
		Method: "POST",
		URL:    antigravityOAuthTokenURL,
		Headers: map[string][]string{
			"Content-Type": {"application/x-www-form-urlencoded"},
			"Accept":       {"application/json"},
		},
		Body: []byte(form.Encode()),
	})
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("oauth token refresh returned HTTP %d", resp.StatusCode)
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(resp.Body, &tokenResp); err != nil {
		return "", err
	}
	token := strings.TrimSpace(tokenResp.AccessToken)
	if token == "" {
		return "", fmt.Errorf("oauth token refresh returned empty access_token")
	}
	return token, nil
}

func fetchAntigravity(storage credentialStorage, caller HostCaller, now time.Time, group string) (pluginapi.QuotaFetchResponse, *AccountQuotaInfo, error) {
	accessToken := strings.TrimSpace(storage.AccessToken)
	refreshed := false
	if accessToken == "" && strings.TrimSpace(storage.RefreshToken) != "" {
		if tok, err := refreshAntigravityAccessToken(storage.RefreshToken, caller); err == nil {
			accessToken = tok
			refreshed = true
		}
	}
	if accessToken == "" {
		return pluginapi.QuotaFetchResponse{}, nil, fmt.Errorf("antigravity access token is missing")
	}

	projectID := strings.TrimSpace(storage.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(storage.AntigravityCapabilityHints.ProjectID)
	}

	base := strings.TrimRight(strings.TrimSpace(storage.AntigravityCapabilityHints.Endpoint), "/")
	endpoints := []string{
		base + "/v1internal:retrieveUserQuotaSummary",
		"https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
		"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary",
		"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
	}

	attemptFetch := func(token string) ([]byte, int, error) {
		seen := make(map[string]struct{}, len(endpoints))
		var lastErr error
		var lastStatus int
		for _, endpoint := range endpoints {
			if endpoint == "/v1internal:retrieveUserQuotaSummary" || endpoint == "" {
				continue
			}
			if _, ok := seen[endpoint]; ok {
				continue
			}
			seen[endpoint] = struct{}{}
			requestBody := map[string]string{}
			if projectID != "" {
				requestBody["project"] = projectID
			}
			encoded, err := json.Marshal(requestBody)
			if err != nil {
				return nil, 0, err
			}
			response, err := doHostHTTP(caller, pluginapi.HTTPRequest{
				Method: "POST",
				URL:    endpoint,
				Headers: map[string][]string{
					"Authorization": {"Bearer " + token},
					"Content-Type":  {"application/json"},
					"Accept":        {"application/json"},
					"User-Agent":    {"Antigravity/1.0.0"},
				},
				Body: encoded,
			})
			if err != nil {
				lastErr = err
				continue
			}
			lastStatus = response.StatusCode
			if response.StatusCode == 401 {
				return nil, 401, fmt.Errorf("quota endpoint returned HTTP 401")
			}
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				lastErr = fmt.Errorf("quota endpoint returned HTTP %d", response.StatusCode)
				continue
			}
			if len(response.Body) > 0 {
				return response.Body, response.StatusCode, nil
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("quota endpoint returned an empty response")
		}
		return nil, lastStatus, lastErr
	}

	body, status, varErr := attemptFetch(accessToken)
	if status == 401 && !refreshed && strings.TrimSpace(storage.RefreshToken) != "" {
		if newToken, errRefresh := refreshAntigravityAccessToken(storage.RefreshToken, caller); errRefresh == nil {
			body, _, varErr = attemptFetch(newToken)
		}
	}
	if len(body) == 0 {
		if varErr == nil {
			varErr = fmt.Errorf("quota endpoint returned an empty response")
		}
		return pluginapi.QuotaFetchResponse{}, nil, varErr
	}

	weekly, weeklyReset, short, shortReset, ok := ParseAntigravityQuotaDetailsJSON(body, group, now)
	if !ok {
		return pluginapi.QuotaFetchResponse{}, nil, fmt.Errorf("antigravity quota response has no matching buckets")
	}
	info := &AccountQuotaInfo{
		Provider:                    "antigravity",
		Email:                       strings.TrimSpace(storage.Email),
		WeeklyRemainingPercent:      weekly,
		WeeklyResetAt:               valueOrZero(weeklyReset),
		ShortWindowRemainingPercent: short,
		ShortWindowResetAt:          valueOrZero(shortReset),
		IsHealthy:                   true,
		ObservedAt:                  now.UTC(),
	}
	return parseAntigravityQuotaResponse(body), info, nil
}

func fetchCodex(storage credentialStorage, caller HostCaller, now time.Time) (pluginapi.QuotaFetchResponse, *AccountQuotaInfo, error) {
	if strings.TrimSpace(storage.AccessToken) == "" {
		return pluginapi.QuotaFetchResponse{}, nil, fmt.Errorf("codex access token is missing")
	}
	base := strings.TrimRight(strings.TrimSpace(storage.BaseURL), "/")
	if base == "" {
		base = "https://chatgpt.com"
	}
	headers := map[string][]string{
		"Authorization": {"Bearer " + storage.AccessToken},
		"Accept":        {"application/json"},
	}
	if strings.TrimSpace(storage.AccountID) != "" {
		headers["Chatgpt-Account-Id"] = []string{strings.TrimSpace(storage.AccountID)}
	}
	response, err := doHostHTTP(caller, pluginapi.HTTPRequest{
		Method:  "GET",
		URL:     base + "/backend-api/wham/usage",
		Headers: headers,
	})
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return pluginapi.QuotaFetchResponse{}, nil, fmt.Errorf("codex quota endpoint returned HTTP %d", response.StatusCode)
	}
	weekly, weeklyReset, short, shortReset, ok := ParseCodexUsageJSON(response.Body, now)
	if !ok {
		return pluginapi.QuotaFetchResponse{}, nil, fmt.Errorf("codex quota response has no rate-limit windows")
	}
	info := &AccountQuotaInfo{
		Provider:                    "codex",
		Email:                       strings.TrimSpace(storage.Email),
		WeeklyRemainingPercent:      weekly,
		WeeklyResetAt:               valueOrZero(weeklyReset),
		ShortWindowRemainingPercent: short,
		ShortWindowResetAt:          valueOrZero(shortReset),
		IsHealthy:                   true,
		ObservedAt:                  now.UTC(),
	}
	weeklyResetStr := ""
	if weeklyReset != nil {
		weeklyResetStr = weeklyReset.UTC().Format(time.RFC3339)
	}
	shortResetStr := ""
	if shortReset != nil {
		shortResetStr = shortReset.UTC().Format(time.RFC3339)
	}
	fetchResp := pluginapi.QuotaFetchResponse{
		Groups: []pluginapi.QuotaGroup{{
			DisplayName: "Codex Rate Limits",
			Buckets: []pluginapi.QuotaBucket{
				{
					Window:            "5h",
					RemainingFraction: short / 100.0,
					ResetTime:         shortResetStr,
				},
				{
					Window:            "weekly",
					RemainingFraction: weekly / 100.0,
					ResetTime:         weeklyResetStr,
				},
			},
		}},
	}
	return fetchResp, info, nil
}

func doHostHTTP(caller HostCaller, request pluginapi.HTTPRequest) (hostHTTPResponse, error) {
	if caller == nil {
		return hostHTTPResponse{}, fmt.Errorf("native host callback is unavailable")
	}
	payload := map[string]any{
		"method":  request.Method,
		"url":     request.URL,
		"headers": request.Headers,
		"body":    request.Body,
	}
	raw, err := caller.Call(pluginabi.MethodHostHTTPDo, mustJSON(payload))
	if err != nil {
		return hostHTTPResponse{}, err
	}
	var envelope hostEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return hostHTTPResponse{}, fmt.Errorf("decode host HTTP response: %w", err)
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return hostHTTPResponse{}, fmt.Errorf("host HTTP callback: %s", envelope.Error.Message)
		}
		return hostHTTPResponse{}, fmt.Errorf("host HTTP callback failed")
	}
	var response hostHTTPResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		return hostHTTPResponse{}, fmt.Errorf("decode host HTTP result: %w", err)
	}
	return response, nil
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func valueOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func parseAntigravityQuotaResponse(raw []byte) pluginapi.QuotaFetchResponse {
	var response struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
			Buckets     []struct {
				Window            string  `json:"window"`
				RemainingFraction float64 `json:"remainingFraction"`
				ResetTime         string  `json:"resetTime"`
				Description       string  `json:"description"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return pluginapi.QuotaFetchResponse{}
	}
	out := pluginapi.QuotaFetchResponse{}
	for _, group := range response.Groups {
		item := pluginapi.QuotaGroup{DisplayName: group.DisplayName}
		for _, bucket := range group.Buckets {
			item.Buckets = append(item.Buckets, pluginapi.QuotaBucket{
				Window:            bucket.Window,
				RemainingFraction: bucket.RemainingFraction,
				ResetTime:         bucket.ResetTime,
				Description:       bucket.Description,
			})
		}
		out.Groups = append(out.Groups, item)
	}
	return out
}

// RefreshAllFromHost refreshes quota snapshots for all credentials visible to the host
// and syncs dynamic priorities back to the host.
func RefreshAllFromHost(caller HostCaller, cache *Cache, providers []string, antigravityGroup string, now time.Time) {
	cfg := config.DefaultConfig()
	cfg.Providers = providers
	cfg.AntigravityGroup = antigravityGroup
	RefreshAllFromHostWithConfig(caller, cache, cfg, now)
}

// RefreshAllFromHostWithConfig refreshes quota snapshots for all credentials and syncs priorities.
func RefreshAllFromHostWithConfig(caller HostCaller, cache *Cache, cfg config.Config, now time.Time) {
	if caller == nil || cache == nil {
		return
	}
	raw, err := caller.Call(pluginabi.MethodHostAuthList, []byte(`{}`))
	if err != nil {
		return
	}
	var envelope hostEnvelope
	if json.Unmarshal(raw, &envelope) != nil || !envelope.OK {
		return
	}
	var listing hostAuthListResponse
	if json.Unmarshal(envelope.Result, &listing) != nil {
		return
	}
	for _, entry := range listing.Files {
		provider := strings.ToLower(strings.TrimSpace(entry.Provider))
		if provider == "" {
			provider = strings.ToLower(strings.TrimSpace(entry.Type))
		}
		if !providerEnabled(provider, cfg.Providers) || entry.RuntimeOnly {
			continue
		}
		body, err := caller.Call(pluginabi.MethodHostAuthGet, mustJSON(pluginapi.HostAuthGetRequest{AuthIndex: entry.AuthIndex}))
		if err != nil {
			continue
		}
		var getEnvelope hostEnvelope
		var credential hostAuthGetResponse
		if json.Unmarshal(body, &getEnvelope) != nil || !getEnvelope.OK || json.Unmarshal(getEnvelope.Result, &credential) != nil {
			continue
		}
		_, info, err := FetchCredentialQuota(provider, credential.JSON, caller, now, cfg.AntigravityGroup)
		if err != nil || info == nil {
			continue
		}
		StoreAccountQuotaInCache(cache, entry, info)
	}
	if cfg.Enabled {
		_, _ = SyncPrioritiesToHost(caller, cache, cfg, now)
	}
}

// StoreAccountQuotaInCache saves the quota info under both AuthID (filename/ID) and AuthIndex.
func StoreAccountQuotaInCache(cache *Cache, entry pluginapi.HostAuthFileEntry, info *AccountQuotaInfo) {
	if cache == nil || info == nil {
		return
	}
	primaryID := strings.TrimSpace(entry.ID)
	if primaryID == "" {
		primaryID = strings.TrimSpace(entry.Name)
	}
	if primaryID == "" {
		primaryID = strings.TrimSpace(entry.AuthIndex)
	}
	info.AuthID = primaryID
	info.AuthIndex = strings.TrimSpace(entry.AuthIndex)
	info.FileName = strings.TrimSpace(entry.Name)
	if strings.TrimSpace(entry.Email) != "" {
		info.Email = strings.TrimSpace(entry.Email)
	}
	info.Disabled = entry.Disabled
	info.IsHealthy = !entry.Unavailable
	cache.Set(info)
	if entry.AuthIndex != "" && entry.AuthIndex != primaryID {
		copyInfo := *info
		copyInfo.AuthID = entry.AuthIndex
		cache.Set(&copyInfo)
	}
}

type accountSyncRecord struct {
	entry        pluginapi.HostAuthFileEntry
	credential   hostAuthGetResponse
	doc          map[string]any
	hasDoc       bool
	docPriority  int
	info         *AccountQuotaInfo
	isPinned     bool
	pinnedPrio   *int
	targetPrio   int
}

// SyncPrioritiesToHost recalculates priorities for all managed credentials per provider
// and persists changed priorities via host.auth.save so the GUI and built-in selector
// immediately reflect the two-stage rotation order.
func SyncPrioritiesToHost(caller HostCaller, cache *Cache, cfg config.Config, now time.Time) (int, error) {
	if caller == nil || cache == nil || !cfg.Enabled {
		return 0, nil
	}
	syncMu.Lock()
	defer syncMu.Unlock()

	raw, err := caller.Call(pluginabi.MethodHostAuthList, []byte(`{}`))
	if err != nil {
		return 0, err
	}
	var envelope hostEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		return 0, fmt.Errorf("host.auth.list failed")
	}
	var listing hostAuthListResponse
	if err := json.Unmarshal(envelope.Result, &listing); err != nil {
		return 0, err
	}

	byProvider := make(map[string][]*accountSyncRecord)
	for _, entry := range listing.Files {
		if entry.RuntimeOnly {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(entry.Provider))
		if provider == "" {
			provider = strings.ToLower(strings.TrimSpace(entry.Type))
		}
		if !providerEnabled(provider, cfg.Providers) {
			continue
		}

		var info *AccountQuotaInfo
		for _, key := range []string{entry.ID, entry.Name, entry.AuthIndex} {
			if strings.TrimSpace(key) == "" {
				continue
			}
			if found, ok := cache.Get(strings.TrimSpace(key)); ok && found != nil {
				info = found
				break
			}
		}

		rec := &accountSyncRecord{
			entry:       entry,
			docPriority: entry.Priority,
			info:        info,
		}

		if strings.TrimSpace(entry.AuthIndex) != "" {
			if body, errGet := caller.Call(pluginabi.MethodHostAuthGet, mustJSON(pluginapi.HostAuthGetRequest{AuthIndex: entry.AuthIndex})); errGet == nil {
				var getEnv hostEnvelope
				var cred hostAuthGetResponse
				if json.Unmarshal(body, &getEnv) == nil && getEnv.OK && json.Unmarshal(getEnv.Result, &cred) == nil {
					rec.credential = cred
					var doc map[string]any
					if json.Unmarshal(cred.JSON, &doc) == nil && doc != nil {
						rec.doc = doc
						rec.hasDoc = true
						if p, ok := parseDocInt(doc["priority"]); ok {
							rec.docPriority = p
						}
					}
				}
			}
		}

		// Determine if this account is pinned via config or credential metadata
		pinned, fixedPrio := cfg.MatchPinnedAccount(entry.ID, entry.Name, entry.Email, entry.Account, entry.AuthIndex)
		if !pinned && rec.hasDoc {
			if b, ok := toBool(rec.doc["priority_pinned"]); ok && b {
				pinned = true
			} else if b, ok := toBool(rec.doc["pinned_priority"]); ok && b {
				pinned = true
			}
			if pinned {
				if p, ok := parseDocInt(rec.doc["pinned_priority_value"]); ok {
					fixedPrio = &p
				}
			}
		}
		if !pinned {
			noteLower := strings.ToLower(strings.TrimSpace(entry.Note))
			if strings.Contains(noteLower, "[pin]") || strings.Contains(entry.Note, "固定优先级") {
				pinned = true
			}
		}

		rec.isPinned = pinned
		rec.pinnedPrio = fixedPrio
		byProvider[provider] = append(byProvider[provider], rec)
	}

	updatedCount := 0
	for _, records := range byProvider {
		var healthyActive []*accountSyncRecord
		var healthyDisabled []*accountSyncRecord
		var bypassedRecords []*accountSyncRecord

		for _, rec := range records {
			if rec.isPinned {
				if rec.pinnedPrio != nil {
					rec.targetPrio = *rec.pinnedPrio
				} else {
					rec.targetPrio = rec.docPriority
				}
				continue
			}
			if rec.info == nil {
				// No quota info yet; preserve current priority
				rec.targetPrio = rec.docPriority
				continue
			}
			if rec.info.IsBypassed(cfg.StrictShortWindow) || rec.entry.Unavailable {
				bypassedRecords = append(bypassedRecords, rec)
				continue
			}
			if rec.entry.Disabled {
				healthyDisabled = append(healthyDisabled, rec)
			} else {
				healthyActive = append(healthyActive, rec)
			}
		}

		sortRecords := func(slice []*accountSyncRecord) {
			sort.SliceStable(slice, func(i, j int) bool {
				a, b := slice[i].info, slice[j].info
				aStage1 := a.IsStageOneEligible()
				bStage1 := b.IsStageOneEligible()
				if aStage1 != bStage1 {
					return aStage1
				}
				cdA := a.ResetCountdown(now)
				cdB := b.ResetCountdown(now)
				if cdA != cdB {
					return cdA < cdB
				}
				if a.WeeklyRemainingPercent != b.WeeklyRemainingPercent {
					return a.WeeklyRemainingPercent > b.WeeklyRemainingPercent
				}
				return slice[i].entry.Name < slice[j].entry.Name
			})
		}

		sortRecords(healthyActive)
		sortRecords(healthyDisabled)

		allHealthy := make([]*accountSyncRecord, 0, len(healthyActive)+len(healthyDisabled))
		allHealthy = append(allHealthy, healthyActive...)
		allHealthy = append(allHealthy, healthyDisabled...)

		totalHealthy := len(allHealthy)
		topPriority := totalHealthy + 1
		if topPriority < 2 {
			topPriority = 2
		}

		stageOneActiveCount := 0
		for _, rec := range healthyActive {
			if rec.info.IsStageOneEligible() {
				stageOneActiveCount++
			}
		}

		parallelCount := cfg.GetParallelCount()
		if len(healthyActive) > 0 && parallelCount > len(healthyActive) {
			parallelCount = len(healthyActive)
		}

		for idx, rec := range allHealthy {
			if stageOneActiveCount > 0 {
				if idx < stageOneActiveCount {
					rec.targetPrio = topPriority
				} else {
					rec.targetPrio = topPriority - idx
				}
			} else {
				if idx < parallelCount && idx < len(healthyActive) {
					rec.targetPrio = topPriority
				} else {
					rec.targetPrio = topPriority - idx
				}
			}
			if rec.targetPrio < 2 {
				rec.targetPrio = 2
			}
		}

		for _, rec := range bypassedRecords {
			rec.targetPrio = 1
		}

		for _, rec := range records {
			if rec.info != nil {
				rec.info.IsPinned = rec.isPinned
				if rec.isPinned {
					p := rec.targetPrio
					rec.info.PinnedPriority = &p
				} else {
					rec.info.PinnedPriority = nil
				}
				rec.info.ComputedPriority = rec.targetPrio
				StoreAccountQuotaInCache(cache, rec.entry, rec.info)
			}

			if !rec.hasDoc {
				continue
			}
			if rec.docPriority == rec.targetPrio && rec.entry.Priority == rec.targetPrio {
				continue
			}
			saveName := strings.TrimSpace(rec.credential.Name)
			if saveName == "" {
				saveName = strings.TrimSpace(rec.entry.Name)
			}
			saveName = filepath.Base(saveName)
			if !strings.HasSuffix(strings.ToLower(saveName), ".json") {
				continue
			}
			rec.doc["priority"] = rec.targetPrio
			updatedBytes, errMarshal := json.Marshal(rec.doc)
			if errMarshal != nil {
				continue
			}
			if _, errSave := caller.Call(pluginabi.MethodHostAuthSave, mustJSON(pluginapi.HostAuthSaveRequest{
				Name: saveName,
				JSON: updatedBytes,
			})); errSave == nil {
				updatedCount++
			}
		}
	}

	return updatedCount, nil
}

// SetAccountPinnedInHost pins or unpins a specific account in both its credential JSON
// and the active plugin configuration, then triggers a priority sync.
func SetAccountPinnedInHost(caller HostCaller, cache *Cache, cfg *config.Config, accountOrIndex string, pinned bool, priority *int, now time.Time) error {
	if caller == nil || cfg == nil {
		return fmt.Errorf("host caller or config unavailable")
	}
	target := strings.TrimSpace(accountOrIndex)
	if target == "" {
		return fmt.Errorf("account identifier is required")
	}

	raw, err := caller.Call(pluginabi.MethodHostAuthList, []byte(`{}`))
	if err != nil {
		return err
	}
	var envelope hostEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		return fmt.Errorf("host.auth.list failed")
	}
	var listing hostAuthListResponse
	if err := json.Unmarshal(envelope.Result, &listing); err != nil {
		return err
	}

	var matched *pluginapi.HostAuthFileEntry
	targetLower := strings.ToLower(target)
	targetBase := strings.TrimSuffix(filepath.Base(targetLower), ".json")
	for i := range listing.Files {
		entry := &listing.Files[i]
		for _, candidate := range []string{entry.AuthIndex, entry.ID, entry.Name, entry.Email, entry.Account} {
			cLower := strings.ToLower(strings.TrimSpace(candidate))
			if cLower == "" {
				continue
			}
			cBase := strings.TrimSuffix(filepath.Base(cLower), ".json")
			if cLower == targetLower || cBase == targetBase {
				matched = entry
				break
			}
		}
		if matched != nil {
			break
		}
	}
	if matched == nil {
		return fmt.Errorf("credential %q not found", target)
	}

	body, err := caller.Call(pluginabi.MethodHostAuthGet, mustJSON(pluginapi.HostAuthGetRequest{AuthIndex: matched.AuthIndex}))
	if err != nil {
		return err
	}
	var getEnv hostEnvelope
	var cred hostAuthGetResponse
	if err := json.Unmarshal(body, &getEnv); err != nil || !getEnv.OK || json.Unmarshal(getEnv.Result, &cred) != nil {
		return fmt.Errorf("host.auth.get failed for %s", matched.Name)
	}
	var doc map[string]any
	if err := json.Unmarshal(cred.JSON, &doc); err != nil || doc == nil {
		return fmt.Errorf("invalid credential JSON for %s", matched.Name)
	}

	if pinned {
		doc["priority_pinned"] = true
		if priority != nil {
			doc["priority"] = *priority
			doc["pinned_priority_value"] = *priority
		}
	} else {
		delete(doc, "priority_pinned")
		delete(doc, "pinned_priority")
		delete(doc, "pinned_priority_value")
		if priority != nil {
			doc["priority"] = *priority
		}
	}

	saveName := filepath.Base(strings.TrimSpace(cred.Name))
	if saveName == "" {
		saveName = filepath.Base(strings.TrimSpace(matched.Name))
	}
	updatedBytes, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if _, err := caller.Call(pluginabi.MethodHostAuthSave, mustJSON(pluginapi.HostAuthSaveRequest{
		Name: saveName,
		JSON: updatedBytes,
	})); err != nil {
		return err
	}

	// Update in-memory PinnedAccounts slice cleanly
	var nextPinned []string
	tempCfg := config.Config{PinnedAccounts: []string{}}
	for _, spec := range cfg.PinnedAccounts {
		tempCfg.PinnedAccounts = []string{spec}
		if isMatch, _ := tempCfg.MatchPinnedAccount(matched.ID, matched.Name, matched.Email, matched.Account, matched.AuthIndex); !isMatch {
			nextPinned = append(nextPinned, spec)
		}
	}
	if pinned {
		key := matched.Email
		if strings.TrimSpace(key) == "" {
			key = matched.Name
		}
		if priority != nil {
			nextPinned = append(nextPinned, fmt.Sprintf("%s=%d", key, *priority))
		} else {
			nextPinned = append(nextPinned, key)
		}
	}
	cfg.PinnedAccounts = nextPinned

	_, _ = SyncPrioritiesToHost(caller, cache, *cfg, now)
	return nil
}

func parseDocInt(raw any) (int, bool) {
	if raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		return int(i), err == nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		return i, err == nil
	default:
		return 0, false
	}
}

func providerEnabled(provider string, providers []string) bool {
	if len(providers) == 0 {
		return provider == "codex" || provider == "antigravity"
	}
	for _, item := range providers {
		if strings.ToLower(strings.TrimSpace(item)) == provider {
			return true
		}
	}
	return false
}
