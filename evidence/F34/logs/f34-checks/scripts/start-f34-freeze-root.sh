set -euo pipefail
NH=/home/tbound/tbh
[ -e $NH ] || install -d -o tbound -g tbound -m 0700 $NH
mountpoint -q $NH || mount --bind /home/tbound/tbound-h $NH
[ "$(stat -c %i $NH)" = "$(stat -c %i /home/tbound/tbound-h)" ] || { echo "FATAL tbh"; exit 1; }
echo "tbh bound: $(findmnt -no SOURCE $NH)"
su tbound -s /bin/bash -c 'bash /mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/300b9e68-97e7-4a01-aa27-ad0df38df07f/scratchpad/f31w/start-f34-freeze.sh'
