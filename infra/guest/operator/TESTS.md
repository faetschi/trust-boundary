# tbound-op test record (pre-Z1, real guest)

Run: 2026-10-21T06:39:54Z, guest 172.29.201.195, by `run-tests.sh` (same directory). Every verb under test is called as `sudo -n /opt/tbound/operator/tbound-op ...` by tboundadmin, the way it is called after Z1'. Pre-Z1 sudo appears only in rows marked `setup` or `inspect` (staging files, reading root-only state).

**Result: 160 passed, 0 failed, 160 total.**

Not run on purpose: `client-run smoke` (would spend the one-use smoke admission), `negative n6` and `negative n7` (only their refusal before the smoke run is tested), `guest-evidence capability|netsink` (would write evidence artifacts / touch the net sink), `netsink-rotate` (moves the sink journal), the positive paths of `cfg-install|reg-measure|reg-install|prof-install` for g1/rp/dr/pilot (they need owner signatures; the smoke chain exercises the same code), `abandon` (needs a request id the dispatcher generated).

### 0. Baseline

#### 0.1-status  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status`

Expected: rc=0, output ~ /anchor: ok.*service=active socket=active/. Got rc=0.

```
tbound-op status 2026-10-21T06:37:59Z pins F=b42b920e9ad3f70e0082aa26d26fe4a27df64e4e
anchor: ok (pi-host-profile.pub sha256:be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac)
runner: sha256:5467398f606bddae tbound-profile: sha256:444713b350035bf5
service=active socket=active
installed: cfg sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc reg sha256:4b99e9d96010ecb8 prof sha256:cf766b7b23f1bbe8 tpl sha256:cf97920f5549a43b
installed config matches state of: smoke
-- smoke:
     cfg=sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc
     prof=sha256:c13a0d111b726f7c3d0080caae5ef082a3f760e06db499bca79273cef6da2222
     profjson=sha256:cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210
     profsig=sha256:4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112
     reg=sha256:271e0e446821ee854949621bd6f3488fa05688a0b9650653105c856e9bcd421b
... (27 lines)
```

#### 0.2-adopt-smoke-idempotent  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op adopt smoke`

Expected: rc=0, output ~ /adopt smoke ok.*state started/. Got rc=0.

```
cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43  d09-cell.json
2bb77f0b3d2ecacb2799e0f8e3f3a8c891ba4173a0fee53cf24a8ac01466d6a9  managed-live-root.ed25519
4b99e9d96010ecb83e71faffc330d53ec9f4420242b512d09ce1c7714e54fee2  managed-live-root.json
4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112  pi-host-profile.ed25519
cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210  pi-host-profile.json
be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac  pi-host-profile.pub
a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc  serve-pi-service.json
adopt smoke ok: cfg sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc reg sha256:271e0e446821ee854949621bd6f3488fa05688a0b9650653105c856e9bcd421b profile sha256:c13a0d111b726f7c3d0080caae5ef082a3f760e06db499b
```

### 1. Read-only verbs (diag, collect)

#### 1.diag-l2  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag l2`

Expected: rc=0, output ~ /-/. Got rc=0.

```
== service cgroup
cells
runner
tbound-pi:tbound-pi 755
domain
cpu memory pids
-- runner procs
279672
-- cgroup-root-digest (profile: sha256:8c64df4047d2e49832d3fd5334c06eca57a9812c2bb374c26ab8a947ac2e03ce)
sha256:8c64df4047d2e49832d3fd5334c06eca57a9812c2bb374c26ab8a947ac2e03ce
== mounts
/usr/local/bin/tbound ro,nosuid,nodev,relatime
... (19 lines)
```

#### 1.diag-mounts  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag mounts`

Expected: rc=0, output ~ /-/. Got rc=0.

```
/usr/local/bin/tbound ro,nosuid,nodev,relatime
/usr/local/bin/node ro,nosuid,nodev,relatime
/usr/bin/podman ro,nosuid,nodev,relatime
/usr/bin/crun ro,nosuid,nodev,relatime
/opt/tbound/cell ro,nosuid,nodev,relatime
/var/lib/tbound/runtime ro,nosuid,nodev,relatime
/var/lib/tbound/live rw,nosuid,nodev,relatime
```

#### 1.diag-census-live  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag census-live`

Expected: rc=0, output ~ /-/. Got rc=0.

```
{"dev": 1792, "gid": 986, "ino": 12, "kind": "file", "mode": "0o644", "path": "SMOKE-README.txt", "uid": 995}
{"dev": 1792, "gid": 986, "ino": 11, "kind": "dir", "mode": "0o755", "path": "lost+found", "uid": 995}
{"counts": {"dir": 2, "file": 1, "other": 0, "symlink": 0}, "cross_device_pruned": [], "entries": 3, "errors": {"getxattr": 0, "listxattr": 0, "lstat": [], "readlink": 0, "scandir": []}, "record": "summary", "root": "/var/lib/tbou
```

#### 1.diag-podman-ps  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag podman-ps`

Expected: rc=0, output ~ /-/. Got rc=0.

```
time="2026-10-21T06:38:03Z" level=error msg="cannot find UID/GID for user tbound-pi: no subuid ranges found for user \"tbound-pi\" in /etc/subuid - check rootless mode in man pages."
time="2026-10-21T06:38:03Z" level=warning msg="Using rootless single mapping into the namespace. This might break some images. Check /etc/subuid and /etc/subgid for adding sub*ids if not using a network user"
CONTAINER ID  IMAGE       COMMAND     CREATED     STATUS      PORTS       NAMES
time="2026-10-21T06:38:03Z" level=warning msg="Failed to add pause process to systemd sandbox cgroup: dbus: couldn't determine address of session bus"
```

#### 1.diag-podman-ps-noreap  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag podman-ps-noreap`

Expected: rc=0, output ~ /-/. Got rc=0.

```
CONTAINER ID  IMAGE       COMMAND     CREATED     STATUS      PORTS       NAMES
```

#### 1.diag-nft-sink  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag nft-sink`

Expected: rc=0, output ~ /-/. Got rc=0.

```
Error: No such file or directory
list table inet tbound_net_sink
                ^^^^^^^^^^^^^^^
-- attest
cat: /run/tbound/net-sink.attest: No such file or directory
```

#### 1.diag-probe-capabilities  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag probe-capabilities`

Expected: rc=0, output ~ /-/. Got rc=0.

```
Read-only guest capability probe
Observed: kernel=7.0.0-38-generic arch=x86_64 uid=0 user=root
PASS         cgroup-v2: mounted at /sys/fs/cgroup
PASS         cgroup-domain: /sys/fs/cgroup/user.slice/user-1000.slice/session-4205.scope/cgroup.type reports domain
PASS         cgroup.kill-interface: write-only kernel control is present at /sys/fs/cgroup/user.slice/user-1000.slice/session-4205.scope/cgroup.kill
UNVERIFIED   cgroup.kill-write: write permission appears available; behavior is untested because writing kills processes
PASS         pidfd: pidfd_open(current probe process) succeeded and the descriptor was closed
PASS         landlock-abi: landlock_create_ruleset VERSION query returned ABI 8
PASS         d09-landlock-profile: registered D09 profile requires exactly Landlock ABI 8
PASS         seccomp-filter: disposable child installed a filter and reported Seccomp: 2
FAIL         subuid: no range of at least 65536 IDs is configured for root
FAIL         subgid: no range of at least 65536 IDs is configured for root
... (19 lines)
```

#### 1.diag-image-probe  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag image-probe`

Expected: rc=0, output ~ /-/. Got rc=0.

```
exists=0
75493d8d65a7a1e95b511d0342bf10090430246000a907e7a36ba91300012fba sha256:485c99a8c9f597ac0eca0d9e9a5e8e0086355342f9241e9f9147aca04b63b339 localhost/tbound/pi-cell@sha256:485c99a8c9f597ac0eca0d9e9a5e8e0086355342f9241e9f9147aca04b63b
pins: CFG=sha256:75493d8d65a7a1e95b511d0342bf10090430246000a907e7a36ba91300012fba IMGDIGEST=sha256:485c99a8c9f597ac0eca0d9e9a5e8e0086355342f9241e9f9147aca04b63b339
```

