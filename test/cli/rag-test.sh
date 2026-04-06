#!/usr/bin/env bash
#
# RAG end-to-end CLI test
#
# What this script verifies:
#   1. POST /admin/rag/ingest  — returns 200 with doc_id + ingested_count
#   2. GET  /v1/chat/completions (with X-RAG-Collection header)
#          — gateway injects retrieved context; LLM response contains the
#            unique magic token that only exists in the ingested document,
#            proving that RAG augmentation reached the LLM.
#   3. DELETE /admin/rag/doc   — returns 204 No Content
#
# Usage:
#   bash test/cli/rag-test.sh
#
# Environment overrides (all optional):
#   COMPOSE_FILE      Path to the compose file (default: docker-compose.yml)
#   TEST_MODEL        LLM model name            (default: gpt-4o-mini)
#   KEEP_SERVICES     Set to 1 to leave containers running after the test
#   SKIP_COMPOSE      Set to 1 to skip docker compose up (test against a
#                     stack that is already running)

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
SCRIPT_ENV_FILE="${SCRIPT_DIR}/.env"
ROOT_ENV_FILE="${ROOT_DIR}/.env"
ROOT_ENV_EXAMPLE_FILE="${ROOT_DIR}/.env.example"
COMPOSE_FILE="${COMPOSE_FILE:-${ROOT_DIR}/docker-compose.yml}"
TEST_MODEL="${TEST_MODEL:-gpt-4o-mini}"
KEEP_SERVICES="${KEEP_SERVICES:-0}"
SKIP_COMPOSE="${SKIP_COMPOSE:-0}"

GATEWAY_URL="http://127.0.0.1:8080"
ADMIN_URL="http://127.0.0.1:8081"

# A unique string that is almost certainly not in the LLM's training data.
# The test injects it via RAG, then asks the LLM to repeat it.
# If the token appears in the response, RAG augmentation is confirmed.
RAG_MAGIC_TOKEN="RAGTEST_CRIMSON_ZEPHYR_9427"

# Track test results
TESTS_PASSED=0
TESTS_FAILED=0

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------

log()  { printf '[rag-test] %s\n'      "$*" >&2; }
pass() { printf '[rag-test] PASS: %s\n' "$*" >&2; TESTS_PASSED=$((TESTS_PASSED + 1)); }
fail() { printf '[rag-test] FAIL: %s\n' "$*" >&2; TESTS_FAILED=$((TESTS_FAILED + 1)); }
die()  { printf '[rag-test] FATAL: %s\n' "$*" >&2; exit 1; }

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
    fail "${label}: expected response to contain '${needle}'"
  fi
}

# ---------------------------------------------------------------------------
# Env bootstrap (mirrors docker-compose-test.sh)
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
      die "${SCRIPT_ENV_FILE} is missing required variable: ${var_name}"
    fi
  done

  if [[ "${EMBED_API_KEY}" == "sk-your-embedding-api-key" || "${COMPL_API_KEY}" == "sk-your-completion-api-key" ]]; then
    die "${SCRIPT_ENV_FILE} still contains example API keys. Replace them with real values and rerun."
  fi

  if [[ "${ADMIN_SECRET}" == "change-me-to-a-strong-random-secret" ]]; then
    die "${SCRIPT_ENV_FILE} still contains the example ADMIN_SECRET. Replace it with a real value and rerun."
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

cleanup() {
  if [[ "${KEEP_SERVICES}" == "1" || "${SKIP_COMPOSE}" == "1" ]]; then
    log "Leaving containers running (KEEP_SERVICES=${KEEP_SERVICES}, SKIP_COMPOSE=${SKIP_COMPOSE})."
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
  log "Compose service status:"
  "${COMPOSE_CMD[@]}" --env-file "${SCRIPT_ENV_FILE}" -f "${COMPOSE_FILE}" ps || true
  log "Recent gateway logs:"
  "${COMPOSE_CMD[@]}" --env-file "${SCRIPT_ENV_FILE}" -f "${COMPOSE_FILE}" logs --tail 60 gateway || true
  log "Recent rag-service logs:"
  "${COMPOSE_CMD[@]}" --env-file "${SCRIPT_ENV_FILE}" -f "${COMPOSE_FILE}" logs --tail 60 rag-service || true
}

# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------

# Wait until the admin API responds 200 to a token-create request.
# Prints the raw response body on success; returns 1 on timeout.
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

extract_json_number() {
  local json="${1}" field="${2}"
  sed -n "s/.*\"${field}\":\([0-9]*\).*/\1/p" <<<"${json}" | head -1
}

# ---------------------------------------------------------------------------
# Test steps
# ---------------------------------------------------------------------------

# Step 1 — ingest a document with the magic token.
test_ingest() {
  local collection="${1}"
  log ""
  log "=== STEP 1: Ingest document into collection '${collection}' ==="

  local payload
  payload="$(cat <<EOF
{
  "collection": "${collection}",
  "source": "rag-cli-test.md",
  "chunks": [
    {
      "content": "The RAG magic token is ${RAG_MAGIC_TOKEN}. Remember this value exactly.",
      "chunk_index": 0,
      "total_chunks": 1
    }
  ]
}
EOF
)"

  local raw body status
  raw="$(
    curl -sS -w $'\n%{http_code}' \
      -X POST "${ADMIN_URL}/admin/rag/ingest" \
      -H "X-Admin-Secret: ${ADMIN_SECRET}" \
      -H "Content-Type: application/json" \
      -d "${payload}"
  )"
  body="${raw%$'\n'*}"
  status="${raw##*$'\n'}"

  log "Ingest response status: ${status}"
  log "Ingest response body:   ${body}"

  assert_eq "ingest HTTP status" "${status}" "200"

  local doc_id ingested_count
  doc_id="$(extract_json_field "${body}" "doc_id")"
  ingested_count="$(extract_json_number "${body}" "ingested_count")"

  if [[ -n "${doc_id}" ]]; then
    pass "ingest returned a non-empty doc_id (${doc_id})"
  else
    fail "ingest response missing doc_id"
  fi

  assert_eq "ingest ingested_count" "${ingested_count}" "1"

  printf '%s' "${doc_id}"
}

# Step 2 — send a chat completion that should be augmented with the RAG context.
test_augmented_completion() {
  local token="${1}" collection="${2}"
  log ""
  log "=== STEP 2: Augmented completion with X-RAG-Collection: ${collection} ==="

  # Give Qdrant a moment to index the newly ingested vector.
  sleep 2

  local question="What is the RAG magic token? Reply with the token value only, no other text."

  local raw body status
  raw="$(
    curl -sS -w $'\n%{http_code}' \
      -X POST "${GATEWAY_URL}/v1/chat/completions" \
      -H "Authorization: Bearer ${token}" \
      -H "X-RAG-Collection: ${collection}" \
      -H "Content-Type: application/json" \
      -d "$(printf '{"model":"%s","stream":true,"messages":[{"role":"user","content":"%s"}]}' \
            "${TEST_MODEL}" "${question}")"
  )"
  body="${raw%$'\n'*}"
  status="${raw##*$'\n'}"

  log "Completion response status: ${status}"
  log "Completion response body:"
  printf '%s\n' "${body}" >&2

  assert_eq "augmented completion HTTP status" "${status}" "200"
  assert_contains "augmented completion SSE done marker" "${body}" "data: [DONE]"

  # Concatenate all SSE chunk "content" values before matching.
  # A token may be split across multiple chunks (e.g. "RAGTEST_CRIMSON_ZEPH" + "YR_9427"),
  # so checking each chunk individually would produce a false negative.
  local full_text
  full_text="$(printf '%s' "${body}" | grep -o '"content":"[^"]*"' | sed 's/"content":"//;s/"$//' | tr -d '\n')"

  if [[ "${full_text}" == *"${RAG_MAGIC_TOKEN}"* ]]; then
    pass "RAG context confirmed: magic token '${RAG_MAGIC_TOKEN}' found in LLM response"
  else
    fail "RAG context NOT confirmed: magic token '${RAG_MAGIC_TOKEN}' absent from LLM response (RAG retrieval may have failed)"
  fi
}

