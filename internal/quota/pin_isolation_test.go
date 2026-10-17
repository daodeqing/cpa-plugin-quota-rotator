package quota

import (
	"reflect"
	"testing"
	"time"

	"github.com/daodeqing/cpa-plugin-quota-rotator/internal/config"
)

func sharedEmailPinFixture() (*mockHostCaller, *Cache, config.Config, time.Time) {
	now := time.Date(2026, 10, 17, 4, 0, 0, 0, time.UTC)
	caller := &mockHostCaller{
		files: []map[string]any{
			{"id": "ag-shared.json", "name": "ag-shared.json", "auth_index": "ag-index", "provider": "antigravity", "email": "shared@example.com", "priority": 2},
			{"id": "codex-shared.json", "name": "codex-shared.json", "auth_index": "codex-index", "provider": "codex", "email": "shared@example.com", "priority": 2},
			{"id": "codex-team.json", "name": "codex-team.json", "auth_index": "codex-team-index", "provider": "codex", "email": "shared@example.com", "priority": 2},
		},
		savedDocs: map[string]map[string]any{
			"ag-shared.json":    {"email": "shared@example.com", "priority": 2},
			"codex-shared.json": {"email": "shared@example.com", "priority": 2},
			"codex-team.json":   {"email": "shared@example.com", "priority": 2},
		},
	}
	cache := NewCache()
	for _, f := range caller.files {
		cache.Set(&AccountQuotaInfo{AuthID: f["id"].(string), AuthIndex: f["auth_index"].(string), FileName: f["name"].(string), Email: f["email"].(string), Provider: f["provider"].(string), WeeklyRemainingPercent: 80, ShortWindowRemainingPercent: 90, WeeklyResetAt: now.Add(24 * time.Hour), IsHealthy: true})
	}
	return caller, cache, config.DefaultConfig(), now
}

func TestPinRejectsAmbiguousSameEmailWithoutMutation(t *testing.T) {
	caller, cache, cfg, now := sharedEmailPinFixture()
	before := string(mustJSON(caller.savedDocs))
	p := 1
	if err := SetAccountPinnedInHost(caller, cache, &cfg, "shared@example.com", true, &p, now); err == nil {
		t.Fatal("ambiguous email accepted; a provider's first record was modified")
	}
	if string(mustJSON(caller.savedDocs)) != before || len(cfg.PinnedAccounts) != 0 {
		t.Fatal("rejected request mutated credentials or pin config")
	}
}

func TestPinByCodexIndexDoesNotPinOtherProviderOrCredential(t *testing.T) {
	caller, cache, cfg, now := sharedEmailPinFixture()
	p := 1
	if err := SetAccountPinnedInHost(caller, cache, &cfg, "codex-index", true, &p, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ag-shared.json", "codex-team.json"} {
		info, _ := cache.Get(id)
		if info.IsPinned {
			t.Fatalf("pin leaked to %s", id)
		}
		if _, exists := caller.savedDocs[id]["priority_pinned"]; exists {
			t.Fatalf("pin metadata leaked to %s", id)
		}
	}
	info, _ := cache.Get("codex-shared.json")
	if !info.IsPinned || info.PinnedPriority == nil || *info.PinnedPriority != 1 {
		t.Fatalf("selected Codex credential was not pinned: %+v", info)
	}
	if reflect.DeepEqual(cfg.PinnedAccounts, []string{"shared@example.com=1"}) {
		t.Fatal("email-only pin persisted")
	}
}

func TestProviderAndCredentialPinsRemainIndependentOnUnpin(t *testing.T) {
	caller, cache, cfg, now := sharedEmailPinFixture()
	a, b, c := 1, 2, 3
	for _, pin := range []struct {
		provider, id string
		priority     *int
	}{
		{"codex", "codex-index", &a}, {"antigravity", "ag-index", &b}, {"codex", "codex-team-index", &c},
	} {
		if err := SetAccountPinnedForProviderInHost(caller, cache, &cfg, pin.provider, pin.id, true, pin.priority, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(cfg.PinnedAccounts) != 3 {
		t.Fatalf("expected 3 independent pin specs: %v", cfg.PinnedAccounts)
	}
	if err := SetAccountPinnedForProviderInHost(caller, cache, &cfg, "codex", "codex-index", false, nil, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ag-shared.json", "codex-team.json"} {
		info, _ := cache.Get(id)
		if !info.IsPinned || caller.savedDocs[id]["priority_pinned"] != true {
			t.Fatalf("unpin leaked to %s", id)
		}
	}
	info, _ := cache.Get("codex-shared.json")
	if info.IsPinned {
		t.Fatal("requested credential remained pinned")
	}
}

func TestPinProviderMismatchAndAmbiguousProviderEmailFailClosed(t *testing.T) {
	for _, input := range []struct{ provider, target string }{
		{"codex", "ag-index"}, {"codex", "shared@example.com"}, {"antigravity", "codex|codex-index"},
	} {
		t.Run(input.provider+input.target, func(t *testing.T) {
			caller, cache, cfg, now := sharedEmailPinFixture()
			before := string(mustJSON(caller.savedDocs))
			priority := 3
			if err := SetAccountPinnedForProviderInHost(caller, cache, &cfg, input.provider, input.target, true, &priority, now); err == nil {
				t.Fatal("invalid target accepted")
			}
			if string(mustJSON(caller.savedDocs)) != before || len(cfg.PinnedAccounts) != 0 {
				t.Fatal("rejected pin changed state")
			}
		})
	}
}

func TestLegacyAmbiguousEmailConfigDoesNotPinEveryMatchingAccount(t *testing.T) {
	caller, cache, cfg, now := sharedEmailPinFixture()
	cfg.PinnedAccounts = []string{"shared@example.com=1"}
	if _, err := SyncPrioritiesToHost(caller, cache, cfg, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ag-shared.json", "codex-shared.json", "codex-team.json"} {
		info, _ := cache.Get(id)
		if info.IsPinned {
			t.Fatalf("ambiguous legacy config leaked to %s", id)
		}
	}
}

func TestScopedConfigPinsOnlyRequestedProvider(t *testing.T) {
	caller, cache, cfg, now := sharedEmailPinFixture()
	cfg.PinnedAccounts = []string{"codex|codex-shared.json=1"}
	if _, err := SyncPrioritiesToHost(caller, cache, cfg, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ag-shared.json", "codex-team.json"} {
		info, _ := cache.Get(id)
		if info.IsPinned {
			t.Fatalf("scoped config leaked to %s", id)
		}
	}
	info, _ := cache.Get("codex-shared.json")
	if !info.IsPinned {
		t.Fatal("scoped config was not applied")
	}
}

func TestScopedEmailStillRequiresUniqueCredential(t *testing.T) {
	caller, cache, cfg, now := sharedEmailPinFixture()
	cfg.PinnedAccounts = []string{"codex|shared@example.com=1"}
	if _, err := SyncPrioritiesToHost(caller, cache, cfg, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"codex-shared.json", "codex-team.json"} {
		info, _ := cache.Get(id)
		if info.IsPinned {
			t.Fatal("scoped email affected multiple credentials")
		}
	}
}
