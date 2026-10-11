# -*- coding: utf-8 -*-
"""
Standalone Python simulation and verification of the Quota Priority Rotator
two-stage state machine (Stage 1: 100% Full Capacity Round-Robin;
Stage 2: <=99% Reset-Soon First; Health Guard / Bypass; Auto Recovery).
"""
import time
from datetime import datetime, timedelta

class AccountQuotaInfo:
    def __init__(self, auth_id, provider, weekly_remaining, weekly_reset_at,
                 short_remaining=100.0, is_healthy=True):
        self.auth_id = auth_id
        self.provider = provider
        self.weekly_remaining = float(weekly_remaining)
        self.weekly_reset_at = weekly_reset_at
        self.short_remaining = float(short_remaining)
        self.is_healthy = is_healthy

    def reset_countdown(self, now):
        if not self.weekly_reset_at:
            return timedelta(days=7)
        diff = self.weekly_reset_at - now
        return diff if diff > timedelta(0) else timedelta(0)

    def is_stage_one_eligible(self):
        return self.weekly_remaining >= 99.999

    def is_bypassed(self, strict_short_window=True):
        if not self.is_healthy:
            return True
        if self.weekly_remaining <= 0.001:
            return True
        if strict_short_window and self.short_remaining <= 0.001:
            return True
        return False

class QuotaRotator:
    def __init__(self, strict_short_window=True):
        self.strict_short_window = strict_short_window
        self.rr_indices = {}
        self.accounts = {}

    def set_account(self, acc):
        self.accounts[acc.auth_id] = acc

    def pick(self, provider, model, candidates, now):
        route_key = f"{provider}:{model.lower()}"
        active = []

        # Step 1: Health Guard
        for cid in candidates:
            acc = self.accounts.get(cid)
            if not acc:
                acc = AccountQuotaInfo(cid, provider, 100.0, now + timedelta(days=7))
            if acc.is_bypassed(self.strict_short_window):
                continue
            active.append((acc, acc.reset_countdown(now)))

        if not active:
            return {"handled": False, "auth_id": None, "stage": "none"}

        # Step 2: Stage 1 (100% Full Capacity Round-Robin)
        stage_one = [item for item in active if item[0].is_stage_one_eligible()]
        if stage_one:
            idx = self.rr_indices.get(route_key, 0)
            if idx >= len(stage_one):
                idx = 0
            picked = stage_one[idx][0]
            self.rr_indices[route_key] = (idx + 1) % len(stage_one)
            return {"handled": True, "auth_id": picked.auth_id, "stage": "stage_1_full"}

        # Step 3: Stage 2 (<=99% Reset-Soon First)
        # Sort ascending by countdown delta t
        active.sort(key=lambda x: (x[1], -x[0].weekly_remaining, x[0].auth_id))
        picked = active[0][0]
        return {"handled": True, "auth_id": picked.auth_id, "stage": "stage_2_reset_soon"}

def run_tests():
    now = datetime.now()
    rot = QuotaRotator(strict_short_window=True)

    print("============================================================")
    print("Running CPA Quota Priority Rotator Scenario Verification")
    print("============================================================")

    # -------------------------------------------------------------
    # Scenario 1: 100% Full Capacity Round-Robin
    # -------------------------------------------------------------
    print("\n[Scenario 1] 100% Full Capacity Round-Robin:")
    rot.set_account(AccountQuotaInfo("acc-A", "codex", 100.0, now + timedelta(days=5)))
    rot.set_account(AccountQuotaInfo("acc-B", "codex", 100.0, now + timedelta(days=2)))

    picks = [rot.pick("codex", "gpt-5", ["acc-A", "acc-B"], now) for _ in range(4)]
    sequence = [p["auth_id"] for p in picks]
    stages = [p["stage"] for p in picks]
    print(f"  Pick sequence: {sequence}")
    print(f"  Active stages: {stages}")
    assert sequence == ["acc-A", "acc-B", "acc-A", "acc-B"], "Failed Scenario 1 RR"
    assert all(s == "stage_1_full" for s in stages), "Failed Scenario 1 Stage check"
    print("  -> PASS: Alternates evenly in Stage 1 Round-Robin.")

    # -------------------------------------------------------------
    # Scenario 2: Transition to Reset-Soon First at <=99%
    # -------------------------------------------------------------
    print("\n[Scenario 2] Transition to Reset-Soon First at <=99%:")
    rot.set_account(AccountQuotaInfo("acc-A", "codex", 99.0, now + timedelta(hours=3)))
    rot.set_account(AccountQuotaInfo("acc-B", "codex", 99.0, now + timedelta(days=4)))

    picks = [rot.pick("codex", "gpt-5", ["acc-B", "acc-A"], now) for _ in range(3)]
    sequence = [p["auth_id"] for p in picks]
    stages = [p["stage"] for p in picks]
    print(f"  Pick sequence: {sequence}")
    print(f"  Active stages: {stages}")
    assert sequence == ["acc-A", "acc-A", "acc-A"], "Failed Scenario 2 Reset-soon"
    assert all(s == "stage_2_reset_soon" for s in stages), "Failed Scenario 2 Stage check"
    print("  -> PASS: 100% traffic locked to acc-A (3h countdown vs 4 days).")

    # -------------------------------------------------------------
    # Scenario 3: Bypass on Short Window (0%) or Weekly Exhaustion (0%)
    # -------------------------------------------------------------
    print("\n[Scenario 3] Strict Bypass on Exhaustion / 5h Short Window (0%):")
    # acc-A has 1h until reset, but 5h short window exhausted (0%)
    rot.set_account(AccountQuotaInfo("acc-A", "codex", 90.0, now + timedelta(hours=1), short_remaining=0.0))
    rot.set_account(AccountQuotaInfo("acc-B", "codex", 85.0, now + timedelta(hours=20), short_remaining=80.0))

    res = rot.pick("codex", "gpt-5", ["acc-A", "acc-B"], now)
    print(f"  Pick result: {res['auth_id']} (Stage: {res['stage']})")
    assert res["auth_id"] == "acc-B", "Failed Scenario 3 short window bypass"
    assert res["stage"] == "stage_2_reset_soon"
    print("  -> PASS: acc-A bypassed immediately due to 0% short window, routed to acc-B.")

    # -------------------------------------------------------------
    # Scenario 4: Auto-Recovery back to Stage 1 when an account resets to 100%
    # -------------------------------------------------------------
    print("\n[Scenario 4] Auto-Recovery to Stage 1 on Quota Reset:")
    # acc-B reaches reset time and restores to 100%
    rot.set_account(AccountQuotaInfo("acc-B", "codex", 100.0, now + timedelta(days=7), short_remaining=100.0))
    res = rot.pick("codex", "gpt-5", ["acc-A", "acc-B"], now)
    print(f"  Pick result: {res['auth_id']} (Stage: {res['stage']})")
    assert res["auth_id"] == "acc-B", "Failed Scenario 4 recovery"
    assert res["stage"] == "stage_1_full"
    print("  -> PASS: Successfully reverted to Stage 1 full-capacity routing.")

    print("\n============================================================")
    print("ALL 4 CORE SCENARIOS VERIFIED SUCCESSFULLY!")
    print("============================================================")

if __name__ == '__main__':
    run_tests()
