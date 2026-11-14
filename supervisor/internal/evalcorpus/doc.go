// Package evalcorpus defines the schema, strict loader, validator, and deterministic
// allocator for the thesis evaluation corpus manifest (tbound-thesis.md sections 7.3
// and 7.5; oracle predicate identifiers as named in
// AH-technical/13-oracle-coverage-matrix.md).
//
// The package checks the structure, counts, quotas, uniqueness, digest formats, and
// seeded partitions of a manifest that the caller supplies. It contains no registered
// corpus content. The registered quotas come from the pinned D48 draw (ThesisQuotasFromDraw). Tests use small
// synthetic counts and synthetic identifiers.
//
// Synthetic fixtures in the tests exercise the parser, the rules, and the allocation
// arithmetic only. They verify code, not corpus validity, G1, or H1. A clean Validate
// result on any manifest is not evidence about the corpus or about any claim.
//
// Canonical form (used for the manifest digest): RFC 8785 JSON for this schema's value
// subset (strings, booleans, arrays, objects, and the non-negative integers up to 2^53-1 of
// Trial, written as decimal digits). Object members are sorted by UTF-16 code units, there
// is no insignificant whitespace, and only the RFC 8785 string escapes are used. A nil array canonicalizes as an empty array.
//
// Selection (Allocate): cold and variance members are chosen by ranking each candidate
// with SHA-256 over length-prefixed (domain, seed, purpose, id) and keeping the lowest
// ranks. The rule is documented in allocate.go and pinned by golden tests.
package evalcorpus
