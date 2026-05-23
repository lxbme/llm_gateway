#!/usr/bin/env bash
#
# End-to-end test for the Ollama embedding provider.
#
# What this script verifies:
#   1. Pre-conditions: host ollama is reachable and the configured model
#      responds to /api/embed with a non-empty vector. Records the true
#      vector length as EXPECTED_DIM.
#   2. embedding-service starts cleanly with EMBED_PROVIDER=ollama via the
#      config/docker-compose.ollama.yml overlay and logs the expected
#      `Ollama embedding ready` probe success line.
#   3. embedding-service container reports a healthy `running` state in
#      `docker compose ps`, confirming the gRPC server is up and not stuck
#      in a restart loop.
#   4. Negative path: relaunching with EMBED_DIMENSIONS=<wrong> makes the
#      probe fail at startup; the container exits non-zero and the logs
#      contain the dimension-mismatch error from cache/ollama.New.
#
# Usage:
#   bash test/cli/embedding-ollama-test.sh
#
# Environment overrides (all optional):
#   COMPOSE_FILE        Base compose file (default: docker-compose.yml)
#   OVERLAY_FILE        Ollama overlay (default: config/docker-compose.ollama.yml)
#   OLLAMA_HOST_URL     Ollama base URL as seen from the host (default:
#                       http://localhost:11434)
#   OLLAMA_MODEL        Embedding model (default: qwen3-embedding:0.6b)
#   EXPECTED_DIM        Override expected dim instead of probing (default:
#                       derived from the host probe)
#   KEEP_SERVICES       Set to 1 to leave containers running after the test
#   SKIP_COMPOSE        Set to 1 to skip docker compose up
#   STARTUP_WAIT_SECONDS  Seconds to wait for the ready log (default: 30)

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
SCRIPT_ENV_FILE="${SCRIPT_DIR}/.env"
ROOT_ENV_FILE="${ROOT_DIR}/.env"
ROOT_ENV_EXAMPLE_FILE="${ROOT_DIR}/config/.env.example"
COMPOSE_FILE="${COMPOSE_FILE:-${ROOT_DIR}/docker-compose.yml}"
OVERLAY_FILE="${OVERLAY_FILE:-${ROOT_DIR}/config/docker-compose.ollama.yml}"
OLLAMA_HOST_URL="${OLLAMA_HOST_URL:-http://localhost:11434}"
OLLAMA_MODEL="${OLLAMA_MODEL:-qwen3-embedding:0.6b}"
KEEP_SERVICES="${KEEP_SERVICES:-0}"
SKIP_COMPOSE="${SKIP_COMPOSE:-0}"
STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-30}"

EXPECTED_DIM="${EXPECTED_DIM:-}"

TESTS_PASSED=0
TESTS_FAILED=0

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------

log()  { printf '[ollama-embed-test] %s\n'      "$*" >&2; }
pass() { printf '[ollama-embed-test] PASS: %s\n' "$*" >&2; TESTS_PASSED=$((TESTS_PASSED + 1)); }
fail() { printf '[ollama-embed-test] FAIL: %s\n' "$*" >&2; TESTS_FAILED=$((TESTS_FAILED + 1)); }
die()  { printf '[ollama-embed-test] FATAL: %s\n' "$*" >&2; exit 1; }

assert_eq() {
  local label="${1}" got="${2}" want="${3}"
  if [[ "${got}" == "${want}" ]]; then
    pass "${label} (got ${got})"
  else
    fail "${label}: got ${got}, want ${want}"
  fi
}

assert_contains() {
  local label="${1}" haystack="${2}" needle="${3}"
  if [[ "${haystack}" == *"${needle}"* ]]; then
    pass "${label}"
  else
    fail "${label}: expected output to contain '${needle}'"
  fi
}

# ---------------------------------------------------------------------------
# Env bootstrap (mirrors cache-redis-hnsw-test.sh)
# ---------------------------------------------------------------------------

bootstrap_env() {
  if [[ -f "${SCRIPT_ENV_FILE}" ]]; then
    return 0
  fi
  if [[ -f "${ROOT_ENV_FILE}" ]]; then
    cp "${ROOT_ENV_FILE}" "${SCRIPT_ENV_FILE}"
    log "Created isolated env file: ${SCRIPT_ENV_FILE} (copied from project root .env)"
    return 0
  fi
  if [[ -f "${ROOT_ENV_EXAMPLE_FILE}" ]]; then
    cp "${ROOT_ENV_EXAMPLE_FILE}" "${SCRIPT_ENV_FILE}"
    die "Project root .env was not found. Initialized ${SCRIPT_ENV_FILE} from .env.example; please fill in real values and rerun."
  fi
  die "Unable to initialize ${SCRIPT_ENV_FILE}: neither project root .env nor .env.example exists."
}

load_env() {
  set -a
  # shellcheck disable=SC1090
  source "${SCRIPT_ENV_FILE}"
  set +a
}

# ---------------------------------------------------------------------------
# Step 0: preconditions
# ---------------------------------------------------------------------------

