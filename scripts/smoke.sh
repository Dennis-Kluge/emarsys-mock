#!/usr/bin/env bash
# Exercises the definition of done against a running instance.
#
#   scripts/smoke.sh [base-url]     default http://localhost:8080
#
# Set CTL_TOKEN when the instance is not reachable over loopback -- an
# unconfigured mock restricts /_ctl and /admin to localhost, and from outside a
# container that is not where the request appears to come from.
#
# CI runs this against the scratch container, which is the only place the
# embedded timezone database and the absence of a shell are really tested.
set -euo pipefail

BASE="${1:-http://localhost:8080}"

CTL_TOKEN="${CTL_TOKEN:-}"

# ctl issues a control-plane request, carrying the token when one is configured.
ctl() {
  if [ -n "$CTL_TOKEN" ]; then
    curl -sf -H "X-Ctl-Token: $CTL_TOKEN" "$@"
  else
    curl -sf "$@"
  fi
}

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok  $*"; }

echo "smoke-testing $BASE"

for _ in $(seq 1 30); do
  curl -sf "$BASE/_ctl/health" >/dev/null 2>&1 && break
  sleep 1
done

health="$(curl -sf "$BASE/_ctl/health")" || fail "health probe did not answer"
grep -q '"status":"ok"' <<<"$health" || fail "not healthy: $health"
ok "health probe"

# Europe/Vienna resolving is the whole point of the time/tzdata import. In a
# scratch image there is no /usr/share/zoneinfo to fall back on.
grep -q '"export_timezone":"Europe/Vienna"' <<<"$health" \
  || fail "timezone did not resolve: $health"
ok "export timezone resolves"

wsse_header() {
  local nonce created digest
  nonce="$(openssl rand -hex 16)"
  created="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  digest="$(printf '%s%s%s' "$nonce" "$created" mock-secret \
    | sha1sum | cut -d' ' -f1 | tr -d '\n' | base64)"
  printf 'X-WSSE: UsernameToken Username="mock-api-user", PasswordDigest="%s", Nonce="%s", Created="%s"' \
    "$digest" "$nonce" "$created"
}

# A WSSE-signed request must be accepted with no client change beyond the base URL.
curl -sf -H "$(wsse_header)" "$BASE/api/v2/field" >/dev/null \
  || fail "a WSSE-signed request was rejected"
ok "WSSE authentication"

# A batch with one good and one bad row is HTTP 200 with replyCode 0, and the
# failure lives in data.errors.
batch="$(curl -sf -H "$(wsse_header)" -H 'Content-Type: application/json' \
  -X POST "$BASE/api/v2/contact" \
  -d '{"key_id":"3","contacts":[{"3":"ok@example.com"},{"3":"bad@example.com","31":"true"}]}')"
grep -q '"replyCode":0' <<<"$batch" || fail "batch was not a success envelope: $batch"
grep -q '2006' <<<"$batch"          || fail "the failing row was not reported: $batch"
ok "batch partial failure returns 200 with the error inside"

# Export: in progress twice, then done, then CSV.
export_id="$(curl -sf -H "$(wsse_header)" -H 'Content-Type: application/json' \
  -X POST "$BASE/api/v2/contact/getchanges" \
  -d '{"contact_fields":[1,3],"add_field_names_header":1}' \
  | sed 's/.*"id":\([0-9]*\).*/\1/')"
for expected in "in progress" "in progress" "done"; do
  status="$(curl -sf -H "$(wsse_header)" "$BASE/api/v2/export/$export_id" \
    | sed 's/.*"status":"\([^"]*\)".*/\1/')"
  [ "$status" = "$expected" ] || fail "export status was '$status', expected '$expected'"
done
curl -sf -H "$(wsse_header)" "$BASE/api/v2/export/$export_id/data" | grep -q '@example.com' \
  || fail "export CSV is empty"
ok "export polls twice then serves the file"

# One 429, then the rule retires itself.
ctl -X POST "$BASE/_ctl/faults" \
  -d '{"match_path_pattern":"/api/*","http_status":429,"reply_code":2011,"reply_text":"Rate limit exceeded","remaining_hits":1}' \
  >/dev/null
code="$(curl -s -o /dev/null -w '%{http_code}' -H "$(wsse_header)" "$BASE/api/v2/field")"
[ "$code" = "429" ] || fail "the armed fault did not fire (got $code)"
code="$(curl -s -o /dev/null -w '%{http_code}' -H "$(wsse_header)" "$BASE/api/v2/field")"
[ "$code" = "200" ] || fail "the fault did not retire after one hit (got $code)"
ok "fault injection fires once and retires"

# Reset has to be fast enough to call between test cases.
start="$(date +%s%N)"
ctl -X POST "$BASE/_ctl/reset" >/dev/null || fail "reset failed"
elapsed=$(( ($(date +%s%N) - start) / 1000000 ))
[ "$elapsed" -lt 500 ] || fail "reset took ${elapsed}ms"
ok "reset in ${elapsed}ms"

# The dashboard has to render, not just exist.
ctl "$BASE/admin/requests" | grep -q '<table' || fail "the dashboard did not render"
ok "dashboard renders"

echo "all checks passed"
