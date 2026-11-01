#!/bin/bash
# P2 rebuild [READ-ONLY on the repo]: two offline builds from P1's frozen LF sources, measured with the frozen measurer.
set -euo pipefail
umask 077
. "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
load_env
logto 25-p2-rebuild

P=$W/p2-rebuild
[ ! -e "$P" ] || die "P2 rebuild already started in $P; refusing a second run"
for n in BUNDLE_DIGEST BUNDLE_TAR_PATH BUNDLE_TAR_DIGEST BUNDLE_TAR_SIZE P2-MODE; do
  [ ! -e "$HF/$n" ] || die "$HF/$n exists: P2 already ran"
done
[ -d "$W/src" ] && [ -x "$W/bin/tbound-profile" ] || die "run 20-p1.sh first"
expect_same "HEAD == frozen commit" "$(git -C "$REPO" rev-parse HEAD)" "$F"
check_prev_values
TBP=$W/bin/tbound-profile
cmp "$TBP" "$W/bin-pass2/tbound-profile" || die "tbound-profile pass1 != pass2"
expect_same "frozen measurer == P1 record" "$(D "$TBP")" "$(cat "$HF/TBP_DIGEST")"

# Pins: BR/manifest.sha256, F0 P2 setup and F0's installed Pi package/shrinkwrap listing.
BR=$TBOUND_LOCAL/assets/bundle-rebuild-20261017
NODE_TAR=$BR/inputs/node-v24.21.0-linux-x64.guest-copy.tar
expect_same "Node toolchain tar" "$(D "$NODE_TAR")" sha256:73a5a6994009445500e9f10e843b889469b24de79470724eeeb4ba388d0199e3
expect_same "F0 file-hash listing" "$(D "$A0/p2/listings/build1.sha256.txt")" sha256:0a9ce223732dcaba4a92ad98b8b49b3a6991dbe68b3fb460ccdbfe53e2466a73
unshare -Ucn true || die "private user/network namespace unavailable; no network fallback"
mkdir "$P"
mkdir -p "$P"/{src-check,tool,cache-master,out,mnt}
# Recreate the P1 LF archive only as a check; both builds below read $W/src.
git -C "$REPO" -c core.autocrlf=false -c core.eol=lf archive --format=tar "$F" supervisor scripts adapter/src adapter/package.json adapter/package-lock.json adapter/pi-runtime-package.json packaging infra corpus > "$P/frozen-src.tar"
tar -x -C "$P/src-check" -f "$P/frozen-src.tar"
diff -r --no-dereference "$P/src-check" "$W/src" || die "P1 sources differ from the frozen LF archive"
echo "OK P1 sources == frozen LF archive $F"
tar -x -C "$P/tool" -f "$NODE_TAR"
NODE_DIR=$P/tool/node-v24.21.0-linux-x64
NODE=$NODE_DIR/bin/node
expect_same "Node binary" "$(D "$NODE")" "$F0_NODE_DIGEST"
expect_same "Node version" "$("$NODE" --version)" v24.21.0
expect_same "npm version" "$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$NODE_DIR/lib/node_modules/npm/package.json")" 11.19.0
cp -a "$HOME/.npm/_cacache" "$P/cache-master/_cacache"
du -sh "$P/cache-master"

for pass in 1 2; do
  disk_preflight
  BUILD=$P/out/build$pass
  mkdir -p "$BUILD"/{home,tmp,npm-cache,pi-prefix}
  cp -a "$P/cache-master/_cacache" "$BUILD/npm-cache/_cacache"
  printf '# private offline build\n' > "$BUILD/npmrc-user"
  printf '# private offline build\n' > "$BUILD/npmrc-global"
  echo "== offline build $pass (private network namespace)"
  unshare -Ucn env -i PATH="$NODE_DIR/bin:/usr/bin:/bin" HOME="$BUILD/home" TMPDIR="$BUILD/tmp" \
    BUILD="$BUILD" SRC="$W/src" NODE="$NODE" \
    npm_config_cache="$BUILD/npm-cache" npm_config_userconfig="$BUILD/npmrc-user" npm_config_globalconfig="$BUILD/npmrc-global" \
    npm_config_offline=true npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false \
    bash -euo pipefail <<'BUILD'
