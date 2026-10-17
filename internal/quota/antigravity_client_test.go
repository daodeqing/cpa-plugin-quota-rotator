package quota

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type httpQuotaHost struct {
	request func(pluginapi.HTTPRequest) (int, []byte, error)
}

func (h httpQuotaHost) Call(method string, raw []byte) ([]byte, error) {
	if method != pluginabi.MethodHostHTTPDo {
		return nil, fmt.Errorf("unexpected callback %s", method)
	}
	var request pluginapi.HTTPRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	status, body, err := h.request(request)
	if err != nil {
		return nil, err
	}
	return mustJSON(map[string]any{"ok": true, "result": pluginapi.HTTPResponse{StatusCode: status, Body: body}}), nil
}

func resetAntigravityVersionCache(t *testing.T) {
	t.Helper()
	reset := func() {
		antigravityVersionCache.Lock()
		antigravityVersionCache.version = ""
		antigravityVersionCache.expiresAt = time.Time{}
		antigravityVersionCache.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

const geminiQuotaFixture = `{"groups":[{"displayName":"Gemini Models","buckets":[{"window":"weekly","remainingFraction":0.72,"resetTime":"2026-10-24T02:00:00Z"},{"window":"5h","remainingFraction":0.43,"resetTime":"2026-10-17T07:00:00Z"}]}]}`
const thirdPartyQuotaFixture = `{"groups":[{"displayName":"Claude and GPT models","buckets":[{"window":"weekly","remainingFraction":1}]}]}`

func TestAntigravityHubIdentityRestoresGeminiQuota(t *testing.T) {
	resetAntigravityVersionCache(t)
	now := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	manifestCalls, quotaCalls := 0, 0
	caller := httpQuotaHost{request: func(req pluginapi.HTTPRequest) (int, []byte, error) {
		if req.URL == antigravityVersionManifestURL {
			manifestCalls++
			if req.Headers.Get("Authorization") != "" {
				t.Fatal("manifest must not receive account token")
			}
			return 200, []byte("version: 2.24.1\n"), nil
		}
		quotaCalls++
		if req.Headers.Get("User-Agent") != "antigravity/hub/2.24.1 darwin/arm64" {
			// 复现旧客户端只收到第三方模型组的服务端行为。
			return 200, []byte(thirdPartyQuotaFixture), nil
		}
		if req.Headers.Get("Authorization") != "Bearer test-access" {
			t.Fatal("missing account authorization")
		}
		return 200, []byte(geminiQuotaFixture), nil
	}}
	for i := 0; i < 2; i++ {
		response, info, err := FetchCredentialQuota("antigravity", []byte(`{"access_token":"test-access","project_id":"aicode-consumers"}`), caller, now, "gemini")
		if err != nil {
			t.Fatal(err)
		}
		if info.WeeklyRemainingPercent != 72 || info.ShortWindowRemainingPercent != 43 {
			t.Fatalf("wrong quota: %+v", info)
		}
		if len(response.Groups) != 1 || len(response.Groups[0].Buckets) != 2 {
			t.Fatalf("management quota missing: %+v", response)
		}
	}
	if manifestCalls != 1 || quotaCalls != 2 {
		t.Fatalf("manifest=%d quota=%d", manifestCalls, quotaCalls)
	}
}

func TestAntigravityHTTP200WithoutGeminiFallsBack(t *testing.T) {
	resetAntigravityVersionCache(t)
	calls := []string{}
	caller := httpQuotaHost{request: func(req pluginapi.HTTPRequest) (int, []byte, error) {
		if req.URL == antigravityVersionManifestURL {
			return 503, nil, nil
		}
		calls = append(calls, req.URL)
		if strings.Contains(req.URL, "daily-cloudcode") {
			return 200, []byte(thirdPartyQuotaFixture), nil
		}
		return 200, []byte(geminiQuotaFixture), nil
	}}
	_, info, err := FetchCredentialQuota("antigravity", []byte(`{"access_token":"test"}`), caller, time.Now(), "gemini")
	if err != nil || info == nil || info.WeeklyRemainingPercent != 72 {
		t.Fatalf("fallback failed: %v %+v", err, info)
	}
	if len(calls) != 2 || calls[1] != "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary" {
		t.Fatalf("wrong fallback sequence: %v", calls)
	}
}

func TestAntigravityRefreshesUnauthorizedTokenOnce(t *testing.T) {
	resetAntigravityVersionCache(t)
	refreshCalls, quotaCalls := 0, 0
	caller := httpQuotaHost{request: func(req pluginapi.HTTPRequest) (int, []byte, error) {
		if req.URL == antigravityVersionManifestURL {
			return 503, nil, nil
		}
		if req.URL == antigravityOAuthTokenURL {
			refreshCalls++
			if !strings.Contains(string(req.Body), "refresh_token=refresh-test") {
				t.Fatal("missing refresh token")
			}
			return 200, []byte(`{"access_token":"new-token"}`), nil
		}
		quotaCalls++
		if req.Headers.Get("Authorization") == "Bearer old-token" {
			return 401, nil, nil
		}
		if req.Headers.Get("Authorization") != "Bearer new-token" {
			t.Fatal("refreshed token was not used")
		}
		return 200, []byte(geminiQuotaFixture), nil
	}}
	_, info, err := FetchCredentialQuota("antigravity", []byte(`{"access_token":"old-token","refresh_token":"refresh-test"}`), caller, time.Now(), "gemini")
	if err != nil || info == nil || refreshCalls != 1 || quotaCalls != 2 {
		t.Fatalf("err=%v refresh=%d quota=%d", err, refreshCalls, quotaCalls)
	}
}

func TestAntigravityVersionManifestValidationAndRetry(t *testing.T) {
	for _, manifest := range []string{"version: 1.0.0\n", "version: invalid\n", "version: 2.24.1.extra\n", "version: 99999999.1.1\n"} {
		t.Run(manifest, func(t *testing.T) {
			resetAntigravityVersionCache(t)
			now := time.Now()
			calls := 0
			caller := httpQuotaHost{request: func(req pluginapi.HTTPRequest) (int, []byte, error) {
				calls++
				if calls == 1 {
					return 200, []byte(manifest), nil
				}
				return 200, []byte("version: 2.24.1\n"), nil
			}}
			if got := antigravityUserAgent(caller, now); got != "antigravity/hub/2.23.0 darwin/arm64" {
				t.Fatal(got)
			}
			_ = antigravityUserAgent(caller, now.Add(time.Minute))
			if calls != 1 {
				t.Fatal("failed manifest was not cached")
			}
			if got := antigravityUserAgent(caller, now.Add(6*time.Minute)); got != "antigravity/hub/2.24.1 darwin/arm64" {
				t.Fatal(got)
			}
		})
	}
}

type pollQuotaHost struct {
	auth *mockHostCaller
	http httpQuotaHost
}

func (h pollQuotaHost) Call(method string, raw []byte) ([]byte, error) {
	if method == pluginabi.MethodHostHTTPDo {
		return h.http.Call(method, raw)
	}
	return h.auth.Call(method, raw)
}

func TestFailedAntigravityPollRemainsVisiblePreservesPriorityAndRecovers(t *testing.T) {
	resetAntigravityVersionCache(t)
	now := time.Now()
	cache := NewCache()
	auth := &mockHostCaller{
		files:     []map[string]any{{"id": "account.json", "name": "account.json", "auth_index": "idx", "provider": "antigravity", "priority": 7}},
		savedDocs: map[string]map[string]any{"account.json": {"access_token": "test", "priority": 7}},
	}
	failing := true
	caller := pollQuotaHost{auth: auth, http: httpQuotaHost{request: func(req pluginapi.HTTPRequest) (int, []byte, error) {
		if req.URL == antigravityVersionManifestURL {
			return 503, nil, nil
		}
		if failing {
			return 503, nil, nil
		}
		return 200, []byte(geminiQuotaFixture), nil
	}}}
	cfg := config.DefaultConfig()
	RefreshAllFromHostWithConfig(caller, cache, cfg, now)
	info, ok := cache.Get("account.json")
	if !ok || info.QuotaError == "" || !info.ObservedAt.IsZero() || !info.IsBypassed(true) {
		t.Fatalf("missing failed account: %+v", info)
	}
	if auth.savedDocs["account.json"]["priority"] != 7 {
		t.Fatal("unknown quota changed saved priority")
	}
	if alias, ok := cache.Get("idx"); !ok || alias.QuotaError == "" {
		t.Fatal("failed account alias missing")
	}
	failing = false
	RefreshAllFromHostWithConfig(caller, cache, cfg, now.Add(time.Minute))
	info, _ = cache.Get("account.json")
	if info.QuotaError != "" || !info.IsHealthy || info.WeeklyRemainingPercent != 72 {
		t.Fatalf("recovery failed: %+v", info)
	}
	observed := info.ObservedAt
	failing = true
	RefreshAllFromHostWithConfig(caller, cache, cfg, now.Add(2*time.Minute))
	info, _ = cache.Get("account.json")
	if info.QuotaError == "" || !info.ObservedAt.Equal(observed) || info.WeeklyRemainingPercent != 72 {
		t.Fatalf("last successful snapshot lost: %+v", info)
	}
}

func TestQuotaFailureDoesNotExposeHostSecrets(t *testing.T) {
	message := quotaFetchErrorMessage(fmt.Errorf("network failed Authorization: Bearer PRIVATE_TOKEN user@example.com"))
	if strings.Contains(message, "PRIVATE_TOKEN") || strings.Contains(message, "user@example.com") {
		t.Fatal(message)
	}
}
