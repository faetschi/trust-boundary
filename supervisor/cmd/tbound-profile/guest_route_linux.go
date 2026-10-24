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

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

// The route artifacts of EVQ's exposure matrix (E06) come from a governed route-probe run: the
// production service, with the real provider or a scripted model, issues four `bash -lc` commands (a
// control read inside the workspace and three requests of the registered outside-workspace target) that
// the production gate sends to the Podman command cell. The command cell settles each command with a
// D09 command receipt that the service journals (`production_d09_command_settled`, command audit) with
// the run and profile identity. This producer reads that journal: it verifies the hash chain, checks
// every receipt with the receipt's own validator, matches each receipt to a probe command by its exact
// command spec, and recomputes the digest of the result the cell must have returned (exit code 1 and
// `cat: <operand>: Permission denied` on stderr, nothing on stdout; for the control, exit 0 and the
// file's bytes). A receipt whose result digest is anything else is not an attempt and nothing is written.
//
// The direct-runtime route is produced from a probe run of the Pi runtime cell (guest_route_direct_linux.go).

const (
	commandSettledKind   = "production_d09_command_settled"
	commandSettledSchema = "tbound-production-d09-command-settled/v2"
	maxAuditJournalBytes = 256 << 20
)

type routeProbeOptions struct {
	route, runID, auditRoot string
	response                string
	target                  string
	controlPath             string
	controlFile             string
	// pins are what the run shares with the pilot (run.json): the run's own signed profile and template are bound to them.
	pins guestevidence.RunBinding
	// template is the cell template of the run (bash-view); hostProfile, hostProfileSig and hostProfileKey are the signed
	// host profile files of the run, the key hashing to pins.AdminPubkeyDigest.
	template                                    string
	hostProfile, hostProfileSig, hostProfileKey string
	// registration and registrationSig are the signed managed live root registration of the run (verified under the same key).
	registration, registrationSig string
	// direct-runtime only: the probe and production cell templates (the run produced no completed response).
	probeTemplate, productionTemplate string
}

// routeProbeEnv holds the seams of the probe: how the journal is read and how a receipt is checked.
type routeProbeEnv struct {
	readJournal     func(path string) ([]byte, error)
	validateReceipt func(piruntime.D09CommandReceipt) error
	readFile        func(path string) ([]byte, error)
}

func productionRouteProbeEnv() routeProbeEnv {
	return routeProbeEnv{
		readFile: func(path string) ([]byte, error) { return readBoundedFile(path, maxGuestInputSize) },
		readJournal: func(path string) ([]byte, error) {
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("%s is not a regular file", path)
			}
			data, err := io.ReadAll(io.LimitReader(f, maxAuditJournalBytes+1))
			if err != nil || len(data) > maxAuditJournalBytes {
				return nil, fmt.Errorf("%s is unreadable or larger than %d bytes", path, maxAuditJournalBytes)
			}
			return data, nil
		},
		validateReceipt: func(r piruntime.D09CommandReceipt) error { return r.ValidateSnapshot() },
	}
}

// commandSettledEvent is the part of the journaled event this producer reads.
type commandSettledEvent struct {
	SchemaVersion string          `json:"schema_version"`
	RunID         string          `json:"run_id"`
	ProfileDigest string          `json:"profile_digest"`
	Receipt       json.RawMessage `json:"receipt"`
}

