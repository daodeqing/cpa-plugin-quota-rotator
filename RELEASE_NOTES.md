# Release Notes v1.0.0

## Features
- **Two-Stage Quota Priority Rotator (两阶段配额优先级轮换)**:
  - **Stage 1 (100% Full Capacity)**: Prefers 100% weekly quota accounts with Round-Robin distribution across the pool.
  - **Stage 2 (<=99% Reset-Soon First)**: When all healthy accounts decrease to <=99%, automatically concentrates requests on the account with the shortest countdown until weekly quota reset (\Delta t = ResetTime - Now).
  - **Health Guard & Bypass**: Completely bypasses accounts with 0% weekly quota, exhausted 5-hour short window, or cooldown/disabled states.
  - **Auto Recovery**: Automatically reverts back to Stage 1 as soon as any account's weekly quota refreshes back to 100%.
- **Cross-Platform CGO Shared Library**: Builds natively for Windows (.dll), Linux (.so), macOS (.dylib).
- **Management API**: Exposes `GET /v0/management/plugins/quota-rotator/status` for real-time observability.
