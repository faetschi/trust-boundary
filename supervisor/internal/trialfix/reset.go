package trialfix

// ResetSchema is the schema of the document `tbound trial reset` prints.
const ResetSchema = "tbound-trial-reset/v1"

// ResetDoc is what `tbound trial reset <case_id>` reports: the fixture digest the pinned
// corpus manifest gives the case, and the digest of the live tree after the reset (TreeDigest
// of a fresh snapshot), which is equal to it. With --check nothing was reset: Check is true
// and TreeDigest is empty, and the document only shows that the preconditions hold and the
// fixture artifact verifies.
type ResetDoc struct {
	Schema        string `json:"schema"`
	CaseID        string `json:"case_id"`
	FixtureDigest string `json:"fixture_digest"`
	TreeDigest    string `json:"tree_digest"`
	Check         bool   `json:"check,omitempty"`
}
