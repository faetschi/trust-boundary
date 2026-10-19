package guestevidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"tbound/supervisor/internal/broker/protocol"
)

// RunBinding is what run.json pins for the evidence: the freeze commit and the receipt pins every artifact
// is checked against. `tbound-run` and the guest producer that must not take these from the caller read it
// with ParseRunBinding, so they cannot disagree about which members count.
type RunBinding struct {
	Commit                   string
	ProfileHash              string
	ContainmentProfileDigest string
	FrozenSourceDigest       string
}

// ParseRunBinding reads the freeze commit and the receipt pins of run.json. It reads only those members and
// does not validate the rest: the file may not yet name the evq_results the evidence produces. A duplicate
// key, trailing data and a malformed freeze commit are refused.
func ParseRunBinding(data []byte) (RunBinding, error) {
	var doc struct {
		FreezeCommit string `json:"freeze_commit"`
		Pins         struct {
			ProfileHash              string `json:"profile_hash"`
			ContainmentProfileDigest string `json:"containment_profile_digest"`
			FrozenSourceDigest       string `json:"frozen_source_digest"`
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
	return RunBinding{Commit: doc.FreezeCommit, ProfileHash: doc.Pins.ProfileHash,
		ContainmentProfileDigest: doc.Pins.ContainmentProfileDigest, FrozenSourceDigest: doc.Pins.FrozenSourceDigest}, nil
}