#### 1.diag-live-listing  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag live-listing`

Expected: rc=0, output ~ /-/. Got rc=0.

```
d 755 995:986 lost+found
f 644 995:986 SMOKE-README.txt
```

#### 1.diag-netsink-journal-size  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag netsink-journal-size`

Expected: rc=0, output ~ /-/. Got rc=0.

```
23725 bytes tbound-netsink:tbound-netsink 600 /var/lib/tbound-netsink/journal.jsonl
```

#### 1.diag-l2-content  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag l2`

Expected: rc=0, output ~ /domain/. Got rc=0.

```
== service cgroup
cells
runner
tbound-pi:tbound-pi 755
domain
cpu memory pids
-- runner procs
279672
-- cgroup-root-digest (profile: sha256:8c64df4047d2e49832d3fd5334c06eca57a9812c2bb374c26ab8a947ac2e03ce)
sha256:8c64df4047d2e49832d3fd5334c06eca57a9812c2bb374c26ab8a947ac2e03ce
== mounts
/usr/local/bin/tbound ro,nosuid,nodev,relatime
... (19 lines)
```

#### 1.diag-image-probe-content  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag image-probe`

Expected: rc=0, output ~ /exists=0/. Got rc=0.

```
exists=0
75493d8d65a7a1e95b511d0342bf10090430246000a907e7a36ba91300012fba sha256:485c99a8c9f597ac0eca0d9e9a5e8e0086355342f9241e9f9147aca04b63b339 localhost/tbound/pi-cell@sha256:485c99a8c9f597ac0eca0d9e9a5e8e0086355342f9241e9f9147aca04b63b
pins: CFG=sha256:75493d8d65a7a1e95b511d0342bf10090430246000a907e7a36ba91300012fba IMGDIGEST=sha256:485c99a8c9f597ac0eca0d9e9a5e8e0086355342f9241e9f9147aca04b63b339
```

#### 1.collect-requests  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op collect requests`

Expected: rc=0, output ~ /collected requests/. Got rc=0.

```
collected requests (0 bytes)
```

#### 1.collect-catalog  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op collect catalog`

Expected: rc=0, output ~ /collected catalog/. Got rc=0.

```
collected catalog (0 bytes)
```

#### 1.collect-l4  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op collect l4`

Expected: rc=0, output ~ /cgroup/. Got rc=0.

```
== 2026-10-21T06:38:10Z cgroup
cells
runner
-- cells
cgroup.controllers
cgroup.events
cgroup.freeze
cgroup.kill
cgroup.max.depth
cgroup.max.descendants
cgroup.pressure
cgroup.procs
... (70 lines)
```

#### 1.collect-evidence-out  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op collect evidence-out`

Expected: rc=0, output ~ /-/. Got rc=0.

```
collected out-procoracle.json
```

#### 1.collect-audit-nonexistent  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op collect audit NOSUCHRUN1`

Expected: rc=1, output ~ /no /var/lib/tbound/audit/NOSUCHRUN1-session-audit.jsonl/. Got rc=1.

```
tbound-op: no /var/lib/tbound/audit/NOSUCHRUN1-session-audit.jsonl
```

#### 1.out-dir-perms  PASS

Command: `stat -c "%U %G %a" /var/tmp/tbound-op /var/tmp/tbound-op/_ | sort -u`

Expected: rc=0, output ~ /root tboundadmin 750/. Got rc=0.

```
root tboundadmin 750
```

#### 1.out-file-perms  PASS

Command: `stat -c "%U %G %a" /var/tmp/tbound-op/_/l4.txt`

Expected: rc=0, output ~ /root tboundadmin 640/. Got rc=0.

```
root tboundadmin 640
```

#### 1.log-perms  PASS

Command: `stat -c "%U %G %a" /var/log/tbound-op.log`

Expected: rc=0, output ~ /root adm 640/. Got rc=0.

```
root adm 640
```

### 2. Argument hardening (service active)

#### 2.no-verb  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op`

Expected: rc=2, output ~ /no verb/. Got rc=2.

```
tbound-op: no verb
```

#### 2.bad-verb  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op bogus`

Expected: rc=2, output ~ /unknown verb/. Got rc=2.

```
tbound-op: unknown verb
```

#### 2.dotdot-slash  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status ../x`

Expected: rc=2, output ~ /allowed alphabet/. Got rc=2.

```
tbound-op: argument outside the allowed alphabet
```

#### 2.space  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status a\ b`

Expected: rc=2, output ~ /allowed alphabet/. Got rc=2.

```
tbound-op: argument outside the allowed alphabet
```

#### 2.newline  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status $'smoke\nstatus'`

Expected: rc=2, output ~ /allowed alphabet/. Got rc=2.

```
tbound-op: argument outside the allowed alphabet
```

#### 2.semicolon  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag l2\;id`

Expected: rc=2, output ~ /allowed alphabet/. Got rc=2.

```
tbound-op: argument outside the allowed alphabet
```

#### 2.dollar  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag \$\(id\)`

Expected: rc=2, output ~ /allowed alphabet/. Got rc=2.

```
tbound-op: argument outside the allowed alphabet
```

#### 2.empty-arg  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status ''`

Expected: rc=2, output ~ /allowed alphabet/. Got rc=2.

```
tbound-op: argument outside the allowed alphabet
```

#### 2.slash-path  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op collect audit /etc/passwd`

Expected: rc=2, output ~ /allowed alphabet/. Got rc=2.

```
tbound-op: argument outside the allowed alphabet
```

#### 2.extra-args-status  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status smoke extra`

Expected: rc=2, output ~ /-/. Got rc=2.

```
tbound-op: status [NAME]
```

#### 2.extra-args-diag  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag l2 extra`

Expected: rc=2, output ~ /wrong number/. Got rc=2.

```
tbound-op: diag: wrong number of arguments
```

#### 2.missing-args  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op cfg-install g1`

Expected: rc=2, output ~ /wrong number/. Got rc=2.

```
tbound-op: cfg-install: wrong number of arguments
```

#### 2.bad-name  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op cfg-install nope 0000000000000000000000000000000000000000000000000000000000000000`

Expected: rc=2, output ~ /bad NAME/. Got rc=2.

```
tbound-op: bad NAME
```

#### 2.bad-sha  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op cfg-install g1 zz`

Expected: rc=2, output ~ /bad SHA/. Got rc=2.

```
tbound-op: bad SHA
```

#### 2.bad-diag-kind  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag nosuch`

Expected: rc=2, output ~ /diag l2/. Got rc=2.

```
tbound-op: diag l2|mounts|census-live|podman-ps|podman-ps-noreap|nft-sink|probe-capabilities|image-probe|live-listing|netsink-journal-size
```

#### 2.bad-runid  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op collect audit short`

Expected: rc=2, output ~ /bad RUNID/. Got rc=2.

```
tbound-op: bad RUNID
```

#### 2.adopt-g1  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op adopt g1`

Expected: rc=2, output ~ /smoke only/. Got rc=2.

```
tbound-op: adopt supports smoke only
```

#### 2.seed-pilot  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op seed-live pilot`

Expected: rc=2, output ~ /seed-live smoke/. Got rc=2.

```
tbound-op: seed-live smoke|g1
```

#### 2.client-run-pilot  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op client-run pilot`

Expected: rc=2, output ~ /client-run smoke/. Got rc=2.

```
tbound-op: client-run smoke|g1|rp|dr
```

#### 2.abandon-bad-rid  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op abandon smoke-req-1`

Expected: rc=2, output ~ /bad RID/. Got rc=2.

```
tbound-op: bad RID
```

#### 2.abandon-foreign-rid  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op abandon smoke-req-20260101T000000Z`

Expected: rc=1, output ~ /not generated by this dispatcher/. Got rc=1.

```
tbound-op: smoke-req-20260101T000000Z was not generated by this dispatcher
```

