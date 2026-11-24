#!/bin/sh
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
MODE=check
SELF=$0
BASELINE_NAME=scripts/comment-baseline.tsv

usage() {
	echo "usage: $0 [--help] [--update] [--init] [--self-test] [--root DIR]" >&2
	echo "  (no flag)   check: comment counts must not exceed $BASELINE_NAME," >&2
	echo "              and a file without a baseline entry must be comment-free" >&2
	echo "  --update    ratchet the baseline down to current counts; refuses any" >&2
	echo "              increase and any new file that arrives with comments" >&2
	echo "  --init      bootstrap $BASELINE_NAME from the current tree (once)" >&2
	echo "  --self-test prove each rule fails on a violating scratch tree" >&2
	echo "  --root DIR  check another tree" >&2
	echo "  corpus/, evidence/, dist/ and node_modules/ are not counted." >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--help) usage ;;
	--update) MODE=update ;;
	--init) MODE=init ;;
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

BASELINE="$ROOT/$BASELINE_NAME"
SCRATCH=$(mktemp -d "${TMPDIR:-/tmp}/comment-ratchet.XXXXXX")
trap 'rm -rf "$SCRATCH"' EXIT

PROG="$SCRATCH/count.awk"
cat >"$PROG" <<'AWK'
FNR == 1 {
	if (counted) print path "\t" counted
	path = FILENAME
	sub(/^\.\//, "", path)
	counted = 0
	shebang = ($0 ~ /^#!/)
}
FILENAME ~ /\.(go|ts|tsx|js|mjs|css)$/ {
	if ($0 ~ /^[[:space:]]*\/\//) {
		if ($0 !~ /^[[:space:]]*\/\/go:/) counted++
	} else if ($0 ~ /[[:space:]]\/\// || $0 ~ /\/\*|\*\//) {
		counted++
	}
	next
}
FILENAME ~ /\.(sh|py|ps1)$/ || FILENAME ~ /(^|\/)Makefile$/ {
	if (FNR == 1 && shebang) next
	if ($0 ~ /^[[:space:]]*#/) counted++
	next
}
END {
	if (counted) print path "\t" counted
}
AWK

CMP="$SCRATCH/compare.awk"
cat >"$CMP" <<'AWK'
NR == FNR { base[$1] = $2; next }
{
	if ($1 in base) {
		seen[$1] = 1
		if ($2 > base[$1]) {
			printf "FAIL: %s grew from %d to %d comment lines\n", $1, base[$1], $2
			bad++
		}
	} else if ($2 > 0) {
		printf "FAIL: %s has %d comment line(s) and no baseline entry\n", $1, $2
		bad++
	}
}
END {
	for (p in base)
		if (!(p in seen)) printf "note: baseline entry %s has no file; --update prunes it\n", p
	exit (bad ? 1 : 0)
}
AWK

UPD="$SCRATCH/update.awk"
cat >"$UPD" <<'AWK'
NR == FNR { base[$1] = $2; next }
{
	if ($1 in base) {
		if ($2 > base[$1]) {
			printf "REFUSED: %s grew from %d to %d comment lines\n", $1, base[$1], $2
			bad++
		}
	} else if ($2 > 0) {
		printf "REFUSED: new file %s arrives with %d comment line(s); delete them first\n", $1, $2
		bad++
	}
}
END { exit (bad ? 1 : 0) }
AWK

count_tree() {
	root=$1
	dest=$2
	(
		cd "$root" &&
			find . \( -name .git -o -name corpus -o -name evidence -o -name node_modules -o -name dist \) -prune -o \
				-type f \( -name '*.go' -o -name '*.ts' -o -name '*.tsx' -o -name '*.js' -o -name '*.mjs' -o -name '*.css' -o -name '*.sh' -o -name '*.py' -o -name '*.ps1' -o -name Makefile \) -exec awk -f "$PROG" {} + |
			LC_ALL=C sort
	) >"$dest"
}

total_of() {
	awk '{ s += $2 } END { printf "%d\n", s + 0 }' "$1"
}

check_tree() {
	root=$1
	if [ ! -f "$BASELINE" ]; then
		echo "FAIL: $BASELINE_NAME is missing; bootstrap it with: sh $0 --init" >&2
		return 1
	fi
	count_tree "$root" "$SCRATCH/counts"
	st=0
	awk -f "$CMP" "$BASELINE" "$SCRATCH/counts" >"$SCRATCH/report" || st=$?
	if [ "$st" -ne 0 ]; then
		cat "$SCRATCH/report" >&2
		echo "comment ratchet: counts may not grow; delete comments or fix the tree" >&2
		return 1
	fi
	grep '^note:' "$SCRATCH/report" || true
	echo "comment ratchet OK: no file exceeds scripts/comment-baseline.tsv"
}

update_tree() {
	root=$1
	if [ ! -f "$BASELINE" ]; then
		echo "FAIL: $BASELINE_NAME is missing; bootstrap it with: sh $0 --init" >&2
		return 1
	fi
	count_tree "$root" "$SCRATCH/counts"
	st=0
	awk -f "$UPD" "$BASELINE" "$SCRATCH/counts" >"$SCRATCH/upd-report" || st=$?
	if [ "$st" -ne 0 ]; then
		cat "$SCRATCH/upd-report" >&2
		echo "ratchet update refused: the tree above would grow the comment mass" >&2
		return 1
	fi
	before=$(total_of "$BASELINE")
	cp "$SCRATCH/counts" "$BASELINE"
	after=$(total_of "$BASELINE")
	echo "ratchet updated: $before -> $after comment lines"
}

init_tree() {
	root=$1
	count_tree "$root" "$BASELINE"
	echo "baseline written: $(total_of "$BASELINE") comment lines across $(wc -l <"$BASELINE" | tr -d ' ') files"
}

make_base_tree() {
	d=$1
	mkdir -p "$d/scripts" "$d/supervisor/internal/demo" "$d/corpus"
	printf '//go:build linux\n\npackage demo\n\n// Doc keeps its line.\nfunc A() {}\n\nfunc B() {} // trailing note\n' \
		>"$d/supervisor/internal/demo/demo.go"
	printf '#!/bin/sh\necho hi\n# one comment\n' >"$d/scripts/tool.sh"
	printf '#!/usr/bin/env python3\nprint("hi")\n' >"$d/scripts/app.py"
	printf 'all:\n\t@echo hi\n# makefile note\n' >"$d/Makefile"
	printf 'package corpus\n\n// one\n// two\n// three\n' >"$d/corpus/data.go"
	printf 'supervisor/internal/demo/demo.go\t2\nscripts/tool.sh\t1\nMakefile\t1\n' >"$d/scripts/comment-baseline.tsv"
}

selftest_failures=0
selftest_cases=0

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

run_update_case() {
	selftest_cases=$((selftest_cases + 1))
	st=0
	sh "$SELF" --update --root "$3" >"$SCRATCH/case-out" 2>&1 || st=$?
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
	make_base_tree "$base"
	run_case pass "clean tree" "$base"

	fresh() {
		rm -rf "$SCRATCH/case"
		mkdir -p "$SCRATCH/case"
		cp -R "$base/." "$SCRATCH/case/"
	}

	fresh
	printf '\n// an added comment\n' >>"$SCRATCH/case/supervisor/internal/demo/demo.go"
	run_case fail "(a) baselined file gains a comment" "$SCRATCH/case"

	fresh
	printf 'package demo\n\n// arrives unbaselined\nfunc C() {}\n' >"$SCRATCH/case/supervisor/internal/demo/extra.go"
	run_case fail "(b) new file arrives with comments" "$SCRATCH/case"

	fresh
	printf '//go:build linux\n\npackage demo\n\nfunc D() {}\n' >"$SCRATCH/case/supervisor/internal/demo/empty.go"
	run_case pass "(b) new file with directives only is clean" "$SCRATCH/case"

	fresh
	printf '\n# another note\n' >>"$SCRATCH/case/Makefile"
	run_case fail "(a) Makefile gains a comment" "$SCRATCH/case"

	fresh
	printf 'package corpus\n\n// four\n' >>"$SCRATCH/case/corpus/data.go"
	run_case pass "(d) corpus is not counted" "$SCRATCH/case"

	fresh
	sed -i 's| // trailing note||' "$SCRATCH/case/supervisor/internal/demo/demo.go"
	run_case pass "a deletion may sit below the baseline" "$SCRATCH/case"

	fresh
	sed -i 's| // trailing note||' "$SCRATCH/case/supervisor/internal/demo/demo.go"
	run_update_case pass "(c) --update accepts the lower count" "$SCRATCH/case"
	run_case pass "(c) the lowered baseline then checks clean" "$SCRATCH/case"

	fresh
	printf '\n// an added comment\n' >>"$SCRATCH/case/supervisor/internal/demo/demo.go"
	run_update_case fail "(c) --update refuses an increase" "$SCRATCH/case"

	if [ "$selftest_failures" -ne 0 ]; then
		echo "comment ratchet self-test: $selftest_failures of $selftest_cases case(s) failed" >&2
		return 1
	fi
	echo "comment ratchet self-test: $selftest_cases case(s) ok"
}

case "$MODE" in
check) check_tree "$ROOT" ;;
update) update_tree "$ROOT" ;;
init) init_tree "$ROOT" ;;
self-test) self_test ;;
esac