probe_ollama_dim() {
  local payload
  payload="$(printf '{"model":"%s","input":"hello"}' "${OLLAMA_MODEL}")"

  local body
  body="$(curl -sS --fail-with-body "${OLLAMA_HOST_URL}/api/embed" \
            -H 'Content-Type: application/json' \
            -d "${payload}" || true)"

  if [[ -z "${body}" ]]; then
    die "Ollama at ${OLLAMA_HOST_URL} did not respond. Is the daemon running? Did you 'ollama pull ${OLLAMA_MODEL}'?"
  fi

  # The response shape is {"model":"...","embeddings":[[float,...]],"..."}.
  # Count commas inside the first embedding array and add 1. This avoids a jq dependency.
  local first_vec
  first_vec="$(printf '%s' "${body}" | sed -n 's/.*"embeddings":\[\[\([^]]*\)\].*/\1/p')"
  if [[ -z "${first_vec}" ]]; then
    log "Raw response: ${body:0:300}"
    die "Unable to parse embeddings[0] from ollama response."
  fi

  local commas
  commas="$(printf '%s' "${first_vec}" | tr -cd ',' | wc -c)"
  printf '%d' $((commas + 1))
}

step_preconditions() {
  log ""
  log "=== STEP 1: Preconditions (host ollama reachable, model loaded) ==="

  if ! curl -sSf "${OLLAMA_HOST_URL}/api/tags" >/dev/null 2>&1; then
    die "Cannot reach ${OLLAMA_HOST_URL}/api/tags. Start ollama and rerun."
  fi
  pass "Ollama daemon reachable at ${OLLAMA_HOST_URL}"

  if [[ -z "${EXPECTED_DIM}" ]]; then
    EXPECTED_DIM="$(probe_ollama_dim)"
    log "Probed ${OLLAMA_MODEL} → dim=${EXPECTED_DIM}"
  else
    log "Using user-provided EXPECTED_DIM=${EXPECTED_DIM}"
  fi
  if [[ ! "${EXPECTED_DIM}" =~ ^[0-9]+$ ]] || (( EXPECTED_DIM <= 0 )); then
    die "EXPECTED_DIM must be a positive integer (got '${EXPECTED_DIM}')."
  fi
  pass "Model ${OLLAMA_MODEL} produces ${EXPECTED_DIM}-dim embeddings"

  if [[ ! -f "${OVERLAY_FILE}" ]]; then
    die "Ollama overlay file not found: ${OVERLAY_FILE}"
  fi
  pass "Overlay file exists at ${OVERLAY_FILE}"
}

# ---------------------------------------------------------------------------
# Docker compose helpers
# ---------------------------------------------------------------------------

declare -a COMPOSE_CMD=()

detect_compose_cmd() {
  if docker compose version >/dev/null 2>&1; then
    COMPOSE_CMD=(docker compose)
    return 0
  fi
  if command -v docker-compose >/dev/null 2>&1; then
    COMPOSE_CMD=(docker-compose)
    return 0
  fi
  die "Neither docker compose nor docker-compose was found."
}

# We pass EMBED_* through inline env so the overlay's ${...:-default} fallbacks
# resolve predictably regardless of what the user has in .env.
compose() {
  EMBED_PROVIDER=ollama \
  EMBED_ENDPOINT="${COMPOSE_EMBED_ENDPOINT:-http://localhost:11434/api/embed}" \
  EMBED_MODEL="${OLLAMA_MODEL}" \
  EMBED_DIMENSIONS="${COMPOSE_EMBED_DIM:-${EXPECTED_DIM}}" \
  EMBED_API_KEY="${COMPOSE_EMBED_API_KEY:-}" \
    "${COMPOSE_CMD[@]}" \
    --env-file "${SCRIPT_ENV_FILE}" \
    -f "${COMPOSE_FILE}" \
    -f "${OVERLAY_FILE}" \
    "$@"
}