#### 2.dr-extra-template-arg  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op prof-install smoke 0000000000000000000000000000000000000000000000000000000000000000 0000000000000000000000000000000000000000000000000000000000000000 0000000000000000000000000000000000000000000000000000000000000000 0000000000000000000000000000000000000000000000000000000000000000`

Expected: rc=2, output ~ /only dr/. Got rc=2.

```
tbound-op: only dr takes SHA_PRODTEMPLATE
```

#### 2.env-BASH_ENV  PASS

Command: `rm -f /tmp/evil-marker; printf "touch /tmp/evil-marker\n" > /tmp/evil.sh; sudo -n env BASH_ENV=/tmp/evil.sh ENV=/tmp/evil.sh /opt/tbound/operator/tbound-op status smoke | head -1; test ! -e /tmp/evil-marker && echo marker-absent; rm -f /tmp/evil.sh`

Expected: rc=0, output ~ /tbound-op status/. Got rc=0.

```
tbound-op status 2026-10-21T06:38:26Z pins F=b42b920e9ad3f70e0082aa26d26fe4a27df64e4e
marker-absent
```

#### 2.env-marker-absent  PASS

Command: `rm -f /tmp/evil-marker; printf "touch /tmp/evil-marker\n" > /tmp/evil.sh; BASH_ENV=/tmp/evil.sh PATH=/tmp:$PATH sudo -n /opt/tbound/operator/tbound-op status smoke >/dev/null 2>&1; test ! -e /tmp/evil-marker && echo marker-absent; rm -f /tmp/evil.sh`

Expected: rc=0, output ~ /marker-absent/. Got rc=0.

```
marker-absent
```

#### 2.sudoers-visudo  PASS

Command: `sudo -n visudo -c 2>&1; sudo -n stat -c "%U:%G %a %n" /etc/sudoers.d/50-tbound-op`

Expected: rc=0, output ~ /parsed OK/. Got rc=0.

```
/etc/sudoers: parsed OK
/etc/sudoers.d/50-tbound-op: parsed OK
/etc/sudoers.d/90-tbound-tboundadmin: parsed OK
/etc/sudoers.d/README: parsed OK
root:root 440 /etc/sudoers.d/50-tbound-op
```

#### 2.installed-modes  PASS

Command: `stat -c "%U:%G %a %n" /opt/tbound/operator /opt/tbound/operator/tbound-op /opt/tbound/operator/pins.env; ls -ld /opt/tbound/operator | head -0`

Expected: rc=0, output ~ /root:root 755 /opt/tbound/operator/tbound-op/. Got rc=0.

```
root:root 755 /opt/tbound/operator
root:root 755 /opt/tbound/operator/tbound-op
root:root 444 /opt/tbound/operator/pins.env
```

### 3. Refusals while the service is active

#### 3.cfg-install-active  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op cfg-install smoke a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc`

Expected: rc=1, output ~ /must be inactive/. Got rc=1.

```
tbound-op: tbound-pi.service and .socket must be inactive (svc stop first)
```

#### 3.rollback-active  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op rollback smoke prof`

Expected: rc=1, output ~ /must be inactive/. Got rc=1.

```
tbound-op: tbound-pi.service and .socket must be inactive (svc stop first)
```

#### 3.reg-measure-active  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op reg-measure smoke`

Expected: rc=1, output ~ /must be inactive/. Got rc=1.

```
tbound-op: tbound-pi.service and .socket must be inactive (svc stop first)
```

#### 3.reg-install-active  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op reg-install smoke a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc`

Expected: rc=1, output ~ /must be inactive/. Got rc=1.

```
tbound-op: tbound-pi.service and .socket must be inactive (svc stop first)
```

#### 3.prof-install-active  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op prof-install smoke a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc`

Expected: rc=1, output ~ /must be inactive/. Got rc=1.

```
tbound-op: tbound-pi.service and .socket must be inactive (svc stop first)
```

#### 3.client-run-g1-not-installed  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op client-run g1`

Expected: rc=1, output ~ /not g1's/. Got rc=1.

```
tbound-op: g1: installed config is not g1's, or g1 is not in state started (adopt/svc start)
```

#### 3.client-run-dr-not-installed  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op client-run dr`

Expected: rc=1, output ~ /not dr's/. Got rc=1.

```
tbound-op: dr: installed config is not dr's, or dr is not in state started (adopt/svc start)
```

#### 3.negative-n6-before-smoke-run  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op negative n6`

Expected: rc=1, output ~ /after the smoke client-run/. Got rc=1.

```
tbound-op: n6 runs after the smoke client-run, with the smoke config installed
```

#### 3.negative-n7-before-smoke-run  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op negative n7`

Expected: rc=1, output ~ /after the smoke client-run/. Got rc=1.

```
tbound-op: n7 runs after the smoke client-run, with the smoke config installed
```

#### 3.guest-evidence-no-g1-archive  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op guest-evidence provider-exchange`

Expected: rc=1, output ~ /missing/. Got rc=1.

```
tbound-op: /root/ga/g1/pi-host-profile.json missing (prof-install g1)
```

### 4. Stop window: staged-file hardening, install paths, rollback, adopt, seed-live (service stopped, then restarted)

#### 4.setup-staging  PASS

Command: `cd ~/stage-smoke && cp /etc/tbound/serve-pi-service.json /etc/tbound/managed-live-root.json /etc/tbound/managed-live-root.ed25519 /etc/tbound/pi-host-profile.json /etc/tbound/pi-host-profile.ed25519 /etc/tbound/d09-cell.json . && chmod 664 *.json *.ed25519 && sha256sum d09-cell.json`

Expected: rc=0, output ~ /cf97920f/. Got rc=0.

```
cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43  d09-cell.json
```

#### 4.svc-stop  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op svc stop`

Expected: rc=0, output ~ /svc stop ok/. Got rc=0.

```
svc stop ok: service and socket inactive
```

#### 4.inspect-inactive  PASS

Command: `systemctl is-active tbound-pi.service tbound-pi.socket`

Expected: rc=3, output ~ /inactive/. Got rc=3.

```
inactive
inactive
```

#### 4.cfg-install-identical  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op cfg-install smoke a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc`

Expected: rc=0, output ~ /config already installed/. Got rc=0.

```
config already installed
state smoke unchanged: started
cfg-install smoke ok: sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc (state started)
```

#### 4.setup-tamper-cfg  PASS

Command: `cd ~/stage-smoke && cp serve-pi-service.json /tmp/cfg.orig && sed -i "s/\"workflow_max_tokens\": 10000/\"workflow_max_tokens\": 100000/" serve-pi-service.json`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.cfg-install-tampered-config  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op cfg-install smoke 37097161995979a108d46e70fe3df55ae9cc1fbec1cd15fc2de1e6ecfcd24285`

Expected: rc=1, output ~ /differs from the shipped template/. Got rc=1.

```
cfgcheck: differs from the shipped template at line 16: want '  "workflow_max_tokens": 10000,' got '  "workflow_max_tokens": 100000,'
tbound-op: config for smoke is not the shipped template with only the allowed members changed
```

#### 4.setup-restore-cfg-and-symlink  PASS

Command: `cd ~/stage-smoke && cp /tmp/cfg.orig serve-pi-service.json && rm /tmp/cfg.orig`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.cfg-install-wrong-sha  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op cfg-install smoke 0000000000000000000000000000000000000000000000000000000000000000`

Expected: rc=1, output ~ /does not hash to the given SHA/. Got rc=1.

```
tbound-op: staged serve-pi-service.json does not hash to the given SHA (copy hashed: sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc)
```

#### 4.setup-symlink-file  PASS

Command: `cd ~/stage-smoke && mv serve-pi-service.json serve-pi-service.json.real && ln -s /etc/tbound/serve-pi-service.json serve-pi-service.json`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.cfg-install-staged-symlink  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op cfg-install smoke a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc`

Expected: rc=1, output ~ /refused/. Got rc=1.

```
stage: [Errno 40] Too many levels of symbolic links: 'serve-pi-service.json'
tbound-op: staged file smoke/serve-pi-service.json refused
```

#### 4.setup-unlink  PASS

Command: `cd ~/stage-smoke && rm serve-pi-service.json && mv serve-pi-service.json.real serve-pi-service.json`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.setup-symlink-dir  PASS

Command: `mv ~/stage-smoke ~/stage-smoke.real && ln -s ~/stage-smoke.real ~/stage-smoke`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.cfg-install-staged-dir-symlink  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op cfg-install smoke a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc`

