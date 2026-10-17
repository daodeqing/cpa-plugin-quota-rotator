package config

import "testing"

func TestProviderScopedPinDoesNotMatchAnotherProvider(t *testing.T) {
	cfg := Config{PinnedAccounts: []string{"codex|shared-id=3"}}
	if pinned, _ := cfg.MatchPinnedAccountForProvider("antigravity", "shared-id"); pinned {
		t.Fatal("provider isolation failed")
	}
	pinned, priority := cfg.MatchPinnedAccountForProvider("codex", "shared-id")
	if !pinned || priority == nil || *priority != 3 {
		t.Fatal("correct provider pin missing")
	}
}

func TestBareEmailConfigCannotBeAppliedWithoutDisambiguation(t *testing.T) {
	cfg := Config{PinnedAccounts: []string{"user@example.com=3"}}
	for _, provider := range []string{"codex", "antigravity"} {
		if pinned, _ := cfg.MatchPinnedAccountForProvider(provider, "user@example.com"); pinned {
			t.Fatal("bare email bypassed ambiguity resolution")
		}
	}
}

func TestPinIdentifierDoesNotUseEmailSubstring(t *testing.T) {
	cfg := Config{PinnedAccounts: []string{"codex|user@example.com=3"}}
	if pinned, _ := cfg.MatchPinnedAccountForProvider("codex", "other-user@example.com"); pinned {
		t.Fatal("substring email matched")
	}
}

func TestScopedNumericCredentialDoesNotBecomePriority(t *testing.T) {
	pattern, priority := ParsePinnedSpec("codex|123456")
	if pattern != "codex|123456" || priority != nil {
		t.Fatal("numeric credential identity was parsed as priority")
	}
}
