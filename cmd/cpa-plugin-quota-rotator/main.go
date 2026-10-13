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
	version     = "1.1.1"
	repository  = "https://github.com/daodeqing/cpa-plugin-quota-rotator"
	pluginID    = "quota-rotator"
	statusRoute = "/plugins/" + pluginID + "/status"
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
	Routes []managementRoute `json:"routes"`
}

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

type managementRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
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
		return okEnvelope(managementRegistration{Routes: []managementRoute{{
			Method:      http.MethodGet,
			Path:        statusRoute,
			Description: "Current Quota Rotator status: active stages, countdowns, and account states.",
		}}})
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
		Interval:         cfg.GetRefreshDuration(),
		Providers:        cfg.Providers,
		AntigravityGroup: cfg.AntigravityGroup,
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
	response, info, err := quota.FetchCredentialQuota(req.Provider, req.StorageJSON, nativeHostCaller{}, time.Now(), cfg.AntigravityGroup)
	if err != nil {
		return nil, err
	}
	if info != nil {
		info.AuthID = req.AuthID
		quotaCache.Set(info)
		if req.AuthIndex != "" && req.AuthIndex != req.AuthID {
			copyInfo := *info
			copyInfo.AuthID = req.AuthIndex
			quotaCache.Set(&copyInfo)
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

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode management request: %w", err)
	}
	if req.Method != http.MethodGet {
		return okEnvelope(jsonResponse(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"}))
	}
	configMu.RLock()
	cfg := activeConfig
	configMu.RUnlock()
	resp := management.BuildStatusResponse(cfg, rotScheduler, quotaCache, time.Now())
	return okEnvelope(jsonResponse(http.StatusOK, resp))
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