Expected: rc=1, output ~ /refused/. Got rc=1.

```
stage: [Errno 20] Not a directory: 'stage-smoke'
tbound-op: staged file smoke/serve-pi-service.json refused
```

#### 4.setup-unlink-dir  PASS

Command: `rm ~/stage-smoke && mv ~/stage-smoke.real ~/stage-smoke`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.reg-install-identical  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op reg-install smoke 4b99e9d96010ecb83e71faffc330d53ec9f4420242b512d09ce1c7714e54fee2 2bb77f0b3d2ecacb2799e0f8e3f3a8c891ba4173a0fee53cf24a8ac01466d6a9`

Expected: rc=0, output ~ /reg-install smoke ok/. Got rc=0.

```
reg-install smoke ok: REGDIGEST sha256:271e0e446821ee854949621bd6f3488fa05688a0b9650653105c856e9bcd421b (state reg)
```

#### 4.reg-install-wrong-sig-sha  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op reg-install smoke 4b99e9d96010ecb83e71faffc330d53ec9f4420242b512d09ce1c7714e54fee2 4b99e9d96010ecb83e71faffc330d53ec9f4420242b512d09ce1c7714e54fee2`

Expected: rc=1, output ~ /does not hash to the given SHA/. Got rc=1.

```
tbound-op: staged managed-live-root.ed25519 does not hash to the given SHA (copy hashed: sha256:2bb77f0b3d2ecacb2799e0f8e3f3a8c891ba4173a0fee53cf24a8ac01466d6a9)
```

#### 4.setup-bad-sig  PASS

Command: `cd ~/stage-smoke && cp managed-live-root.ed25519 /tmp/rsig.orig && printf X | dd of=managed-live-root.ed25519 bs=1 conv=notrunc 2>/dev/null`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.reg-install-bad-signature  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op reg-install smoke 4b99e9d96010ecb83e71faffc330d53ec9f4420242b512d09ce1c7714e54fee2 b6d5e0387a39ada3bcf82d28d6748dce8a76df703eafc149bc08bcfd3dffaed8`

Expected: rc=1, output ~ /managed-root-verify failed/. Got rc=1.

```
tbound-op: managed-root-verify failed: managed-root signature/policy verification failed: managed D09 live-root registration or runtime identity is not admitted
```

#### 4.setup-restore-sig  PASS

Command: `cd ~/stage-smoke && cp /tmp/rsig.orig managed-live-root.ed25519 && rm /tmp/rsig.orig`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.setup-keep-template  PASS

Command: `cp ~/stage-smoke/d09-cell.json /tmp/tpl.orig`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.setup-tamper-template-cpus  PASS

Command: `cd ~/stage-smoke && cp /tmp/tpl.orig d09-cell.json && sed -i 's/"cpus":"1"/"cpus":"2"/' d09-cell.json && ! cmp -s d09-cell.json /tmp/tpl.orig`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.prof-install-tampered-template-cpus  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op prof-install smoke cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210 4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112 27b18c5a5a5e264d5eb99084b51d37d926b7befee30bcbbbbc8c5938450fc8bc`

Expected: rc=1, output ~ /BASE_TD/. Got rc=1.

```
tbound-op: template BASE_TD differs from the pin
```

#### 4.setup-tamper-template-pids_max  PASS

Command: `cd ~/stage-smoke && cp /tmp/tpl.orig d09-cell.json && sed -i 's/"pids_max":256/"pids_max":257/' d09-cell.json && ! cmp -s d09-cell.json /tmp/tpl.orig`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.prof-install-tampered-template-pids_max  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op prof-install smoke cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210 4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112 f4e71fd8723541cd06d36cdbaad8563fbbf8045969cdcb0970a6ed50301d86d7`

Expected: rc=1, output ~ /BASE_TD/. Got rc=1.

```
tbound-op: template BASE_TD differs from the pin
```

#### 4.setup-tamper-template-image  PASS

Command: `cd ~/stage-smoke && cp /tmp/tpl.orig d09-cell.json && sed -i 's/"expected_image_digest":"sha256:4/"expected_image_digest":"sha256:5/' d09-cell.json && ! cmp -s d09-cell.json /tmp/tpl.orig`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.prof-install-tampered-template-image  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op prof-install smoke cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210 4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112 c1cc1ae7200d8f884336424a2efacb5191f35dd7726d795f650f3a1fe96cafd5`

Expected: rc=1, output ~ /BASE_TD/. Got rc=1.

```
tbound-op: template BASE_TD differs from the pin
```

#### 4.setup-tamper-template-regdigest  PASS

Command: `cd ~/stage-smoke && cp /tmp/tpl.orig d09-cell.json && sed -i 's/"managed_live_root_registration_digest":"sha256:2/"managed_live_root_registration_digest":"sha256:3/' d09-cell.json && ! cmp -s d09-cell.json /tmp/tpl.orig`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.prof-install-tampered-template-regdigest  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op prof-install smoke cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210 4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112 ab9824d4d89ab58d588312cb3ccf3597e2bf24c2648264e22397dbdb8a59475f`

Expected: rc=1, output ~ /registration digest/. Got rc=1.

```
tbound-op: template does not name the registration digest
```

#### 4.setup-restore-template  PASS

Command: `cp /tmp/tpl.orig ~/stage-smoke/d09-cell.json && rm /tmp/tpl.orig`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.setup-bad-profile-sig  PASS

Command: `cd ~/stage-smoke && cp pi-host-profile.ed25519 /tmp/psig.orig && printf X | dd of=pi-host-profile.ed25519 bs=1 conv=notrunc 2>/dev/null`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.prof-install-bad-signature  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op prof-install smoke cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210 81b397106ab25bdbd336f2d210c436cf38463281c420a7d199300e951dd95606 cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43`

Expected: rc=1, output ~ /profile verify failed/. Got rc=1.

```
tbound-op: profile verify failed: host profile verification failed: native Pi host attestation is missing
```

#### 4.setup-restore-profile-sig  PASS

Command: `cd ~/stage-smoke && cp /tmp/psig.orig pi-host-profile.ed25519 && rm /tmp/psig.orig`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.prof-install-identical  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op prof-install smoke cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210 4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112 cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43`

Expected: rc=0, output ~ /prof-install smoke ok.*state prof/. Got rc=0.

```
cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43  d09-cell.json
2bb77f0b3d2ecacb2799e0f8e3f3a8c891ba4173a0fee53cf24a8ac01466d6a9  managed-live-root.ed25519
4b99e9d96010ecb83e71faffc330d53ec9f4420242b512d09ce1c7714e54fee2  managed-live-root.json
4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112  pi-host-profile.ed25519
cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210  pi-host-profile.json
be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac  pi-host-profile.pub
a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc  serve-pi-service.json
prof-install smoke ok: profile sha256:c13a0d111b726f7c3d0080caae5ef082a3f760e06db499bca79273cef6da2222 TD sha256:cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43 (state prof)
```

#### 4.inspect-installed-unchanged  PASS

Command: `sha256sum /etc/tbound/d09-cell.json`

Expected: rc=0, output ~ /cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43/. Got rc=0.

```
cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43  /etc/tbound/d09-cell.json
```

#### 4.setup-clear-measure-dir  PASS

Command: `sudo -n rm -rf /root/ga/reg-smoke`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.reg-measure  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op reg-measure smoke`

Expected: rc=0, output ~ /reg-measure smoke ok/. Got rc=0.

```
reg-measure smoke ok: sha256:19e7aeca7d5681520e49ca6d0d8ca9110e0630f3c5c981bd03f4ead62762960d -> /var/tmp/tbound-op/smoke/managed-live-root.unsigned.json (fetch, sign on the owner machine)
```

#### 4.inspect-measured-equals-installed  PASS

