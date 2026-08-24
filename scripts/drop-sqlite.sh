#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="${1:-.env}"
case "$ENV_FILE" in
  /*) env_path="$ENV_FILE" ;;
  *) env_path="$REPO_ROOT/$ENV_FILE" ;;
esac

if [ -f "$env_path" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$env_path"
  set +a
fi

database_path="${MULTICA_SQLITE_PATH:-$REPO_ROOT/.multica/multica.db}"
case "$database_path" in
  /*) ;;
  *) database_path="$REPO_ROOT/$database_path" ;;
esac

database_dir="$(cd "$(dirname "$database_path")" 2>/dev/null && pwd)" || {
  echo "Refusing to delete SQLite database: parent directory does not exist." >&2
  exit 1
}
database_path="$database_dir/$(basename "$database_path")"

case "$(basename "$database_path")" in
  *.db) ;;
  *) echo "Refusing to delete non-.db path: $database_path" >&2; exit 1 ;;
esac

printf 'Delete local SQLite database %s? [y/N] ' "$database_path"
read -r reply || reply=n
case "$reply" in
  y|Y|yes|YES) ;;
  *) echo "Cancelled."; exit 2 ;;
esac

rm -f -- "$database_path" "$database_path-wal" "$database_path-shm"
echo "Deleted local SQLite database: $database_path"
