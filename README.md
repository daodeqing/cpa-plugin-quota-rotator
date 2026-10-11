# Quota Priority Rotator (`cpa-plugin-quota-rotator`)

A native CGO dynamic scheduler plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that implements the **Reset-Soon First (即将刷新优先)** two-stage account quota rotation strategy.

## Key Mechanisms

1. **Stage 1 (100% Full-Capacity Round-Robin)**:
   - When healthy accounts with 100% weekly quota exist, requests are load-balanced among them using Round-Robin.
2. **Stage 2 (<=99% Reset-Soon First)**:
   - When all healthy accounts drop to **99% or below**, the scheduler switches to **Reset-Soon First**.
   - Traffic is directed to the account with the shortest time remaining until weekly reset (\Delta t = ResetTime - Now).
   - Maximizes quota utilization before the weekly reset wipes or restores the quota.
3. **Health Guard & Strict Bypass**:
   - Accounts with **0% weekly quota** are bypassed immediately.
   - Accounts whose **5-hour short window is 100% used (0% remaining)** are strictly bypassed (`strict_short_window: true`).
   - Accounts in cooldown or disabled state are bypassed.
4. **Auto-Recovery**:
   - Once an account reaches its weekly reset and returns to 100%, the system returns to Stage 1.

## Configuration

In your CPA `config.yaml`:

```yaml
plugins:
  enabled: true
  configs:
    quota-rotator:
      enabled: true
      strict_short_window: true
      refresh_interval: "10m"
      providers:
        - "codex"
        - "antigravity"
      antigravity_group: "gemini"
```

## Management API

- `GET /v0/management/plugins/quota-rotator/status`: View live status, active stage, account quotas, and countdowns.
