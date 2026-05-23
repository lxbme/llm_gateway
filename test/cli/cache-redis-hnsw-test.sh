#!/usr/bin/env bash
#
# End-to-end test for the redis_hnsw semantic cache backend.
#
# What this script verifies:
#   1. Stack starts with the redis_hnsw overlay and cache-service successfully
#      creates (or reuses) the RediSearch HNSW index on Redis Stack.
#   2. First request with a unique prompt is a cache MISS:
#        - Response chat ID does NOT start with `chatcmpl-cached-`.
#        - Async writeback lands a hash in Redis under the configured key prefix.
#        - FT.SEARCH against the index returns >= 1 document.
#   3. Second request with the SAME prompt is a cache HIT:
#        - Response chat ID DOES start with `chatcmpl-cached-`.
#        - The cached response carries the same magic token from step 2.
#        - cache-service logs include `[Info] Hit cache: <key>`.
#   4. Cleanup: stack is torn down (unless KEEP_SERVICES=1).
#
# Usage:
#   bash test/cli/cache-redis-hnsw-test.sh
#
# Environment overrides (all optional):
#   COMPOSE_FILE         Base compose file (default: docker-compose.yml)
#   OVERLAY_FILE         Redis Stack overlay (default: config/docker-compose.hnsw.yml)
#   TEST_MODEL           LLM model name (default: gpt-4o-mini)
#   REDIS_HNSW_HOST_PORT Host port mapped to redis-stack (default: 6380)
#   KEEP_SERVICES        Set to 1 to leave containers running after the test
#   SKIP_COMPOSE         Set to 1 to skip docker compose up (test against a
#                        stack that is already running with redis_hnsw selected)
#   CACHE_WRITEBACK_WAIT_SECONDS  Seconds to wait for async cache writeback
#                                 (default: 4)

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
SCRIPT_ENV_FILE="${SCRIPT_DIR}/.env"
ROOT_ENV_FILE="${ROOT_DIR}/.env"
ROOT_ENV_EXAMPLE_FILE="${ROOT_DIR}/config/.env.example"
COMPOSE_FILE="${COMPOSE_FILE:-${ROOT_DIR}/docker-compose.yml}"
OVERLAY_FILE="${OVERLAY_FILE:-${ROOT_DIR}/config/docker-compose.hnsw.yml}"
TEST_MODEL="${TEST_MODEL:-gpt-4o-mini}"
REDIS_HNSW_HOST_PORT="${REDIS_HNSW_HOST_PORT:-6380}"
KEEP_SERVICES="${KEEP_SERVICES:-0}"
SKIP_COMPOSE="${SKIP_COMPOSE:-0}"
CACHE_WRITEBACK_WAIT_SECONDS="${CACHE_WRITEBACK_WAIT_SECONDS:-4}"

GATEWAY_URL="http://127.0.0.1:8080"
ADMIN_URL="http://127.0.0.1:8081"

# Defaults that match cache/redis_hnsw/config.go and config/docker-compose.hnsw.yml.
REDIS_HNSW_INDEX_NAME_DEFAULT="llm_semantic_cache_idx"
REDIS_HNSW_KEY_PREFIX_DEFAULT="llm_semantic_cache"

# Tracking
TESTS_PASSED=0
TESTS_FAILED=0

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------

log()  { printf '[redis-hnsw-test] %s\n'      "$*" >&2; }
pass() { printf '[redis-hnsw-test] PASS: %s\n' "$*" >&2; TESTS_PASSED=$((TESTS_PASSED + 1)); }
fail() { printf '[redis-hnsw-test] FAIL: %s\n' "$*" >&2; TESTS_FAILED=$((TESTS_FAILED + 1)); }
die()  { printf '[redis-hnsw-test] FATAL: %s\n' "$*" >&2; exit 1; }

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

assert_not_contains() {
  local label="${1}" haystack="${2}" needle="${3}"
  if [[ "${haystack}" != *"${needle}"* ]]; then
    pass "${label}"
  else
    fail "${label}: did not expect output to contain '${needle}'"
  fi
}

# ---------------------------------------------------------------------------
# Env bootstrap (mirrors docker-compose-test.sh / rag-test.sh)
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