umask 077
test "$(id -u)" -ne 0
npm cache verify --offline
cd "$BUILD/pi-prefix"
npm install --offline --prefix "$BUILD/pi-prefix" --ignore-scripts --no-audit --no-fund --save-exact @earendil-works/pi-coding-agent@0.87.1 </dev/null
npm ls --offline @earendil-works/pi-coding-agent --depth=0 --prefix "$BUILD/pi-prefix"
test -e node_modules/.bin/pi
test ! -e node_modules/@earendil-works/chord
printf '%s\n' \
  '627631b613ba4ca29eba8df793f5280fd20b19f01d73826e9ffda14c15def5dc  node_modules/@earendil-works/pi-coding-agent/package.json' \
  'e29f0b61463e48a81ca173eaeb462b0bdd03d04b863168895daae3816d62c470  node_modules/@earendil-works/pi-coding-agent/npm-shrinkwrap.json' | sha256sum -c -
python3 -I "$SRC/scripts/prepare_official_pi_runtime.py" \
  --pi "$BUILD/pi-prefix/node_modules/.bin/pi" --node "$NODE" --out "$BUILD/runtime-out" \
  --extension-dir "$SRC/adapter/src" --extension-package "$SRC/adapter/pi-runtime-package.json" </dev/null
sha256sum "$BUILD/runtime-out/closure-manifest.json" "$BUILD/runtime-out/closure-manifest.sha256"
cp -a "$BUILD/runtime-out" "$BUILD/runtime-src"
find "$BUILD/runtime-src" -type f -perm /111 -exec chmod 0555 {} +
find "$BUILD/runtime-src" -type f ! -perm /111 -exec chmod 0444 {} +
find "$BUILD/runtime-src" -type d -exec chmod 0555 {} +
( cd "$BUILD/runtime-src" && find . -printf '%y %m %s %p\n' | LC_ALL=C sort -k4 ) > "$BUILD/find.txt"
( cd "$BUILD/runtime-src" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum ) > "$BUILD/sha256.txt"
wc -l "$BUILD/find.txt" "$BUILD/sha256.txt"
BUILD
  # The external CLI closure must still be the pinned F0 one; only the frozen extension/builder may change.
  grep '  ./cli/' "$A0/p2/listings/build1.sha256.txt" > "$BUILD/f0-cli.sha256.txt"
  grep '  ./cli/' "$BUILD/sha256.txt" | cmp - "$BUILD/f0-cli.sha256.txt" || die "external Pi CLI closure differs from F0"
done

diff -r --no-dereference "$P/src-check" "$W/src" || die "frozen sources changed during P2"
cmp "$P/out/build1/find.txt" "$P/out/build2/find.txt" || die "build listings differ"
cmp "$P/out/build1/sha256.txt" "$P/out/build2/sha256.txt" || die "build file hashes differ"
diff -r --no-dereference "$P/out/build1/runtime-src" "$P/out/build2/runtime-src" || die "build bytes differ"
echo "OK build1 == build2: listings, hashes and bytes identical"

# Same read-only bind measurement as F0 P2/40-digest.sh; no sudo, mounts vanish on namespace exit.
for pass in 1 2; do
  M=$P/mnt/runtime-$pass
  mkdir "$M"
  unshare -rm bash -euo pipefail -c '
    mount --make-rprivate /
    mount --bind "$1" "$2"
    mount -o remount,bind,ro,nosuid,nodev "$2"
    findmnt -n -o TARGET,OPTIONS "$2"
    "$3" runtime-bundle-digest --root "$2"
  ' bash "$P/out/build$pass/runtime-src" "$M" "$TBP" | tee "$P/out/build$pass/measure.out"
  MEASURED=$(sed -n 's/^Pi runtime bundle tree digest: //p' "$P/out/build$pass/measure.out")
  [[ $MEASURED =~ ^sha256:[0-9a-f]{64}$ ]] || die "bad build$pass BUNDLE_DIGEST: $MEASURED"
  echo "$MEASURED" > "$P/out/build$pass/BUNDLE_DIGEST"
