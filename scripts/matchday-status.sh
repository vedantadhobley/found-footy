#!/usr/bin/env bash
# Print a read-only match-day snapshot for one Found Footy environment.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/matchday-status.sh [dev|prod] [lookahead-hours]

Shows Compose service state, scoped Firefox fleet state, upcoming/recent
fixtures, event/downstream/candidate progress, and candidate durability
violations. The database transaction is READ ONLY.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

readonly ENVIRONMENT="${1:-prod}"
readonly LOOKAHEAD_HOURS="${2:-6}"

case "$ENVIRONMENT" in
  dev|prod) ;;
  *)
    printf 'invalid environment: %s (want dev or prod)\n' "$ENVIRONMENT" >&2
    exit 2
    ;;
esac
if [[ ! "$LOOKAHEAD_HOURS" =~ ^[0-9]+$ ]] || ((LOOKAHEAD_HOURS > 72)); then
  printf 'invalid lookahead: %s (want an integer from 0 through 72)\n' "$LOOKAHEAD_HOURS" >&2
  exit 2
fi

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(git -C "$SCRIPT_DIR/.." rev-parse --show-toplevel)"
readonly COMPOSE_FILE="$REPO_ROOT/docker-compose.$ENVIRONMENT.yml"
readonly SCOPE="found-footy-$ENVIRONMENT"
readonly SQL_FILE="$SCRIPT_DIR/matchday-status.sql"
compose=(docker compose -f "$COMPOSE_FILE")

command -v docker >/dev/null || { printf 'docker is required\n' >&2; exit 1; }

printf 'Found Footy %s status (lookahead: %sh)\n\n' "$ENVIRONMENT" "$LOOKAHEAD_HOURS"
"${compose[@]}" ps

printf '\nScoped Firefox fleet\n'
fleet="$({
  docker ps -a \
    --filter 'label=found-footy.fleet=firefox' \
    --filter "label=found-footy.fleet.scope=$SCOPE" \
    --format '{{.Names}}\t{{.Status}}\t{{.Label "found-footy.fleet.event"}}'
} 2>/dev/null)"
if [[ -n "$fleet" ]]; then
  printf 'NAME\tSTATUS\tEVENT\n'
  printf '%s\n' "$fleet"
else
  printf 'none\n'
fi

postgres_id="$("${compose[@]}" ps -q postgres)"
if [[ -z "$postgres_id" ]]; then
  printf '\npostgres is not running for %s\n' "$ENVIRONMENT" >&2
  exit 1
fi

printf '\nApplication state\n'
docker exec -i "$postgres_id" sh -c \
  'exec psql -X --set=ON_ERROR_STOP=1 --set=lookahead_hours="$1" -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
  _ "$LOOKAHEAD_HOURS" < "$SQL_FILE"
