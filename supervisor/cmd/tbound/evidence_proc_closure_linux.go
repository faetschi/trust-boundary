//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
)

// evidenceProcClosure implements `proc-closure` (D49): the guest's own reading, after the interval's request, of whether
// the interval is provably complete. It is read-only. It finds the interval's admission and its completed receipt with
// the lookup e04-points uses, never from the client's exit; reads the settlement of every command of that run from the
// run's command journal (each receipt re-validated, digest included); lists the children of the registered cells
// cgroup (the registered cell is the persistent parent, which says nothing about its command cells); looks for the
// each command's network proof (the delta, no-routes, empty cap_eff and no_new_privs its signed proof records); and runs a fresh identity, process and socket scan of the registered cell. The host
// judges all of it (procoracle.EvaluateClosure); a failure here is an error, never a document that looks provable.
func evidenceProcClosure(ctx context.Context, env evidenceEnv, f map[string]string) (procoracle.ClosureDoc, error) {
	ref := f["--interval-ref"]
	cell, reg, err := evidenceRegistration(ctx, env, f)
	if err != nil {
		return procoracle.ClosureDoc{}, err
	}
	if env.readTrace == nil || env.cellChildren == nil || env.hostNetNS == nil {
		return procoracle.ClosureDoc{}, errors.New("no closure instruments")
	}
	entry, err := evidenceAdmissionForInterval(env, ref)
	if err != nil {
		return procoracle.ClosureDoc{}, err
	}
	receipt, err := evidenceCompletedReceipt(env, entry)
	if err != nil {
		return procoracle.ClosureDoc{}, err
	}
	if receipt.PublicationIntervalRef != "" && receipt.PublicationIntervalRef != ref {
		return procoracle.ClosureDoc{}, errors.New("the receipt names another interval")
	}
	// A run that did not settle its worker is not provably complete; the host would refuse it anyway, so say so here.
	if receipt.Settlement != string(piruntime.SettlementStopped) || !receipt.ExitObserved || !receipt.DescendantsSettled || receipt.CommandCellSettlements < 0 {
		return procoracle.ClosureDoc{}, fmt.Errorf("the receipt does not report a settled run (settlement %q)", receipt.Settlement)
	}
	rawReceipt, err := json.Marshal(receipt)
	if err != nil {
		return procoracle.ClosureDoc{}, err
	}
	sum := sha256.Sum256(rawReceipt)
	doc := procoracle.ClosureDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcClosure, IntervalRef: ref, RunID: receipt.RunID,
		RegistrationDigest: reg.Digest(), ReceiptVerified: true, ReceiptDigest: hex.EncodeToString(sum[:]),
		ExpectedSettlements: receipt.CommandCellSettlements, Commands: []procoracle.CommandSettlement{}}
	if doc.Commands, err = evidenceCommandSettlements(env, receipt.RunID, receipt.CommandCellSettlements); err != nil {
		return procoracle.ClosureDoc{}, err
	}
	if doc.CellChildren, doc.CellChildrenTotal, err = env.cellChildren(cell); err != nil {
		return procoracle.ClosureDoc{}, fmt.Errorf("the cells cgroup cannot be listed: %w", err)
	}
	var inodes []uint64
	for _, c := range doc.Commands {
		if c.NetworkNSInode != 0 { // a command that names none is refused by the host's predicate
			inodes = append(inodes, c.NetworkNSInode)
		}
	}
	hostNS, err := env.hostNetNS()
	if err != nil {
		return procoracle.ClosureDoc{}, fmt.Errorf("the host network namespace cannot be read: %w", err)
	}
	for _, ino := range inodes {
		doc.HostNetNS = doc.HostNetNS || ino == hostNS
	}
	// The scan comes last: it must be younger than everything above it.
	if doc.Scan, err = env.capture(cell, reg, procoracle.PointInterval, 1); err != nil {
		return procoracle.ClosureDoc{}, err
	}
	return doc, nil
}

