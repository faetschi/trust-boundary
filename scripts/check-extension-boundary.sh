#!/bin/sh
# check-extension-boundary.sh: deterministic guard for the optional web chat
# extension (extensions/webchat). Pure POSIX sh and grep: no Node, no Go.
#
# The core (supervisor/) must build, test and run without the extension, and the
# extension may talk to the core only through its authenticated service contract.
# Rules, checked against the tree under --root:
#   a. no Go file under supervisor/ imports or names the extension;
#   b. supervisor/go.mod requires or replaces no tbound/webchat module;
#   c. the extension imports no tbound/* package except its own, and its go.mod
#      names no other tbound module;
#   d. no go.work file joins supervisor/ with the extension. Other go.work files
#      are reported, not failed.
#
# Usage:
#   sh scripts/check-extension-boundary.sh               check this repository
#   sh scripts/check-extension-boundary.sh --self-test   prove each rule fails on a violating copy
#   sh scripts/check-extension-boundary.sh --root DIR    check another tree
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
MODE=check
SELF=$0

usage() {
	echo "usage: $0 [--self-test] [--root DIR]" >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--self-test) MODE=self-test ;;
	--root)
		[ $# -ge 2 ] || usage
		ROOT=$2
		shift
		;;
	*) usage ;;
	esac
	shift
done

SCRATCH=$(mktemp -d "${TMPDIR:-/tmp}/extension-boundary.XXXXXX")
trap 'rm -rf "$SCRATCH"' EXIT

CORE_NAMES='tbound/webchat|extensions/webchat|tbound/supervisor/internal/webchat|cmd/tbound-webchat'

check_tree() {
	root=$1
	failures=0
	report() {
		echo "FAIL: $1" >&2
		failures=$((failures + 1))
	}

	if [ ! -d "$root/supervisor" ] || [ ! -d "$root/extensions/webchat" ]; then
		report "supervisor/ or extensions/webchat/ is missing under $root"
		return 1
	fi
	for gomod in "$root/supervisor/go.mod" "$root/extensions/webchat/go.mod"; do
		[ -f "$gomod" ] || report "$gomod is missing; the boundary cannot be checked"
	done

	# (a) core Go code must not import or name the extension.
	st=0
	grep -r -n --include='*.go' -E "$CORE_NAMES" "$root/supervisor" >"$SCRATCH/a" 2>/dev/null || st=$?
	[ "$st" -le 1 ] || report "(a) could not scan $root/supervisor"
	if [ -s "$SCRATCH/a" ]; then
		report "(a) core Go code imports or names the extension:"
		cat "$SCRATCH/a" >&2
	fi

	# (b) the core module must not depend on the extension.
	if grep -q 'tbound/webchat' "$root/supervisor/go.mod"; then
		report "(b) supervisor/go.mod requires or replaces tbound/webchat"
	fi

	# (c) the extension may import only its own tbound/* packages.
	st=0
	grep -r -n --include='*.go' -E '"tbound/' "$root/extensions/webchat" >"$SCRATCH/c-all" 2>/dev/null || st=$?
	[ "$st" -le 1 ] || report "(c) could not scan $root/extensions/webchat"
	grep -v -F '"tbound/webchat/' "$SCRATCH/c-all" >"$SCRATCH/c" || true
	if [ -s "$SCRATCH/c" ]; then
		report "(c) extension imports a core or foreign tbound package:"
		cat "$SCRATCH/c" >&2
	fi
	if grep -n 'tbound/' "$root/extensions/webchat/go.mod" | grep -v -E '^[0-9]+:module tbound/webchat[[:space:]]*$' >/dev/null; then
		report "(c) extensions/webchat/go.mod names a tbound module other than tbound/webchat"
	fi

	# (d) no workspace may join the core and the extension.
	find "$root" -name .git -prune -o -name go.work -type f -print >"$SCRATCH/work"
	while IFS= read -r work; do
		if grep -q -F 'supervisor' "$work" && grep -q -F 'extensions/webchat' "$work"; then
			report "(d) $work joins supervisor/ and extensions/webchat"
		else
			echo "note: $work exists and does not join supervisor/ with the extension"
		fi
	done <"$SCRATCH/work"

	if [ "$failures" -ne 0 ]; then
		echo "extension boundary: $failures rule(s) failed" >&2
		return 1
	fi
	echo "extension boundary OK: core imports no extension; extension imports no core package"
}