validate_env() {
  # completion-service runs in pool mode (pool_config.json), so COMPL_API_KEY /
  # COMPL_ENDPOINT are not required — at least one of the pool-referenced keys
  # (CLOSEAI_KEY / DS_KEY / ALL_KEY) needs to be set for upstream calls to
  # succeed. We warn but don't fail; a 401 downstream is a clearer signal than
  # a startup gate.
  local required_vars=(
    EMBED_PROVIDER
    EMBED_API_KEY
    EMBED_ENDPOINT
    EMBED_MODEL
    ADMIN_SECRET
  )

  local var_name=""
  for var_name in "${required_vars[@]}"; do
    if [[ -z "${!var_name:-}" ]]; then
      die "${SCRIPT_ENV_FILE} is missing required variable: ${var_name}"
    fi
  done

  if [[ "${EMBED_API_KEY}" == "sk-your-embedding-api-key" ]]; then
    die "${SCRIPT_ENV_FILE} still contains an example EMBED_API_KEY. Replace it and rerun."
  fi

  if [[ "${ADMIN_SECRET}" == "change-me-to-a-strong-random-secret" ]]; then
    die "${SCRIPT_ENV_FILE} still contains the example ADMIN_SECRET. Replace it with a real value and rerun."
  fi

  # Soft warning if none of the pool-referenced upstream keys are set.
  if [[ -z "${CLOSEAI_KEY:-}" && -z "${DS_KEY:-}" && -z "${ALL_KEY:-}" && -z "${COMPL_API_KEY:-}" ]]; then
    log "[Warning] None of CLOSEAI_KEY / DS_KEY / ALL_KEY / COMPL_API_KEY are set — upstream completion calls will 401."
  fi

  if [[ ! -f "${OVERLAY_FILE}" ]]; then
    die "Redis Stack overlay file not found: ${OVERLAY_FILE}"
  fi
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

# Wraps the compose command with both base file and overlay, and forces
# CACHE_STORE_PROVIDER=redis_hnsw via an inline env so the cache-service picks
# up the new backend.
compose() {
  CACHE_STORE_PROVIDER=redis_hnsw \
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
  log "Recent cache-service logs:"
  compose logs --tail 80 cache-service || true
  log "Recent gateway logs:"
  compose logs --tail 60 gateway || true
  log "Recent redis-stack logs:"
  compose logs --tail 40 redis-stack || true
}

# ---------------------------------------------------------------------------
# HTTP & Redis helpers
# ---------------------------------------------------------------------------

wait_for_admin_ready() {
  local alias_name="${1}"
  local attempts="${2:-60}"
  local attempt=0
  local raw="" body="" status=""

  log "Waiting for admin API (alias: ${alias_name})..."

  while (( attempt < attempts )); do
    raw="$(
      curl -sS -w $'\n%{http_code}' \
        -X POST "${ADMIN_URL}/admin/create" \
        -H "X-Admin-Secret: ${ADMIN_SECRET}" \
        -H "Content-Type: application/json" \
        -d "{\"alias\":\"${alias_name}\"}" || true
    )"
    body="${raw%$'\n'*}"
    status="${raw##*$'\n'}"

    if [[ "${status}" == "200" ]]; then
      log "Admin API ready. Response: ${body}"
      printf '%s' "${body}"
      return 0
    fi

    log "  attempt $((attempt + 1))/${attempts}: status=${status}"
    sleep 2
    attempt=$((attempt + 1))
  done

  printf '%s' "${body}"
  return 1
}

extract_json_field() {
  local json="${1}" field="${2}"
  sed -n "s/.*\"${field}\":\"\([^\"]*\)\".*/\1/p" <<<"${json}" | head -1
}

# Run a redis-cli command inside the llm_redis_stack container. We use docker
# exec instead of host redis-cli so the test has no extra system dependency.
redis_cli() {
  docker exec llm_redis_stack redis-cli "$@"
}

# Count hashes under the configured key prefix. `docker exec redis-cli KEYS`
# runs in non-interactive mode and emits one bare key per line (no quotes),
# so we count non-empty lines.
count_cache_keys() {
  local prefix="${1:-${REDIS_HNSW_KEY_PREFIX_DEFAULT}}"
  local raw
  raw="$(redis_cli KEYS "${prefix}:*" 2>/dev/null || true)"
  if [[ -z "${raw}" ]]; then
    printf '0'
    return 0
  fi
  printf '%s' "${raw}" | grep -c -v '^$' || true
}

# Returns 0 if the cache-service log mentions a successful index creation
# or reuse for the redis_hnsw backend, 1 otherwise.
verify_index_initialized() {
  local logs
  logs="$(compose logs --no-color cache-service 2>/dev/null || true)"
  if [[ "${logs}" == *"Created RediSearch index"* ]]; then
    pass "cache-service created the RediSearch index"
    return 0
  fi
  if [[ "${logs}" == *"Reusing RediSearch index"* ]]; then
    pass "cache-service reused an existing RediSearch index"
    return 0
  fi
  fail "cache-service logs do not contain a RediSearch index init line"
  printf '%s\n' "${logs}" | tail -40 >&2
  return 1
}

