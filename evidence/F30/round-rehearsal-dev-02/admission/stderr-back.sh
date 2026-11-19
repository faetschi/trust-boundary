set -eu
S=/home/tbound/tbound-h/f30/evq-report; A=/home/tbound/tbound-work/round30b-aside/evq-report-empty-stderr
for f in adapter-capture.stderr route-report.stderr evq.stderr; do mv -n -- $A/$f $S/$f; stat -c '%n %s %y %a' $S/$f; done
ls -A $A | wc -l