make_clean_tree() {
	d=$1
	mkdir -p "$d/supervisor/cmd/app" "$d/supervisor/internal/core" \
		"$d/extensions/webchat/cmd/tbound-webchat" "$d/extensions/webchat/internal/webchat"
	printf 'module tbound/supervisor\n\ngo 1.21\n' >"$d/supervisor/go.mod"
	printf 'package main\n\nfunc main() {}\n' >"$d/supervisor/cmd/app/main.go"
	printf 'package core\n' >"$d/supervisor/internal/core/core.go"
	printf 'module tbound/webchat\n\ngo 1.21\n' >"$d/extensions/webchat/go.mod"
	printf 'package main\n\nimport _ "tbound/webchat/internal/webchat"\n\nfunc main() {}\n' \
		>"$d/extensions/webchat/cmd/tbound-webchat/main.go"
	printf 'package webchat\n' >"$d/extensions/webchat/internal/webchat/webchat.go"
}

selftest_failures=0
selftest_cases=0

# run_case EXPECT LABEL DIR: EXPECT is pass (exit 0) or fail (exit 1).
run_case() {
	selftest_cases=$((selftest_cases + 1))
	st=0
	sh "$SELF" --root "$3" >"$SCRATCH/case-out" 2>&1 || st=$?
	case "$1:$st" in
	pass:0 | fail:1)
		echo "self-test ok: $2 ($1)"
		;;
	*)
		echo "self-test FAILED: $2 expected $1, exit $st" >&2
		sed 's/^/  /' "$SCRATCH/case-out" >&2
		selftest_failures=$((selftest_failures + 1))
		;;
	esac
}

self_test() {
	base="$SCRATCH/base"
	make_clean_tree "$base"
	run_case pass "clean tree" "$base"

	# Each violation is injected into a fresh copy of the clean tree.
	fresh() {
		rm -rf "$SCRATCH/case"
		mkdir -p "$SCRATCH/case"
		cp -R "$base/." "$SCRATCH/case/"
	}

	fresh
	printf 'package core\n\nimport _ "tbound/webchat/internal/webchat"\n' >"$SCRATCH/case/supervisor/internal/core/leak.go"
	run_case fail "(a) core imports the extension" "$SCRATCH/case"

	fresh
	printf 'package core\n\n// Mentions extensions/webchat directly.\n' >"$SCRATCH/case/supervisor/internal/core/note.go"
	run_case fail "(a) core names the extension" "$SCRATCH/case"

	fresh
	printf 'require tbound/webchat v0.0.0\n' >>"$SCRATCH/case/supervisor/go.mod"
	run_case fail "(b) core go.mod requires the extension" "$SCRATCH/case"

	fresh
	printf 'package webchat\n\nimport _ "tbound/supervisor/internal/core"\n' >"$SCRATCH/case/extensions/webchat/internal/webchat/leak.go"
	run_case fail "(c) extension imports a core package" "$SCRATCH/case"

	fresh
	printf 'require tbound/supervisor v0.0.0\n' >>"$SCRATCH/case/extensions/webchat/go.mod"
	run_case fail "(c) extension go.mod requires the core" "$SCRATCH/case"

	fresh
	printf 'go 1.21\n\nuse (\n\t./supervisor\n\t./extensions/webchat\n)\n' >"$SCRATCH/case/go.work"
	run_case fail "(d) go.work joins core and extension" "$SCRATCH/case"

	fresh
	printf 'go 1.21\n\nuse ./extensions/webchat\n' >"$SCRATCH/case/go.work"
	run_case pass "(d) go.work with the extension alone is reported, not failed" "$SCRATCH/case"

	if [ "$selftest_failures" -ne 0 ]; then
		echo "extension boundary self-test: $selftest_failures of $selftest_cases case(s) FAILED" >&2
		return 1
	fi
	echo "extension boundary self-test PASS: $selftest_cases of $selftest_cases cases"
}

if [ "$MODE" = self-test ]; then
	self_test
else
	check_tree "$ROOT"
fi
