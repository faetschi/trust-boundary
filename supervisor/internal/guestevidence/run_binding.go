package guestevidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"tbound/supervisor/internal/broker/protocol"
)

// RunBinding is what run.json pins for the evidence: the freeze commit and the pins every artifact is
// checked against. `tbound-run` and the guest producers that must not take these from the caller read it
// with ParseRunBinding, so they cannot disagree about which members count.
type RunBinding struct {
	Commit string
	// FrozenSourceDigest, BaseTemplateDigest, ImageDigest, AdminPubkeyDigest, RuntimeBundleDigest and RunnerDigest
	// are what the G1, route-probe and direct-runtime probe runs share with the pilot (ProfileBinding). The pilot's own profile_hash and
	// containment_profile_digest are receipt pins (runner.ReceiptPins), not part of this binding.
	FrozenSourceDigest string
	BaseTemplateDigest string
	ImageDigest        string
	// AdminPubkeyDigest is the digest of the admin public key file that verifies the signed host profiles.
	AdminPubkeyDigest string
	// RuntimeBundleDigest is the Pi runtime bundle the runs share with the pilot (the signed profiles'
	// PiRuntimeBundleDigest, BUNDLE_DIGEST). RunnerDigest is the digest of the service executable (`tbound`) the
	// owner recorded from the reproducible build of the frozen commit (TBOUND_DIGEST); each run's signed
	// registration must name it as its runner.
	RuntimeBundleDigest string
	RunnerDigest        string
}

// ParseRunBinding reads the freeze commit and the pins of run.json. It reads only those members and
// does not validate the rest: the file may not yet name the evq_results the evidence produces. A duplicate
// key, trailing data and a malformed freeze commit are refused.
func ParseRunBinding(data []byte) (RunBinding, error) {
	var doc struct {
		FreezeCommit string `json:"freeze_commit"`
		VImageDigest string `json:"v_image_digest"`
		Pins         struct {
			FrozenSourceDigest string `json:"frozen_source_digest"`
			BaseTemplateDigest string `json:"base_template_digest"`
			AdminPubkeyDigest  string `json:"admin_pubkey_digest"`
			RuntimeBundle      string `json:"runtime_bundle_digest"`
			RunnerDigest       string `json:"runner_digest"`
		} `json:"pins"`
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return RunBinding{}, fmt.Errorf("config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		return RunBinding{}, fmt.Errorf("config: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return RunBinding{}, errors.New("config: trailing data")
	}
	if !IsCommit(doc.FreezeCommit) {
		return RunBinding{}, fmt.Errorf("config: freeze_commit %q is not 40 lowercase hex digits", doc.FreezeCommit)
	}
	return RunBinding{Commit: doc.FreezeCommit, FrozenSourceDigest: doc.Pins.FrozenSourceDigest, BaseTemplateDigest: doc.Pins.BaseTemplateDigest,
		ImageDigest: doc.VImageDigest, AdminPubkeyDigest: doc.Pins.AdminPubkeyDigest,
		RuntimeBundleDigest: doc.Pins.RuntimeBundle, RunnerDigest: doc.Pins.RunnerDigest}, nil
}
