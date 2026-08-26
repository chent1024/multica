<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="docs/assets/logo-light.svg">
  <img alt="Multica" src="docs/assets/logo-light.svg" width="50">
</picture>

# Vision

**One local place to manage personal work and carry it through Codex.**

**English | [简体中文](VISION.zh.md)**

</div>

## Why Multica

Codex can execute substantial work, but a chat window alone does not make that work easy to manage. Tasks become scattered across sessions, the current stage is hard to see, and evidence such as commits, checks, and final outcomes is separated from the original intent.

Multica turns that execution into a personal task workflow. A local Git project supplies the working context. An Issue records what the user wants. Each Codex Run advances the same durable conversation, while stages describe how the work is progressing. Comments and outcomes preserve the decisions and evidence needed to continue or review the task later.

The product is deliberately local and single-user. There is one local data space, one operator, and no account, cloud workspace, organization, member, invitation, subscription, or tenant model. SQLite stores the task record on the current computer, and the desktop application talks only to its loopback service.

## Product principles

- **Tasks before telemetry.** The interface explains the goal, current state, conversation, and next action before exposing execution detail.
- **One durable conversation.** Follow-up instructions and Codex responses remain attached to the same Issue Session instead of becoming disconnected chats.
- **Stages are descriptive.** Analysis, implementation, review, test, commit, push, and release help organize progress; changing a stage does not authorize or execute delivery work.
- **Evidence stays connected.** Progress, failures, commits, verification, and release records remain with the task that produced them.
- **Local by default and by design.** Projects, data, execution, and configuration belong to the current computer.
- **Technical detail is available, not dominant.** Full Run history and diagnostics remain accessible in the execution log without turning the task page into an operations console.

Multica is not a collaboration platform or an autonomous company. It is a focused personal workbench for deciding what to do, asking Codex to do it, and understanding what happened.

For the current implementation and development commands, see the [README](README.md).
