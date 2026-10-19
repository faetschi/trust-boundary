//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
//   - the journal's hash chain, and exactly one proof event, of this run, whose recorded digest is the
//     digest of the proof it carries;
//   - the signed host profile the probe run used: its digest is the profile digest the proof event records
//     (so the profile files are the service's own), its containment profile digest is the probe template's,
//     and it is the production profile plus the probe template and nothing else: with the production
//     template's digest put back, it hashes to the run's pinned profile_hash. The frozen source digest is
//     read from that profile (source_provenance_digest) and must equal the run's pin, and the production
//     template must be the one the run pins (containment_profile_digest). The pins come from run.json, the
//     file that also names the freeze commit, so the artifact says nothing the caller asserts. The public key
//     that verifies the profile must be the admin key the run pins: the key file is hashed and compared with
//     pins.admin_pubkey_digest before it verifies anything, so a caller cannot choose the key;
//   - the proof against the probe template (image, template, Landlock ruleset, user mapping, empty
//     capability set, no_new_privs, seccomp filter, cgroup v2, verified mounts, no route out, PID 1);
//   - that the probe template is the production template plus the probe member and nothing else, so the
//     probe ran in the cell a production Pi run runs in;
//   - that the probe the cell ran is the one asked for (target, control) and complete;
//   - what the probe measured inside the cell before its attempts (internal/cellview): the lstat facts of every
//     component of the target's path as the cell's own mounts show them, and the identity of the probe. The
//     validator computes ordinary file permissions from them and requires them to allow search of every
//     directory the walk passes and read of the file and of its directory, with no POSIX ACL, so that only
//     the cell's confinement keeps the bytes from it. Nothing is read from the image or from a directory the
//     operator points at.
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
	ProfileID     string                `json:"profile_id"`
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
		!guestevidence.IsDigest(o.profileHash) || !guestevidence.IsDigest(o.frozenSource) || !guestevidence.IsDigest(o.containmentDigest) ||
		!guestevidence.IsDigest(o.adminPubkeyDigest) || o.probeTemplate == "" || o.productionTemplate == "" || o.hostProfile == "" || o.hostProfileSig == "" || o.hostProfileKey == "" {
		return d, "", errors.New("direct-runtime needs --run-id, an absolute --audit-root, an absolute --target, a relative --control-path, the run pins (profile_hash, containment_profile_digest, " +
			"frozen_source_digest, admin_pubkey_digest, from --run-config), --probe-template, --production-template and the signed host profile of the probe run (--host-profile, --host-profile-signature, --host-profile-key)")
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
	profile, err := verifyProbeHostProfile(env, o)
	if err != nil {
		return d, "", fmt.Errorf("the signed host profile of the probe run: %w", err)
	}
	if err := checkProbeProfile(profile, event, o, probeDigest, productionDigest); err != nil {
		return d, "", err
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
	view := result.View

	observed := make([]guestevidence.RouteDirectObservation, len(result.Attempts))
	for i, a := range result.Attempts {
		observed[i] = guestevidence.RouteDirectObservation{Method: a.Method, Op: a.Op, Errno: a.Errno}
	}
	control, attempts, err := guestevidence.DirectRuntimeAttempts(o.target, probe.Control, event.Proof.ContainerName, proofDigest, sequence, result.ControlRead.Errno, observed)
	if err != nil {
		return d, "", err
	}
	d = guestevidence.RouteDenied{RunID: o.runID, ProfileHash: o.profileHash, FrozenSourceDigest: profile.SourceProvenanceDigest, Target: o.target,
		ProbeProfileDigest: profile.Digest, ProfileSourceDigest: profile.SourceProvenanceDigest,
		Cell: guestevidence.RouteCell{Kind: guestevidence.RouteCellPiRuntime, ImageDigest: event.Proof.ImageDigest, TemplateDigest: event.Proof.TemplateDigest,
			LandlockRulesetDigest: event.Proof.LandlockRulesetDigest, ProductionTemplateDigest: productionDigest,
			CellUID: uint32(probeTemplate.CellUID), CellGID: uint32(probeTemplate.CellGID)},
		Journal: guestevidence.RouteJournal{Records: uint64(len(trace.Records)), HeadHash: trace.Records[len(trace.Records)-1].Hash},
		View:    &view, AdminPubkeyDigest: o.adminPubkeyDigest, Limits: guestevidence.DirectRuntimeLimits,
		Control: control, Attempts: attempts}
	return d, kind, nil
}

// verifyProbeHostProfile verifies the signed host profile of the probe run under the admin public key the run
// pins. The key is read once: the bytes that hash to the pinned digest are the bytes that verify the profile.
func verifyProbeHostProfile(env routeProbeEnv, o routeProbeOptions) (piruntime.HostProfile, error) {
	key, err := env.readFile(o.hostProfileKey)
	if err != nil {
		return piruntime.HostProfile{}, fmt.Errorf("--host-profile-key: %w", err)
	}
	if got := piruntime.DigestBytes(key); got != o.adminPubkeyDigest {
		return piruntime.HostProfile{}, fmt.Errorf("--host-profile-key hashes to %s, the run pins admin_pubkey_digest %s: the profile is not verified under the admin key of the run", got, o.adminPubkeyDigest)
	}
	profileBytes, err := env.readFile(o.hostProfile)
	if err != nil {
		return piruntime.HostProfile{}, fmt.Errorf("--host-profile: %w", err)
	}
	signature, err := env.readFile(o.hostProfileSig)
	if err != nil {
		return piruntime.HostProfile{}, fmt.Errorf("--host-profile-signature: %w", err)
	}
	return piruntime.VerifyLinuxSignedHostProfile(profileBytes, signature, ed25519.PublicKey(key))
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

// checkProbeProfile ties the run's identity to the signed host profile the service journaled: the profile
// is the one whose digest the proof event records, it names the probe template, it is the production profile
// plus the probe template and nothing else, and it holds the pinned frozen source.
func checkProbeProfile(profile piruntime.HostProfile, event tcbProofEvent, o routeProbeOptions, probeDigest, productionDigest string) error {
	if event.ProfileDigest != profile.Digest || event.ProfileID != profile.ID {
		return fmt.Errorf("the journaled launch proof is of profile %q (%s), the signed profile given is %q (%s): not the profile the service ran",
			event.ProfileID, event.ProfileDigest, profile.ID, profile.Digest)
	}
	if profile.ContainmentProfileDigest != probeDigest {
		return fmt.Errorf("the profile names containment template %s, the probe template is %s", profile.ContainmentProfileDigest, probeDigest)
	}
	production := profile
	production.ContainmentProfileDigest = productionDigest
	signing, err := piruntime.LinuxHostProfileSigningBytes(production)
	if err != nil {
		return err
	}
	if got := piruntime.DigestBytes(signing); got != o.profileHash {
		return fmt.Errorf("the probe profile with the production template put back hashes to %s, the run pins profile_hash %s: the probe did not run under the production profile", got, o.profileHash)
	}
	if productionDigest != o.containmentDigest {
		return fmt.Errorf("the production template is %s, the run pins containment_profile_digest %s", productionDigest, o.containmentDigest)
	}
	if profile.SourceProvenanceDigest != o.frozenSource {
		return fmt.Errorf("the profile the probe ran under holds source %s, the run pins frozen_source_digest %s", profile.SourceProvenanceDigest, o.frozenSource)
	}
	return nil
}

// directRuntimePins reads the expectations of a direct-runtime artifact from run.json. The freeze commit in
// the file must be --commit, and --profile-hash and --frozen-source-digest, which the other producers take,
// are accepted only if they repeat the file. Every pin the producer compares the journal with must be there.
func directRuntimePins(runConfig, commit, profileHash, frozenSource string) (guestevidence.RunBinding, error) {
	if runConfig == "" {
		return guestevidence.RunBinding{}, errors.New("direct-runtime needs --run-config: the expectations come from run.json, not from flags")
	}
	data, err := readBoundedFile(runConfig, maxGuestInputSize)
	if err != nil {
		return guestevidence.RunBinding{}, fmt.Errorf("--run-config: %w", err)
	}
	b, err := guestevidence.ParseRunBinding(data)
	if err != nil {
		return b, err
	}
	switch {
	case b.Commit != commit:
		return b, fmt.Errorf("run.json is frozen at %s, --commit is %s", b.Commit, commit)
	case profileHash != "" && profileHash != b.ProfileHash, frozenSource != "" && frozenSource != b.FrozenSourceDigest:
		return b, errors.New("--profile-hash or --frozen-source-digest differs from run.json pins")
	case !guestevidence.IsDigest(b.ProfileHash) || !guestevidence.IsDigest(b.ContainmentProfileDigest) || !guestevidence.IsDigest(b.FrozenSourceDigest) || !guestevidence.IsDigest(b.AdminPubkeyDigest):
		return b, errors.New("run.json pins need profile_hash, containment_profile_digest, frozen_source_digest and admin_pubkey_digest")
	}
	return b, nil
}
