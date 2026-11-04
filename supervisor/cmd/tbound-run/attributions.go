package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/runner"
)

const attributionsSchema = "tbound-attributions/v1"

// attributionFile is the pinned attribution input of the friction candidates
// (B succeeds, C fails): one entry per task, decided under the frozen rubric
// the file names. A supported or unsupported entry needs evidence.
type attributionFile struct {
	Schema       string             `json:"schema"`
	RubricDigest string             `json:"rubric_digest"`
	Entries      []attributionEntry `json:"entries"`
}

type attributionEntry struct {
	TaskID          string                `json:"task_id"`
	Attribution     evalscore.Attribution `json:"attribution"`
	EvidenceDigests []string              `json:"evidence_digests"`
	Note            string                `json:"note"`
}

// attributionSet is what a loaded file gives the export and the report. The zero
// value means no file: every candidate stays unresolved and the report digests are empty.
type attributionSet struct {
	digest, rubricDigest string
	byTask               map[string]evalscore.Attribution
}

// loadAttributions reads the pinned file strictly. A changed file is refused, never re-pinned.
func loadAttributions(pin pinnedFile) (attributionSet, error) {
	if pin.Path == "" {
		return attributionSet{}, nil
	}
	f, err := os.Open(pin.Path)
	if err != nil {
		return attributionSet{}, fmt.Errorf("attributions: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return attributionSet{}, fmt.Errorf("attributions: %s is not a regular file", pin.Path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return attributionSet{}, errors.New("attributions: unreadable or too large")
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != pin.SHA256 {
		return attributionSet{}, fmt.Errorf("attributions: %s hashes to %s, run.json pins %s", pin.Path, digest, pin.SHA256)
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return attributionSet{}, fmt.Errorf("attributions: %w", err)
	}
	var file attributionFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return attributionSet{}, fmt.Errorf("attributions: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return attributionSet{}, errors.New("attributions: trailing data")
	}
	if file.Schema != attributionsSchema {
		return attributionSet{}, fmt.Errorf("attributions: schema %q, want %q", file.Schema, attributionsSchema)
	}
	if !digestRE.MatchString(file.RubricDigest) {
		return attributionSet{}, errors.New("attributions: rubric_digest must be sha256:<64 hex>")
	}
	set := attributionSet{digest: digest, rubricDigest: file.RubricDigest, byTask: map[string]evalscore.Attribution{}}
	for i, e := range file.Entries {
		if e.TaskID == "" {
			return attributionSet{}, fmt.Errorf("attributions: entry %d has no task_id", i)
		}
		if _, dup := set.byTask[e.TaskID]; dup {
			return attributionSet{}, fmt.Errorf("attributions: duplicate task_id %q", e.TaskID)
		}
		switch e.Attribution {
		case evalscore.AttributionSupported, evalscore.AttributionUnsupported:
			if len(e.EvidenceDigests) == 0 {
				return attributionSet{}, fmt.Errorf("attributions: %q is %s without an evidence digest", e.TaskID, e.Attribution)
			}
		case evalscore.AttributionUnresolved:
		default:
			return attributionSet{}, fmt.Errorf("attributions: %q has attribution %q: use supported, unsupported or unresolved", e.TaskID, e.Attribution)
		}
		for _, d := range e.EvidenceDigests {
			if !digestRE.MatchString(d) {
				return attributionSet{}, fmt.Errorf("attributions: %q has evidence digest %q, want sha256:<64 hex>", e.TaskID, d)
			}
		}
		set.byTask[e.TaskID] = e.Attribution
	}
	return set, nil
}

// checkCandidates refuses an entry for a task that is not a candidate obstruction
// (B succeeds, C fails) in any exported attempt: an attribution cannot be attached to one.
func (s attributionSet) checkCandidates(res runner.ExportResult) error {
	candidate := map[string]bool{}
	for _, r := range res.Input.Compliant {
		if r.BSuccess && !r.CSuccess {
			candidate[r.TaskID] = true
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.byTask)) {
		if !candidate[id] {
			return fmt.Errorf("attributions: %q is not a candidate obstruction in any exported attempt", id)
		}
	}
	return nil
}
