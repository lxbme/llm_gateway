#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
SCRIPT_ENV_FILE="${SCRIPT_DIR}/.env"
ROOT_ENV_FILE="${ROOT_DIR}/.env"
ROOT_ENV_EXAMPLE_FILE="${ROOT_DIR}/.env.example"
COMPOSE_FILE="${COMPOSE_FILE:-${ROOT_DIR}/docker-compose.yml}"
TEST_MODEL="${TEST_MODEL:-gpt-4o-mini}"
TEST_PROMPT="${TEST_PROMPT:-Reply with OK only.}"
TEST_REQUEST_COUNT="${TEST_REQUEST_COUNT:-3}"
KEEP_SERVICES="${KEEP_SERVICES:-0}"

log() {
  printf '[test] %s\n' "$*" >&2
}

fail() {
  printf '[test] %s\n' "$*" >&2
  exit 1
}

now_ms() {
  printf '%s' "$(( $(date +%s%N) / 1000000 ))"
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
    EMBED_PROVIDER
    EMBED_API_KEY
    EMBED_ENDPOINT
    EMBED_MODEL
    COMPL_API_KEY
    COMPL_ENDPOINT
    ADMIN_SECRET
  )

  local var_name=""
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
    fail "TEST_REQUEST_COUNT must be a positive integer. Current value: ${TEST_REQUEST_COUNT}"
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

cleanup() {
  if [[ "${KEEP_SERVICES}" == "1" ]]; then
    log "Keeping test containers running because KEEP_SERVICES=1."
    return 0
  fi

  if [[ ${#COMPOSE_CMD[@]} -eq 0 ]]; then
    return 0
  fi

  log "Stopping test containers..."
  "${COMPOSE_CMD[@]}" --env-file "${SCRIPT_ENV_FILE}" -f "${COMPOSE_FILE}" down --remove-orphans >/dev/null 2>&1 || true
}

print_debug_info() {
  if [[ ${#COMPOSE_CMD[@]} -eq 0 ]]; then
    return 0
  fi

  log "Current compose service status:"
  "${COMPOSE_CMD[@]}" --env-file "${SCRIPT_ENV_FILE}" -f "${COMPOSE_FILE}" ps || true

  log "Recent gateway logs:"
  "${COMPOSE_CMD[@]}" --env-file "${SCRIPT_ENV_FILE}" -f "${COMPOSE_FILE}" logs --tail 80 gateway || true
}

wait_for_token_creation() {
  local alias_name="docker-compose-test-$(date +%s)"
  local attempts="${1:-60}"
  local attempt=0
  local raw_response=""
  local response_body=""
  local status_code=""

  log "Creating test token via admin API..."
  log "Admin request alias: ${alias_name}"

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
      log "Admin response status: ${status_code}"
      log "Admin response body: ${response_body}"
      printf '%s' "${response_body}"
      return 0
    fi

    log "Admin create attempt $((attempt + 1))/${attempts} not ready yet (status: ${status_code}, body: ${response_body})"
    sleep 2
    attempt=$((attempt + 1))
  done

  printf '%s' "${response_body}"
  return 1
}

extract_token() {
  local response_body="${1}"
  sed -n 's/.*"token":"\([^"]*\)".*/\1/p' <<<"${response_body}" | sed -n '1p'
}

delete_token() {
  local token="${1}"
  log "Deleting test token: ${token}"
  curl -sS \
    -X POST "http://127.0.0.1:8081/admin/delete" \
    -H "X-Admin-Secret: ${ADMIN_SECRET}" \
    -H "Content-Type: application/json" \
    -d "{\"token\":\"${token}\"}" >/dev/null || true
}

run_gateway_request() {
  local token="${1}"
  local prompt="${2}"
  curl -sS -w $'\n%{http_code}' \
    -X POST "http://127.0.0.1:8080/v1/chat/completions" \
    -H "Authorization: Bearer ${token}" \
    -H "Content-Type: application/json" \
    -d "$(printf '{"model":"%s","stream":true,"messages":[{"role":"user","content":"%s"}]}' "${TEST_MODEL}" "${prompt}")"
}

main() {
  bootstrap_env
  load_env
  validate_env
  detect_compose_cmd

  trap cleanup EXIT

  log "Using compose file: ${COMPOSE_FILE}"
  log "Using env file: ${SCRIPT_ENV_FILE}"
  log "Test model: ${TEST_MODEL}"
  log "Test prompt: ${TEST_PROMPT}"
  log "Repeated request count: ${TEST_REQUEST_COUNT}"
  log "Starting docker compose stack..."
  "${COMPOSE_CMD[@]}" --env-file "${SCRIPT_ENV_FILE}" -f "${COMPOSE_FILE}" up -d --build

  log "Waiting for admin API to become available..."
  local create_response=""
  if ! create_response="$(wait_for_token_creation 60)"; then
    print_debug_info
    fail "Failed to create a test token through the admin API. Last response: ${create_response}"
  fi

  local token=""
  token="$(extract_token "${create_response}")"
  if [[ -z "${token}" ]]; then
    print_debug_info
    fail "Unable to parse token from admin/create response: ${create_response}"
  fi

  log "Test token: ${token}"
  log "Starting repeated gateway requests with the same prompt..."

  local request_index=0
  while (( request_index < TEST_REQUEST_COUNT )); do
    local request_no=$((request_index + 1))
    local start_ms
    local end_ms
    local elapsed_ms
    local gateway_response=""
    local gateway_body=""
    local gateway_status=""

    log "----- Request ${request_no}/${TEST_REQUEST_COUNT} -----"
    log "Request token: ${token}"
    log "Request prompt: ${TEST_PROMPT}"

    start_ms="$(now_ms)"
    gateway_response="$(run_gateway_request "${token}" "${TEST_PROMPT}")"
    end_ms="$(now_ms)"
    elapsed_ms="$((end_ms - start_ms))"

    gateway_body="${gateway_response%$'\n'*}"
    gateway_status="${gateway_response##*$'\n'}"

    log "Response status: ${gateway_status}"
    log "Response latency: ${elapsed_ms} ms"
    log "Response body:"
    printf '%s\n' "${gateway_body}" >&2

    if [[ "${gateway_status}" != "200" ]]; then
      print_debug_info
      fail "Gateway returned non-200 status on request ${request_no}: ${gateway_status}"
    fi

    if [[ "${gateway_body}" != *"data: [DONE]"* ]]; then
      print_debug_info
      fail "Gateway response on request ${request_no} did not include the SSE completion marker."
    fi

    request_index=$((request_index + 1))
  done

  delete_token "${token}"
  log "Gateway verification completed successfully."
}

declare -a COMPOSE_CMD=()
main "$@"
