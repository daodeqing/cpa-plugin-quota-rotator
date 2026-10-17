package quota

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	antigravityFallbackVersion    = "2.23.0"
	antigravityVersionManifestURL = "https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml"
)

var antigravityVersionCache struct {
	sync.Mutex
	version   string
	expiresAt time.Time
}

// 使用官方 Hub 更新清单，避免旧客户端标识导致 Gemini 配额组被服务端省略。
func antigravityUserAgent(caller HostCaller, now time.Time) string {
	antigravityVersionCache.Lock()
	defer antigravityVersionCache.Unlock()
	if antigravityVersionCache.version != "" && now.Before(antigravityVersionCache.expiresAt) {
		return "antigravity/hub/" + antigravityVersionCache.version + " darwin/arm64"
	}

	version := antigravityVersionCache.version
	if version == "" {
		version = antigravityFallbackVersion
	}
	retryAfter := 5 * time.Minute
	response, err := doHostHTTP(caller, pluginapi.HTTPRequest{
		Method: "GET",
		URL:    antigravityVersionManifestURL,
		Headers: map[string][]string{
			"User-Agent":    {"electron-builder"},
			"Cache-Control": {"no-cache"},
		},
	})
	if err == nil && response.StatusCode == 200 && len(response.Body) <= 4096 {
		var manifest struct {
			Version string `yaml:"version"`
		}
		if yaml.Unmarshal(response.Body, &manifest) == nil && validAntigravityVersion(manifest.Version) {
			// 不允许更新清单把已经可用的请求标识降回不兼容版本。
			if compareAntigravityVersion(manifest.Version, version) >= 0 {
				version = strings.TrimSpace(manifest.Version)
				retryAfter = 6 * time.Hour
			}
		}
	}
	antigravityVersionCache.version = version
	antigravityVersionCache.expiresAt = now.Add(retryAfter)
	return "antigravity/hub/" + version + " darwin/arm64"
}

func validAntigravityVersion(version string) bool {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 6 {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func compareAntigravityVersion(a, b string) int {
	var av, bv [3]int
	_, _ = fmt.Sscanf(strings.TrimSpace(a), "%d.%d.%d", &av[0], &av[1], &av[2])
	_, _ = fmt.Sscanf(strings.TrimSpace(b), "%d.%d.%d", &bv[0], &bv[1], &bv[2])
	for i := range av {
		if av[i] < bv[i] {
			return -1
		}
		if av[i] > bv[i] {
			return 1
		}
	}
	return 0
}
