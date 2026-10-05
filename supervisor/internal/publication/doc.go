// Package publication applies a validated session-generation delta to a
// descriptor-anchored Linux workspace. It owns workflow and live-root
// repository locking, single-use commit-token consumption, target-filesystem
// staging, and per-object recovery evidence. It deliberately does not claim a
// global filesystem atomic transaction.
//
// The caller must supply a trusted policy authorizer, an authenticated delta
// decision verifier, a durable audit journal, a private supervisor-owned state
// directory, and a quiescent target. The live root uses the workspace package's
// normalized metadata profile. Regular-file create/overwrite/delete and
// directory creation are publishable. Directory removal and type replacement
// fail closed. OriginProposalID must be a proposal-call member of the approved
// transition chain; lease-only origins fail closed in this bounded slice.
//
// Recovery observes recorded progress and the live tree. It never replays a
// consumed token, rolls back blindly, or restores an old checkpoint over later
// effects. Ambiguous observations remain UNKNOWN and pause further publication.
// Checkpoint restoration is a separate disposable materialization under the
// private state root; it never targets or replaces the live tree.
package publication
