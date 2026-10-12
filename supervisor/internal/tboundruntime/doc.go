// Package tboundruntime composes durable tool execution and workflow
// publication around the presentation-independent piruntime supervisor.
//
// It is a library composition seam, not a production host binder: provider,
// host-profile verification, child launch, and containment remain supplied by
// their respective owners. The cmd/tbound package retains the CLI and
// source-compatible aliases for its existing composition surface.
package tboundruntime
