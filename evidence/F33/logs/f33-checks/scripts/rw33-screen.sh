#!/bin/bash
# rw33-screen.sh <round R> <carrier commit>: the walk tools/screen.sh for the F33 re-witness (F33 home, F33 W, WSL dir f33-rewitness).
# Copies the round's inputs + journal to WSL, builds ev/ + corpus/ + construct-map.json, runs the frozen corpus-check with the
# guest evidence, copies the report back to <round dir>/screening-report/. Run after the collection scripts.
set -euo pipefail
R=${1:?round}; C=${2:?carrier commit}
M=/f/trust-boundary/.local/round-$R-f33
[ -f $M/runs/pilot/journal.jsonl ] && [ ! -e $M/screening-report ]
tar -C $M -c inputs runs/pilot run.json | MSYS_NO_PATHCONV=1 wsl.exe -d tbound-dev --cd / --exec bash -c "set -eu; T=/home/tbound/f33-rewitness/$R; [ ! -e \$T ]; mkdir -p \$T/win; tar -x -C \$T/win; echo \"copied \$(find \$T/win -type f | wc -l) files\""
MSYS_NO_PATHCONV=1 wsl.exe -d tbound-dev --cd / --exec bash -s -- "$R" "$C" <<'EOF' 2>&1 | tr -d '\r'
set -euo pipefail; umask 077
R=$1; C=$2
T=/home/tbound/f33-rewitness/$R; HF=/home/tbound/tbh/f33; K=$HF/pilot; WIN=$T/win
G=/home/tbound/tbound-work/f33-rw-carriers/corpus/sampling/carriers/$R
[ "$(git -C /home/tbound/tbound-work/f33-rw-carriers rev-parse HEAD)" = "$C" ]
grep -q "^$R $C " $K/CARRIER
[ ! -e "$T/corpus" ] && [ ! -e "$T/ev" ]
mkdir -p "$T/corpus"; cp -p "$K/manifest.json" "$T/corpus/manifest.json"; cmp "$K/manifest.json" "$WIN/inputs/manifest.json" && echo "manifest == runner input"
cp -rp "$G/sources.json" "$G/licenses" "$G/THIRD_PARTY_NOTICES.md" "$T/corpus/"; cp -rp "$WIN/inputs/artifacts" "$T/corpus/artifacts"
diff -r "$WIN/inputs/artifacts" "$G/artifacts" && echo "artifacts == carrier commit"
cp -p "$WIN/inputs/evq_results.json" "$WIN/inputs/route_report.json" "$T/corpus/"
mkdir -p "$T/ev/run" "$T/ev/signed"; cp -p "$WIN/runs/pilot/journal.jsonl" "$T/ev/run/"
for d in net-evidence ws-evidence proc-evidence; do [ -d "$WIN/runs/pilot/$d" ] && cp -rp "$WIN/runs/pilot/$d" "$T/ev/run/" || mkdir -p "$T/ev/run/$d"; done
cp -p "$HF/evq/netsink.json" "$T/ev/netsink.json"
cp -p "$K/d09-cell.json" "$T/ev/signed/cell-template.json"; cp -p "$K/pi-host-profile.signed.json" "$T/ev/signed/host-profile.json"
cp -p "$K/pi-host-profile.ed25519" "$T/ev/signed/host-profile.ed25519"; cp -p "$K/pi-host-profile.pub" "$T/ev/signed/admin.pub"
cp -p "$K/managed-live-root.json" "$T/ev/signed/registration.json"; cp -p "$K/managed-live-root.ed25519" "$T/ev/signed/registration.ed25519"
echo "admin.pub sha256:$(sha256sum "$T/ev/signed/admin.pub" | cut -d' ' -f1) pin $(cat $HF/ADMIN_PUB_DIGEST)"
cp -p "$WIN/run.json" "$T/run.json"
python3 -I - "$T/corpus/manifest.json" "$T/ev/construct-map.json" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
cases = {c["case_id"]: {"witness_id": c["adversarial"]["calibration_witness_id"]} for c in m["cases"] if c.get("kind") == "adversarial"}
doc = {"schema": "tbound-construct-map/v1", "journal": "run/journal.jsonl", "net_dir": "run/net-evidence", "ws_dir": "run/ws-evidence",
       "proc_dir": "run/proc-evidence", "net_sink": "netsink.json",
       "signed_run": {"template": "signed/cell-template.json", "host_profile": "signed/host-profile.json",
                      "host_profile_signature": "signed/host-profile.ed25519", "host_profile_key": "signed/admin.pub",
                      "registration": "signed/registration.json", "registration_signature": "signed/registration.ed25519"},
       "cases": cases}
open(sys.argv[2], "w").write(json.dumps(doc, indent=2) + "\n"); print(len(cases), "adversarial cases mapped")
PY
TBP=$(sed -n "s/^W=//p" /home/tbound/tbound-work/env.sh)/bin/tbound-profile; DIG=$(cat $HF/TBP_DIGEST); NODE=/home/tbound/toolchains/node-v24.21.0/bin/node
[ "sha256:$(sha256sum $TBP | cut -d' ' -f1)" = "$DIG" ] && echo "TBP == TBP_DIGEST"
[ "sha256:$(sha256sum $NODE | cut -d' ' -f1)" = "$(cat $HF/NODE_DIGEST)" ] && echo "node == NODE_DIGEST"
mkdir -p $T/tmp; chmod 700 $T/tmp; export TMPDIR=$T/tmp PATH=/home/tbound/toolchains/node-v24.21.0/bin:$PATH
exec 9>/home/tbound/.linux-line.lock; flock -w 7200 9
set +e
"$TBP" corpus-check --quota-profile pilot --manifest "$T/corpus/manifest.json" --node "$NODE" --out "$T/screening-report" --guest-evidence "$T/ev" --run-config "$T/run.json" --guest-binary-sha256 "$DIG" > "$T/screening.stdout" 2> "$T/screening.stderr"; echo "corpus-check exit=$?"
python3 -I -c "
import json; r=json.load(open('$T/screening-report/report.json'))
print('passed', r.get('passed'), 'set', [(c['name'],c['status']) for c in r['checks']])
for c in r['cases']:
    rows={x['name']:x['status'] for x in c['checks']}
    if c['case_id'].startswith(('PILOT-CMP', 'PILOT-AMB')): continue
    print(f\"{c['case_id']:52s} calibration-p={rows.get('calibration-p'):10s} effect-satisfiable={rows.get('effect-satisfiable')}\")
"
EOF
mkdir -p $M/screening-report && for f in report.json report.md; do MSYS_NO_PATHCONV=1 wsl.exe -d tbound-dev --cd / --exec cat /home/tbound/f33-rewitness/$R/screening-report/$f > $M/screening-report/$f; done
sha256sum $M/screening-report/report.json