Command: `cd /var/tmp/tbound-op/smoke && python3 -I -c "
import json
a=json.load(open(\"managed-live-root.unsigned.json\")); b=json.load(open(\"/etc/tbound/managed-live-root.json\"))
print(\"DIFFERENT=%d\" % sum(1 for k in set(a)|set(b) if a.get(k)!=b.get(k)), \"fields=%d\" % len(a))
"; test ! -e /sys/fs/cgroup/system.slice/tbound-pi.service && echo cgroup-removed`

Expected: rc=0, output ~ /DIFFERENT=0/. Got rc=0.

```
DIFFERENT=0 fields=42
cgroup-removed
```

#### 4.reg-measure-second-time  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op reg-measure smoke`

Expected: rc=1, output ~ /exists already/. Got rc=1.

```
tbound-op: /root/ga/reg-smoke exists already
```

#### 4.rollback-no-backup-g1  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op rollback g1 cfg`

Expected: rc=1, output ~ /no backup/. Got rc=1.

```
tbound-op: no backup /root/ga/prev/g1/serve-pi-service.json: rollback refused
```

#### 4.rollback-no-backup-smoke-cfg  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op rollback smoke cfg`

Expected: rc=1, output ~ /no backup/. Got rc=1.

```
tbound-op: no backup /root/ga/prev/smoke/serve-pi-service.json: rollback refused
```

#### 4.rollback-all-incomplete-changes-nothing  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op rollback smoke all`

Expected: rc=1, output ~ /no backup/. Got rc=1.

```
tbound-op: no backup /root/ga/prev/smoke/serve-pi-service.json: rollback refused
```

#### 4.rollback-smoke-prof  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op rollback smoke prof`

Expected: rc=0, output ~ /rollback smoke prof ok/. Got rc=0.

```
rollback smoke prof ok (state of smoke cleared; restored: pi-host-profile.json pi-host-profile.ed25519 d09-cell.json); run status to see which NAME the installed set matches
```

#### 4.svc-start-without-state  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op svc start`

Expected: rc=1, output ~ /no NAME with state/. Got rc=1.

```
tbound-op: no NAME with state >= prof matches the installed config (status shows the chain)
```

#### 4.adopt-smoke-after-rollback  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op adopt smoke`

Expected: rc=0, output ~ /adopt smoke ok.*state prof/. Got rc=0.

```
cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43  d09-cell.json
2bb77f0b3d2ecacb2799e0f8e3f3a8c891ba4173a0fee53cf24a8ac01466d6a9  managed-live-root.ed25519
4b99e9d96010ecb83e71faffc330d53ec9f4420242b512d09ce1c7714e54fee2  managed-live-root.json
4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112  pi-host-profile.ed25519
cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210  pi-host-profile.json
be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac  pi-host-profile.pub
a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc  serve-pi-service.json
adopt smoke ok: cfg sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc reg sha256:271e0e446821ee854949621bd6f3488fa05688a0b9650653105c856e9bcd421b profile sha256:c13a0d111b726f7c3d0080caae5ef082a3f760e06db499b
```

#### 4.seed-live-smoke-noop  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op seed-live smoke`

Expected: rc=0, output ~ /already is the smoke fixture/. Got rc=0.

```
live root already is the smoke fixture
== listing
d 755 995:986 lost+found
f 644 995:986 SMOKE-README.txt
== census
{"counts": {"dir": 2, "file": 1, "other": 0, "symlink": 0}, "cross_device_pruned": [], "entries": 3, "errors": {"getxattr": 0, "listxattr": 0, "lstat": [], "readlink": 0, "scandir": []}, "record": "summary", "root": "/var/lib/tbou
```

#### 4.seed-live-g1  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op seed-live g1`

Expected: rc=0, output ~ /src/release.txt/. Got rc=0.

```
== listing
d 755 995:986 lost+found
d 755 995:986 out
d 755 995:986 src
f 644 995:986 src/release.txt
== census
{"counts": {"dir": 4, "file": 1, "other": 0, "symlink": 0}, "cross_device_pruned": [], "entries": 5, "errors": {"getxattr": 0, "listxattr": 0, "lstat": [], "readlink": 0, "scandir": []}, "record": "summary", "root": "/var/lib/tbou
```

#### 4.seed-live-g1-again  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op seed-live g1`

Expected: rc=0, output ~ /already is the g1 fixture/. Got rc=0.

```
live root already is the g1 fixture
== listing
d 755 995:986 lost+found
d 755 995:986 out
d 755 995:986 src
f 644 995:986 src/release.txt
== census
{"counts": {"dir": 4, "file": 1, "other": 0, "symlink": 0}, "cross_device_pruned": [], "entries": 5, "errors": {"getxattr": 0, "listxattr": 0, "lstat": [], "readlink": 0, "scandir": []}, "record": "summary", "root": "/var/lib/tbou
```

#### 4.seed-live-smoke-reseed  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op seed-live smoke`

Expected: rc=0, output ~ /SMOKE-README.txt/. Got rc=0.

```
== listing
d 755 995:986 lost+found
f 644 995:986 SMOKE-README.txt
== census
{"counts": {"dir": 2, "file": 1, "other": 0, "symlink": 0}, "cross_device_pruned": [], "entries": 3, "errors": {"getxattr": 0, "listxattr": 0, "lstat": [], "readlink": 0, "scandir": []}, "record": "summary", "root": "/var/lib/tbou
```

#### 4.seed-live-smoke-noop-again  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op seed-live smoke`

Expected: rc=0, output ~ /already is the smoke fixture/. Got rc=0.

```
live root already is the smoke fixture
== listing
d 755 995:986 lost+found
f 644 995:986 SMOKE-README.txt
== census
{"counts": {"dir": 2, "file": 1, "other": 0, "symlink": 0}, "cross_device_pruned": [], "entries": 3, "errors": {"getxattr": 0, "listxattr": 0, "lstat": [], "readlink": 0, "scandir": []}, "record": "summary", "root": "/var/lib/tbou
```

#### 4.inspect-live-root-exact  PASS

Command: `sudo -n find /var/lib/tbound/live -mindepth 1 -printf "%y %m %U:%G %P\n" | sort; sudo -n stat -c "%s %U:%G %a" /var/lib/tbound/live/SMOKE-README.txt`

Expected: rc=0, output ~ /^d 755 995:986 lost\+found
f 644 995:986 SMOKE-README.txt/. Got rc=0.

```
d 755 995:986 lost+found
f 644 995:986 SMOKE-README.txt
86 tbound-pi:tbound-pi 644
```

#### 4.setup-draft-manifest  PASS

Command: `true`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 4.manifest-install-draft-refused  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op manifest-install 434797e794c55cc20bc2d1599ae390517fee37e502acf1c81aa4c8d0e7e58892 1111111111111111111111111111111111111111111111111111111111111111`

Expected: rc=1, output ~ /corpus-digest refused the manifest/. Got rc=1.

```
tbound-op: corpus-digest refused the manifest: corpus manifest refused: evalcorpus.Validate under the pilot quotas: 84 violation(s): review.human: case "PILOT-ADV-A-01" review[0] is not by a human reviewer; review.blinded: case "P
```

#### 4.inspect-no-manifest  PASS

Command: `test ! -e /etc/tbound/corpus && echo no-corpus-dir; rm -rf ~/stage-pilot`

Expected: rc=0, output ~ /no-corpus-dir/. Got rc=0.

```
no-corpus-dir
```

#### 4.svc-start  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op svc start`

Expected: rc=0, output ~ /svc start ok for smoke/. Got rc=0.

```
Oct 21 06:39:14 tbound-abi8-seed systemd[1]: Started tbound-pi.service - TBound managed Pi service (socket-activated, one fixed workflow).
svc start ok for smoke: service and socket active
```

#### 4.inspect-active  PASS

Command: `systemctl is-active tbound-pi.service tbound-pi.socket`

Expected: rc=0, output ~ /^active
active/. Got rc=0.

```
active
active
```

#### 4.svc-start-idempotent  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op svc start`

Expected: rc=0, output ~ /svc start ok for smoke/. Got rc=0.

```
-- No entries --
svc start ok for smoke: service and socket active
```

### 5. stage-ga and the copy-then-hash property

#### 5.setup-stage-ga  PASS

