package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// HostCaller is the narrow native host callback surface required by the poller.
type HostCaller interface {
	Call(method string, request []byte) ([]byte, error)
}

type PollerConfig struct {
	Interval         time.Duration
	Providers        []string
	AntigravityGroup string
}

// RunPoller refreshes quota snapshots immediately and then at the configured interval.
func RunPoller(ctx context.Context, cache *Cache, caller HostCaller, cfg PollerConfig) {
	interval := cfg.Interval
	if interval < time.Minute {
		interval = 10 * time.Minute
	}
	refresh := func() {
		RefreshAllFromHost(caller, cache, cfg.Providers, cfg.AntigravityGroup, time.Now())
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

type quotaFetchEnvelope struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type antigravityHints struct {
	Endpoint string `json:"endpoint"`
}

type credentialStorage struct {
	AccessToken                string           `json:"access_token"`
	AccountID                  string           `json:"account_id"`
	ProjectID                  string           `json:"project_id"`
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

func fetchAntigravity(storage credentialStorage, caller HostCaller, now time.Time, group string) (pluginapi.QuotaFetchResponse, *AccountQuotaInfo, error) {
	if strings.TrimSpace(storage.AccessToken) == "" {
		return pluginapi.QuotaFetchResponse{}, nil, fmt.Errorf("antigravity access token is missing")
	}
	base := strings.TrimRight(strings.TrimSpace(storage.AntigravityCapabilityHints.Endpoint), "/")
	endpoints := []string{
		base + "/v1internal:retrieveUserQuotaSummary",
		"https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
		"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary",
		"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
	}
	seen := make(map[string]struct{}, len(endpoints))
	body, varErr := []byte(nil), error(nil)
	for _, endpoint := range endpoints {
		if endpoint == "/v1internal:retrieveUserQuotaSummary" || endpoint == "" {
			continue
		}
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		requestBody := map[string]string{}
		if strings.TrimSpace(storage.ProjectID) != "" {
			requestBody["project"] = storage.ProjectID
		}
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return pluginapi.QuotaFetchResponse{}, nil, err
		}
		response, err := doHostHTTP(caller, pluginapi.HTTPRequest{
			Method: "POST",
			URL:    endpoint,
			Headers: map[string][]string{
				"Authorization": {"Bearer " + storage.AccessToken},
				"Content-Type":  {"application/json"},
				"Accept":        {"application/json"},
				"User-Agent":    {"Antigravity/1.0.0"},
			},
			Body: encoded,
		})
		if err != nil {
			varErr = err
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			varErr = fmt.Errorf("quota endpoint returned HTTP %d", response.StatusCode)
			continue
		}
		body = response.Body
		varErr = nil
		break
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
	response, err := doHostHTTP(caller, pluginapi.HTTPRequest{
		Method: "GET",
		URL:    base + "/backend-api/wham/usage",
		Headers: map[string][]string{
			"Authorization": {"Bearer " + storage.AccessToken},
			"Accept":        {"application/json"},
		},
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
		WeeklyRemainingPercent:      weekly,
		WeeklyResetAt:               valueOrZero(weeklyReset),
		ShortWindowRemainingPercent: short,
		ShortWindowResetAt:          valueOrZero(shortReset),
		IsHealthy:                   true,
		ObservedAt:                  now.UTC(),
	}
	return pluginapi.QuotaFetchResponse{}, info, nil
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

// RefreshAllFromHost refreshes quota snapshots for all credentials visible to the host.
func RefreshAllFromHost(caller HostCaller, cache *Cache, providers []string, antigravityGroup string, now time.Time) {
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
		if !providerEnabled(provider, providers) || entry.Disabled || entry.Unavailable {
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
		_, info, err := FetchCredentialQuota(provider, credential.JSON, caller, now, antigravityGroup)
		if err != nil || info == nil {
			continue
		}
		info.AuthID = entry.ID
		info.IsHealthy = true
		cache.Set(info)
		if entry.AuthIndex != "" && entry.AuthIndex != entry.ID {
			copyInfo := *info
			copyInfo.AuthID = entry.AuthIndex
			cache.Set(&copyInfo)
		}
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
