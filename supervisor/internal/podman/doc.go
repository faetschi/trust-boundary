// Package podman implements the first bounded slice of a Linux-only,
// rootless-Podman command cell for the supervisor's command-runner seam.
//
// The runner here is a development profile. It is explicitly
// NON-claim-bearing: Probe and Run report only mechanisms that were actually
// measured and never report command containment as established. The supervisor
// store continues to label command containment "not-established".
//
// Deferred until a later slice, and required before any H1 containment claim:
//
//   - a frozen, signed in-image entrypoint verified with offline cosign;
//   - an image pin by repo digest supplied and frozen by the experiment
//     manifest;
//   - a real settlement observer backed by cgroup and pidfd inspection rather
//     than this package's process-scan and podman-query observations; and
//   - the full frozen-mechanism profile (Landlock ABI, static seccomp digest,
//     delegated non-threaded cgroup-v2 subtree, pidfd behavior).
//
// Until all of those exist this package must not be cited as evidence that
// forbidden effects from an untrusted process are confined.
//
// The current CommandRunner seam does not carry the command lease identity, so
// a Runner must be constructed with the lease it is authorized for.
package podman