Command: `install -d -m 0700 ~/stage-ga && cd ~/stage-ga && cp /usr/local/bin/tbound-profile tbound-profile && F=b42b920e9ad3f70e0082aa26d26fe4a27df64e4e && printf "{\"freeze_commit\":\"%s\",\"v_image_digest\":\"%s\",\"pins\":{\"frozen_source_digest\":\"%s\",\"base_template_digest\":\"%s\",\"admin_pubkey_digest\":\"%s\",\"runtime_bundle_digest\":\"%s\",\"runner_digest\":\"%s\"}}\n" $F sha256:485c99a8c9f597ac0eca0d9e9a5e8e0086355342f9241e9f9147aca04b63b339 sha256:09e87c90872b3f05098a0151b6856566ac4a2a6ebab90d73c11e228b6ef8492a sha256:d677865ade117134b03d8882378c30b4b79a724e481d1e524fbfba5f1d1b7b25 sha256:be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac sha256:8e405ef0f2a3a3ed725dba2815d71bd8e5db007fe993f4ac3979ad59016d4f48 sha256:5467398f606bddae441576e7a193e83df5239da6cb12c34d1a118ffeeb43979e > run.A && sed "s/b42b920e/b42b920f/" run.A > run.B && cp run.A run.json`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 5.stage-tbound-profile  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op stage-ga tbound-profile 444713b350035bf599c41f9e11f54e59989ce665ce4949d56019a0785cbddfac`

Expected: rc=0, output ~ /stage-ga tbound-profile ok/. Got rc=0.

```
stage-ga tbound-profile ok: sha256:444713b350035bf599c41f9e11f54e59989ce665ce4949d56019a0785cbddfac -> /root/ga/tbound-profile (0500)
```

#### 5.stage-tbound-profile-wrong-sha  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op stage-ga tbound-profile f9ed3af8851a52b09e9b419f2cb206ac394c7e57dbf2204dd00263f8ae5733bc`

Expected: rc=1, output ~ /does not hash/. Got rc=1.

```
tbound-op: staged tbound-profile does not hash to the given SHA (copy hashed: sha256:444713b350035bf599c41f9e11f54e59989ce665ce4949d56019a0785cbddfac)
```

#### 5.stage-test-binary-wrong-sha  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op stage-ga procoracle-test 0000000000000000000000000000000000000000000000000000000000000000`

Expected: rc=1, output ~ /does not hash|No such file/. Got rc=1.

```
tbound-op: staged procoracle.test does not hash to the given SHA (copy hashed: sha256:986a5f0b9df70a4f0716b502d883d8a9b304ed3e17d58f2c0e0302cc77b93bc0)
```

#### 5.stage-bogus-kind  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op stage-ga bogus 444713b350035bf599c41f9e11f54e59989ce665ce4949d56019a0785cbddfac`

Expected: rc=2, output ~ /stage-ga tbound-profile/. Got rc=2.

```
tbound-op: stage-ga tbound-profile|procoracle-test|cell-entrypoint-test|run-json SHA
```

#### 5.setup-run-json-B  PASS

Command: `cd ~/stage-ga && cp run.B run.json`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 5.stage-run-json-not-from-pins  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op stage-ga run-json 61ca3f23fc7aa8ebdcbf151e60715924fe5e91d8af2aa3a6c73dc260a3ce031c`

Expected: rc=1, output ~ /not the file generated from the pins/. Got rc=1.

```
tbound-op: staged run.json is not the file generated from the pins
```

#### 5.setup-run-json-A  PASS

Command: `cd ~/stage-ga && cp run.A run.json`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 5.stage-run-json  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op stage-ga run-json f9ed3af8851a52b09e9b419f2cb206ac394c7e57dbf2204dd00263f8ae5733bc`

Expected: rc=0, output ~ /stage-ga run-json ok/. Got rc=0.

```
stage-ga run-json ok: sha256:f9ed3af8851a52b09e9b419f2cb206ac394c7e57dbf2204dd00263f8ae5733bc -> /root/ga/run.json (0444)
```

#### 5.stage-run-json-again  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op stage-ga run-json f9ed3af8851a52b09e9b419f2cb206ac394c7e57dbf2204dd00263f8ae5733bc`

Expected: rc=0, output ~ /stage-ga run-json ok/. Got rc=0.

```
stage-ga run-json ok: sha256:f9ed3af8851a52b09e9b419f2cb206ac394c7e57dbf2204dd00263f8ae5733bc -> /root/ga/run.json (0444)
```

#### 5.inspect-run-json  PASS

Command: `sudo -n stat -c "%a %U" /root/ga/run.json; sudo -n sha256sum /root/ga/run.json`

Expected: rc=0, output ~ /f9ed3af8851a52b09e9b419f2cb206ac394c7e57dbf2204dd00263f8ae5733bc.*/root/ga/run.json/. Got rc=0.

```
444 root
f9ed3af8851a52b09e9b419f2cb206ac394c7e57dbf2204dd00263f8ae5733bc  /root/ga/run.json
```

#### 5.swap-race-30-calls  PASS

Command: `cd ~/stage-ga; rm -f /tmp/stop; ( while [ ! -e /tmp/stop ]; do cp run.A r.t; mv -f r.t run.json; cp run.B r.t; mv -f r.t run.json; done ) & W=$!; ok=0; refused=0; for i in $(seq 1 30); do o=$(sudo -n /opt/tbound/operator/tbound-op stage-ga run-json f9ed3af8851a52b09e9b419f2cb206ac394c7e57dbf2204dd00263f8ae5733bc 2>&1); case "$o" in *"stage-ga run-json ok"*) ok=$((ok+1));; *) refused=$((refused+1));; esac; done; touch /tmp/stop; wait $W; rm -f /tmp/stop; s=$(sudo -n sha256sum /root/ga/run.json | cut -d" " -f1); [ "$s" = f9ed3af8851a52b09e9b419f2cb206ac394c7e57dbf2204dd00263f8ae5733bc ] && y=yes || y=NO; echo "ok=$ok refused=$refused installed-ok=$y"`

Expected: rc=0, output ~ /ok=[0-9]+ refused=[0-9]+ installed-ok=yes/. Got rc=0.

```
ok=13 refused=17 installed-ok=yes
```

#### 5.setup-cleanup  PASS

Command: `rm -rf ~/stage-ga/run.A ~/stage-ga/run.B ~/stage-ga/run.json ~/stage-ga/r.t ~/stage-ga/tbound-profile; cd ~/stage-smoke && rm -f serve-pi-service.json managed-live-root.json managed-live-root.ed25519 pi-host-profile.json pi-host-profile.ed25519 d09-cell.json`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 5.guest-evidence-no-g1-archive  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op guest-evidence provider-exchange`

Expected: rc=1, output ~ /missing/. Got rc=1.

```
tbound-op: /root/ga/g1/pi-host-profile.json missing (prof-install g1)
```

#### 5.guest-evidence-bad-runid  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op guest-evidence route-direct-runtime NOSUCHRUNID1`

Expected: rc=1, output ~ /no /var/lib/tbound/audit//. Got rc=1.

```
tbound-op: no /var/lib/tbound/audit/NOSUCHRUNID1-session-audit.jsonl
```

#### 5.guest-evidence-runid-required  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op guest-evidence route-bash-view`

Expected: rc=2, output ~ /RUNID required/. Got rc=2.

```
tbound-op: RUNID required
```

### 6. Negative cases n2 and n5 (read-only refusals) and the lock

#### 6.negative-n2  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op negative n2`

Expected: rc=0, output ~ /PermissionError.*result=PASS/. Got rc=0.

```
Traceback (most recent call last):
  File "<string>", line 1, in <module>
PermissionError: [Errno 13] Permission denied
result=PASS
```

#### 6.negative-n5  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op negative n5`

Expected: rc=0, output ~ /cannot be selected by the client.*result=PASS/. Got rc=0.

