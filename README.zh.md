<div align="center">

# Multica

**本地 Codex + Git 任务工作台**

</div>

Multica 在一个本地桌面应用中连接 Git 项目、用户任务、Codex 执行和阶段时间线。应用完全运行在当前电脑上：没有账户、云端 Workspace、团队、邀请、订阅、PostgreSQL 或 Redis。

## 本地模型

- Project = 本地 Git 项目
- Issue = 用户任务
- Task/Run = 一次 Codex 执行
- Comment/Event = 进展、提交、验证和发布记录
- `thread_id` = 对应 Codex 会话

API 只监听 loopback，数据存入单个 SQLite 文件。历史命名中的 `workspace` 只是唯一、本地数据空间的兼容标识，不代表租户，也不能新增或切换。

## 任务工作流

Issue 详情页就是一个任务的工作界面：

- 顶部保留任务定义和本地 Git 项目。
- 状态条明确区分任务状态与当前工作阶段，并展示该阶段最近一次 Run 的状态。
- 会话区按时间展示用户指令和 Codex 回复。只有发生真实阶段切换时才插入阶段分隔；普通执行事件不打断阅读，失败事件仍会明确显示。
- 输入框继续同一个 Issue Session，并排队一次 Codex Run。记录阶段只是进度操作，不会自行执行 commit、push 或 release。
- 完整 Run 历史、执行转录和诊断信息保留在执行日志中。

## 开发

需要 Node.js 22、pnpm 10.28.2 和 Go 1.26.6，不需要 Docker。

```bash
pnpm install
make dev
```

```bash
make up             # 启动 SQLite API 和 Electron 桌面端
make status         # 查看当前 checkout 的本地进程
make down           # 停止进程并保留数据
make db-reset       # 重建当前 checkout 的 SQLite 数据库
make check          # 类型检查、测试和桌面端构建
```

桌面包内置 `multica-server`（本地 SQLite API）和 `multica`（本地执行 CLI）。

从仓库源码安装、启动 Worktree 和执行验证，见 [CLI_INSTALL.md](CLI_INSTALL.md)。

## 许可证

遵循 [Multica License](LICENSE)、[NOTICE](NOTICE) 中的品牌、Logo、版权和署名要求。