# Returns 0 if cache-service logs contain at least one `Hit cache:` entry
# emitted AFTER the timestamp passed in (Unix epoch seconds).
verify_hit_log() {
  local since_ts="${1}"
  local logs
  logs="$(compose logs --no-color --since "${since_ts}" cache-service 2>/dev/null || true)"
  if [[ "${logs}" == *"Hit cache:"* ]]; then
    pass "cache-service logged a Hit cache entry"
    return 0
  fi
  fail "cache-service did not log a Hit cache entry"
  printf '%s\n' "${logs}" | tail -40 >&2
  return 1
}

# ---------------------------------------------------------------------------
# Completion request — non-streaming
# ---------------------------------------------------------------------------

# Sends a chat completion. Captures the full JSON body. We use stream=false so
# the chat ID is trivial to extract (otherwise SSE chunks repeat the id).
post_completion() {
  local token="${1}" prompt="${2}"
  curl -sS -w $'\n%{http_code}' \
    -X POST "${GATEWAY_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${token}" \
    -H "Content-Type: application/json" \
    -d "$(printf '{"model":"%s","stream":false,"messages":[{"role":"user","content":"%s"}]}' \
          "${TEST_MODEL}" "${prompt}")"
}

# Sends a streaming chat completion. cache hits go through the streaming
# returnCachedAnswer path (gateway/server.go:247), so we always read the SSE
# stream and look for the cached-chat-ID marker.
post_completion_stream() {
  local token="${1}" prompt="${2}"
  curl -sS -w $'\n%{http_code}' \
    -X POST "${GATEWAY_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${token}" \
    -H "Content-Type: application/json" \
    -d "$(printf '{"model":"%s","stream":true,"messages":[{"role":"user","content":"%s"}]}' \
          "${TEST_MODEL}" "${prompt}")"
}

# Concatenate all SSE delta.content fragments — the magic token can be split
# across multiple chunks.
join_sse_content() {
  local body="${1}"
  printf '%s' "${body}" | grep -o '"content":"[^"]*"' | sed 's/"content":"//;s/"$//' | tr -d '\n'
}

# ---------------------------------------------------------------------------
# Test steps
# ---------------------------------------------------------------------------

test_first_request_miss() {
  local token="${1}" prompt="${2}"
  log ""
  log "=== STEP 1: First request (expect cache MISS) ==="
  log "Prompt: ${prompt}"

  local raw body status
  raw="$(post_completion_stream "${token}" "${prompt}")"
  body="${raw%$'\n'*}"
  status="${raw##*$'\n'}"

  log "Response status: ${status}"
  log "Response (first 800 chars):"
  printf '%s\n' "${body:0:800}" >&2

  assert_eq "first request HTTP status" "${status}" "200"
  assert_contains "first request SSE done marker" "${body}" "data: [DONE]"
  assert_not_contains "first request is not a cache hit" "${body}" "\"id\":\"chatcmpl-cached-"

  local answer
  answer="$(join_sse_content "${body}")"
  if [[ -z "${answer}" ]]; then
    fail "first request: extracted answer is empty"
  else
    pass "first request returned non-empty answer (${#answer} chars)"
  fi
  printf '%s' "${answer}"
}

test_writeback_landed() {
  log ""
  log "=== STEP 2: Verify async writeback landed in Redis ==="

  log "Waiting ${CACHE_WRITEBACK_WAIT_SECONDS}s for the async writeback worker..."
  sleep "${CACHE_WRITEBACK_WAIT_SECONDS}"

  local key_count
  key_count="$(count_cache_keys "${REDIS_HNSW_KEY_PREFIX_DEFAULT}")"
  log "Hashes under prefix ${REDIS_HNSW_KEY_PREFIX_DEFAULT}:*  count=${key_count}"

  if [[ "${key_count}" -ge 1 ]]; then
    pass "Redis contains >= 1 cache record (count=${key_count})"
  else
    fail "Redis contains 0 cache records — writeback did not land"
  fi

  local ft_total
  ft_total="$(redis_cli FT.SEARCH "${REDIS_HNSW_INDEX_NAME_DEFAULT}" "*" "LIMIT" "0" "0" 2>/dev/null | head -1 || true)"
  log "FT.SEARCH ${REDIS_HNSW_INDEX_NAME_DEFAULT} '*' total: ${ft_total}"
  if [[ "${ft_total}" =~ ^[1-9][0-9]*$ ]]; then
    pass "RediSearch index reports >= 1 indexed document (total=${ft_total})"
  else
    fail "RediSearch index reports no indexed documents (raw=${ft_total})"
  fi
}

