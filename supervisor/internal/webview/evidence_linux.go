//go:build linux

package webview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"tbound/supervisor/internal/sessionrepo"
)

func reconstructGenerationProjection(raw []byte) (GenerationProjection, error) {
	if len(raw) == 0 || len(raw) > maxEvidenceBundleBytes {
		return GenerationProjection{}, errors.New("evidence bundle is empty or exceeds the 32 MiB projection bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle sessionrepo.EvidenceBundle
	if err := decoder.Decode(&bundle); err != nil {
		return GenerationProjection{}, fmt.Errorf("decode session-repository evidence bundle: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return GenerationProjection{}, errors.New("evidence bundle contains trailing JSON data")
	}
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil {
		return GenerationProjection{}, fmt.Errorf("sessionrepo.Reconstruct: %w", err)
	}
	projection := GenerationProjection{
		SchemaVersion: "tbound-generation-projection/v1", Status: "reconstructed",
		Schema: disposition.SchemaVersion, Baseline: disposition.Baseline, Sealed: disposition.Sealed,
		AuditRecords: disposition.AuditRecordCount, AuditHeadHash: disposition.AuditHeadHash,
		Attempts: disposition.Attempts, Decisions: disposition.Decisions, Effects: disposition.Effects,
		Successes: disposition.Successes, Unknowns: disposition.Unknowns, Denials: disposition.Denials,
		Quarantined: disposition.Quarantined,
		Limitations: []string{
			"structural evidence reconstruction only; this projection is not an authorization verdict",
			"external policy-decision authenticity and command-runner settlement are not re-authenticated by Reconstruct",
			"bundle claims do not establish host containment or a real provider exchange",
		},
	}
	if !bundle.RealProviderExchange {
		projection.Limitations = append(projection.Limitations, "bundle records no real provider exchange")
	}
	if !bundle.PiAdapterWired {
		projection.Limitations = append(projection.Limitations, "bundle records no Pi adapter wiring")
	}
	if bundle.CommandContainmentStatus != "established" {
		projection.Limitations = append(projection.Limitations, "command containment is not established")
	}
	projection.Generations = make([]string, 0, len(disposition.Generations))
	for _, generation := range disposition.Generations {
		projection.Generations = append(projection.Generations, generation.ID)
	}
	return projection, nil
}
