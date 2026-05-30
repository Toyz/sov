#!/usr/bin/env bash
# End-to-end demo for the chirp example. Drive against either:
#   examples/chirp/cmd/monolith   (single binary, all services in-process)
#   examples/chirp/cmd/hybrid     (gateway + auth+authz+user local, chirp+feed remote)
#   examples/chirp/cmd/mesh/...   (multi-binary, gateway routes remotes)
# Output should be byte-equivalent across all three — that's PEMM.
#
# Sign-up is intentionally two calls: Auth.register mints a subject id;
# User.register binds a profile to that subject. Auth and User share the
# subject as their only common key; neither imports the other's types.
#
# Usage: BASE=http://localhost:8080 bash walkthrough.sh

set -euo pipefail
BASE="${BASE:-http://localhost:8080}"

post() {
  local path="$1"; shift
  local body="$1"; shift
  local token="${1:-}"
  if [[ -n "$token" ]]; then
    curl -sS -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $token" -d "$body" "$BASE$path"
  else
    curl -sS -X POST -H "Content-Type: application/json" -d "$body" "$BASE$path"
  fi
  echo
}

# Capture subject id from an Auth.register response.
sub_from() {
  echo "$1" | sed -nE 's/.*"subject":"([^"]+)".*/\1/p'
}

# Capture session token from an Auth.login response.
tok_from() {
  echo "$1" | sed -nE 's/.*"token":"([^"]+)".*/\1/p'
}

echo "== 1. Sign up (Auth.register → User.register, two-call) =="
ALICE_REG=$(post /rpc/Auth/register '{"args":[{"handle":"alice","password":"pw"}]}')
echo "  auth:  $ALICE_REG"
ALICE_SUB=$(sub_from "$ALICE_REG")
post /rpc/User/register "{\"args\":[{\"subject\":\"$ALICE_SUB\",\"handle\":\"alice\",\"display\":\"Alice\"}]}"

BOB_REG=$(post /rpc/Auth/register '{"args":[{"handle":"bob","password":"pw"}]}')
BOB_SUB=$(sub_from "$BOB_REG")
post /rpc/User/register "{\"args\":[{\"subject\":\"$BOB_SUB\",\"handle\":\"bob\",\"display\":\"Bob\"}]}"

echo "== 2. Log in =="
ALICE_LOGIN=$(post /rpc/Auth/login '{"args":[{"handle":"alice","password":"pw"}]}')
echo "  alice: $ALICE_LOGIN"
ALICE_TOK=$(tok_from "$ALICE_LOGIN")

BOB_LOGIN=$(post /rpc/Auth/login '{"args":[{"handle":"bob","password":"pw"}]}')
BOB_TOK=$(tok_from "$BOB_LOGIN")

echo "== 3. Anonymous follow attempt — authz returns {authenticate:true} → 401 =="
post /rpc/User/follow "{\"args\":[{\"followee_id\":\"$BOB_SUB\"}]}"

echo "== 4. Alice follows Bob (authenticated, 200) =="
post /rpc/User/follow "{\"args\":[{\"followee_id\":\"$BOB_SUB\"}]}" "$ALICE_TOK"

echo "== 5. Bob posts two chirps =="
BOB_CHIRP=$(post /rpc/Chirp/post '{"args":[{"body":"hello from bob"}]}' "$BOB_TOK")
echo "  $BOB_CHIRP"
BOB_CHIRP_ID=$(echo "$BOB_CHIRP" | sed -nE 's/.*"id":"([^"]+)".*/\1/p')
post /rpc/Chirp/post '{"args":[{"body":"second chirp"}]}' "$BOB_TOK"

echo "== 6. Alice's timeline (should contain bob's chirps) =="
post /rpc/Feed/timeline '{"args":[{"limit":10}]}' "$ALICE_TOK"

echo "== 7. Bob attempts mod-only Chirp.delete — authz returns 'mod role required' → 403 =="
post /rpc/Chirp/delete "{\"args\":[{\"id\":\"$BOB_CHIRP_ID\"}]}" "$BOB_TOK"

echo "== 8. Alice (granted 'mod' in the authz RBAC map) deletes the chirp → 200 =="
post /rpc/Chirp/delete "{\"args\":[{\"id\":\"$BOB_CHIRP_ID\"}]}" "$ALICE_TOK"

echo "== 9. /rpc/_health rollup =="
curl -sS "$BASE/rpc/_health"
echo