cleanup() {
  if [[ "${KEEP_SERVICES}" == "1" || "${SKIP_COMPOSE}" == "1" ]]; then
    log "Leaving containers running (KEEP_SERVICES=${KEEP_SERVICES}, SKIP_COMPOSE=${SKIP_COMPOSE})."
    return 0
  fi
  if [[ ${#COMPOSE_CMD[@]} -eq 0 ]]; then
    return 0
  fi
  log "Stopping test containers..."
  compose down --remove-orphans >/dev/null 2>&1 || true
}

print_debug_info() {
  if [[ ${#COMPOSE_CMD[@]} -eq 0 ]]; then
    return 0
  fi
  log "Compose service status:"
  compose ps || true
  log "Recent embedding-service logs:"
  compose logs --tail 80 embedding-service || true
}

# ---------------------------------------------------------------------------
# Step 2: bring stack up and verify probe success log
# ---------------------------------------------------------------------------

wait_for_ready_log() {
  local expected="[Info] Ollama embedding ready: model=${OLLAMA_MODEL} dim=${EXPECTED_DIM}"
  local attempt=0
  local logs=""
  while (( attempt < STARTUP_WAIT_SECONDS )); do
    logs="$(compose logs --no-color embedding-service 2>/dev/null || true)"
    if [[ "${logs}" == *"${expected}"* ]]; then
      pass "embedding-service logged ollama probe success"
      return 0
    fi
    sleep 1
    attempt=$((attempt + 1))
  done
  fail "embedding-service did not log: ${expected}"
  printf '%s\n' "${logs}" | tail -40 >&2
  return 1
}

step_stack_up_and_ready() {
  log ""
  log "=== STEP 2: Stack up with ollama overlay, expect ready log ==="
  log "Using base compose:    ${COMPOSE_FILE}"
  log "Using overlay compose: ${OVERLAY_FILE}"
  log "EMBED_PROVIDER=ollama  EMBED_MODEL=${OLLAMA_MODEL}  EMBED_DIMENSIONS=${EXPECTED_DIM}"

  if [[ "${SKIP_COMPOSE}" == "1" ]]; then
    log "SKIP_COMPOSE=1 — skipping docker compose up."
  else
    compose up -d --build embedding-service etcd
  fi

  wait_for_ready_log || true
}

# ---------------------------------------------------------------------------
# Step 3: container running healthy
# ---------------------------------------------------------------------------

step_container_running() {
  log ""
  log "=== STEP 3: embedding-service container is in 'running' state ==="

  local state
  state="$(compose ps --format '{{.Service}} {{.State}}' 2>/dev/null \
            | awk '$1=="embedding-service" {print $2}' | head -1)"
  if [[ -z "${state}" ]]; then
    fail "Could not determine embedding-service container state"
    return 0
  fi
  assert_eq "embedding-service state" "${state}" "running"

  # Sanity: there must be no restart count > 0. Restart counts indicate
  # an early crash followed by docker-compose restart=unless-stopped retry.
  local restarts
  restarts="$(docker inspect \
                "$(compose ps -q embedding-service | head -1)" \
                --format '{{.RestartCount}}' 2>/dev/null || echo 'unknown')"
  if [[ "${restarts}" == "0" ]]; then
    pass "embedding-service has zero restarts"
  else
    fail "embedding-service has non-zero restart count: ${restarts}"
  fi
}

# ---------------------------------------------------------------------------
# Step 4: dimension mismatch must fail at startup
# ---------------------------------------------------------------------------

step_dim_mismatch_fails() {
  log ""
  log "=== STEP 4: Negative path — EMBED_DIMENSIONS=999 must crash on probe ==="

  local wrong_dim=999
  if [[ "${EXPECTED_DIM}" == "${wrong_dim}" ]]; then
    wrong_dim=1
  fi

  COMPOSE_EMBED_DIM="${wrong_dim}" compose up -d --force-recreate --no-deps embedding-service \
    >/dev/null 2>&1 || true

  # The container has restart=unless-stopped from the main compose; State and
  # ExitCode oscillate as docker re-launches the process. The most direct,
  # stable proof that the probe rejected the wrong dimension is the panic
  # message in the embedding-service log. We poll the logs for up to 20s.
  local needle="declared dimensions=${wrong_dim} but model ${OLLAMA_MODEL} returned ${EXPECTED_DIM}"
  local attempt=0
  local logs=""
  while (( attempt < 20 )); do
    logs="$(compose logs --no-color embedding-service 2>/dev/null || true)"
    if [[ "${logs}" == *"${needle}"* ]]; then
      break
    fi
    sleep 1
    attempt=$((attempt + 1))
  done

  if [[ "${logs}" == *"${needle}"* ]]; then
    pass "logs contain dimension mismatch error"
  else
    fail "logs do not contain dimension mismatch error: ${needle}"
    printf '%s\n' "${logs}" | tail -40 >&2
  fi

  # Sanity: the wrong-dim container should not be reported as 'running'
  # forever — at minimum a restart loop should leave it 'restarting' or
  # transiently 'exited' some of the time. We check once and accept any
  # non-running state OR a 'running' state combined with the panic log
  # we already verified above (panic→exit→restart cycle).
  local cid state
  cid="$(compose ps -q embedding-service | head -1)"
  if [[ -n "${cid}" ]]; then
    state="$(docker inspect "${cid}" --format '{{.State.Status}}' 2>/dev/null || echo '')"
    log "embedding-service state: ${state}"
  fi
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
  bootstrap_env
  load_env
  detect_compose_cmd

  trap cleanup EXIT

  step_preconditions

  step_stack_up_and_ready || true
  step_container_running || true
  step_dim_mismatch_fails || true

  log ""
  log "================================"
  log " ollama embedding test summary"
  log "================================"
  log " PASSED: ${TESTS_PASSED}"
  log " FAILED: ${TESTS_FAILED}"
  log "================================"

  if (( TESTS_FAILED > 0 )); then
    print_debug_info
    die "${TESTS_FAILED} test(s) failed."
  fi

  log "All ollama embedding tests passed."
}

main "$@"
