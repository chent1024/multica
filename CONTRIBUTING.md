# Contributing

Read `AGENTS.md` and `CLAUDE.md` before making changes.

## Prerequisites

- Node.js 22
- pnpm 10.28.2
- Go 1.26.6

Docker, PostgreSQL, Redis, and cloud credentials are not required.

## Local development

```bash
pnpm install
make worktree-env
make up
```

Each checkout owns a SQLite file and local ports. `make down` preserves its data; `make destroy` removes only that registered local environment.

## Verification

```bash
make check
```

The check runs the Desktop dependency graph's TypeScript checks and tests, all Go packages, and the packaged Electron build.