func probeRouteDenied(o routeProbeOptions, env routeProbeEnv) (guestevidence.RouteDenied, string, error) {
	var d guestevidence.RouteDenied
	kind := ""
	switch o.route {
	case "bash-view":
		kind = guestevidence.KindRouteBashView
	case "direct-runtime":
		return probeRouteDirectRuntime(o, env)
	default:
		return d, "", fmt.Errorf("--route %q is not bash-view or direct-runtime", o.route)
	}
	if o.runID == "" || o.response == "" || !filepath.IsAbs(o.auditRoot) || !filepath.IsAbs(o.target) || o.controlPath == "" || o.controlFile == "" {
		return d, "", errors.New("--run-id, --response, an absolute --audit-root, an absolute --target, --control-path and --control-file are required")
	}
	response, err := providerExchangeFrom(o.response)
	if err != nil {
		return d, "", err
	}
	identity, err := guestevidence.RunIdentityFromResponse(response.Response)
	if err != nil {
		return d, "", err
	}
	if identity.RunID != o.runID {
		return d, "", fmt.Errorf("the response is for run %q, not %q", identity.RunID, o.runID)
	}
	binding, err := bindRunFromFiles(env, o.profileFiles(), o.pins, identity.ProfileHash)
	if err != nil {
		return d, "", err
	}
	controlBytes, err := os.ReadFile(o.controlFile)
	if err != nil || len(controlBytes) == 0 {
		return d, "", fmt.Errorf("--control-file: %v", err)
	}

	raw, err := env.readJournal(filepath.Join(o.auditRoot, o.runID+"-command-audit.jsonl"))
	if err != nil {
		return d, "", err
	}
	trace, err := audit.Verify(bytes.NewReader(raw))
	if err != nil {
		return d, "", fmt.Errorf("the command audit journal does not verify: %w", err)
	}
	if len(trace.Records) == 0 {
		return d, "", errors.New("the command audit journal is empty")
	}
	d = guestevidence.RouteDenied{RunID: identity.RunID, ProfileHash: identity.ProfileHash, FrozenSourceDigest: identity.FrozenSourceDigest, Target: o.target, Binding: binding,
		Journal: guestevidence.RouteJournal{Records: uint64(len(trace.Records)), HeadHash: trace.Records[len(trace.Records)-1].Hash}}

	commands := guestevidence.RouteProbeCommands(o.target, o.controlPath)
	expected := map[string]sessionrepo.CommandResult{
		guestevidence.RouteMethodControl: {ExitCode: 0, ExitObserved: true, Stdout: controlBytes},
	}
	for method, operand := range map[string]string{
		guestevidence.RouteMethodAbsolute:  o.target,
		guestevidence.RouteMethodTraversal: guestevidence.RouteTraversalOperand(o.target),
		guestevidence.RouteMethodSymlink:   guestevidence.RouteProbeLink,
	} {
		expected[method] = sessionrepo.CommandResult{ExitCode: 1, ExitObserved: true, Stderr: []byte("cat: " + operand + ": Permission denied\n")}
	}

	found := map[string]guestevidence.RouteAttempt{}
	for _, rec := range trace.Records {
		if rec.Event.Kind != commandSettledKind {
			continue
		}
		var ev commandSettledEvent
		if err := decodeStrictAllowUnknown(rec.Event.Data, &ev); err != nil {
			return d, "", fmt.Errorf("journal record %d: %w", rec.Sequence, err)
		}
		if ev.SchemaVersion != commandSettledSchema || ev.RunID != o.runID {
			continue
		}
		if ev.ProfileDigest != identity.ProfileHash {
			return d, "", fmt.Errorf("journal record %d: the command ran under profile %q, the response is for %q", rec.Sequence, ev.ProfileDigest, identity.ProfileHash)
		}
		var receipt piruntime.D09CommandReceipt
		if err := json.Unmarshal(ev.Receipt, &receipt); err != nil {
			return d, "", fmt.Errorf("journal record %d: receipt: %w", rec.Sequence, err)
		}
		if err := env.validateReceipt(receipt); err != nil {
			return d, "", fmt.Errorf("journal record %d: %w", rec.Sequence, err)
		}
		if err := checkCommandCellProof(receipt); err != nil {
			return d, "", fmt.Errorf("journal record %d: %w", rec.Sequence, err)
		}
		method := ""
		for m, cmd := range commands {
			if receipt.CommandSpec.Executable == "/bin/bash" && len(receipt.CommandSpec.Args) == 2 && receipt.CommandSpec.Args[0] == "-lc" && receipt.CommandSpec.Args[1] == cmd {
				method = m
			}
		}
		if method == "" {
			continue // a command of the run that is not part of the probe
		}
		if _, dup := found[method]; dup {
			return d, "", fmt.Errorf("the %s command ran twice (journal record %d): a probe run issues each command once", method, rec.Sequence)
		}
		encoded, err := json.Marshal(expected[method])
		if err != nil {
			return d, "", err
		}
		outcome := "other"
		if receipt.ResultDigest == piruntime.DigestBytes(encoded) {
			outcome = guestevidence.RouteOutcomeDenied
			if method == guestevidence.RouteMethodControl {
				outcome = guestevidence.RouteOutcomeRead
			}
		}
		found[method] = guestevidence.RouteAttempt{Method: method, Argv: append([]string{receipt.CommandSpec.Executable}, receipt.CommandSpec.Args...), ExitCode: receipt.ExitCode,
			Outcome: outcome, ResultDigest: receipt.ResultDigest, ReceiptDigest: receipt.ReceiptDigest, ContainerID: receipt.ContainerID, JournalSequence: rec.Sequence}
		if d.Cell.Kind == "" {
			d.Cell = guestevidence.RouteCell{Kind: guestevidence.RouteCellCommand, ImageDigest: receipt.ImageDigest, TemplateDigest: receipt.TemplateDigest,
				LandlockRulesetDigest: receipt.Proof.LandlockRulesetDigest}
		} else if d.Cell.ImageDigest != receipt.ImageDigest || d.Cell.TemplateDigest != receipt.TemplateDigest || d.Cell.LandlockRulesetDigest != receipt.Proof.LandlockRulesetDigest {
			return d, "", fmt.Errorf("journal record %d: the command cell differs from the cell of the other probe commands", rec.Sequence)
		}
	}
	for _, method := range append([]string{guestevidence.RouteMethodControl}, guestevidence.RouteMethods...) {
		a, ok := found[method]
		if !ok {
			return d, "", fmt.Errorf("the journal holds no settled %s command (%q)", method, commands[method])
		}
		if a.Outcome == "other" {
			return d, "", fmt.Errorf("the %s command returned something other than %s (exit %d, result %s): not a policy denial", method,
				map[bool]string{true: "the control file's bytes", false: "a refusal of cat with Permission denied and no output"}[method == guestevidence.RouteMethodControl], a.ExitCode, a.ResultDigest)
		}
		if method == guestevidence.RouteMethodControl {
			d.Control = a
		} else {
			d.Attempts = append(d.Attempts, a)
		}
	}
	return d, kind, nil
}

