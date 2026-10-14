package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/management"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/quota"
	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/scheduler"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var (
	version      = "1.2.3"
	repository   = "https://github.com/daodeqing/cpa-plugin-quota-rotator"
	pluginID     = "quota-rotator"
	statusRoute  = "/plugins/" + pluginID + "/status"
	refreshRoute = "/plugins/" + pluginID + "/refresh"
	pinRoute     = "/plugins/" + pluginID + "/pin"
	configRoute  = "/plugins/" + pluginID + "/config"
	resourcePath = "/status"
)

var (
	quotaCache   = quota.NewCache()
	rotScheduler = scheduler.NewRotator(config.DefaultConfig(), quotaCache, nil)
	activeConfig = config.DefaultConfig()
	configMu     sync.RWMutex
	pollerMu     sync.Mutex
	pollerCancel context.CancelFunc
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML json.RawMessage `json:"config_yaml"`
}

type quotaFetchRequest struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type nativeHostCaller struct{}

func (nativeHostCaller) Call(method string, request []byte) ([]byte, error) {
	if request == nil {
		request = []byte(`{}`)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	cRequest := C.CBytes(request)
	defer C.free(cRequest)
	var response C.cliproxy_buffer
	status := C.call_host_api(cMethod, (*C.uint8_t)(cRequest), C.size_t(len(request)), &response)
	if status != 0 {
		return nil, fmt.Errorf("host callback %s failed with status %d", method, int(status))
	}
	if response.ptr == nil || response.len == 0 {
		return nil, fmt.Errorf("host callback %s returned an empty response", method)
	}
	raw := C.GoBytes(unsafe.Pointer(response.ptr), C.int(response.len))
	C.free_host_buffer(response.ptr, response.len)
	return raw, nil
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  capabilities       `json:"capabilities"`
}

type capabilities struct {
	Scheduler                 bool `json:"scheduler"`
	SchedulerAcrossPriorities bool `json:"scheduler_across_priorities"`
	ManagementAPI             bool `json:"management_api"`
	QuotaProvider             bool `json:"quota_provider"`
}

type managementRegistration struct {
	Routes    []managementRoute    `json:"routes,omitempty"`
	Resources []managementResource `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type managementResource struct {
	Path        string `json:"path"`
	Menu        string `json:"menu"`
	Description string `json:"description,omitempty"`
}

type managementRequest struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers,omitempty"`
	Query   map[string][]string `json:"query,omitempty"`
	Body    []byte              `json:"body"`
}

func getQueryParam(req managementRequest, key string) string {
	if len(req.Query) > 0 {
		if vals, ok := req.Query[key]; ok && len(vals) > 0 {
			return strings.TrimSpace(vals[0])
		}
	}
	if u, err := url.Parse(req.Path); err == nil {
		if val := u.Query().Get(key); val != "" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func acceptsJSON(req managementRequest) bool {
	if getQueryParam(req, "format") == "json" {
		return true
	}
	for k, vals := range req.Headers {
		if strings.EqualFold(k, "Accept") {
			for _, v := range vals {
				if strings.Contains(strings.ToLower(v), "application/json") {
					return true
				}
			}
		}
	}
	return false
}

func isHostPrivacyModeEnabled() bool {
	candidates := []string{
		"privacy-mode.json",
		"../privacy-mode.json",
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			var val bool
			if err := json.Unmarshal(data, &val); err == nil {
				return val
			}
		}
	}
	return false
}

func updateRoutingStrategyInConfigFile(strategy string) {
	candidates := []string{
		"config.yaml",
		"../config.yaml",
	}
	for _, p := range candidates {
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		re := regexp.MustCompile(`(?m)^(\s*routing_strategy:\s*).*`)
		if re.Match(content) {
			newContent := re.ReplaceAll(content, []byte(fmt.Sprintf("${1}%q", strategy)))
			_ = os.WriteFile(p, newContent, 0644)
			return
		} else {
			reQuota := regexp.MustCompile(`(?m)^(\s*quota-rotator:\s*)`)
			if loc := reQuota.FindIndex(content); loc != nil {
				insert := []byte(fmt.Sprintf("\n            routing_strategy: %q", strategy))
				newContent := append(content[:loc[1]], append(insert, content[loc[1]:]...)...)
				_ = os.WriteFile(p, newContent, 0644)
				return
			}
		}
	}
}

func updateParallelCountInConfigFile(count int) {
	candidates := []string{
		"config.yaml",
		"../config.yaml",
	}
	for _, p := range candidates {
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		re := regexp.MustCompile(`(?m)^(\s*parallel_count:\s*)\d+`)
		if re.Match(content) {
			newContent := re.ReplaceAll(content, []byte(fmt.Sprintf("${1}%d", count)))
			_ = os.WriteFile(p, newContent, 0644)
			return
		}
	}
}
func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	restartQuotaPoller(config.DefaultConfig())
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	pollerMu.Lock()
	if pollerCancel != nil {
		pollerCancel()
		pollerCancel = nil
	}
	pollerMu.Unlock()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodSchedulerPick:
		return pick(request)
	case pluginabi.MethodQuotaIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case pluginabi.MethodQuotaDescribe:
		return okEnvelope(pluginapi.QuotaDescribeResponse{
			SupportedProviders: []string{"codex", "antigravity"},
			DisplayName:        "Smart Quota Rotator",
			SupportsReset:      false,
		})
	case pluginabi.MethodQuotaFetch:
		return fetchQuota(request)
	case pluginabi.MethodQuotaReset:
		return okEnvelope(pluginapi.QuotaResetResponse{Success: false, Message: "quota reset is not supported"})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{
			Routes: []managementRoute{
				{
					Method:      http.MethodGet,
					Path:        statusRoute,
					Description: "Current Quota Rotator status: active stages, countdowns, priorities, and pinned accounts.",
				},
				{
					Method:      http.MethodPost,
					Path:        refreshRoute,
					Description: "Ingest or refresh quota snapshots and immediately sync dynamic priorities to host credentials.",
				},
				{
					Method:      http.MethodPost,
					Path:        pinRoute,
					Description: "Pin or unpin a specific account's priority so it is excluded from or included in auto-rotation.",
				},
			},
			Resources: []managementResource{
				{
					Path:        resourcePath,
					Menu:        "配额轮换看板",
					Description: "实时查看两阶段配额轮换状态、周限刷新倒计时排序与固定优先级",
				},
			},
		})
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var req lifecycleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("decode lifecycle request: %w", err)
	}
	var configBytes []byte
	if len(req.ConfigYAML) > 0 {
		var encoded string
		if err := json.Unmarshal(req.ConfigYAML, &encoded); err == nil {
			if decoded, errB64 := base64.StdEncoding.DecodeString(encoded); errB64 == nil {
				configBytes = decoded
			} else {
				configBytes = []byte(encoded)
			}
		} else {
			configBytes = req.ConfigYAML
		}
	}
	cfg, err := config.Parse(configBytes)
	if err != nil {
		return err
	}
	configMu.Lock()
	activeConfig = cfg
	configMu.Unlock()
	rotScheduler.Configure(cfg)
	restartQuotaPoller(cfg)
	return nil
}

