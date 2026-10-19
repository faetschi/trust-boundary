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
	return []string{KindCapability, KindNetSink, KindProviderExchange, KindLandlockConnectTests, KindProcOracleTests}
}

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
	// ProfileHash and FrozenSourceDigest are run.json pins.profile_hash and pins.frozen_source_digest:
	// the receipt of a real provider exchange must carry both.
	ProfileHash        string
	FrozenSourceDigest string
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
	if err := protocol.ValidateStrictJSON(data); err != nil {
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
	if !IsCommit(e.Commit) || !IsDigest(e.BinarySHA256) {
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
	case env.BinarySHA256 != e.BinarySHA256:
		return fmt.Errorf("guest artifact %s: written by binary %q, the frozen build is %q", kind, env.BinarySHA256, e.BinarySHA256)
	case len(env.Data) == 0:
		return fmt.Errorf("guest artifact %s: no data", kind)
	}
	var err error
	switch kind {
	case KindCapability:
		err = validateCapability(env.Data)
	case KindNetSink:
		err = validateNetSink(env.Data)
	case KindProviderExchange:
		err = validateProviderExchange(env.Data, e)
	case KindProcOracleTests, KindLandlockConnectTests:
		err = validateTests(kind, env.Data, e)
	}
	if err != nil {
		return fmt.Errorf("guest artifact %s: %w", kind, err)
	}
	return nil
}