// evidenceCommandSettlements reads the settled command receipts of a run from its command journal.
func evidenceCommandSettlements(env evidenceEnv, runID string, want int) ([]procoracle.CommandSettlement, error) {
	out := []procoracle.CommandSettlement{}
	if filepath.Base(runID) != runID || runID == "." || runID == ".." || strings.ContainsAny(runID, `/\`) {
		return nil, errors.New("the run id is not a file name")
	}
	trace, err := env.readTrace(filepath.Join(productionAuditRootDefault, runID+"-command-audit.jsonl"), want > 0)
	if err != nil {
		return nil, fmt.Errorf("the command journal cannot be read: %w", err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind != productionD09SettledKind {
			continue
		}
		var header struct {
			RunID         string          `json:"run_id"`
			ReceiptDigest string          `json:"receipt_digest"`
			Receipt       json.RawMessage `json:"receipt"`
		}
		if err := json.Unmarshal(record.Event.Data, &header); err != nil {
			return nil, fmt.Errorf("command journal record %d is unreadable", record.Sequence)
		}
		if header.RunID != runID {
			return nil, fmt.Errorf("command journal record %d is of another run", record.Sequence)
		}
		var r piruntime.D09CommandReceipt
		if err := json.Unmarshal(header.Receipt, &r); err != nil || r.ValidateSnapshot() != nil || r.ReceiptDigest != header.ReceiptDigest {
			return nil, fmt.Errorf("command journal record %d holds no valid settled receipt", record.Sequence)
		}
		e := r.Evidence
		// The network proof is structural (D49): the delta the cell was launched under fixes its Podman --network value,
		// and the proof the receipt digest covers records the routes, capability and privilege facts.
		delta, derr := piruntime.ParseD09CellDelta(r.Proof.Delta)
		if derr != nil {
			return nil, fmt.Errorf("command journal record %d names an unknown delta: %w", record.Sequence, derr)
		}
		out = append(out, procoracle.CommandSettlement{LeaseID: r.LeaseID, ReceiptDigest: r.ReceiptDigest, NetworkNSInode: r.Proof.NetworkNSInode,
			Delta: r.Proof.Delta, NetworkMode: delta.NetworkMode(), NoExternalRoutes: r.Proof.NoExternalRoutes, CapEff: r.Proof.CapEff, CapPrm: r.Proof.CapPrm, CapBnd: r.Proof.CapBnd, CapInh: r.Proof.CapInh, CapAmb: r.Proof.CapAmb, NoNewPrivs: r.Proof.NoNewPrivs,
			ContainerExitInspected: e.ContainerExitInspected, FinalCommandProofRead: e.FinalCommandProofRead, PodmanWaitCompleted: e.PodmanWaitCompleted,
			OutputWritersDrained: e.OutputWritersDrained, ContainerRemoved: e.ContainerRemoved, LeaderPidfdExited: e.LeaderPidfdExited,
			CgroupPopulatedZero: e.CgroupPopulatedZero, MountNamespaceReleased: e.MountNamespaceReleased,
			CommandOutputWithinBound: e.CommandOutputWithinBound, CommandCgroupRemoved: e.CommandCgroupRemoved,
			SurvivorsTolerated: e.SurvivorsTolerated, OutputMarkerObserved: e.OutputMarkerObserved,
			ContainerLeftRunning: e.ContainerLeftRunning, CommandScopeDetached: e.CommandScopeDetached})
	}
	// The count is the receipt's own; the host compares it with what was read, so a short or long journal is not hidden.
	return out, nil
}

// listCellChildren lists the cgroups directly below the cells cgroup directory: the total, and for the first
// MaxCellChildren (by name) whether each is populated and has processes. A child whose files cannot be read is
// reported as unreadable, never as empty: the host then refuses the proof.
func listCellChildren(c evidenceCell) ([]procoracle.CellChild, int, error) {
	entries, err := os.ReadDir(c.CgroupPath)
	if err != nil {
		return nil, 0, err
	}
	out, total := []procoracle.CellChild{}, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if total++; total > procoracle.MaxCellChildren {
			continue
		}
		dir := filepath.Join(c.CgroupPath, e.Name())
		child := procoracle.CellChild{Name: e.Name()}
		events, eerr := os.ReadFile(filepath.Join(dir, "cgroup.events"))
		procsEmpty, perr := cgroupProcsEmpty(filepath.Join(dir, "cgroup.procs"))
		child.Populated = bytes.Contains(events, []byte("populated 1"))
		child.ProcsEmpty = procsEmpty
		child.Unreadable = eerr != nil || perr != nil || !bytes.Contains(events, []byte("populated "))
		out = append(out, child)
	}
	return out, total, nil
}

// cgroupProcsEmpty reports whether a cgroup.procs file lists no process (it reads one byte at most).
func cgroupProcsEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var b [1]byte
	n, err := f.Read(b[:])
	if err == io.EOF {
		return true, nil
	}
	return n == 0 && err == nil, err
}
