#!/usr/bin/env bash
#
# docker-run.sh — bring the llm_gateway stack up or down with the right
# overlays for the providers selected in .env.
#
# Reads CACHE_STORE_PROVIDER and EMBED_PROVIDER from the project-root .env
# (or the current shell environment, which takes precedence) and appends the
# matching `config/docker-compose.<feature>.yml` overlay to the compose
# invocation. The main `docker-compose.yml` is always included.
#
# Usage:
#   bash docker-run.sh                  # foreground: docker compose ... up --build
#   bash docker-run.sh -d               # background: docker compose ... up -d --build
#   bash docker-run.sh --down           # tear down all services (with overlays)
#   bash docker-run.sh --prod           # use docker-compose.prod.yml (ghcr.io images)
#   bash docker-run.sh --prod -d        # prod, detached
#   bash docker-run.sh --prod --down    # tear down the prod stack
#   bash docker-run.sh --help           # show this help
#
# Recognized provider → overlay mappings:
#   EMBED_PROVIDER=ollama         → config/docker-compose.ollama.yml
#   CACHE_STORE_PROVIDER=redis_hnsw → config/docker-compose.hnsw.yml
#
# Any other value (e.g. EMBED_PROVIDER=openai, CACHE_STORE_PROVIDER=qdrant)
# adds no overlay — those providers are handled entirely by the main compose.
#
# Shell env > .env: if you `export CACHE_STORE_PROVIDER=redis_hnsw` before
# invoking this script, it overrides whatever is in .env.
#
# --prod selects docker-compose.prod.yml (pre-built ghcr.io images, no local
# build) instead of docker-compose.yml. The same overlays are applied either
# way, and `up` does NOT pass --build in prod mode (images are pulled).

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="${SCRIPT_DIR}"
ENV_FILE="${ROOT_DIR}/.env"
DEV_COMPOSE_FILE="${ROOT_DIR}/docker-compose.yml"
PROD_COMPOSE_FILE="${ROOT_DIR}/docker-compose.prod.yml"
OLLAMA_OVERLAY="${ROOT_DIR}/config/docker-compose.ollama.yml"
HNSW_OVERLAY="${ROOT_DIR}/config/docker-compose.hnsw.yml"

# ---------------------------------------------------------------------------
# Arg parsing
# ---------------------------------------------------------------------------

DETACH=0
ACTION="up"
PROD=0

usage() {
  sed -n '2,/^set -euo pipefail$/p' "$0" | sed -e 's/^# \{0,1\}//' -e '/^set -euo/d'
}

while (( $# > 0 )); do
  case "${1}" in
    -d)         DETACH=1; shift ;;
    --down)     ACTION="down"; shift ;;
    --prod)     PROD=1; shift ;;
    -h|--help)  usage; exit 0 ;;
    *)
      echo "Unknown argument: ${1}" >&2
      echo "Run '$0 --help' for usage." >&2
      exit 2
      ;;
  esac
done

if (( PROD )); then
  COMPOSE_FILE="${PROD_COMPOSE_FILE}"
  COMPOSE_PROFILE_LABEL="prod"
else
  COMPOSE_FILE="${DEV_COMPOSE_FILE}"
  COMPOSE_PROFILE_LABEL="dev"
fi

if [[ ! -f "${COMPOSE_FILE}" ]]; then
  echo "[docker-run] Compose file missing: ${COMPOSE_FILE}" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Compose CLI detection
# ---------------------------------------------------------------------------

declare -a COMPOSE_CMD=()
if docker compose version >/dev/null 2>&1; then
  COMPOSE_CMD=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE_CMD=(docker-compose)
else
  echo "Neither 'docker compose' nor 'docker-compose' is available." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Load provider env (shell wins over .env)
# ---------------------------------------------------------------------------