# Step 3 — delete the ingested document.
test_delete_doc() {
  local doc_id="${1}" collection="${2}"
  log ""
  log "=== STEP 3: Delete document (doc_id=${doc_id}, collection=${collection}) ==="

  local raw body status
  raw="$(
    curl -sS -w $'\n%{http_code}' \
      -X DELETE "${ADMIN_URL}/admin/rag/doc" \
      -H "X-Admin-Secret: ${ADMIN_SECRET}" \
      -H "Content-Type: application/json" \
      -d "{\"doc_id\":\"${doc_id}\",\"collection\":\"${collection}\"}"
  )"
  body="${raw%$'\n'*}"
  status="${raw##*$'\n'}"

  log "Delete response status: ${status}"
  [[ -n "${body}" ]] && log "Delete response body: ${body}"

  assert_eq "delete doc HTTP status" "${status}" "204"
}

# Step 4 — after deletion, a follow-up request should succeed but the LLM
# should no longer have RAG context (no magic token in training data).
test_post_delete_completion() {
  local token="${1}" collection="${2}"
  log ""
  log "=== STEP 4: Post-delete completion (magic token should be absent) ==="

  # Give Qdrant a moment to remove the deleted vectors.
  sleep 2

  local question="What is the RAG magic token? Reply with the token value only, no other text."

  local raw body status
  raw="$(
    curl -sS -w $'\n%{http_code}' \
      -X POST "${GATEWAY_URL}/v1/chat/completions" \
      -H "Authorization: Bearer ${token}" \
      -H "X-RAG-Collection: ${collection}" \
      -H "Content-Type: application/json" \
      -d "$(printf '{"model":"%s","stream":true,"messages":[{"role":"user","content":"%s"}]}' \
            "${TEST_MODEL}" "${question}")"
  )"
  body="${raw%$'\n'*}"
  status="${raw##*$'\n'}"

  log "Post-delete response status: ${status}"
  log "Post-delete response body:"
  printf '%s\n' "${body}" >&2

  assert_eq "post-delete completion HTTP status" "${status}" "200"
  assert_contains "post-delete completion SSE done marker" "${body}" "data: [DONE]"

  # Same concatenation logic as step 2: join all chunk content values before matching.
  local full_text
  full_text="$(printf '%s' "${body}" | grep -o '"content":"[^"]*"' | sed 's/"content":"//;s/"$//' | tr -d '\n')"

  if [[ "${full_text}" != *"${RAG_MAGIC_TOKEN}"* ]]; then
    pass "post-delete: magic token absent from LLM response (document successfully removed)"
  else
    fail "post-delete: magic token still present in LLM response (document may not have been deleted)"
  fi
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
    log "Using compose file: ${COMPOSE_FILE}"
    log "Using env file:     ${SCRIPT_ENV_FILE}"
    log "Starting docker compose stack..."
    "${COMPOSE_CMD[@]}" --env-file "${SCRIPT_ENV_FILE}" -f "${COMPOSE_FILE}" up -d --build
  fi

  local ts
  ts="$(date +%s)"
  local token_alias="rag-test-${ts}"
  local rag_collection="rag-cli-test-${ts}"

  # Wait for admin API and create a token.
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
  log "RAG collection: ${rag_collection}"
  log "Magic token: ${RAG_MAGIC_TOKEN}"

  local doc_id=""

  # Run test steps (continue even if individual assertions fail so we can
  # attempt cleanup, but track failures for the final exit code).
  doc_id="$(test_ingest "${rag_collection}")" || true
  test_augmented_completion "${token}" "${rag_collection}" || true

  if [[ -n "${doc_id}" ]]; then
    test_delete_doc "${doc_id}" "${rag_collection}" || true
    test_post_delete_completion "${token}" "${rag_collection}" || true
  else
    log "Skipping delete and post-delete steps (no doc_id — ingest failed)."
  fi

  delete_token "${token}"

  # ---------------------------------------------------------------------------
  # Summary
  # ---------------------------------------------------------------------------
  log ""
  log "=============================="
  log " RAG test summary"
  log "=============================="
  log " PASSED: ${TESTS_PASSED}"
  log " FAILED: ${TESTS_FAILED}"
  log "=============================="

  if (( TESTS_FAILED > 0 )); then
    print_debug_info
    die "${TESTS_FAILED} test(s) failed."
  fi

  log "All RAG tests passed."
}

main "$@"
