#!/bin/bash
# Writes $W/session-env.sh (non-secret paths + IMGDIGEST read from $HF/IMGDIGEST), like F0 mkenv.sh. Run after 30-p3.sh.
# The cosign tool was already staged into $W/bin by 20-p1.sh.
set -euo pipefail
umask 077
. "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
load_env
logto session-env

[ -x "$W/bin/cosign" ] || die "no $W/bin/cosign (run 20-p1.sh)"
[ -x "$W/bin/tbound-profile" ] || die "no $W/bin/tbound-profile (run 20-p1.sh)"
[[ $(cat "$HF/IMGDIGEST") =~ ^sha256:[0-9a-f]{64}$ ]] || die "no valid $HF/IMGDIGEST (run 30-p3.sh)"
sha256sum "$W/bin/cosign"

cat > "$W/session-env.sh" <<EOT
# TBound $FREEZE signing session environment (non-secret). Source it: . $W/session-env.sh
umask 077
export W=$W B=$B H=$H HF=$HF
export F=\$(cat \$HF/FROZEN-COMMIT)
export COSIGN=\$W/bin/cosign TBP=\$W/bin/tbound-profile PATH=\$W/bin:\$PATH
D() { echo "sha256:\$(sha256sum "\$1" | cut -d' ' -f1)"; }
export IMGDIGEST=\$(cat \$HF/IMGDIGEST)
EOT
chmod 600 "$W/session-env.sh"
cat "$W/session-env.sh"
finish_step "$W/session-env.sh"
echo "== DONE session-env: $W/session-env.sh"
