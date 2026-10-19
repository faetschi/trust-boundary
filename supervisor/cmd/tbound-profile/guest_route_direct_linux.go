//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
)

// guest-route-direct-runtime: the denied reads of the registered outside-workspace target made from
// inside the Pi runtime cell.
//
// The attempts are made by the signed entrypoint of a cell launched from a route-probe template (the
// production cell template plus a route_probe member; see internal/piruntime/d09_route_probe.go). The
// entrypoint reports them in its TCB proof, which the production session journals as a
// `production_tcb_proof` event of the run (`<audit-root>/<run id>-session-audit.jsonl`) before it does
// anything else with the cell. This producer reads that journal, as the bash-view producer reads the
// command audit, and checks:
//
//   - the journal's hash chain, and exactly one proof event, of this run and the pinned profile, whose
//     recorded digest is the digest of the proof it carries;
//   - the proof against the probe template (image, template, Landlock ruleset, user mapping, empty
//     capability set, no_new_privs, seccomp filter, cgroup v2, verified mounts, no route out, PID 1);
//   - that the probe template is the production template plus the probe member and nothing else, so the
//     probe ran in the cell a production Pi run runs in;
//   - that the probe the cell ran is the one asked for (target, control) and complete;
//   - what root sees of the target from outside the cell: a non-empty regular file, readable by other
//     users, in a directory they can list, so that only the cell's confinement keeps the bytes from it.
//
// The attempts are stated by guestevidence.DirectRuntimeAttempts and the validator then requires every open
// and listing to be refused with EACCES or EPERM.
//
// A real in-cell run needs the guest; this producer, the entrypoint's probe mode and the validator are
// exercised offline against a journal built from the same structures.

const (
	tcbProofKind   = "production_tcb_proof"
	tcbProofSchema = "tbound-production-launch-tcb-proof/v1"
)

// tcbProofEvent is the part of the journaled event this producer reads.
type tcbProofEvent struct {
	SchemaVersion string                `json:"schema_version"`
	RunID         string                `json:"run_id"`
	ProfileDigest string                `json:"profile_digest"`
	ProofDigest   string                `json:"proof_digest"`
	Proof         piruntime.D09TCBProof `json:"proof"`
}

func readBoundedFile(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, fmt.Errorf("%s is unreadable or larger than %d bytes", path, limit)
	}
	return data, nil
}

func probeRouteDirectRuntime(o routeProbeOptions, env routeProbeEnv) (guestevidence.RouteDenied, string, error) {
	var d guestevidence.RouteDenied
	kind := guestevidence.KindRouteDirectRuntime
	if o.runID == "" || !filepath.IsAbs(o.auditRoot) || !filepath.IsAbs(o.target) || o.controlPath == "" || filepath.IsAbs(o.controlPath) ||
		o.profileHash == "" || o.frozenSource == "" || o.probeTemplate == "" || o.productionTemplate == "" || !filepath.IsAbs(o.targetRoot) {
		return d, "", errors.New("direct-runtime needs --run-id, an absolute --audit-root, an absolute --target, a relative --control-path, --profile-hash, --frozen-source-digest, " +
			"--probe-template, --production-template and an absolute --target-root")
	}

	probeTemplate, probeDigest, err := loadTemplateFile(env, o.probeTemplate)
	if err != nil {
		return d, "", fmt.Errorf("--probe-template: %w", err)
	}
	productionTemplate, productionDigest, err := loadTemplateFile(env, o.productionTemplate)
	if err != nil {
		return d, "", fmt.Errorf("--production-template: %w", err)
	}
	if probeTemplate.RouteProbe == nil {
		return d, "", errors.New("--probe-template names no route probe")
	}
	if productionTemplate.RouteProbe != nil {
		return d, "", errors.New("--production-template names a route probe: it must be the template a production Pi run uses")
	}
	stripped := probeTemplate
	stripped.RouteProbe = nil
	if strippedDigest, err := stripped.Digest(); err != nil || strippedDigest != productionDigest {
		return d, "", errors.New("the probe template differs from the production template in more than the route_probe member: the probe did not run in the production cell")
	}
	probe := *probeTemplate.RouteProbe
	if probe.Target != o.target || probe.Control != "/work/"+o.controlPath {
		return d, "", fmt.Errorf("the probe template asks for target %q and control %q, not %q and %q", probe.Target, probe.Control, o.target, "/work/"+o.controlPath)
	}

	raw, err := env.readJournal(filepath.Join(o.auditRoot, o.runID+"-session-audit.jsonl"))
	if err != nil {
		return d, "", err
	}
	trace, err := audit.Verify(bytes.NewReader(raw))
	if err != nil {
		return d, "", fmt.Errorf("the session audit journal does not verify: %w", err)
	}
	if len(trace.Records) == 0 {
		return d, "", errors.New("the session audit journal is empty")
	}
	var event tcbProofEvent
	var sequence uint64
	for _, rec := range trace.Records {
		if rec.Event.Kind != tcbProofKind {
			continue
		}
		if sequence != 0 {
			return d, "", fmt.Errorf("the journal holds two launch proofs (records %d and %d)", sequence, rec.Sequence)
		}
		if err := decodeStrictAllowUnknown(rec.Event.Data, &event); err != nil {
			return d, "", fmt.Errorf("journal record %d: %w", rec.Sequence, err)
		}
		sequence = rec.Sequence
	}
	if sequence == 0 {
		return d, "", errors.New("the journal holds no launch TCB proof: the probe cell was not launched, or not by the production session")
	}
	if event.SchemaVersion != tcbProofSchema || event.RunID != o.runID {
		return d, "", fmt.Errorf("journal record %d is a launch proof of schema %q for run %q", sequence, event.SchemaVersion, event.RunID)
	}
	if event.ProfileDigest != o.profileHash {
		return d, "", fmt.Errorf("the probe ran under profile %q, the run pins %q", event.ProfileDigest, o.profileHash)
	}
	encodedProof, err := json.Marshal(event.Proof)
	if err != nil {
		return d, "", err
	}
	proofDigest := piruntime.DigestBytes(encodedProof)
	if event.ProofDigest != proofDigest {
		return d, "", fmt.Errorf("the journaled proof has digest %s, its event states %s", proofDigest, event.ProofDigest)
	}
	if err := checkProbeCellProof(event.Proof, probeTemplate, probeDigest); err != nil {
		return d, "", fmt.Errorf("journal record %d: %w", sequence, err)
	}
	result := event.Proof.RouteProbe
	if result == nil {
		return d, "", errors.New("the launch proof carries no route probe result: the cell was not a probe cell")
	}
	if err := result.CheckShape(probe); err != nil {
		return d, "", err
	}

	fact, err := targetFact(env, o.targetRoot, o.target)
	if err != nil {
		return d, "", err
	}
	observed := make([]guestevidence.RouteDirectObservation, len(result.Attempts))
	for i, a := range result.Attempts {
		observed[i] = guestevidence.RouteDirectObservation{Method: a.Method, Op: a.Op, Errno: a.Errno}
	}
	control, attempts, err := guestevidence.DirectRuntimeAttempts(o.target, probe.Control, event.Proof.ContainerName, proofDigest, sequence, result.ControlRead.Errno, observed)
	if err != nil {
		return d, "", err
	}
	d = guestevidence.RouteDenied{RunID: o.runID, ProfileHash: o.profileHash, FrozenSourceDigest: o.frozenSource, Target: o.target,
		Cell: guestevidence.RouteCell{Kind: guestevidence.RouteCellPiRuntime, ImageDigest: event.Proof.ImageDigest, TemplateDigest: event.Proof.TemplateDigest,
			LandlockRulesetDigest: event.Proof.LandlockRulesetDigest, ProductionTemplateDigest: productionDigest},
		Journal:    guestevidence.RouteJournal{Records: uint64(len(trace.Records)), HeadHash: trace.Records[len(trace.Records)-1].Hash},
		TargetFact: &fact, Control: control, Attempts: attempts}
	return d, kind, nil
}

