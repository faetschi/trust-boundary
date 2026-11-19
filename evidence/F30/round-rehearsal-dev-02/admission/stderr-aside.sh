set -eu
S=/home/tbound/tbound-h/f30/evq-report; A=/home/tbound/tbound-work/round30b-aside/evq-report-empty-stderr
mkdir -p $A
for f in adapter-capture.stderr route-report.stderr evq.stderr; do
  [ -f $S/$f ] && [ ! -s $S/$f ] || { echo "not a 0-byte file: $S/$f"; exit 3; }
  stat -c '%n %s %y %a' $S/$f
  mv -n -- $S/$f $A/$f
done
ls -la $A
