# Smart Quota Rotator v1.2.2 Release Notes

## New Features & Enhancements
- **Privacy Mode Adaptation (隐私模式适配)**: Automatically detects host privacy mode from `privacy-mode.json`, masks email and account identities (e.g. `la***@g***.com`), and provides an interactive toggle button directly on the quota window header for instant client-side switching.
- **Top Bar Parallel Accounts Selector (把并行账号改到上面)**: Moved parallel count control directly to the top of the quota window, allowing users to switch between 1~8 parallel accounts without opening the plugin configuration modal.
- **Inline Selectable Account Priority (固定账号优先级做成可选择的)**: Added interactive priority dropdowns to every row in the table, allowing users to switch an account between auto-rotation and fixed priority tiers (1..10) in one click with instant status feedback.
