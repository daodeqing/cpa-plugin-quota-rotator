# Release v1.1.0

## English
- Adds a native quota provider for Codex and Antigravity.
- Refreshes quota snapshots through CPA host callbacks and feeds weekly and five-hour windows into the scheduler.
- Makes reset-soon routing and `parallel_count` use live quota data instead of an empty cache.

## 中文
- 新增 Codex 与 Antigravity 原生配额提供者。
- 通过 CPA 宿主回调后台刷新周限额与五小时短窗，并写入调度缓存。
- “即将刷新优先”和 `parallel_count` 开始使用实时配额数据。

---

# Release v1.0.0

## English
### Features
- **Two-Stage Quota Priority Rotator**:
  - **Stage 1 (100% Full-Capacity)**: Prefers 100% weekly quota accounts with Round-Robin distribution across the pool.
  - **Stage 2 (<=99% Reset-Soon First)**: When all healthy accounts decrease to <=99%, automatically concentrates requests on the account with the shortest countdown until weekly quota reset to maximize utilization before reset.
  - **Health Guard & Bypass**: Strictly bypasses accounts with 0% weekly quota, exhausted 5-hour short window (0% remaining), or cooldown/disabled states.
  - **Auto-Recovery**: Automatically reverts back to Stage 1 as soon as any account weekly quota refreshes back to 100%.
- **Management API**: Exposes GET /v0/management/plugins/quota-rotator/status for real-time observability.
- **Cross-Platform CGO Shared Library**: Builds natively for Windows (.dll), Linux (.so), macOS (.dylib).

---

## Chinese Documentation
### Features
- **Two-Stage Quota Priority Rotator**:
  - **Stage 1**: When healthy accounts with 100% weekly quota exist, requests are prioritized on 100% accounts with Round-Robin distribution.
  - **Stage 2**: When all healthy accounts decrease to <=99%, automatically switches to Reset-Soon First, prioritizing accounts closest to their weekly reset.
  - **Health Guard**: 0% weekly quota, 0% short window, or cooldown/disabled accounts are strictly bypassed.
  - **Auto-Recovery**: Instantly reverts back to Stage 1 when an account resets to 100%.
- **Management API**: GET /v0/management/plugins/quota-rotator/status.
- **Cross-Platform**: Windows (.dll), Linux (.so), macOS (.dylib).
