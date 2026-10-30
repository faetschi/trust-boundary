// Package workspace imports and scans supervisor-owned Linux workspace trees.
//
// It produces delta.TreeManifest values from opened filesystem objects and
// verifies imports by scanning the destination again. It does not publish or
// seal generations, persist transitions, decide policy, or create mounts.
//
// Callers must hold their workflow lock and attest that the root is quiescent.
// The root descriptor must refer to a supervisor-owned directory. The scanner
// rejects links, special files, nested mounts, visible extended attributes,
// and trees exceeding configured limits. Linux may hide xattr names from an
// unprivileged caller, so scans fail closed without a visibility attestation
// for the frozen host profile. A successful visible-xattr scan alone does not
// prove that every xattr, ACL, or file capability is absent.
// ImportPolicy.ReservedMetadataRejectLiveGit is a narrow opt-in admission
// filter. It rejects a .git path component before opening the entry and checks
// recursive enumeration again after each directory walk; the default policy
// remains unchanged for existing callers. The strict policy is used by private
// Git staging so live Git administration, including pointer files, is never
// read or copied by the import walker.
// ReservedMetadataExcludeRootPrivateGit is only for a caller that separately
// verifies one newly-created private root .git directory; it excludes that
// one root entry from the logical tree and still rejects nested .git names.
// Import requires disjoint source and destination directory trees and rejects
// equal or ancestor roots using descriptor-relative ancestry checks.
//
// File contents and the executable/non-executable distinction are preserved.
// Ownership is normalized to the effective UID/GID. Timestamps are used only
// to detect source changes during copying; they do not contribute to tree
// identity. Sparse files are read as ordinary byte streams.
package workspace
