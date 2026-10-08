# Governed host setup (Phase 2)

This runbook covers what an **operator** must provision before `tbound serve --pi`
can be admitted. Nothing here is automated yet, and none of it should be done
without explicit operator authorization. Run the read-only checker first:

```sh
scripts/check-governed-host.sh
```

It never changes state and prints PASS/WARN/FAIL for each requirement.

## Scope and honesty boundary

`tbound serve --pi` stays **refused** until every item below exists on the
declared evaluation host (D09/D10/D11). WSL2 and workstation hosts are
development-only; they are not the confirmation profile. A capability probe, a
`null` callback, or a boolean is never a substitute for the real profile, signed
image, and containment evidence.

## 1. Host and filesystem (D10)

- A dedicated Linux VM/host with a recorded image, hypervisor, kernel/config,
  filesystem and mount options.
- No host-share mounts, clipboard, host credentials/sockets, or personal repos in
  the guest; synthetic repositories and canaries only.
- A clean snapshot plus a demonstrated restore, and a reset procedure per trial.

## 2. Container runtime and image (D09)

- Rootless **Podman** with **crun** at pinned versions.
- A digest-pinned cell image and an offline **Cosign** verifier with the
  authorized public key.
- A signed in-image entrypoint (one conforming entrypoint: Go or minimal Rust).
- No pulls at run time; identity drift must fail closed.

## 3. Containment composition (D11)

- Namespaces, `no_new_privs`, process-wide **Landlock**, one static
  **seccomp-BPF** profile, and **pidfd**.
- A fixed, non-threaded, **delegated cgroup v2 subtree** with the required
  controllers (memory/pids) and writable `cgroup.kill`, observable
  `cgroup.events:populated`, and recursive `populated=0` teardown.
- A surviving runner that owns TBound, Pi, and every cell and settles descendants;
  unresolved cleanup must report `UNKNOWN`, never fake `STOPPED`.

`scripts/check-governed-host.sh` inspects these read-only.

## 4. Signed host profile

The launcher verifies a root-owned, non-group/world-writable profile at:

```
/etc/tbound/pi-host-profile.json
/etc/tbound/pi-host-profile.ed25519
/etc/tbound/pi-host-profile.pub
```

> **Blocked:** the profile generator/signer tooling does not exist yet. The profile
> schema is defined by the runtime host-profile code, which is still in flight and
> uncommitted. The signer will be added once that schema is frozen, so there is a
> single source of truth rather than two schemas.

## 5. Provider

Credentials stay in the Go broker; Pi/cells never receive the key. The approved
test channel is `nvidia/nemotron-3.5-lightning:free` with the secure loader and
synthetic content. Genuine provider runs need separate approval.

## 6. Evidence and reset

- Freeze the profile: VM/filesystem/kernel, runtime/image identities, generated
  OCI + static seccomp source/digest, Landlock ABI/rights, rootless mappings,
  cgroup/pidfd behavior, entrypoint.
- Retain source, image, runtime, observer, and result hashes per trial.

## Checklist

- [ ] `scripts/check-governed-host.sh` exits 0
- [ ] Podman + crun + Cosign pinned and verified offline
- [ ] Signed cell image + signed entrypoint
- [ ] Delegated cgroup v2 subtree with `cgroup.kill`
- [ ] Landlock + seccomp + `no_new_privs` + pidfd effective
- [ ] Root-owned signed `/etc/tbound/pi-host-profile.*` (blocked: tooling)
- [ ] Surviving runner with descendant settlement
- [ ] Profile frozen and recorded

Only when all of the above holds **and** the runtime composition and authoritative
verifier are implemented does `tbound serve --pi` have a path to admission. Provisioning
alone does not enable it: this runbook and the read-only checker are advisory, and the
runtime verifier — not a probe — decides admission.
