// Package podman implements the first bounded slice of a Linux-only,
// rootless-Podman command cell for the supervisor's command-runner seam.
//
// The runner here is a development profile. It is explicitly
// NON-claim-bearing: Probe and Run report only mechanisms that were actually
// measured and never report command containment as established. The supervisor
// store continues to label command containment "not-established".
//
// The image is pinned in-process to a manifest-frozen content digest
// (ExpectedImageDigest), and Run re-verifies the launched image's repo digest;
// no image is ever pulled and a merely-present or mismatched tag is rejected.
// Settlement is observed through a private cidfile, a /proc cmdline/environ
// scan, a bounded /sys/fs/cgroup scan, and /proc/self/mountinfo.
//
// Deferred until a later slice, and required before any H1 containment claim:
//
//   - a frozen, signed in-image entrypoint verified with offline cosign (the
//     embedded script here is host-supplied argument text, not a signed
//     artifact);
//   - cosign verification of the pinned image plus the experiment-manifest
//     record of that pin and its signer (an in-process constant establishes
//     identity, not signer authorization);
//   - authoritative teardown and settlement through cgroup.kill and pidfd
//     inspection rather than this package's observational scans; and
//   - the full frozen-mechanism profile (Landlock ABI, static seccomp digest,
//     delegated non-threaded cgroup-v2 subtree, pidfd behavior).
//
// Until all of those exist this package must not be cited as evidence that
// forbidden effects from an untrusted process are confined.
//
// The current CommandRunner seam does not carry the command lease identity, so
// a Runner must be constructed with the lease it is authorized for.
package podman