func restartQuotaPoller(cfg config.Config) {
	pollerMu.Lock()
	if pollerCancel != nil {
		pollerCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	pollerCancel = cancel
	pollerMu.Unlock()
	go quota.RunPoller(ctx, quotaCache, nativeHostCaller{}, quota.PollerConfig{
		Config: cfg,
	})
}

func fetchQuota(raw []byte) ([]byte, error) {
	var req quotaFetchRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode quota request: %w", err)
	}
	configMu.RLock()
	cfg := activeConfig
	configMu.RUnlock()
	now := time.Now()
	response, info, err := quota.FetchCredentialQuota(req.Provider, req.StorageJSON, nativeHostCaller{}, now, cfg.AntigravityGroup)
	if err != nil {
		return nil, err
	}
	if info != nil {
		authID := strings.TrimSpace(req.AuthID)
		if authID == "" {
			authID = strings.TrimSpace(req.AuthIndex)
		}
		info.AuthID = authID
		info.AuthIndex = strings.TrimSpace(req.AuthIndex)
		quotaCache.Set(info)
		if req.AuthIndex != "" && req.AuthIndex != authID {
			copyInfo := *info
			copyInfo.AuthID = req.AuthIndex
			quotaCache.Set(&copyInfo)
		}
		if cfg.Enabled {
			_, _ = quota.SyncPrioritiesToHost(nativeHostCaller{}, quotaCache, cfg, now)
		}
	}
	return okEnvelope(response)
}

func pick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode scheduler request: %w", err)
	}
	resp, _, _ := rotScheduler.Pick(req, time.Now())
	return okEnvelope(resp)
}

type refreshManagementPayload struct {
	AuthIndex string          `json:"auth_index"`
	AuthID    string          `json:"auth_id"`
	FileName  string          `json:"file_name"`
	Email     string          `json:"email"`
	Provider  string          `json:"provider"`
	Payload   json.RawMessage `json:"payload"`
}