# Snapshot every variable docker-compose's `${...}` substitution will read so
# that shell-provided overrides survive the `source .env` below. Without this
# snapshot, a blank or differently-set value in .env silently overwrites what
# the caller exported (and embedding-service then panics on EMBED_API_KEY="").
_SHELL_SNAPSHOT_VARS=(
  EMBED_PROVIDER EMBED_API_KEY EMBED_ENDPOINT EMBED_MODEL EMBED_DIMENSIONS
  CACHE_STORE_PROVIDER CACHE_MODE CACHE_BUFFER_SIZE CACHE_WORKER_COUNT
  QDRANT_COLLECTION_NAME QDRANT_SIMILARITY_THRESHOLD
  REDIS_HNSW_ADDR REDIS_HNSW_INDEX_NAME REDIS_HNSW_KEY_PREFIX
  REDIS_HNSW_SIMILARITY_THRESHOLD REDIS_HNSW_DISTANCE_METRIC
  REDIS_HNSW_M REDIS_HNSW_EF_CONSTRUCTION REDIS_HNSW_EF_RUNTIME
)
declare -A _SHELL_SNAPSHOT
for _v in "${_SHELL_SNAPSHOT_VARS[@]}"; do
  _SHELL_SNAPSHOT[$_v]="${!_v-}"
done

if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
else
  echo "[docker-run] .env not found at ${ENV_FILE}." >&2
  echo "[docker-run] Copy config/.env.example to .env and fill in real values." >&2
  exit 1
fi

# Restore every shell-provided override on top of whatever .env supplied.
for _v in "${_SHELL_SNAPSHOT_VARS[@]}"; do
  if [[ -n "${_SHELL_SNAPSHOT[$_v]+set}" && -n "${_SHELL_SNAPSHOT[$_v]}" ]]; then
    export "${_v}=${_SHELL_SNAPSHOT[$_v]}"
  fi
done

EMBED_PROVIDER="${EMBED_PROVIDER:-}"
CACHE_STORE_PROVIDER="${CACHE_STORE_PROVIDER:-}"

# ---------------------------------------------------------------------------
# Build the overlay list
# ---------------------------------------------------------------------------

declare -a FILE_ARGS=(-f "${COMPOSE_FILE}")
declare -a OVERLAYS_USED=()

if [[ "${EMBED_PROVIDER}" == "ollama" ]]; then
  if [[ ! -f "${OLLAMA_OVERLAY}" ]]; then
    echo "[docker-run] EMBED_PROVIDER=ollama but overlay missing: ${OLLAMA_OVERLAY}" >&2
    exit 1
  fi
  FILE_ARGS+=(-f "${OLLAMA_OVERLAY}")
  OVERLAYS_USED+=("config/docker-compose.ollama.yml")
fi

if [[ "${CACHE_STORE_PROVIDER}" == "redis_hnsw" ]]; then
  if [[ ! -f "${HNSW_OVERLAY}" ]]; then
    echo "[docker-run] CACHE_STORE_PROVIDER=redis_hnsw but overlay missing: ${HNSW_OVERLAY}" >&2
    exit 1
  fi
  FILE_ARGS+=(-f "${HNSW_OVERLAY}")
  OVERLAYS_USED+=("config/docker-compose.hnsw.yml")
fi

# ---------------------------------------------------------------------------
# Compose action
# ---------------------------------------------------------------------------

declare -a ACTION_ARGS=()
case "${ACTION}" in
  up)
    # In prod mode we pull pre-built ghcr.io images — no --build.
    if (( PROD )); then
      if (( DETACH )); then
        ACTION_ARGS=(up -d)
      else
        ACTION_ARGS=(up)
      fi
    else
      if (( DETACH )); then
        ACTION_ARGS=(up -d --build)
      else
        ACTION_ARGS=(up --build)
      fi
    fi
    ;;
  down)
    ACTION_ARGS=(down --remove-orphans)
    ;;
esac

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

echo "[docker-run] Profile: ${COMPOSE_PROFILE_LABEL} (${COMPOSE_FILE})"
echo "[docker-run] EMBED_PROVIDER=${EMBED_PROVIDER:-<unset>}"
echo "[docker-run] CACHE_STORE_PROVIDER=${CACHE_STORE_PROVIDER:-<unset>}"
if (( ${#OVERLAYS_USED[@]} == 0 )); then
  echo "[docker-run] Overlays: (none — base compose only)"
else
  echo "[docker-run] Overlays: ${OVERLAYS_USED[*]}"
fi
echo "[docker-run] Running: ${COMPOSE_CMD[*]} ${FILE_ARGS[*]} ${ACTION_ARGS[*]}"

exec "${COMPOSE_CMD[@]}" "${FILE_ARGS[@]}" "${ACTION_ARGS[@]}"
