#!/usr/bin/env bash
#
# End-to-end test for the etcd discovery path.
#
# What it verifies:
#   1. Starting the full stack with each gRPC instance service scaled to
#      SCALE_PER_SERVICE produces SCALE_PER_SERVICE keys per service under
#      etcd prefix `services/<name>`.
#   2. A /v1/chat/completions request succeeds end-to-end.
#   3. Killing one container of each scaled service causes its etcd key to
#      disappear (lease eviction or active deregister) within
#      LEASE_WAIT_SECONDS.
#   4. After the kill, /v1/chat/completions still succeeds — meaning the
#      gateway's gRPC clients rebalanced onto the surviving instances.
#
# Run from the repo root or this directory:
#   bash test/cli/etcd-e2e-test.sh
#
# Knobs (all optional):
#   SCALE_PER_SERVICE   default 2     - instances per gRPC service
#   LEASE_WAIT_SECONDS  default 14    - sleep after kill, must exceed 10s TTL
#   TEST_REQUEST_COUNT  default 2     - requests per phase (pre/post kill)
#   TEST_MODEL          default gpt-4o-mini
#   TEST_PROMPT         default "Reply with OK only."
#   COMPOSE_FILE        default ../../docker-compose.yml
#   KEEP_SERVICES       default 0     - set 1 to leave containers up for inspection

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
SCRIPT_ENV_FILE="${SCRIPT_DIR}/.env"
ROOT_ENV_FILE="${ROOT_DIR}/.env"
ROOT_ENV_EXAMPLE_FILE="${ROOT_DIR}/.env.example"
COMPOSE_FILE="${COMPOSE_FILE:-${ROOT_DIR}/docker-compose.yml}"
TEST_MODEL="${TEST_MODEL:-gpt-4o-mini}"
TEST_PROMPT="${TEST_PROMPT:-Reply with OK only.}"
TEST_REQUEST_COUNT="${TEST_REQUEST_COUNT:-2}"
SCALE_PER_SERVICE="${SCALE_PER_SERVICE:-2}"
LEASE_WAIT_SECONDS="${LEASE_WAIT_SECONDS:-14}"
KEEP_SERVICES="${KEEP_SERVICES:-0}"
# Use a distinct compose project so this test doesn't fight with the
# single-instance docker-compose-test.sh stack.
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-llm-gateway-etcd-test}"

# Services that are scaled in this test. The order is also the kill order.
SCALED_SERVICES=(
  embedding-service
  cache-service
  completion-service
  auth-service
  rag-service
)

log() {
  printf '[etcd-test] %s\n' "$*" >&2
}

fail() {
  printf '[etcd-test] %s\n' "$*" >&2
  exit 1
}

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
    fail "Project root .env was not found. Initialized ${SCRIPT_ENV_FILE} from .env.example; please fill in real values and rerun."
  fi
  fail "Unable to initialize ${SCRIPT_ENV_FILE}: neither project root .env nor .env.example exists."
}

load_env() {
  set -a
  # shellcheck disable=SC1090
  source "${SCRIPT_ENV_FILE}"
  set +a
}

validate_env() {
  local required_vars=(
    EMBED_PROVIDER EMBED_API_KEY EMBED_ENDPOINT EMBED_MODEL
    COMPL_API_KEY COMPL_ENDPOINT ADMIN_SECRET
  )
  for var_name in "${required_vars[@]}"; do
    if [[ -z "${!var_name:-}" ]]; then
      fail "${SCRIPT_ENV_FILE} is missing required variable: ${var_name}"
    fi
  done

  if [[ "${EMBED_API_KEY}" == "sk-your-embedding-api-key" || "${COMPL_API_KEY}" == "sk-your-completion-api-key" ]]; then
    fail "${SCRIPT_ENV_FILE} still contains example API keys. Replace them with real values and rerun."
  fi
  if [[ "${ADMIN_SECRET}" == "change-me-to-a-strong-random-secret" ]]; then
    fail "${SCRIPT_ENV_FILE} still contains the example ADMIN_SECRET. Replace it with a real value and rerun."
  fi
  if ! [[ "${TEST_REQUEST_COUNT}" =~ ^[1-9][0-9]*$ ]]; then
    fail "TEST_REQUEST_COUNT must be a positive integer: ${TEST_REQUEST_COUNT}"
  fi
  if ! [[ "${SCALE_PER_SERVICE}" =~ ^[2-9][0-9]*$ ]]; then
    fail "SCALE_PER_SERVICE must be >= 2 to make the kill-one verification meaningful: ${SCALE_PER_SERVICE}"
  fi
}

detect_compose_cmd() {
  if docker compose version >/dev/null 2>&1; then
    COMPOSE_CMD=(docker compose)
    return 0
  fi
  if command -v docker-compose >/dev/null 2>&1; then
    COMPOSE_CMD=(docker-compose)
    return 0
  fi
  fail "Neither docker compose nor docker-compose was found."
}

