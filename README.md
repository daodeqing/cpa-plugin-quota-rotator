# Quota Priority Rotator

> [English](#english-documentation) | [中文说明](#chinese-documentation) | [README.zh-CN.md](README.zh-CN.md)

---

## English Documentation

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


---

## Chinese Documentation

# 配额优先级轮换插件 (Quota Priority Rotator)

适用于 [CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) 的原生 CGO 动态调度器插件，实现严格的两阶段周限“即将刷新优先（Reset-soon first）”自动轮换机制。

## 核心机制与调度规则

1. **阶段一（100% 满额轮询期）**：
   - 只要账号池中存在周限制额度为 100% 的健康可用账号，所有流量绝对优先打入 100% 账号，并在满额账号之间通过 Round-Robin 均匀轮询。
2. **阶段二（<=99% 即将刷新优先期）**：
   - 当所有健康可用账号的周限均下降至 99% 及以下（且 >0%）时，自动切换至“即将刷新优先”状态。
   - 算法计算每个账号距离下次周限刷新的倒计时：
     \Delta t = \text{ResetTime} - \text{Now}
   - 流量全力导入 \Delta t 最小（距离刷新时间最近）的头部账号，最大化利用即将重置的过期额度。
3. **避让与降级保护 (Health Guard)**：
   - 周限彻底耗尽（0%）的账号直接剔除避让。
   - 5 小时短窗耗尽（0% 剩余 / 100% 使用率）的账号在开启 `strict_short_window: true` 时强制剔除避让。
   - 处于 Cooldown（冷却）或 Disabled（禁用）的账号自动剔除避让。
4. **自动复位机制 (Auto Recovery)**：
   - 当某个账号到达刷新周期并恢复为 100% 时，调度器无缝回退至阶段一，重新优先轮询 100% 账号。

## 配置说明

在 CPA `config.yaml` 的 `plugins.configs` 中添加：

```yaml
plugins:
  enabled: true
  configs:
    quota-rotator:
      enabled: true                     # 自动轮换总开关
      strict_short_window: true         # 强制 5 小时短窗耗尽避让保护
      refresh_interval: "10m"           # 配额后台异步刷新频率
      providers:                        # 生效的渠道列表
        - "codex"
        - "antigravity"
      antigravity_group: "gemini"       # Antigravity 针对的模型分组
```

## 管理与诊断接口

- `GET /v0/management/plugins/quota-rotator/status`：查看插件实时状态、当前处于阶段（`stage_1_full` 或 `stage_2_reset_soon`）、各账号配额百分比、刷新倒计时秒数与避让原因。
