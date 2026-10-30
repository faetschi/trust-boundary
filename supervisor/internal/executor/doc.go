// Package executor adapts the fail-closed sandbox cell to the supervisor's
// command-runner seam.
//
// The runner implemented here is a development/WSL profile. It is explicitly
// NON-claim-bearing: it reports the mechanisms the sandbox actually measured
// and never reports command containment as established. The supervisor store
// continues to label command containment "not-established".
//
// The current CommandRunner seam does not carry the command lease identity, so
// a Runner must be constructed with the lease it is authorized for.
package executor
