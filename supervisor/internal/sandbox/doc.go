// Package sandbox implements a directly testable, fail-closed Linux isolation
// cell for a single command.
//
// The cell is composed, in order, of a user namespace, a private mount
// namespace, PID/IPC/UTS/network namespaces, a writable bind of the supplied
// cell root, read-only runtime binds, a private tmpfs /tmp, dropped
// capabilities, no_new_privs, a Landlock ruleset (ABI 1), and a pinned
// seccomp-bpf allowlist. The isolation profile implemented here is a
// development/WSL profile. It is explicitly NON-claim-bearing: Probe and Launch
// report only mechanisms that were actually measured, and the package never
// reports containment as established. Missing or unverifiable mechanisms fail
// closed.
//
// The package holds no policy and does not establish a read-only Pi mount.
// Callers keep policy, authority, and durable operation records in the
// supervisor; this package only launches one already-authorized command.
package sandbox
