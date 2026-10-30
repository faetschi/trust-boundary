// Package piruntime contains the importable, presentation-independent seams
// used by the native Pi host composition root.
//
// The package does not launch Pi by default, select a provider, or turn a
// callback into a trust attestation. The Supervisor is the shared IPC -> broker
// -> gate -> executor loop. Child and host lifecycle helpers are deliberately
// conservative: a process is not classified as STOPPED until its direct exit
// and the independently supplied descendant settlement are both observed.
package piruntime
