<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="docs/assets/logo-light.svg">
  <img alt="Multica" src="docs/assets/logo-light.svg" width="50">
</picture>

# Multica

**A local Codex + Git task workbench.**

</div>

Multica connects Git projects, user tasks, Codex runs, and their progress timeline in one local desktop application. It runs entirely on the current computer: there are no accounts, cloud workspaces, teams, invitations, subscriptions, PostgreSQL, or Redis.

## Local model

- Project = a local Git project.
- Issue = a task you want Codex to complete.
- Task/Run = one Codex execution.
- Comment/Event = progress, commits, verification, and release records.
- `thread_id` = the related Codex session.

The API listens on loopback and stores data in one SQLite file. The historical `workspace` identifier is a compatibility name for the single local data space; it is not a tenant and cannot be added or switched.

## Development

Prerequisites: Node.js 22, pnpm 10.28.2, and Go 1.26.6. Docker is not required.

```bash
pnpm install
make dev
```

Useful commands:

```bash
make up             # start the SQLite API and Electron desktop
make status         # inspect this checkout's local processes
make down           # stop processes and keep local data
make db-reset       # recreate this checkout's SQLite database
make check          # typecheck, tests, and packaged desktop build
```

The desktop package bundles both `multica-server` (the loopback SQLite API) and `multica` (the local execution CLI).

## Architecture

- `apps/desktop`: Electron shell and local-server lifecycle.
- `packages/views`: shared project, issue, run, and timeline screens.
- `packages/core`: API, React Query, and client view stores.
- `packages/ui`: UI primitives.
- `server/cmd/server`: loopback-only SQLite API.
- `server/internal/localworkbench`: local schema and business data chain.
- `server/internal/daemon`: local agent execution process.

## License

[Multica License](LICENSE) — the complete Apache License 2.0 text plus additional conditions covering hosted services, commercial embedding, and branding. Multica branding, Logo, copyright, and attribution notices remain governed by [LICENSE](LICENSE) and [NOTICE](NOTICE).
