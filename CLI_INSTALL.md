# Multica Local Workbench — Repository Setup Guide

> **Audience:** developers and coding agents running the current repository. Multica is a local Electron application with a loopback Go API, SQLite storage, and a bundled execution CLI. This setup has no account, login, cloud workspace, team, or subscription step.

## Prerequisites

- Node.js 22 or newer
- pnpm 10.28.2
- Go 1.26.6
- Git
- Codex CLI available as `codex` and already authenticated on the current computer

The repository does not require Docker, PostgreSQL, or Redis.

## Start from a main checkout

From the repository root:

```bash
pnpm install
make dev
```

`make dev` creates `.env` from `.env.example` when needed, initializes the local SQLite database on first use, and starts the loopback API plus Electron desktop.

For an already prepared checkout, the managed lifecycle commands are:

```bash
make up
make status
make down
```

`make status` is the source of truth for which local processes and ports belong to the current checkout.

## Start from a Git worktree

Use the worktree-specific environment so its ports, SQLite file, Electron profile, and daemon profile do not collide with another checkout:

```bash
make setup-worktree
make start-worktree
make status
```

Stop that worktree without deleting its local data:

```bash
make stop-worktree
```

## Run individual components

After the environment exists, these commands use the current checkout configuration:

```bash
make up C=api
make desktop-dev
```

The CLI is built and run from this repository; no published cloud CLI is required:

```bash
make multica MULTICA_ARGS="version"
```

The Electron application manages its local execution process and profile. Use standalone API or renderer commands for development and diagnosis, not as an additional account, daemon, or remote-workspace setup.

## Local data and configuration

- The API stores business data in SQLite. A bare checkout defaults to `.multica/multica.db`; the desktop application and managed worktree environments assign their own local paths.
- `.env` is used by the main checkout.
- `.env.worktree` is generated for linked worktrees.
- The historical `workspace_id` in routes and internal contracts identifies the one local data space. It is not a selectable tenant.

To recreate only the current checkout's SQLite database:

```bash
make db-reset
```

This deletes local task data for that checkout, so use it only when that loss is intended.

## Verify the repository

Run the repository verification pipeline:

```bash
make check
```

For a worktree with `.env.worktree`:

```bash
make check-worktree
```

The check covers TypeScript type checking, frontend tests, Go tests, and the packaged desktop build. A successful setup should open Electron directly into the local task interface without an authentication redirect or cloud request.

## Troubleshooting

- Run `make status` before restarting processes; it reports ownership and port identity for the current environment.
- If a worktree has no environment file, run `make worktree-env` or `make setup-worktree`.
- If Codex is unavailable, verify `codex` is on `PATH` and authenticated locally, then restart the local environment.
- Use `make down` to stop processes while preserving data. `make destroy` also deletes the managed environment's database and profile.
