#!/usr/bin/env bash
# Read-only inventory: the thesis-run assets that must be present in a checkout.
# This does not run the thesis, validate signatures, or claim readiness.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
required=(
  corpus/pilot/README.md
  corpus/pilot/build.py
  corpus/g1/README.md
  infra/conformance/evq-mapping.json
  infra/conformance/route-matrix.json
  infra/guest/PROVISIONING-SIGNING.md
  infra/guest/README.md
  infra/guest/serve-pi-service.smoke.json
  infra/guest/systemd/tbound-pi.service
  infra/guest/systemd/tbound-pi.socket
  packaging/governed/cell_image.py
  packaging/governed/sign_cell_image.py
  supervisor/cmd/tbound-profile/main_linux.go
  supervisor/cmd/tbound-run/main.go
  supervisor/cmd/tbound-doctor/main.go
  docs/governed-setup.md
  docs/status.md
  docs/reproducible-setup.md
)

pass=0
fail=0
for rel in "${required[@]}"; do
  if [[ -e "$ROOT/$rel" ]]; then
    printf 'PASS  %s\n' "$rel"
    pass=$((pass + 1))
  else
    printf 'FAIL  missing %s\n' "$rel" >&2
    fail=$((fail + 1))
  fi
done

# The setup guide must point at the owner-gated signing and corpus steps; a
# checkout without those links is not a complete setup handoff.
for needle in 'PROVISIONING-SIGNING.md' 'corpus/pilot' 'infra/conformance'; do
  if grep -Fq "$needle" "$ROOT/docs/reproducible-setup.md"; then
    printf 'PASS  setup guide references %s\n' "$needle"
    pass=$((pass + 1))
  else
    printf 'FAIL  setup guide does not reference %s\n' "$needle" >&2
    fail=$((fail + 1))
  fi
done

printf '\nthesis asset inventory: %d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
