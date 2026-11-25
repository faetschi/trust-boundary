set -u
cd /home/tbound/tbound-work/f33-src/supervisor
export PATH=/home/tbound/toolchains/node-v24.21.0/bin:/home/tbound/toolchains/go1.27.1/bin:/usr/bin:/bin
R=/home/tbound/tbound-work/qq/tmp
mkdir -p $R
echo "root_len=$(printf %s "$R/" | wc -c) (new CF root /home/tbound/tbh/cf33/work/tmp/ is $(printf %s /home/tbound/tbh/cf33/work/tmp/ | wc -c))"
TMPDIR=$R GOFLAGS=-mod=mod go test -count=5 -run 'TestRelayCarriesBytesBothWaysAndDiesWithItsProcess' ./internal/vrun/ 2>&1 | tail -3
rm -rf /home/tbound/tbound-work/qq