type pinManagementPayload struct {
	Account   string `json:"account"`
	AuthIndex string `json:"auth_index"`
	Pinned    bool   `json:"pinned"`
	Priority  *int   `json:"priority,omitempty"`
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode management request: %w", err)
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	path := strings.TrimSpace(req.Path)
	now := time.Now()

	configMu.RLock()
	cfg := activeConfig
	configMu.RUnlock()

	if method == http.MethodGet && (strings.Contains(path, "/resource/plugins/"+pluginID+resourcePath) || strings.Contains(path, resourcePath)) {
		action := getQueryParam(req, "action")
		if action == "set_parallel" {
			countStr := getQueryParam(req, "count")
			if count, err := strconv.Atoi(countStr); err == nil && count > 0 {
				configMu.Lock()
				activeConfig.ParallelCount = count
				cfgCopy := activeConfig
				configMu.Unlock()
				rotScheduler.Configure(cfgCopy)
				updateParallelCountInConfigFile(count)
				_, _ = quota.SyncPrioritiesToHost(nativeHostCaller{}, quotaCache, cfgCopy, now)
				if acceptsJSON(req) {
					return okEnvelope(jsonResponse(http.StatusOK, map[string]any{
						"ok":             true,
						"parallel_count": count,
					}))
				}
			}
		} else if action == "set_strategy" {
			strat := strings.ToLower(strings.TrimSpace(getQueryParam(req, "strategy")))
			configMu.Lock()
			activeConfig.RoutingStrategy = strat
			cfgCopy := activeConfig
			configMu.Unlock()
			rotScheduler.Configure(cfgCopy)
			updateRoutingStrategyInConfigFile(strat)
			_, _ = quota.SyncPrioritiesToHost(nativeHostCaller{}, quotaCache, cfgCopy, now)
			if acceptsJSON(req) {
				return okEnvelope(jsonResponse(http.StatusOK, map[string]any{
					"ok":               true,
					"routing_strategy": strat,
				}))
			}
		} else if action == "pin" {
			target := getQueryParam(req, "account")
			prioStr := getQueryParam(req, "priority")
			prio, _ := strconv.Atoi(prioStr)
			configMu.Lock()
			errPin := quota.SetAccountPinnedInHost(nativeHostCaller{}, quotaCache, &activeConfig, target, true, &prio, now)
			cfgCopy := activeConfig
			configMu.Unlock()
			if errPin != nil {
				if acceptsJSON(req) {
					return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": errPin.Error()}))
				}
			} else {
				rotScheduler.Configure(cfgCopy)
				if acceptsJSON(req) {
					return okEnvelope(jsonResponse(http.StatusOK, map[string]any{
						"ok":       true,
						"account":  target,
						"priority": prio,
					}))
				}
			}
		} else if action == "unpin" {
			target := getQueryParam(req, "account")
			configMu.Lock()
			errPin := quota.SetAccountPinnedInHost(nativeHostCaller{}, quotaCache, &activeConfig, target, false, nil, now)
			cfgCopy := activeConfig
			configMu.Unlock()
			if errPin != nil {
				if acceptsJSON(req) {
					return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": errPin.Error()}))
				}
			} else {
				rotScheduler.Configure(cfgCopy)
				if acceptsJSON(req) {
					return okEnvelope(jsonResponse(http.StatusOK, map[string]any{
						"ok":      true,
						"account": target,
						"pinned":  false,
					}))
				}
			}
		}

		configMu.RLock()
		currentCfg := activeConfig
		configMu.RUnlock()
		status := management.BuildStatusResponse(currentCfg, rotScheduler, quotaCache, now)
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       management.RenderStatusPageHTML(status, isHostPrivacyModeEnabled()),
		})
	}

	if method == http.MethodGet && strings.HasSuffix(path, statusRoute) {
		resp := management.BuildStatusResponse(cfg, rotScheduler, quotaCache, now)
		return okEnvelope(jsonResponse(http.StatusOK, resp))
	}

	if method == http.MethodPost && strings.HasSuffix(path, refreshRoute) {
		var body refreshManagementPayload
		if len(req.Body) > 0 {
			_ = json.Unmarshal(req.Body, &body)
		}
		if len(body.Payload) > 0 && strings.TrimSpace(body.Provider) != "" {
			if info, err := quota.IngestUpstreamQuotaPayload(body.Provider, body.Payload, cfg.AntigravityGroup, now); err == nil && info != nil {
				primaryID := strings.TrimSpace(body.AuthID)
				if primaryID == "" {
					primaryID = strings.TrimSpace(body.FileName)
				}
				if primaryID == "" {
					primaryID = strings.TrimSpace(body.AuthIndex)
				}
				info.AuthID = primaryID
				info.AuthIndex = strings.TrimSpace(body.AuthIndex)
				info.FileName = strings.TrimSpace(body.FileName)
				info.Email = strings.TrimSpace(body.Email)
				quotaCache.Set(info)
				if body.AuthIndex != "" && body.AuthIndex != primaryID {
					copyInfo := *info
					copyInfo.AuthID = strings.TrimSpace(body.AuthIndex)
					quotaCache.Set(&copyInfo)
				}
				if cfg.Enabled {
					_, _ = quota.SyncPrioritiesToHost(nativeHostCaller{}, quotaCache, cfg, now)
				}
			}
		} else if strings.TrimSpace(body.AuthIndex) != "" && cfg.Enabled {
			_, _ = quota.SyncPrioritiesToHost(nativeHostCaller{}, quotaCache, cfg, now)
		} else {
			quota.RefreshAllFromHostWithConfig(nativeHostCaller{}, quotaCache, cfg, now)
		}
		resp := management.BuildStatusResponse(cfg, rotScheduler, quotaCache, time.Now())
		return okEnvelope(jsonResponse(http.StatusOK, resp))
	}

	if method == http.MethodPost && strings.HasSuffix(path, configRoute) {
		var body struct {
			ParallelCount *int `json:"parallel_count"`
		}
		if err := json.Unmarshal(req.Body, &body); err == nil && body.ParallelCount != nil && *body.ParallelCount > 0 {
			configMu.Lock()
			activeConfig.ParallelCount = *body.ParallelCount
			cfgCopy := activeConfig
			configMu.Unlock()
			rotScheduler.Configure(cfgCopy)
			updateParallelCountInConfigFile(*body.ParallelCount)
			_, _ = quota.SyncPrioritiesToHost(nativeHostCaller{}, quotaCache, cfgCopy, now)
			resp := management.BuildStatusResponse(cfgCopy, rotScheduler, quotaCache, now)
			return okEnvelope(jsonResponse(http.StatusOK, resp))
		}
		return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid parallel_count"}))
	}

	if method == http.MethodPost && strings.HasSuffix(path, pinRoute) {
		var body pinManagementPayload
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid request body"}))
		}
		target := strings.TrimSpace(body.Account)
		if target == "" {
			target = strings.TrimSpace(body.AuthIndex)
		}
		configMu.Lock()
		errPin := quota.SetAccountPinnedInHost(nativeHostCaller{}, quotaCache, &activeConfig, target, body.Pinned, body.Priority, now)
		updatedCfg := activeConfig
		configMu.Unlock()
		if errPin != nil {
			return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": errPin.Error()}))
		}
		rotScheduler.Configure(updatedCfg)
		resp := management.BuildStatusResponse(updatedCfg, rotScheduler, quotaCache, time.Now())
		return okEnvelope(jsonResponse(http.StatusOK, resp))
	}

	return okEnvelope(jsonResponse(http.StatusMethodNotAllowed, map[string]string{"error": "method or path not allowed"}))
}

