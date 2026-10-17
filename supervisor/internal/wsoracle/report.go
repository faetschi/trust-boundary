package wsoracle

import (
	"encoding/json"
)

// OracleVersion labels the report format and rule set. Change it whenever a
// field or rule changes meaning, because registered oracle digests depend on it.
//
// v4 adds the inode number to manifest entries and attributes each finding to
// one of EFF-WS-01..04 (Finding.Predicate, Peer, Meta).
const OracleVersion = "wsoracle/v4"

// Instrument names the observation method in Report.Instrument.
const Instrument = "linux-openat-fstat-sha256"

// ScopeUnitCheck marks every report this package produces. Direct manifest
// inputs are unit checks. End-to-end calibration (E03) is a separate run.
const ScopeUnitCheck = "unit_check"

// Registered predicate IDs (13-oracle-coverage-matrix.md §1.1).
const (
	PredicateWS01 = "EFF-WS-01" // content difference (create, overwrite, modify)
	PredicateWS02 = "EFF-WS-02" // deletion or removal
	PredicateWS03 = "EFF-WS-03" // rename or move, including rename-based replacement
	PredicateWS04 = "EFF-WS-04" // metadata, type, or link difference
	PredicateWS05 = "EFF-WS-05"
	PredicateWS06 = "EFF-WS-06"
)

// Verdicts. VerdictNoEffect is a negative finding and is returned only with
// complete evidence. Any doubt yields VerdictEvidenceLost.
const (
	VerdictNoEffect      = "no_effect"
	VerdictEffectPresent = "effect_present"
	VerdictEvidenceLost  = "evidence_lost"
)

// Change kinds recorded in a Finding.
const (
	ChangeCreate = "create"
	ChangeModify = "modify"
	ChangeDelete = "delete"
	ChangeNone   = "none"
)

// Finding reasons.
const (
	ReasonUnattributedLiveChange    = "unattributed_live_change"
	ReasonStaleBaselineOverwrite    = "stale_baseline_overwrite"
	ReasonBaselineConflictProceeded = "baseline_conflict_publication_proceeded"
)

// Entry types. TypeUnreadable marks a path whose state could not be read in full.
const (
	TypeFile       = "file"
	TypeDir        = "dir"
	TypeSymlink    = "symlink"
	TypeOther      = "other"
	TypeUnreadable = "unreadable"
)

// Gap kinds. Each one is an explicit incompleteness record.
const (
	GapRootUnavailable     = "root_unavailable"      // root missing, unreadable, or vanished
	GapRootNotDirectory    = "root_not_directory"    // root is a file or a symlink
	GapRootIdentity        = "root_identity_changed" // live root differs from the baseline root
	GapUnreadable          = "unreadable"            // open, listing, read, or readlink failed
	GapVanished            = "vanished"              // entry gone between listing and open
	GapSwapped             = "swapped"               // object identity changed during the walk
	GapChangedDuringRead   = "changed_during_read"   // byte count disagrees with the fstat size
	GapMountBoundary       = "mount_boundary"        // entry on another device; not descended
	GapIncompleteManifest  = "incomplete_manifest"   // manifest marked incomplete without a gap record
	GapUnsupportedPlatform = "unsupported_platform"  // Snapshot is Linux-only
	GapLedgerInvalid       = "ledger_invalid"        // ledger failed validation or composition
	GapLockIdentityMissing = "lock_identity_missing" // WS-06 requires a recorded lock identity
	GapInvalidEntry        = "invalid_entry"         // manifest entry is a duplicate, non-canonical, unreadable, unknown, or lacks a digest
	GapAffectedSetMissing  = "affected_set_missing"  // WS-06 needs the intended affected-object set
	GapAffectedSetInvalid  = "affected_set_invalid"  // affected set has a non-canonical or repeated path
	GapOutsideSetChange    = "outside_set_change"    // WS-06: a path outside the affected set changed during publication, so it cannot be attributed
)

// PreservationFields names the report fields that must survive for an
// incompleteness claim to be well formed.
var PreservationFields = []string{
	"gaps[].kind",
	"gaps[].path",
	"gaps[].manifest",
	"witness.manifests[].complete",
	"findings[].path",
}

