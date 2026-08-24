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

## 许可证

遵循 [Multica License](LICENSE)、[NOTICE](NOTICE) 中的品牌、Logo、版权和署名要求。