```
== --native-host-profile /etc/tbound/pi-host-profile.json -> exit 2
ordinary `serve --pi` is a local service client; profile/source/workspace are service-owned and cannot be selected by the client
== --workspace /work -> exit 2
ordinary `serve --pi` is a local service client; profile/source/workspace are service-owned and cannot be selected by the client
== --source /src -> exit 2
ordinary `serve --pi` is a local service client; profile/source/workspace are service-owned and cannot be selected by the client
result=PASS
```

#### 6.lock-busy  PASS

Command: `sudo -n flock /run/tbound-op.lock sleep 8 & sleep 1; sudo -n /opt/tbound/operator/tbound-op svc stop 2>&1 | tail -1; sudo -n /opt/tbound/operator/tbound-op status smoke 2>&1 | sed -n 4p; systemctl is-active tbound-pi.service tbound-pi.socket | tr "\n" " "; wait`

Expected: rc=0, output ~ /busy: another tbound-op verb/. Got rc=0.

```
tbound-op: busy: another tbound-op verb or a client-run holds /run/tbound-op.lock
service=active socket=active
active active 
```

#### 6.lock-did-not-stop-service  PASS

Command: `systemctl is-active tbound-pi.service tbound-pi.socket | tr "\n" " "`

Expected: rc=0, output ~ /active active/. Got rc=0.

```
active active 
```

### 6b. Output root and client-run status (review finding: status must never read from /var/tmp/tbound-op)

#### 6b.setup-status-fixture  PASS

Command: `sudo -n sh -c 'printf "launched FIXTURE rid=pilot-req-20000101T000000Z\n" > /root/ga/state/pilot.client-status'`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 6b.tboundadmin-cannot-mkdir-in-out-root  PASS

Command: `mkdir /var/tmp/tbound-op/pilot 2>&1; test -d /var/tmp/tbound-op/pilot && echo created; false`

Expected: rc=1, output ~ /Permission denied/. Got rc=1.

```
mkdir: cannot create directory ‘/var/tmp/tbound-op/pilot’: Permission denied
```

#### 6b.tboundadmin-cannot-plant-symlink  PASS

Command: `ln -s /etc/tbound/netsink.secret /var/tmp/tbound-op/_/client-run.status 2>&1; test -L /var/tmp/tbound-op/_/client-run.status && echo planted; false`

Expected: rc=1, output ~ /Permission denied/. Got rc=1.

```
ln: failed to create symbolic link '/var/tmp/tbound-op/_/client-run.status': Permission denied
```

#### 6b.setup-root-symlink-in-out-dir  PASS

Command: `sudo -n sh -c "install -d -o root -g tboundadmin -m 0750 /var/tmp/tbound-op/pilot && ln -sf /etc/tbound/netsink.secret /var/tmp/tbound-op/pilot/client-run.status"; sudo -n ls -l /var/tmp/tbound-op/pilot/client-run.status`

Expected: rc=0, output ~ /-/. Got rc=0.

```
lrwxrwxrwx 1 root root 26 Oct 21 06:39 /var/tmp/tbound-op/pilot/client-run.status -> /etc/tbound/netsink.secret
```

#### 6b.status-shows-state-file-status  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status pilot`

Expected: rc=0, output ~ /pilot client-run.status: launched FIXTURE rid=pilot-req-20000101T000000Z/. Got rc=0.

```
tbound-op status 2026-10-21T06:39:41Z pins F=b42b920e9ad3f70e0082aa26d26fe4a27df64e4e
anchor: ok (pi-host-profile.pub sha256:be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac)
runner: sha256:5467398f606bddae tbound-profile: sha256:444713b350035bf5
service=active socket=active
installed: cfg sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc reg sha256:4b99e9d96010ecb8 prof sha256:cf766b7b23f1bbe8 tpl sha256:cf97920f5549a43b
installed config matches state of: smoke
-- pilot client-run.status: launched FIXTURE rid=pilot-req-20000101T000000Z
-- pilot: no state
-- last log lines
2026-10-21T06:39:36Z tboundadmin END rc=1 argv=svc stop 
2026-10-21T06:39:36Z tboundadmin BEGIN argv=status smoke 
2026-10-21T06:39:36Z tboundadmin OK status smoke 
... (14 lines)
```

#### 6b.status-does-not-follow-symlink  PASS

Command: `S=$(sudo -n head -c 20 /etc/tbound/netsink.secret); n=$(sudo -n /opt/tbound/operator/tbound-op status pilot | grep -c "$S"); echo leaked=$n`

Expected: rc=0, output ~ /leaked=0/. Got rc=0.

```
leaked=0
```

#### 6b.cleanup-root-symlink  PASS

Command: `sudo -n rm -f /var/tmp/tbound-op/pilot/client-run.status; sudo -n rmdir /var/tmp/tbound-op/pilot`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 6b.setup-attacker-created-out-root  PASS

Command: `sudo -n mv /var/tmp/tbound-op /var/tmp/tbound-op.saved && mkdir -p /var/tmp/tbound-op/pilot && ln -s /etc/tbound/netsink.secret /var/tmp/tbound-op/pilot/client-run.status && ls -ld /var/tmp/tbound-op | cut -d" " -f3,4 | sed "s/^/attacker-dir /"`

Expected: rc=0, output ~ /attacker-dir/. Got rc=0.

```
attacker-dir tboundadmin tboundadmin
```

#### 6b.status-refuses-attacker-out-root  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status pilot`

Expected: rc=1, output ~ /not a root:tboundadmin 0750 directory/. Got rc=1.

```
tbound-op: /var/tmp/tbound-op is not a root:tboundadmin 0750 directory (owner must remove it)
```

#### 6b.diag-refuses-attacker-out-root  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op diag live-listing`

Expected: rc=1, output ~ /not a root:tboundadmin 0750 directory/. Got rc=1.

```
tbound-op: /var/tmp/tbound-op is not a root:tboundadmin 0750 directory (owner must remove it)
```

#### 6b.collect-refuses-attacker-out-root  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op collect requests`

Expected: rc=1, output ~ /not a root:tboundadmin 0750 directory/. Got rc=1.

```
tbound-op: /var/tmp/tbound-op is not a root:tboundadmin 0750 directory (owner must remove it)
```

#### 6b.svc-refuses-attacker-out-root  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op svc stop`

Expected: rc=1, output ~ /not a root:tboundadmin 0750 directory/. Got rc=1.

```
tbound-op: /var/tmp/tbound-op is not a root:tboundadmin 0750 directory (owner must remove it)
```

#### 6b.adopt-refuses-attacker-out-root  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op adopt smoke`

Expected: rc=1, output ~ /not a root:tboundadmin 0750 directory/. Got rc=1.

```
tbound-op: /var/tmp/tbound-op is not a root:tboundadmin 0750 directory (owner must remove it)
```

#### 6b.inspect-service-still-active  PASS

Command: `systemctl is-active tbound-pi.service tbound-pi.socket | tr "\n" " "`

Expected: rc=0, output ~ /active active/. Got rc=0.

```
active active 
```

#### 6b.setup-attacker-symlink-out-root  PASS

Command: `rm -rf /var/tmp/tbound-op && ln -s /root /var/tmp/tbound-op && echo attacker-symlink`

Expected: rc=0, output ~ /attacker-symlink/. Got rc=0.

```
attacker-symlink
```

#### 6b.status-refuses-symlinked-out-root  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status pilot`

Expected: rc=1, output ~ /is a symlink: refused/. Got rc=1.

```
tbound-op: /var/tmp/tbound-op is a symlink: refused
```

#### 6b.cleanup-attacker-out-root  PASS

Command: `sudo -n rm -rf /var/tmp/tbound-op && sudo -n mv /var/tmp/tbound-op.saved /var/tmp/tbound-op && stat -c "%U %G %a" /var/tmp/tbound-op`

Expected: rc=0, output ~ /root tboundadmin 750/. Got rc=0.

```
root tboundadmin 750
```

#### 6b.setup-missing-out-root-is-created-root-owned  PASS

Command: `sudo -n mv /var/tmp/tbound-op /var/tmp/tbound-op.saved && sudo -n /opt/tbound/operator/tbound-op status >/dev/null; stat -c "%U %G %a" /var/tmp/tbound-op; sudo -n rm -rf /var/tmp/tbound-op && sudo -n mv /var/tmp/tbound-op.saved /var/tmp/tbound-op`