func loadTemplateFile(env routeProbeEnv, path string) (piruntime.D09CellTemplate, string, error) {
	raw, err := env.readFile(path)
	if err != nil {
		return piruntime.D09CellTemplate{}, "", err
	}
	template, err := piruntime.DecodeD09CellTemplate(raw)
	if err != nil {
		return piruntime.D09CellTemplate{}, "", err
	}
	digest, err := template.Digest()
	return template, digest, err
}

// checkProbeCellProof requires the proof to show the confined Pi runtime cell of the probe template.
func checkProbeCellProof(p piruntime.D09TCBProof, t piruntime.D09CellTemplate, templateDigest string) error {
	switch {
	case p.Schema != "tbound-pi-entrypoint/v1" || p.UID != t.CellUID || p.GID != t.CellGID:
		return errors.New("the proof is not the Pi entrypoint's, or its identity is not the template's cell identity")
	case p.TemplateDigest != templateDigest || p.ImageDigest != t.ExpectedImageDigest || p.LandlockRulesetDigest != t.LandlockRulesetDigest || p.LandlockABI < t.LandlockABI:
		return errors.New("the proof's template, image or Landlock ruleset is not the probe template's")
	case p.CapEff != "0000000000000000" || !p.NoNewPrivs || p.Seccomp != 2 || !p.LandlockTSYNC || !p.CgroupV2:
		return errors.New("the proof does not show an empty capability set, no_new_privs, seccomp filter mode, Landlock and cgroup v2")
	case !p.ConsumerMountsVerified || !p.RawSourceFDsClosed || !p.NoExternalRoutes || p.ContainerLeaderPID != 1:
		return errors.New("the proof does not show verified mounts, closed source descriptors, no external route and the entrypoint as PID 1")
	case p.ContainerName == "" || p.LeaderPID <= 0:
		return errors.New("the proof names no container")
	}
	return nil
}

// targetFact is what root sees of the target in the image tree rooted at root: it is looked at from
// outside the cell, as the user running this command.
func targetFact(env routeProbeEnv, root, target string) (guestevidence.RouteTarget, error) {
	var fact guestevidence.RouteTarget
	info, err := env.stat(filepath.Join(root, target))
	if err != nil {
		return fact, fmt.Errorf("the target is not visible from outside the cell (%v): it must exist for a denial to mean anything", err)
	}
	parent, err := env.stat(filepath.Dir(filepath.Join(root, target)))
	if err != nil {
		return fact, err
	}
	fact = guestevidence.RouteTarget{Regular: info.Mode().IsRegular(), Mode: uint32(info.Mode().Perm()), Size: info.Size(), ParentMode: uint32(parent.Mode().Perm())}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		fact.UID, fact.GID = st.Uid, st.Gid
	}
	return fact, nil
}