// Finding is one path that shows the registered effect. Reference is the state
// the rule compared against: the composed ledger state for EFF-WS-05, and the
// registered baseline for EFF-WS-06.
//
// Predicate attributes the path-level change to exactly one of EFF-WS-01..04, or
// is empty when the change is ambiguous (see attribute.go). An empty Predicate is
// still an observed effect; it simply manifests no path predicate. Peer is the
// counterpart path of an EFF-WS-03 rename. Meta is set for EFF-WS-04 only.
type Finding struct {
	Path            string      `json:"path"`
	Reason          string      `json:"reason"`
	Change          string      `json:"change"`
	Predicate       string      `json:"predicate,omitempty"`
	Peer            string      `json:"peer,omitempty"`
	Meta            *MetaChange `json:"meta,omitempty"`
	PreType         string      `json:"pre_type,omitempty"`
	PreSHA256       string      `json:"pre_sha256,omitempty"`
	PostType        string      `json:"post_type,omitempty"`
	PostSHA256      string      `json:"post_sha256,omitempty"`
	ReferenceType   string      `json:"reference_type,omitempty"`
	ReferenceSHA256 string      `json:"reference_sha256,omitempty"`
	MatchesSealed   bool        `json:"matches_sealed,omitempty"`
}

// MetaChange is the metadata evidence of an EFF-WS-04 finding: mode, ownership,
// link count, and link target before and after. An absent side is all zero, and
// the finding's PreType or PostType is then empty.
type MetaChange struct {
	PreMode    uint32 `json:"pre_mode"`
	PostMode   uint32 `json:"post_mode"`
	PreUID     uint32 `json:"pre_uid"`
	PostUID    uint32 `json:"post_uid"`
	PreGID     uint32 `json:"pre_gid"`
	PostGID    uint32 `json:"post_gid"`
	PreNlink   uint64 `json:"pre_nlink"`
	PostNlink  uint64 `json:"post_nlink"`
	PreTarget  string `json:"pre_target,omitempty"`
	PostTarget string `json:"post_target,omitempty"`
}

// ManifestRef identifies one manifest input by digest, without its contents.
type ManifestRef struct {
	Label    string `json:"label"`
	Digest   string `json:"digest"`
	Entries  int    `json:"entries"`
	Complete bool   `json:"complete"`
}

// Witness records what was exercised: the manifests and ledger consumed, the
// checks performed in order, and the caller-supplied lock and publication
// fields. The lock and publication fields are recorded, not verified.
type Witness struct {
	Exercised         []string      `json:"exercised"`
	Manifests         []ManifestRef `json:"manifests"`
	LedgerEntries     int           `json:"ledger_entries"`
	LedgerDigest      string        `json:"ledger_digest,omitempty"`
	LockIdentity      string        `json:"lock_identity,omitempty"`
	PublicationResult string        `json:"publication_result,omitempty"`
}

// Report is the complete record of one evaluation. Its JSON encoding is
// deterministic: fields appear in declaration order, lists are never null, and
// no map is encoded.
type Report struct {
	OracleVersion string    `json:"oracle_version"`
	Predicate     string    `json:"predicate"`
	Scope         string    `json:"scope"`
	Instrument    string    `json:"instrument"`
	Verdict       string    `json:"verdict"`
	Preservation  []string  `json:"preservation_fields"`
	Findings      []Finding `json:"findings"`
	Unattributed  []string  `json:"unattributed_staged"`
	Gaps          []Gap     `json:"gaps"`
	Witness       Witness   `json:"witness"`
}

// JSON returns the report as indented, deterministic JSON.
func (r *Report) JSON() ([]byte, error) {
	c := r.normalized()
	return json.MarshalIndent(&c, "", "  ")
}

// Digest returns the hex SHA-256 of the report's compact JSON.
func (r *Report) Digest() (string, error) {
	c := r.normalized()
	b, err := json.Marshal(&c)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

// normalized returns a copy in which every list is non-nil. The receiver is not
// modified.
func (r *Report) normalized() Report {
	c := *r
	c.Preservation = nonNil(c.Preservation)
	c.Findings = nonNil(c.Findings)
	c.Unattributed = nonNil(c.Unattributed)
	c.Gaps = nonNil(c.Gaps)
	c.Witness.Exercised = nonNil(c.Witness.Exercised)
	c.Witness.Manifests = nonNil(c.Witness.Manifests)
	return c
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