test_second_request_hit() {
  local token="${1}" prompt="${2}" first_answer="${3}" hit_since_ts="${4}"
  log ""
  log "=== STEP 3: Second request (expect cache HIT) ==="
  log "Prompt: ${prompt}"

  local raw body status
  raw="$(post_completion_stream "${token}" "${prompt}")"
  body="${raw%$'\n'*}"
  status="${raw##*$'\n'}"

  log "Response status: ${status}"
  log "Response (first 800 chars):"
  printf '%s\n' "${body:0:800}" >&2

  assert_eq "second request HTTP status" "${status}" "200"
  assert_contains "second request SSE done marker" "${body}" "data: [DONE]"
  assert_contains "second request is a cache hit" "${body}" "\"id\":\"chatcmpl-cached-"

  local cached_answer
  cached_answer="$(join_sse_content "${body}")"
  if [[ -z "${cached_answer}" ]]; then
    fail "second request: cached answer is empty"
  else
    pass "second request returned non-empty cached answer (${#cached_answer} chars)"
  fi

  if [[ -n "${first_answer}" && "${cached_answer}" == "${first_answer}" ]]; then
    pass "cached answer matches first response exactly"
  else
    # Fuzzy fallback: cached answer should at least share a substantive prefix
    # with the first answer. LLMs can produce minor whitespace differences in
    # streaming vs cached streaming, so we check first 40 runes.
    local short_first="${first_answer:0:40}"
    if [[ -n "${short_first}" && "${cached_answer}" == *"${short_first}"* ]]; then
      pass "cached answer shares a 40-char prefix with first response (best-effort match)"
    else
      fail "cached answer differs from first response — possible threshold miss"
      log "  first  (first 200 chars): ${first_answer:0:200}"
      log "  cached (first 200 chars): ${cached_answer:0:200}"
    fi
  fi

  verify_hit_log "${hit_since_ts}" || true
}

delete_token() {
  local token="${1}"
  log "Deleting test token: ${token}"
  curl -sS \
    -X POST "${ADMIN_URL}/admin/delete" \
    -H "X-Admin-Secret: ${ADMIN_SECRET}" \
    -H "Content-Type: application/json" \
    -d "{\"token\":\"${token}\"}" >/dev/null || true
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
  bootstrap_env
  load_env
  validate_env
  detect_compose_cmd

  trap cleanup EXIT

  if [[ "${SKIP_COMPOSE}" == "1" ]]; then
    log "SKIP_COMPOSE=1 — skipping docker compose up."
  else
    log "Using base compose:    ${COMPOSE_FILE}"
    log "Using overlay compose: ${OVERLAY_FILE}"
    log "Using env file:        ${SCRIPT_ENV_FILE}"
    log "CACHE_STORE_PROVIDER:  redis_hnsw (forced via inline env)"
    log "Starting docker compose stack..."
    compose up -d --build
  fi

  local ts
  ts="$(date +%s)"
  local token_alias="redis-hnsw-test-${ts}"

  # Ensure admin API is up before we go further.
  local create_response=""
  if ! create_response="$(wait_for_admin_ready "${token_alias}" 60)"; then
    print_debug_info
    die "Admin API did not become available. Last response: ${create_response}"
  fi

  local token
  token="$(extract_json_field "${create_response}" "token")"
  if [[ -z "${token}" ]]; then
    print_debug_info
    die "Unable to parse token from admin/create response: ${create_response}"
  fi

  log "Test token: ${token}"

  # ---------------------------------------------------------------------------
  # Verify provider selection: cache-service must have built the HNSW index.
  # ---------------------------------------------------------------------------
  log ""
  log "=== STEP 0: Verify cache-service selected redis_hnsw backend ==="
  verify_index_initialized || true

  # Unique prompt prevents collisions with cached data from any prior run.
  local magic="REDISHNSW_TEST_${ts}_$$"
  local prompt="Reply with the literal token ${magic} and nothing else."

  local hit_log_since
  hit_log_since="$(date +%s)"

  # Step 1: cache miss.
  local first_answer=""
  first_answer="$(test_first_request_miss "${token}" "${prompt}")" || true

  # Step 2: writeback landed.
  test_writeback_landed || true

  # Step 3: cache hit.
  test_second_request_hit "${token}" "${prompt}" "${first_answer}" "${hit_log_since}" || true

  delete_token "${token}"

  # ---------------------------------------------------------------------------
  # Summary
  # ---------------------------------------------------------------------------
  log ""
  log "================================"
  log " redis_hnsw cache test summary"
  log "================================"
  log " PASSED: ${TESTS_PASSED}"
  log " FAILED: ${TESTS_FAILED}"
  log "================================"

  if (( TESTS_FAILED > 0 )); then
    print_debug_info
    die "${TESTS_FAILED} test(s) failed."
  fi

  log "All redis_hnsw cache tests passed."
}

main "$@"
