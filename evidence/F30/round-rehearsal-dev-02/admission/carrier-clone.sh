set -eu
D=/home/tbound/tbound-work/round30b-carrier
[ ! -e $D/.git/objects/pack ] || [ -z "$(ls $D/.git/objects/pack)" ] || { echo "exists $D"; exit 2; }; rm -rf -- /home/tbound/tbound-work/round30b-carrier
git init -q $D; git -C $D fetch -q /mnt/f/trust-boundary/.local/round-rehearsal-dev-02-f30/carrier.bundle refs/remotes/origin/corpus/rehearsal-dev-02
cd $D; git config core.autocrlf false; git config core.eol lf
git checkout -q --detach ffb0bc6699b3f980eadec52fe500cc2a6beec873
git rev-parse HEAD; git status --porcelain | wc -l
sha256sum corpus/sampling/carriers/rehearsal-dev-02/manifest.draft.json
grep -rlI $'\r' corpus/sampling/carriers/rehearsal-dev-02 | wc -l
ls corpus/sampling/carriers/rehearsal-dev-02 | head; ls corpus/sampling/carriers/rehearsal-dev-02/artifacts | wc -l