done
cmp "$P/out/build1/BUNDLE_DIGEST" "$P/out/build2/BUNDLE_DIGEST" || die "measured build digests differ"
BUNDLE_DIGEST=$(cat "$P/out/build1/BUNDLE_DIGEST")
note_f0 BUNDLE_DIGEST "$BUNDLE_DIGEST" "$F0_BUNDLE_DIGEST"
note_prev BUNDLE_DIGEST "$BUNDLE_DIGEST" "$PREV_BUNDLE_DIGEST"

disk_preflight
# Exact deterministic GNU tar recipe from F0 P2/50-package.sh.
mk() { tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -C "$1" -cf "$2" .; }
mk "$P/out/build1/runtime-src" "$P/out/b1.tar"
mk "$P/out/build2/runtime-src" "$P/out/b2.tar"
cmp "$P/out/b1.tar" "$P/out/b2.tar" || die "bundle tars differ"
echo "OK TAR REPRODUCIBLE: build1 == build2 (byte-identical)"
TAR=$P/runtime-bundle-${F:0:8}-lf.tar
mv "$P/out/b1.tar" "$TAR"
TARSHA=$(D "$TAR")
( cd "$P" && sha256sum "${TAR##*/}" > "${TAR##*/}.sha256" )
mkdir "$P/verify"
tar -x -C "$P/verify" -f "$TAR" --no-same-owner -p
( cd "$P/verify" && find . -printf '%y %m %s %p\n' | LC_ALL=C sort -k4 ) | cmp - "$P/out/build1/find.txt" || die "extraction listing differs"
( cd "$P/verify" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum ) | cmp - "$P/out/build1/sha256.txt" || die "extraction file hashes differ"
echo "OK extraction listing and content == build1"

# Five records, plus tar size and the explicit rebuild mode.
# A reuse rc=6 may have left the path lists; retain those only if they are identical.
git -C "$REPO" diff --name-only --no-ext-diff "$F0COMMIT" "$F" > "$P/out/CHANGED-PATHS-vs-F0.txt"
git -C "$REPO" diff --name-only --no-ext-diff "$PREV_COMMIT" "$F" > "$P/out/CHANGED-PATHS-vs-$PREV_FREEZE.txt"
for n in CHANGED-PATHS-vs-F0.txt CHANGED-PATHS-vs-$PREV_FREEZE.txt; do
  if [ -e "$HF/$n" ]; then cmp "$P/out/$n" "$HF/$n" || die "$HF/$n differs"; fi
done
expect_same "HEAD still frozen" "$(git -C "$REPO" rev-parse HEAD)" "$F"
disk_preflight
set -C
for n in CHANGED-PATHS-vs-F0.txt CHANGED-PATHS-vs-$PREV_FREEZE.txt; do
  if [ ! -e "$HF/$n" ]; then cat "$P/out/$n" > "$HF/$n"; fi
done
printf '%s\n' "$BUNDLE_DIGEST" > "$HF/BUNDLE_DIGEST"
printf '%s\n' "$TAR" > "$HF/BUNDLE_TAR_PATH"
printf '%s\n' "$TARSHA" > "$HF/BUNDLE_TAR_DIGEST"
stat -c %s "$TAR" > "$HF/BUNDLE_TAR_SIZE"
printf '%s\n' rebuild > "$HF/P2-MODE"
chmod 600 "$HF/BUNDLE_DIGEST" "$HF/BUNDLE_TAR_PATH" "$HF/BUNDLE_TAR_DIGEST" "$HF/BUNDLE_TAR_SIZE" "$HF/P2-MODE" "$HF/CHANGED-PATHS-vs-F0.txt" "$HF/CHANGED-PATHS-vs-$PREV_FREEZE.txt"
echo "TAR=$TAR $TARSHA ($(stat -c %s "$TAR") bytes)"
echo "== DONE 25-p2-rebuild: BUNDLE_DIGEST=$BUNDLE_DIGEST (two offline builds identical)"
finish_step "$TAR" "$TAR.sha256" "$P/out/build1/find.txt" "$P/out/build2/find.txt" "$P/out/build1/sha256.txt" "$P/out/build2/sha256.txt" "$P/out/build1/measure.out" "$P/out/build2/measure.out"
