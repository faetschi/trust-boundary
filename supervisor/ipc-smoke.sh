#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

if [[ -n "${GO_BIN:-}" ]]; then
  go_bin="$GO_BIN"
elif [[ -x "$HOME/go-sdk/go/bin/go" ]]; then
  go_bin="$HOME/go-sdk/go/bin/go"
else
  go_bin="$(command -v go || true)"
fi
if [[ -z "$go_bin" || ! -x "$go_bin" ]]; then
  echo "Go executable not found; set GO_BIN or install Go" >&2
  exit 1
fi

if [[ -n "${NODE_BIN:-}" ]]; then
  node_bin="$NODE_BIN"
elif [[ -x "$HOME/node-v24.15.0-linux-x64/bin/node" ]]; then
  node_bin="$HOME/node-v24.15.0-linux-x64/bin/node"
else
  node_bin="$(command -v node || true)"
fi
if [[ -z "$node_bin" || ! -x "$node_bin" ]]; then
  echo "Node.js executable not found; set NODE_BIN (Node 24.15.0 recommended)" >&2
  exit 1
fi

work_dir="$(mktemp -d "$HOME/tbound-ipc-smoke.XXXXXX")"
chmod 700 "$work_dir"
mkdir -m 700 "$work_dir/tmp" "$work_dir/socket" "$work_dir/supervisor" "$work_dir/adapter"
export TMPDIR="$work_dir/tmp"

server_pid=""
cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf -- "$work_dir"
}
trap cleanup EXIT

cp -a "$repo_root/supervisor/." "$work_dir/supervisor/"
mkdir -m 700 "$work_dir/adapter/src"
cp "$repo_root/adapter/src/ipc-transport.ts" "$work_dir/adapter/src/"
cp "$repo_root/adapter/src/ipc-smoke-client.ts" "$work_dir/adapter/src/"

(
  cd "$work_dir/supervisor"
  "$go_bin" test -race -count=1 ./...
  "$go_bin" build -o "$work_dir/tbound" ./cmd/tbound
)

"$work_dir/tbound" --smoke-listen --socket-dir "$work_dir/socket" \
  >"$work_dir/transcript.ndjson" 2>"$work_dir/supervisor.log" &
server_pid=$!

for ((attempt = 0; attempt < 200; attempt += 1)); do
  if [[ -S "$work_dir/socket/tbound.sock" ]]; then
    break
  fi
  if ! kill -0 "$server_pid" 2>/dev/null; then
    wait "$server_pid" || true
    cat "$work_dir/supervisor.log" >&2
    echo "supervisor exited before creating its socket" >&2
    exit 1
  fi
  sleep 0.05
done

if [[ ! -S "$work_dir/socket/tbound.sock" ]]; then
  echo "supervisor did not create its Unix socket in time" >&2
  cat "$work_dir/supervisor.log" >&2
  exit 1
fi
[[ "$(stat -c '%a' "$work_dir/socket")" == "700" ]]
[[ "$(stat -c '%a' "$work_dir/socket/tbound.sock")" == "600" ]]
[[ "$(stat -c '%a' "$work_dir/socket/tbound.token")" == "600" ]]

if ! "$node_bin" --experimental-strip-types \
  "$work_dir/adapter/src/ipc-smoke-client.ts" \
  --socket "$work_dir/socket/tbound.sock" \
  --token-file "$work_dir/socket/tbound.token" \
  >"$work_dir/client-results.ndjson"; then
  cat "$work_dir/supervisor.log" >&2
  exit 1
fi

if ! wait "$server_pid"; then
  server_pid=""
  cat "$work_dir/supervisor.log" >&2
  cat "$work_dir/transcript.ndjson" >&2
  echo "supervisor smoke session failed" >&2
  exit 1
fi
server_pid=""

"$node_bin" -e '
const assert = require("node:assert/strict");
const fs = require("node:fs");
const readLines = (path) => fs.readFileSync(path, "utf8").trim().split(/\r?\n/).map((line) => JSON.parse(line));
const expected = ["read", "write", "edit", "bash"];
const transcript = readLines(process.argv[1]);
const results = readLines(process.argv[2]);
assert.equal(transcript.length, expected.length, "transcript must contain exactly four records");
assert.equal(results.length, expected.length, "adapter must print exactly four results");
for (let index = 0; index < expected.length; index += 1) {
  const tool = expected[index];
  const record = transcript[index];
  const result = results[index];
  assert.equal(record.proposal.tool, tool);
  assert.equal(record.proposal.tool_call_id, `smoke-${tool}`);
  assert.equal(record.correlation.accepted, true);
  assert.equal(record.correlation.reason_code, "matched");
  assert.equal(record.decision.verdict, "ALLOW");
  assert.equal(record.decision.reason_code, "policy_rule_allow");
  assert.equal(record.result.tool, tool);
  assert.equal(record.result.verdict, "ALLOW");
  assert.equal(record.result.sequence, index + 1);
  assert.equal(result.tool, tool);
  assert.equal(result.tool_call_id, `smoke-${tool}`);
  assert.equal(result.verdict, "ALLOW");
  assert.equal(result.sequence, index + 1);
}
' "$work_dir/transcript.ndjson" "$work_dir/client-results.ndjson"

echo "Linux cross-language IPC smoke passed (4 proposal/correlation/gate/result records)."
