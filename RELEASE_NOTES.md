# Smart Quota Rotator v1.2.0 Release Notes

## New Features & Fixes
- **Fixed Antigravity Short-Reset Weekly Bucket Classification**: Explicitly inspects `bucket.window` (`"weekly"` vs `"5h"`) in `retrieveUserQuotaSummary` so weekly quotas with `< 8h` remaining until reset are never misclassified as 5-hour short windows.
- **Immediate Priority Sync on Quota Refresh (`host.auth.save`)**: Automatically recalculates and persists account `priority` values (`N, N-1, ..., 2`, with `0%` exhausted at `1` and top `parallel_count` accounts sharing the top priority tier) whenever quotas are refreshed in the GUI or background poller.
- **Pin Account Priority (`pinned_accounts` / `priority_pinned`)**: Users can fix specific accounts' priority via `pinned_accounts` (e.g. `["user@gmail.com", "user@gmail.com=10"]`), via the credential settings dialog (`固定优先级`), or via the one-click Pin button on the Quota page.
- **Status Dashboard & Management Routes**: Added browser status dashboard (`/v0/resource/plugins/quota-rotator/status`) and management endpoints `POST /v0/management/plugins/quota-rotator/refresh` and `POST /v0/management/plugins/quota-rotator/pin`.
