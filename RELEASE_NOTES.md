# Smart Quota Rotator v1.2.5

- 修复同一邮箱跨供应商误固定：下拉框提交供应商和唯一凭证 ID；固定规则保存为供应商与凭证 ID 的组合。
- 固定和取消固定均只影响选中的凭证。同供应商下同邮箱的个人版、团队版凭证也独立处理。
- 旧客户端若只提交有歧义的邮箱，直接拒绝写入，避免选中列表里的第一个账号。
- 旧配置中的邮箱规则只在完整凭证列表里能唯一匹配时转换；存在歧义时不再扩散。凭证自身已保存的固定标记保持独立。
- 修复资源页保存失败却显示成功的问题。保留 v1.2.4 的 Antigravity Hub 配额兼容修复。

Pins and unpins are now bound to a provider and one credential. Existing rotation and parallel-count behavior is unchanged.