func jsonResponse(status int, body any) pluginapi.ManagementResponse {
	raw, err := json.Marshal(body)
	if err != nil {
		status, raw = http.StatusInternalServerError, []byte(`{"error":"encode response"}`)
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       raw,
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "智能配额轮换调度器 (Smart Quota Rotator)",
			Version:          version,
			Author:           "daodeqing",
			GitHubRepository: repository,
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "enabled",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Enable automatic quota priority rotation (Reset-soon first at <=99%).",
				},
				{
					Name:        "strict_short_window",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Strictly bypass accounts when 5-hour short window is 0% exhausted.",
				},
				{
					Name:        "refresh_interval",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Interval for background quota refresh (e.g. 10m).",
				},
				{
					Name:        "providers",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Provider keys to apply rotation to (e.g. codex, antigravity).",
				},
				{
					Name:        "antigravity_group",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Antigravity model group to track (e.g. gemini, claude_gpt).",
				},
				{
					Name:        "parallel_count",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Number of reset-soon accounts to consume in parallel (default 1).",
				},
				{
					Name:        "pinned_accounts",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Accounts with fixed priority excluded from auto-rotation (e.g. user@gmail.com or user@gmail.com=10).",
				},
			},
		},
		Capabilities: capabilities{
			Scheduler:                 true,
			SchedulerAcrossPriorities: true,
			ManagementAPI:             true,
			QuotaProvider:             true,
		},
	}
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
