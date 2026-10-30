package guestevidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"

	"tbound/supervisor/internal/broker/protocol"
)

// Schema is the envelope schema.
const Schema = "tbound-guest-evidence/v1"

// MaxBytes bounds one artifact file.
const MaxBytes = 1 << 20

// The artifact kinds. Each is named by the EVQ mapping.
const (
	KindCapability           = "guest-capability"
	KindNetSink              = "guest-netsink"
	KindProviderExchange     = "guest-provider-exchange"
	KindProcOracleTests      = "guest-tests-procoracle"
	KindLandlockConnectTests = "guest-tests-landlock-connect"
)

// Kinds lists every kind, sorted.
func Kinds() []string {
	return []string{KindAdapterTests, KindCapability, KindNetSink, KindProviderExchange, KindRouteBashView, KindRouteDirectRuntime, KindLandlockConnectTests, KindProcOracleTests}
}

// IsGuestKind reports whether a kind is produced by `tbound-profile guest-evidence` on the guest and
// so bound to the digest of the tbound-profile executable.
func IsGuestKind(kind string) bool { return kind != KindAdapterTests && slices.Contains(Kinds(), kind) }

var (
	commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// IsCommit reports whether s is a 40-digit lowercase hex commit id.
func IsCommit(s string) bool { return commitRE.MatchString(s) }

// IsDigest reports whether s is sha256:<64 lowercase hex>.
func IsDigest(s string) bool { return digestRE.MatchString(s) }

// Expect is what the frozen run requires of every artifact.
type Expect struct {
	// Commit is run.json freeze_commit.
	Commit string
	// BinarySHA256 is the digest of the tbound-profile executable built from Commit (P1).
	BinarySHA256 string
	// FrozenSourceDigest, BaseTemplateDigest, ImageDigest and AdminPubkeyDigest are what a G1 run, a route-probe
	// run and a direct-runtime probe run share with the pilot (see ProfileBinding): run.json pins.frozen_source_digest,
	// pins.base_template_digest (the cell template without its registration digest), v_image_digest and
	// pins.admin_pubkey_digest (the key that verifies the signed host profile of the run). The pilot's own
	// profile_hash and containment_profile_digest are not expected of these artifacts: each of those runs has
	// its own config, registration, template and profile.
	FrozenSourceDigest string
	BaseTemplateDigest string
	ImageDigest        string
	AdminPubkeyDigest  string
	// RuntimeBundleDigest (pins.runtime_bundle_digest) is the Pi runtime bundle of the pilot's signed profile; a run's
	// profile must hold it. RunnerDigest (pins.runner_digest) is the service executable built from the frozen
	// commit; a run's signed registration must name it as its runner (the template's registration digest then
	// carries it into the template, and so into the profile).
	RuntimeBundleDigest string
	RunnerDigest        string
	// CommitTree is the git tree id of Commit as the Go capture recorded it: the adapter run is bound to
	// the commit through it.
	CommitTree string
	// TestBinaries maps a package import path to the sha256 of the test binary the frozen-commit
	// capture built for it (`tbound-run evq-capture`).
	TestBinaries map[string]string
}

type envelope struct {
	Schema       string          `json:"schema"`
	Kind         string          `json:"kind"`
	Commit       string          `json:"commit"`
	BinarySHA256 string          `json:"binary_sha256"`
	Data         json.RawMessage `json:"data"`
}

// DecodeStrict decodes one JSON document into v: a duplicate key, an unknown member and trailing data
// are refused.
func DecodeStrict(data []byte, v any) error {
	if err := checkEvidenceJSON(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

// checkEvidenceJSON bounds the size, then applies the case-fold-aware member rule that the other evidence importers use.
func checkEvidenceJSON(data []byte) error {
	if len(data) == 0 || len(data) > MaxBytes {
		return fmt.Errorf("JSON document size %d is outside 1..%d bytes", len(data), MaxBytes)
	}
	return protocol.ValidateEvidenceJSON(data)
}

// Encode renders an artifact of kind with the given data (a struct of that kind) in the envelope.
func Encode(kind, commit, binarySHA256 string, data any) ([]byte, error) {
	inner, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(envelope{Schema: Schema, Kind: kind, Commit: commit, BinarySHA256: binarySHA256, Data: inner}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Validate checks one artifact file of the given kind against what the frozen run expects. It returns
// nil only for a well-formed artifact of that kind, bound to the commit and the binary, whose values
// show the required facts.
func Validate(kind string, file []byte, e Expect) error {
	if !slices.Contains(Kinds(), kind) {
		return fmt.Errorf("guest artifact kind %q is unknown", kind)
	}
	if len(file) == 0 || len(file) > MaxBytes {
		return fmt.Errorf("guest artifact %s: size %d is outside 1..%d bytes", kind, len(file), MaxBytes)
	}
	if !IsCommit(e.Commit) || (IsGuestKind(kind) && !IsDigest(e.BinarySHA256)) {
		return fmt.Errorf("guest artifact %s: the expected commit and binary digest are not given", kind)
	}
	var env envelope
	if err := DecodeStrict(file, &env); err != nil {
		return fmt.Errorf("guest artifact %s: %w", kind, err)
	}
	switch {
	case env.Schema != Schema:
		return fmt.Errorf("guest artifact %s: schema %q, want %q", kind, env.Schema, Schema)
	case env.Kind != kind:
		return fmt.Errorf("guest artifact %s: it is a %q document", kind, env.Kind)
	case env.Commit != e.Commit:
		return fmt.Errorf("guest artifact %s: made for commit %q, the run is frozen at %q", kind, env.Commit, e.Commit)
	case !IsDigest(env.BinarySHA256):
		return fmt.Errorf("guest artifact %s: the producer binary digest %q is malformed", kind, env.BinarySHA256)
	case IsGuestKind(kind) && env.BinarySHA256 != e.BinarySHA256:
		return fmt.Errorf("guest artifact %s: written by binary %q, the frozen build is %q", kind, env.BinarySHA256, e.BinarySHA256)
	case len(env.Data) == 0:
		return fmt.Errorf("guest artifact %s: no data", kind)
	}
	var err error
	switch kind {
	case KindAdapterTests:
		err = validateAdapter(env.Data, e)
	case KindCapability:
		err = validateCapability(env.Data)
	case KindNetSink:
		err = validateNetSink(env.Data)
	case KindProviderExchange:
		err = validateProviderExchange(env.Data, e)
	case KindRouteBashView, KindRouteDirectRuntime:
		err = validateRoute(kind, env.Data, e)
	case KindProcOracleTests, KindLandlockConnectTests:
		err = validateTests(kind, env.Data, e)
	}
	if err != nil {
		return fmt.Errorf("guest artifact %s: %w", kind, err)
	}
	return nil
}