// checkCommandCellProof requires the receipt's proof to show the unrelaxed, fully confined command cell.
func checkCommandCellProof(r piruntime.D09CommandReceipt) error {
	p := r.Proof
	switch {
	case p.Delta != "" || p.DeltaTemplateDigest != "":
		return fmt.Errorf("the command ran in the relaxed cell %q, the route probe needs the base cell", p.Delta)
	case p.CapEff != "0000000000000000" || !p.NoNewPrivs || p.Seccomp != 2 || !p.LandlockTSYNC || !p.CgroupV2:
		return errors.New("the cell proof does not show an empty capability set, no_new_privs, seccomp filter mode, Landlock and cgroup v2")
	case !p.ConsumerMountsVerified || !p.RawSourceFDsClosed || p.CommandPath != "/bin/bash" || !p.NoExternalRoutes:
		return errors.New("the cell proof does not show verified mounts, closed source descriptors, the Bash identity and no external route")
	}
	return nil
}

// decodeStrictAllowUnknown decodes one JSON document that another component owns: a duplicate key and
// trailing data are refused, members this producer does not read are accepted.
func decodeStrictAllowUnknown(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

// profileFiles names the signed profile, registration and template files of the run.
func (o routeProbeOptions) profileFiles() runProfileFiles {
	return runProfileFiles{template: o.template, hostProfile: o.hostProfile, hostProfileSig: o.hostProfileSig, hostProfileKey: o.hostProfileKey,
		registration: o.registration, registrationSig: o.registrationSig}
}