Expected: rc=0, output ~ /root tboundadmin 750/. Got rc=0.

```
root tboundadmin 750
```

#### 6b.cleanup-fixture  PASS

Command: `sudo -n rm -f /root/ga/state/pilot.client-status`

Expected: rc=0, output ~ /-/. Got rc=0.

```

```

#### 6b.status-after-cleanup  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status pilot`

Expected: rc=0, output ~ /pilot: no state/. Got rc=0.

```
tbound-op status 2026-10-21T06:39:50Z pins F=b42b920e9ad3f70e0082aa26d26fe4a27df64e4e
anchor: ok (pi-host-profile.pub sha256:be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac)
runner: sha256:5467398f606bddae tbound-profile: sha256:444713b350035bf5
service=active socket=active
installed: cfg sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc reg sha256:4b99e9d96010ecb8 prof sha256:cf766b7b23f1bbe8 tpl sha256:cf97920f5549a43b
installed config matches state of: smoke
-- pilot: no state
-- last log lines
2026-10-21T06:39:48Z tboundadmin END rc=1 argv=status pilot 
2026-10-21T06:39:49Z tboundadmin BEGIN argv=status 
2026-10-21T06:39:49Z tboundadmin OK status 
2026-10-21T06:39:49Z tboundadmin END rc=0 argv=status 
... (13 lines)
```

### 7. Final state

#### 7.status  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op status smoke`

Expected: rc=0, output ~ /service=active socket=active.*state=started/. Got rc=0.

```
tbound-op status 2026-10-21T06:39:51Z pins F=b42b920e9ad3f70e0082aa26d26fe4a27df64e4e
anchor: ok (pi-host-profile.pub sha256:be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac)
runner: sha256:5467398f606bddae tbound-profile: sha256:444713b350035bf5
service=active socket=active
installed: cfg sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc reg sha256:4b99e9d96010ecb8 prof sha256:cf766b7b23f1bbe8 tpl sha256:cf97920f5549a43b
installed config matches state of: smoke
-- smoke:
     cfg=sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc
     prof=sha256:c13a0d111b726f7c3d0080caae5ef082a3f760e06db499bca79273cef6da2222
     profjson=sha256:cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210
     profsig=sha256:4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112
     reg=sha256:271e0e446821ee854949621bd6f3488fa05688a0b9650653105c856e9bcd421b
... (23 lines)
```

#### 7.adopt-verifies-chain  PASS

Command: `sudo -n /opt/tbound/operator/tbound-op adopt smoke`

Expected: rc=0, output ~ /adopt smoke ok.*state started/. Got rc=0.

```
cf97920f5549a43b2ac99dfb05fbaade8eb98d801be0772b9c79ae4bbe66cf43  d09-cell.json
2bb77f0b3d2ecacb2799e0f8e3f3a8c891ba4173a0fee53cf24a8ac01466d6a9  managed-live-root.ed25519
4b99e9d96010ecb83e71faffc330d53ec9f4420242b512d09ce1c7714e54fee2  managed-live-root.json
4ced4292fa19e66422136acbd5801a8746fe04e074fec0e58ce693860ee39112  pi-host-profile.ed25519
cf766b7b23f1bbe8e2435d47d505500d31934f2f35a73bf3d78d20e2f201c210  pi-host-profile.json
be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac  pi-host-profile.pub
a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc  serve-pi-service.json
adopt smoke ok: cfg sha256:a46353903e6d82bc4e716ac64914be5b97ae72c7a0b7daa5875aa8db65dbd4fc reg sha256:271e0e446821ee854949621bd6f3488fa05688a0b9650653105c856e9bcd421b profile sha256:c13a0d111b726f7c3d0080caae5ef082a3f760e06db499b
```

#### 7.live-root  PASS

Command: `sudo -n find /var/lib/tbound/live -mindepth 1 -printf "%y %m %U:%G %P\n" | sort`

Expected: rc=0, output ~ /^d 755 995:986 lost\+found
f 644 995:986 SMOKE-README.txt/. Got rc=0.

```
d 755 995:986 lost+found
f 644 995:986 SMOKE-README.txt
```

#### 7.installed-chain-unchanged  PASS

Command: `sudo -n sha256sum /etc/tbound/serve-pi-service.json /etc/tbound/managed-live-root.json /etc/tbound/pi-host-profile.json /etc/tbound/d09-cell.json | cut -c1-16 | tr "\n" " "`

Expected: rc=0, output ~ /a46353903e6d82bc.*4b99e9d96010ecb8.*cf766b7b23f1bbe8.*cf97920f5549a43b/. Got rc=0.

```
a46353903e6d82bc 4b99e9d96010ecb8 cf766b7b23f1bbe8 cf97920f5549a43b 
```

#### 7.one-use-markers-absent  PASS

Command: `sudo -n sh -c "ls /root/ga/state/*.once 2>/dev/null || echo no-once-files"`

Expected: rc=0, output ~ /no-once-files/. Got rc=0.

```
no-once-files
```

#### 7.installed-hashes  PASS

Command: `sudo -n sha256sum /opt/tbound/operator/tbound-op /opt/tbound/operator/pins.env /etc/sudoers.d/50-tbound-op`

Expected: rc=0, output ~ /-/. Got rc=0.

```
a35bb31de722fb7d28393952bc1ade0eaefbe52ff51a51e5ac811373230e1ef9  /opt/tbound/operator/tbound-op
b3f721e690224d19bad73e142e354662aac24959dc890d9f76ef9b9df7fe35c2  /opt/tbound/operator/pins.env
0ab6ffb495eddb06d5bcc889d217ea5c581669c980cb0ad555ee896c7ca1fc92  /etc/sudoers.d/50-tbound-op
```

### 8. Offline unit tests of the structural config check (owner machine, WSL; not part of run-tests.sh)

The Python function behind `cfg-install` (extracted verbatim from `tbound-op`) run on the real shipped templates with the real podman-config digests (CD 39d4074e..., SD 9ebdcc8b...). g1, rp, dr and pilot configs cannot be installed before the owner signs, so these rules are tested here.

| case | mode | result |
| --- | --- | --- |
| smoke template filled with CD/SD | plain | accepted (rc 0) |
| g1 template filled | plain | accepted |
| route-probe template filled (rp) | plain | accepted |
| g1 config against the smoke template | plain | refused: differs at line 6 (`workflow_admission_id`) |
| route-probe + new admission id `route-probe-dr-2026-10-22` | dr | accepted, prints the id |
| route-probe with the old admission id | dr | refused: dr needs a NEW admission id |
| dr config with `workflow_max_tokens` 200000 | dr | refused: differs at line 16 |
| corpus-pilot filled with CD/SD and a corpus digest | pilot | accepted, prints the digest |
| corpus-pilot with `<CORPUS_DIGEST>` left in | pilot | refused: corpus digest not found exactly once |
| smoke config with `provider_route` research | plain | refused: differs at line 4 |
| smoke config with a wrong storage_conf_digest | plain | refused: differs at line 29 |
| smoke config with CRLF line ends | plain | refused: contains CR |

The guest-side wrapper additionally requires, for `pilot`, that the config's corpus digest equals the digest recorded by `manifest-install`, and for `dr` that the admission id was never used (templates, earlier dr installs).

### 9. Detached client child (harness, not the real client)

The child script of `client-run` (status in `/root/ga/state/<NAME>.client-status`, exports through `install .new` + `mv -f`) was run with `/bin/sh -c 'sleep 4; ...'` as uid 994 in place of `tbound serve`: see the run below.

```
lock-held
done exit=0 2026-10-21T06:40:10Z
total 8
-rw-r----- 1 root tboundadmin 33 Oct 21 06:40 client-run.status
-rw-r----- 1 root tboundadmin 15 Oct 21 06:40 response.txt
-rw-r----- 1 root tboundadmin  0 Oct 21 06:40 response.txt.err
{"fake":1}
994
g1.client-status
```

Result: lock held while the child runs; authoritative status written under the state dir; exports (response, stderr, status) created by install + mv in the checked output directory.
