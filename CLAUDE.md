# Multica Local Workbench

This repository builds one local Electron product. It has no accounts, authentication flow, cloud workspace, tenant switching, teams, invitations, billing, PostgreSQL, Redis, hosted web app, or mobile app.

## Product model

- Project is a local Git project.
- Issue is a user task.
- Task/Run is one Codex execution.
- Comment/Event is timeline progress, a commit, verification, or release record.
- Codex `thread_id` identifies the related Codex session.
- Historical `workspace` names are a compatibility boundary for one fixed local data space. The UI and API must never expose create, switch, member, invitation, or tenant operations.

## Architecture

- `apps/desktop`: Electron lifecycle, local-server process, IPC, and renderer routing.
- `server/cmd/server`: canonical loopback-only SQLite API.
- `server/internal/localworkbench`: SQLite schema, store, and local API handlers.
- `server/internal/daemon`: local agent execution; keep Codex adapter behavior stable unless the task explicitly targets it.
- `packages/core`: headless API, React Query, platform plumbing, and client view stores.
- `packages/ui`: atomic UI primitives with no business logic.
- `packages/views`: shared business views used by Desktop.

Dependency direction is `views -> core + ui`; `core` and `ui` remain independent.

## State

- React Query owns API state: projects, issues, runs, timeline events, agents, runtimes, and the fixed local data-space descriptor.
- Zustand owns client/view state: filters, drafts, modals, tab layout, and navigation history.
- Shared Zustand stores live in `packages/core`.
- Durable preferences, drafts, and layout may persist. API payloads and ephemeral UI state do not.
- Workspace-scoped compatibility query keys include the fixed local data-space ID.

## Package boundaries

- `packages/core`: no `react-dom`, direct `localStorage`, `process.env`, or UI libraries. Use `StorageAdapter`.
- `packages/ui`: no `@multica/core` imports and no business logic.
- `packages/views`: no Electron or `react-router-dom` imports. Use `NavigationAdapter`, `useNavigation`, and `AppLink`.
- `apps/desktop/src/renderer/src/platform`: owns `react-router-dom` wiring.
- Every workspace declares its direct external dependencies.

## Database

- SQLite is the only product database. Redis and external database services are outside the architecture.
- Schema initialization and forward upgrades live in `server/internal/localworkbench` and run transactionally.
- Do not add foreign keys or cascading actions. Validate relationships and dependent cleanup in application transactions.
- Create indexes with SQLite `CREATE INDEX IF NOT EXISTS` statements.

## Commands

Use `Makefile` and package scripts as the command source of truth.

```bash
pnpm install
make dev
make up
make status
make down
make db-reset
make test
make check
pnpm dev:desktop
pnpm typecheck
pnpm test
```

Each checkout owns a SQLite file and local ports. `make down` preserves data; `make destroy` removes only that registered environment.

## Coding and verification

- TypeScript is strict. Go uses `gofmt`, checked errors, and normal package tests.
- Code comments are English.
- Reuse existing services and components. Keep changes inside the requested product boundary.
- Test behavior at the highest affected layer. `make check` is the full local-workbench verification pipeline.
- Do not report a check as passed unless its command completed successfully.

## License

Preserve Multica branding, Logo, copyright, LICENSE, and NOTICE attribution.
