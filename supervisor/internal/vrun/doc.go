// Package vrun holds what the guest and the host share about Configuration V, the native Pi
// utility baseline of the evaluation (tbound-thesis.md section 6): the result document that
// `tbound trial v-run <case_id>` prints (Doc, tbound-v-run/v1), the parser of Pi's JSON event
// stream that fills its usage members (UsageScanner), and the pure construction of the
// `podman run` argument vector that starts native Pi in a plain container (PodmanArgs).
//
// V is descriptive. It runs the pinned official Pi CLI, without the TBound extension, proxy
// tools, gate, service or oracles, in a plain rootless Podman container of the pinned cell
// image on a fresh copy of the case fixture. The package has no build tag so that the host
// reads the document exactly as the guest writes it.
package vrun
