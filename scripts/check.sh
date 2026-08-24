#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ==========================================================================
# Local workbench verification: typecheck → unit tests → Go tests → Desktop build
# Usage: bash scripts/check.sh
# ==========================================================================

ENV_FILE="${ENV_FILE:-.env}"
if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE"
  echo "Create .env from .env.example, or run 'make worktree-env' and use .env.worktree."
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

# shellcheck disable=SC1091
. scripts/local-env.sh

EXIT_CODE=0

# --------------------------------------------------------------------------
# Cleanup: kill only services this script started
# --------------------------------------------------------------------------
cleanup() {
  echo ""
  echo ""
  if [ "$EXIT_CODE" -eq 0 ]; then
    echo "✓ All checks passed."
  else
    echo "✗ Checks FAILED."
  fi
  exit "$EXIT_CODE"
}
trap cleanup EXIT

# --------------------------------------------------------------------------
# Step 0: Configure local SQLite
# --------------------------------------------------------------------------
echo "==> Using env file: $ENV_FILE"
export MULTICA_SQLITE_PATH="${MULTICA_SQLITE_PATH:-$REPO_ROOT/.multica/multica-check.db}"
mkdir -p "$(dirname "$MULTICA_SQLITE_PATH")"
echo "==> SQLite: $MULTICA_SQLITE_PATH"

# --------------------------------------------------------------------------
# Step 1: TypeScript typecheck
# --------------------------------------------------------------------------
echo ""
echo "==> [1/4] TypeScript typecheck..."
pnpm typecheck || { EXIT_CODE=1; exit 1; }

# --------------------------------------------------------------------------
# Step 2: TypeScript unit tests (Vitest)
# --------------------------------------------------------------------------
echo ""
echo "==> [2/4] TypeScript unit tests..."
pnpm test || { EXIT_CODE=1; exit 1; }

# --------------------------------------------------------------------------
# Step 3: Go tests
# --------------------------------------------------------------------------
echo ""
echo "==> [3/4] Go tests..."
echo "==> Verifying Go test wrapper..."
bash scripts/test-go.test.sh || { EXIT_CODE=1; exit 1; }
bash scripts/test-go.sh || { EXIT_CODE=1; exit 1; }

# --------------------------------------------------------------------------
# Step 4: Build the actual Electron desktop bundle, including the SQLite server
# --------------------------------------------------------------------------
echo ""
echo "==> [4/4] Electron desktop build..."
pnpm -C apps/desktop build || { EXIT_CODE=1; exit 1; }
