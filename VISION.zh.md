<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="docs/assets/logo-light.svg">
  <img alt="Multica" src="docs/assets/logo-light.svg" width="50">
</picture>

# 愿景

**在一个本地工作台中管理个人任务，并通过 Codex 持续完成它。**

**[English](VISION.md) | 简体中文**

</div>

## 为什么需要 Multica

Codex 能执行复杂工作，但单独的聊天窗口并不能清楚管理这些工作。任务会散落在不同会话中，当前阶段不容易判断，提交、验证和最终结论等证据也常常与最初目标分离。

Multica 把 Codex 执行组织成个人任务工作流。本地 Git 项目提供工作上下文，Issue 记录用户目标，每次 Codex Run 在同一个持久会话中继续推进，阶段用于描述工作进度，评论和结果则保存后续继续或验收所需的决策与证据。

产品明确采用本地、单用户模式。这里只有一个本地数据空间和一个操作者，没有账户、云端 Workspace、组织、成员、邀请、订阅或租户模型。SQLite 在当前电脑上保存任务记录，桌面应用只访问本机 loopback 服务。

## 产品原则

- **任务优先于遥测。** 界面先说明目标、当前状态、会话和下一步，再展示执行细节。
- **一个持久会话。** 后续指令和 Codex 回复持续归属于同一个 Issue Session，不形成彼此断开的聊天。
- **阶段只描述进度。** 分析、实施、审查、测试、提交、推送和发布用于组织工作；切换阶段不会授权或执行交付动作。
- **证据与任务相连。** 进展、失败、提交、验证和发布记录都保留在产生它们的任务中。
- **本地是默认，也是边界。** 项目、数据、执行和配置都属于当前电脑。
- **技术细节可查，但不喧宾夺主。** 完整 Run 历史和诊断信息保留在执行日志中，任务页不会退化成运维面板。

Multica 不是协作平台，也不是脱离人的自动化公司。它是一个专注的个人工作台：决定要做什么，让 Codex 执行，并清楚理解发生了什么。

当前实现和开发命令见 [README](README.zh.md)。