echo "== 9a. CORS preflight (OPTIONS → 204 with A-C-A-* headers) =="
PREFLIGHT=$(curl -sS -i -X OPTIONS \
  -H "Origin: https://app.example" \
  -H "Access-Control-Request-Method: POST" \
  -H "Access-Control-Request-Headers: Content-Type,Authorization" \
  "$BASE/rpc/Auth/login")
echo "$PREFLIGHT" | head -1
echo "$PREFLIGHT" | grep -i '^access-control-' | sort
status=$(echo "$PREFLIGHT" | head -1 | awk '{print $2}')
[[ "$status" == "204" ]] || { echo "FAIL: preflight status=$status want 204"; exit 1; }
echo "$PREFLIGHT" | grep -qi '^access-control-allow-origin:' || { echo "FAIL: missing A-C-A-Origin"; exit 1; }
echo "$PREFLIGHT" | grep -qi '^access-control-allow-methods:' || { echo "FAIL: missing A-C-A-Methods"; exit 1; }
echo "  preflight OK"

echo "== 9b. Request-ID round-trip (server stamps X-Sov-Request-Id on response) =="
RID_RESP=$(curl -sS -i -X POST -H "Content-Type: application/json" \
  -d '{"args":[{"handle":"alice","password":"pw"}]}' "$BASE/rpc/Auth/login")
echo "$RID_RESP" | grep -i '^x-sov-request-id:' || { echo "FAIL: missing X-Sov-Request-Id response header"; exit 1; }
RID=$(echo "$RID_RESP" | grep -i '^x-sov-request-id:' | tr -d '\r' | awk '{print $2}')
[[ -n "$RID" ]] || { echo "FAIL: empty request id"; exit 1; }
echo "  request-id=$RID"

echo "== 9c. /rpc/_manifest plugins listing =="
MANIFEST=$(curl -sS "$BASE/rpc/_manifest")
echo "$MANIFEST" | python3 -c "
import sys,json
d=json.load(sys.stdin)
plugins=d.get('plugins',[])
names=sorted(p.get('name','') for p in plugins)
print(f'  plugins ({len(names)}): {names}')
assert 'request-id' in names, 'request-id plugin missing'
assert 'cors' in names, 'cors plugin missing'
print('  manifest OK')
"

echo "== 10. /rpc/_introspect catalog (first 200 chars) =="
curl -sS "$BASE/rpc/_introspect" | head -c 200 || true
echo "..."

echo "== 11. Dual-shape args proof — SAME method, two wire shapes, same result =="
echo "  named:      $(curl -sS -X POST -H 'Content-Type: application/json' -d '{"args":{"id":"'"$BOB_SUB"'"}}' "$BASE/rpc/User/get")"
echo "  positional: $(curl -sS -X POST -H 'Content-Type: application/json' -d '{"args":["'"$BOB_SUB"'"]}' "$BASE/rpc/User/get")"

echo "== 12. Type catalog count + drift radar count =="
curl -sS "$BASE/rpc/_introspect" | grep -oE '"types":\{[^}]*' | head -c 80 || true
echo
echo -n "  cross_refs entries: "
curl -sS "$BASE/rpc/_introspect" | grep -oE '"cross_refs":\{[^}]*' | head -c 60 || true
echo

echo "== 13. Explorer UI mount check =="
curl -sS "$BASE/rpc/_explorer/" | head -c 80 || true
echo "..."
echo "  (Open $BASE/rpc/_explorer/ in a browser for the live UI.)"

echo "== 14. Cascading batch — 4 calls in one POST =="
# In mesh mode, gateway sees 4 calls (3 Chirp + 1 User), groups by
# destination, and POSTs ONE /rpc/_batch to the Chirp pod (collapsing 3
# RTTs into 1) plus ONE direct call to the User pod. In monolith mode
# every entry resolves local, so there are no remote round trips to
# coalesce — the response shape is the same either way.
# Note: Chirp.listByAuthors is gated by authz, so we send alice's token.
post /rpc/_batch "$(cat <<JSON
{"calls":{
  "alice":   {"service":"User","method":"get","args":{"id":"$ALICE_SUB"}},
  "list":    {"service":"Chirp","method":"list","args":{"limit":5}},
  "byBob":   {"service":"Chirp","method":"listByAuthors","args":{"author_ids":["$BOB_SUB"],"limit":5}},
  "byAlice": {"service":"Chirp","method":"listByAuthors","args":{"author_ids":["$ALICE_SUB"],"limit":5}}
}}
JSON
)" "$ALICE_TOK"
