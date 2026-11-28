import sys
p = "/etc/wsl.conf"
s = open(p).read()
line = "command = mountpoint -q /home/tbound/tbh || mount --bind /home/tbound/tbound-h /home/tbound/tbh"
if line in s:
    print("already"); sys.exit(0)
if "command" in s.split("[boot]", 1)[1]:
    print("FATAL: [boot] already has a command"); sys.exit(1)
open(p + ".pre-tbh", "w").write(s)
s = s.replace("[boot]\n", "[boot]\n" + line + "\n", 1)
open(p, "w").write(s)
print(open(p).read())
