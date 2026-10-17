package quota

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func credentialProvider(entry pluginapi.HostAuthFileEntry) string {
	if provider := strings.ToLower(strings.TrimSpace(entry.Provider)); provider != "" {
		return provider
	}
	return strings.ToLower(strings.TrimSpace(entry.Type))
}

func credentialPinKey(entry pluginapi.HostAuthFileEntry) string {
	for _, id := range []string{entry.ID, entry.Name, entry.AuthIndex} {
		if strings.TrimSpace(id) != "" {
			return config.ScopedPinnedKey(credentialProvider(entry), id)
		}
	}
	return ""
}

func resolvePinnedCredential(files []pluginapi.HostAuthFileEntry, provider, target string) (*pluginapi.HostAuthFileEntry, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return nil, fmt.Errorf("account identifier is required")
	}
	if scopedProvider, id, scoped := strings.Cut(target, "|"); scoped {
		if provider != "" && scopedProvider != provider {
			return nil, fmt.Errorf("credential provider does not match request")
		}
		provider, target = scopedProvider, id
	}
	// 唯一索引/完整文件名优先，其次才兼容旧的文件名简写和邮箱。
	for tier := 0; tier < 3; tier++ {
		if tier == 1 && strings.Contains(target, "@") && !strings.HasSuffix(target, ".json") {
			continue
		}
		var matches []*pluginapi.HostAuthFileEntry
		for i := range files {
			entry := &files[i]
			if entry.RuntimeOnly || (provider != "" && credentialProvider(*entry) != provider) {
				continue
			}
			candidates := []string{entry.AuthIndex, entry.ID, entry.Name}
			if tier == 2 {
				candidates = []string{entry.Email, entry.Account}
			}
			for _, candidate := range candidates {
				id := strings.ToLower(strings.TrimSpace(candidate))
				if id == "" {
					continue
				}
				match := id == target
				if tier == 1 {
					match = strings.TrimSuffix(filepath.Base(id), ".json") == strings.TrimSuffix(filepath.Base(target), ".json")
				}
				if match {
					matches = append(matches, entry)
					break
				}
			}
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("账号匹配到多个凭证，请刷新页面后重试（需要供应商和唯一凭证）")
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
	}
	return nil, fmt.Errorf("credential not found for requested provider")
}

func resolveLegacyPinnedAccounts(specs []string, files []pluginapi.HostAuthFileEntry) []string {
	var resolved []string
	for _, spec := range specs {
		pattern, priority := config.ParsePinnedSpec(spec)
		entry, err := resolvePinnedCredential(files, "", pattern)
		// 有歧义的旧邮箱规则不再扩散；凭证自身已保存的固定标记仍然独立生效。
		if err != nil {
			continue
		}
		key := credentialPinKey(*entry)
		if key == "" {
			continue
		}
		if priority != nil {
			key = fmt.Sprintf("%s=%d", key, *priority)
		}
		resolved = append(resolved, key)
	}
	return resolved
}