compose() {
  "${COMPOSE_CMD[@]}" --env-file "${SCRIPT_ENV_FILE}" -f "${COMPOSE_FILE}" "$@"
}

cleanup() {
  if [[ "${KEEP_SERVICES}" == "1" ]]; then
    log "KEEP_SERVICES=1 — leaving containers running for inspection."
    log "Project: ${COMPOSE_PROJECT_NAME}"
    return 0
  fi
  if [[ ${#COMPOSE_CMD[@]} -eq 0 ]]; then
    return 0
  fi
  log "Tearing down stack..."
  compose down --remove-orphans >/dev/null 2>&1 || true
}

print_debug_info() {
  if [[ ${#COMPOSE_CMD[@]} -eq 0 ]]; then
    return 0
  fi
  log "Compose service status:"
  compose ps || true
  log "Recent gateway logs:"
  compose logs --tail 80 gateway || true
  log "etcd contents:"
  compose exec -T etcd etcdctl get --prefix services/ --keys-only 2>/dev/null || true
}

# Returns the number of etcd keys under services/<service-short-name>/.
# Service name maps: cache-service -> cache, embedding-service -> embedding, …
etcd_key_count() {
  local service="${1}"
  local short="${service%-service}"
  local output
  output="$(compose exec -T etcd etcdctl get --prefix "services/${short}/" --keys-only 2>/dev/null || true)"
  # Strip blank lines and count.
  printf '%s\n' "${output}" | grep -c "^services/${short}/" || true
}

wait_for_etcd_count() {
  local service="${1}"
  local expected="${2}"
  local attempts="${3:-30}"
  local attempt=0
  local actual
  while (( attempt < attempts )); do
    actual="$(etcd_key_count "${service}")"
    if [[ "${actual}" == "${expected}" ]]; then
      log "etcd: ${service} has ${actual} live endpoint(s) — matches expected ${expected}"
      return 0
    fi
    sleep 1
    attempt=$((attempt + 1))
  done
  log "etcd: ${service} expected=${expected} actual=${actual} after ${attempts}s"
  return 1
}

wait_for_token_creation() {
  local alias_name="etcd-e2e-$(date +%s)"
  local attempts="${1:-60}"
  local attempt=0
  local raw_response=""
  local response_body=""
  local status_code=""

  log "Creating test token via admin API..."
  while (( attempt < attempts )); do
    raw_response="$(
      curl -sS -w $'\n%{http_code}' \
        -X POST "http://127.0.0.1:8081/admin/create" \
        -H "X-Admin-Secret: ${ADMIN_SECRET}" \
        -H "Content-Type: application/json" \
        -d "{\"alias\":\"${alias_name}\"}" || true
    )"
    response_body="${raw_response%$'\n'*}"
    status_code="${raw_response##*$'\n'}"
    if [[ "${status_code}" == "200" ]]; then
      log "Admin response: ${response_body}"
      printf '%s' "${response_body}"
      return 0
    fi
    log "Admin create attempt $((attempt + 1))/${attempts} not ready (status=${status_code}, body=${response_body})"
    sleep 2
    attempt=$((attempt + 1))
  done
  printf '%s' "${response_body}"
  return 1
}

extract_token() {
  sed -n 's/.*"token":"\([^"]*\)".*/\1/p' <<<"${1}" | sed -n '1p'
}

delete_token() {
  curl -sS \
    -X POST "http://127.0.0.1:8081/admin/delete" \
    -H "X-Admin-Secret: ${ADMIN_SECRET}" \
    -H "Content-Type: application/json" \
    -d "{\"token\":\"${1}\"}" >/dev/null || true
}

run_gateway_request() {
  curl -sS -w $'\n%{http_code}' \
    -X POST "http://127.0.0.1:8080/v1/chat/completions" \
    -H "Authorization: Bearer ${1}" \
    -H "Content-Type: application/json" \
    -d "$(printf '{"model":"%s","stream":true,"messages":[{"role":"user","content":"%s"}]}' "${TEST_MODEL}" "${2}")"
}

run_request_batch() {
  local token="${1}"
  local phase_label="${2}"
  local i=0
  while (( i < TEST_REQUEST_COUNT )); do
    local n=$((i + 1))
    log "${phase_label} request ${n}/${TEST_REQUEST_COUNT}"
    local resp
    resp="$(run_gateway_request "${token}" "${TEST_PROMPT}")"
    local body="${resp%$'\n'*}"
    local status="${resp##*$'\n'}"
    if [[ "${status}" != "200" ]]; then
      print_debug_info
      fail "${phase_label} request ${n} returned HTTP ${status}; body: ${body}"
    fi
    if [[ "${body}" != *"data: [DONE]"* ]]; then
      print_debug_info
      fail "${phase_label} request ${n} missing SSE [DONE] marker"
    fi
    i=$((i + 1))
  done
}

# Picks one container of `service` and removes it via `docker rm -f` (SIGKILL +
# remove). Removal — not just docker kill — is required because cache-service
# and rag-service carry `restart: on-failure`, which would otherwise auto-
# respawn the killed container and undo what we are trying to verify.
# Removing the container bypasses the restart policy entirely and exercises
# the etcd lease-TTL eviction path (no graceful deregister was sent).
kill_one_instance() {
  local service="${1}"
  local ids
  ids="$(compose ps -q "${service}")"
  if [[ -z "${ids}" ]]; then
    fail "No running containers for service ${service}"
  fi
  local victim
  victim="$(printf '%s\n' "${ids}" | head -n1)"
  log "Removing one ${service} instance (container ${victim:0:12}) via docker rm -f..."
  docker rm -f "${victim}" >/dev/null
  printf '%s' "${victim}"
}

main() {
  bootstrap_env
  load_env
  validate_env
  detect_compose_cmd

  trap cleanup EXIT

  log "Compose file: ${COMPOSE_FILE}"
  log "Env file:     ${SCRIPT_ENV_FILE}"
  log "Project:      ${COMPOSE_PROJECT_NAME}"
  log "Scale:        ${SCALE_PER_SERVICE} per service"
  log "Lease wait:   ${LEASE_WAIT_SECONDS}s"

  log "Building images..."
  compose build

  log "Bringing up stack at default scale=1..."
  compose up -d

  # Apply scale via the `scale` subcommand. We previously tried multiple
  # `--scale` flags on `up`, but podman-compose 1.5.0 only honors the last
  # one. We also tried per-service `up --scale X=N X` loops, but each call
  # re-evaluates the dependency graph and silently resets earlier scales
  # back to 1 (the leaf-most service — embedding — was the only one that
  # stayed). The `scale` subcommand adjusts replica counts directly without
  # touching unrelated services; combined with --no-deps it does not trigger
  # any dependency restarts.
  log "Scaling all gRPC services to ${SCALE_PER_SERVICE}..."
  local scale_args=()
  for svc in "${SCALED_SERVICES[@]}"; do
    scale_args+=("${svc}=${SCALE_PER_SERVICE}")
  done
  compose scale --no-deps "${scale_args[@]}"

  log "Container counts after scaling:"
  for svc in "${SCALED_SERVICES[@]}"; do
    local count
    count="$(compose ps -q "${svc}" | wc -l | tr -d ' ')"
    log "  ${svc}: ${count}"
  done

  log "Waiting for admin API to come up..."
  local create_response=""
  if ! create_response="$(wait_for_token_creation 60)"; then
    print_debug_info
    fail "admin/create never returned 200. Last response: ${create_response}"
  fi
  local token
  token="$(extract_token "${create_response}")"
  if [[ -z "${token}" ]]; then
    print_debug_info
    fail "Unable to parse token from admin/create response: ${create_response}"
  fi
  log "Test token: ${token}"

  # ---- Phase 1: verify all instances registered ----
  log "----- Phase 1: verify etcd registrations -----"
  for svc in "${SCALED_SERVICES[@]}"; do
    if ! wait_for_etcd_count "${svc}" "${SCALE_PER_SERVICE}" 45; then
      print_debug_info
      fail "Service ${svc} did not register ${SCALE_PER_SERVICE} endpoints"
    fi
  done

  # ---- Phase 2: baseline requests pre-kill ----
  log "----- Phase 2: baseline requests (pre-kill) -----"
  run_request_batch "${token}" "pre-kill"

  # ---- Phase 3: SIGKILL one instance per service ----
  log "----- Phase 3: SIGKILL one instance per scaled service -----"
  local killed_ids=()
  for svc in "${SCALED_SERVICES[@]}"; do
    killed_ids+=("$(kill_one_instance "${svc}")")
  done

  log "Sleeping ${LEASE_WAIT_SECONDS}s to let etcd leases expire..."
  sleep "${LEASE_WAIT_SECONDS}"

  # ---- Phase 4: verify etcd key count dropped ----
  log "----- Phase 4: verify etcd key count dropped to ${SCALE_PER_SERVICE}-1 -----"
  local expected_after=$((SCALE_PER_SERVICE - 1))
  for svc in "${SCALED_SERVICES[@]}"; do
    if ! wait_for_etcd_count "${svc}" "${expected_after}" 30; then
      print_debug_info
      fail "Service ${svc} etcd key count did not drop to ${expected_after} after kill"
    fi
  done

  # ---- Phase 5: verify gateway still serves traffic ----
  log "----- Phase 5: post-kill requests (failover verification) -----"
  run_request_batch "${token}" "post-kill"

  delete_token "${token}"
  log "etcd discovery end-to-end verification PASSED."
}

declare -a COMPOSE_CMD=()
main "$@"
