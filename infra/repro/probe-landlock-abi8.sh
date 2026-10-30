#!/usr/bin/env bash
# Read-only guest identity probe. Success requires the live syscall to return 8.
set -eu

kernel_release=$(uname -r)
machine=$(uname -m)
if [[ -r /etc/os-release ]]; then
  . /etc/os-release
  os_id=${ID:-unknown}
  os_version=${VERSION_ID:-unknown}
else
  os_id=unknown
  os_version=unknown
fi

printf 'schema=tbound-live-kernel-identity/v1\n'
printf 'kernel_release=%s\n' "$kernel_release"
printf 'machine=%s\n' "$machine"
printf 'os_id=%s\n' "$os_id"
printf 'os_version=%s\n' "$os_version"

python3 - <<'PY'
import ctypes
import platform
import sys

machine = platform.machine()
if sys.platform != "linux" or machine != "x86_64":
    print(f"landlock_abi=UNKNOWN machine={machine} platform={sys.platform}")
    raise SystemExit(2)

libc = ctypes.CDLL(None, use_errno=True)
libc.syscall.restype = ctypes.c_long
ctypes.set_errno(0)
abi = libc.syscall(444, None, ctypes.c_size_t(0), ctypes.c_uint(1))
if abi < 0:
    error = ctypes.get_errno()
    print(f"landlock_abi=UNKNOWN errno={error}")
    raise SystemExit(2)

print(f"landlock_abi={abi}")
if abi != 8:
    print("abi8_gate=FAIL")
    raise SystemExit(1)
print("abi8_gate=PASS")
PY
