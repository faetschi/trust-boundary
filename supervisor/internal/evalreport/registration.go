package evalreport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// RegistrationDomain labels the pre-registration record digest. v2 added the run binding, the attribution
// rubric and the corpus-check report (H1-6).
const RegistrationDomain = "tbound-preregistration/v2"

// Registration is the pre-registration record that `tbound-profile preregistration-record` writes: the reviewable
// summary of what the registered freeze pins. Every field except RecordDigest is bound by RecordDigest. An absent
// research profile or ledger configuration is the empty string, which is distinct from every digest.
type Registration struct {
	Domain       string `json:"domain"`
	FreezeCommit string `json:"freeze_commit"`
	// CorpusManifestDigest is evalcorpus.Digest of the manifest, as pinned in the service config.
	CorpusManifestDigest string `json:"corpus_manifest_digest"`
	QuotaProfile         string `json:"quota_profile"`
	// RunnerPlanDigest is the digest of runner.Plan over the manifest, its seeded allocation and the runner config.
	RunnerPlanDigest string `json:"runner_plan_digest"`
	// PlanSteps is the number of steps that plan holds. PlannedUnits is the registered number of units
	// (PlannedUnits); under the thesis profile the two must agree.
	PlanSteps     int `json:"plan_steps"`
	PlannedUnits  int `json:"planned_units"`
	RerunHeadroom int `json:"rerun_headroom"`
	// EvalscoreSchema is evalscore.SchemaVersion.
	EvalscoreSchema            string `json:"evalscore_schema"`
	ResearchProfileDigest      string `json:"research_profile_digest"`
	ResearchLedgerConfigDigest string `json:"research_ledger_config_digest"`
	// RunBinding is the `tbound-run plan` run binding: receipt pins, route and EVQ results digests, admission policy,
	// V image and the PROC settings, the D49 drain grace among them.
	RunBinding string `json:"run_binding"`
	// AttributionRubricDigest is sha256:<hex> of the frozen attribution rubric file; the attribution file of the
	// run must name it as its rubric_digest.
	AttributionRubricDigest string `json:"attribution_rubric_digest"`
	// CorpusCheckDigest is sha256:<hex> of the `tbound-profile corpus-check` report.json of the manifest, accepted
	// only with no failed and no guest-only row.
	CorpusCheckDigest string `json:"corpus_check_digest"`
	// Reviewers are names only, sorted.
	Reviewers    []string `json:"reviewers"`
	RecordDigest string   `json:"record_digest,omitempty"`
}

// ComputeDigest returns sha256:<hex> over the domain label, a NUL byte and the compact JSON of the record without
// its RecordDigest. The field order of the struct is the canonical order.
func (r Registration) ComputeDigest() (string, error) {
	r.RecordDigest = ""
	body, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(RegistrationDomain+"\x00"), body...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ParseRegistration decodes one record strictly and accepts it only if its digest recomputes. The caller checks
// duplicate keys first (protocol.ValidateStrictJSON), as for every pinned input.
func ParseRegistration(data []byte) (Registration, error) {
	var r Registration
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Registration{}, fmt.Errorf("registration record: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Registration{}, errors.New("registration record: trailing data")
	}
	if r.Domain != RegistrationDomain {
		return Registration{}, fmt.Errorf("registration record: domain %q, want %q", r.Domain, RegistrationDomain)
	}
	want, err := r.ComputeDigest()
	if err != nil {
		return Registration{}, err
	}
	if r.RecordDigest != want {
		return Registration{}, fmt.Errorf("registration record: record_digest %q does not recompute (%s)", r.RecordDigest, want)
	}
	return r, nil
}
