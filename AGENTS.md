# Repository Guidelines

This file provides guidance to AI agents when working with code in this repository.

> **Single source of truth:** This file is a concise pointer document.
> All authoritative architecture, coding rules, and conventions
> live in **CLAUDE.md** at the project root. Read that file first.
> Use `Makefile`, `package.json`, and `pnpm-workspace.yaml` as the
> source of truth for the full command list.

## Quick Reference

### Architecture

One local Electron product backed by a loopback Go API, with pnpm workspaces and shared packages.

- `server/cmd/server/` - loopback-only Go API
- `server/internal/localworkbench/` - SQLite schema, store, and local business handlers
- `server/internal/daemon/` - local Codex execution
- `apps/desktop/` - Electron lifecycle, bundled server, IPC, and renderer routing
- `packages/core/` - Headless business logic (Zustand stores, React Query hooks, API client)
- `packages/ui/` - Atomic UI components (shadcn/Base UI, zero business logic)
- `packages/views/` - Project, task, Run, and timeline pages/components
- `packages/tsconfig/` - Shared TypeScript config
- `packages/eslint-config/` - Shared ESLint config

### State Management (critical)

- **React Query** owns API state (projects, issues, runs, timeline events, agents, runtimes, and the fixed local data-space descriptor)
- **Zustand** owns client/view state (view filters, drafts, modals, desktop tab state, and navigation history); the historical workspace identity is a fixed compatibility value
- All Zustand stores live in `packages/core/` - never in `packages/views/` or app directories
- WS events update React Query for server data; store writes are only for clearing client-owned pointers with a single responder/self-event guard

### Package Boundaries (hard rules)

- `packages/core/` - zero react-dom, zero localStorage, zero process.env
- `packages/ui/` - zero `@multica/core` imports
- `packages/views/` - zero Electron APIs and zero `react-router-dom`; use `NavigationAdapter`, `useNavigation`, and `AppLink`
- `apps/desktop/src/renderer/src/platform/` - owns `react-router-dom` wiring

### Database Migrations (hard rules)

- SQLite is the only product database. Schema initialization and forward upgrades live in `server/internal/localworkbench` and run transactionally.
- Never add database foreign keys or cascading actions. Enforce relationships and dependent cleanup in application transactions.
- Create indexes with SQLite `CREATE INDEX IF NOT EXISTS` statements.

### Commands

```bash
make dev              # Auto-setup + start everything
make status           # Inspect this checkout's local processes
make down             # Stop processes and preserve local data
pnpm typecheck        # TypeScript check
pnpm test             # TS unit tests (Vitest)
make test             # Go tests
make check            # Full verification pipeline
```

See CLAUDE.md for the authoritative rules and common commands.
