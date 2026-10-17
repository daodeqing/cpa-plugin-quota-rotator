# Smart Quota Rotator v1.2.4

- 修复反重力账号消失：使用当前 Antigravity Hub 请求标识，并从 Google 官方更新清单缓存最新版本。旧的 1.0.0 标识可能使配额接口省略 Gemini 模型组。
- 配额端点返回 HTTP 200 但没有所选模型组时，继续尝试下一个端点。
- 查询失败的账号保留在看板中，显示失败状态；首次查询失败显示未知值，历史数据明确标记，查询失败不改写原有账号优先级。
- 修复状态 JSON 接口被 HTML 资源路由遮蔽的问题。
- 将构建与发布流程放入 .github，生成 Windows、Linux 和 macOS 共五个平台的安装包及 SHA-256 校验清单。

The Google quota endpoint is still available. This release fixes the client identity and the silent omission of accounts when the selected model group's quota is absent. Existing scheduling strategies and fixed account priorities are preserved.
