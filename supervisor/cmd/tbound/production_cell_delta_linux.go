//go:build linux

package main

import (
	"fmt"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

// commandCellDeltaFor is the command-cell delta of a corpus run, from the admitted role and the delta
// the pinned manifest registers for it. Only the P half runs a relaxed cell: net-sink/v1,
// no-reap/v1 and net-sink+admit-all/v1 given to any other role are refused, never ignored, so a C,
// B, variance, compliant or ambiguous run cannot reach a relaxed template. net-sink+admit-all/v1
// runs the net-sink/v1 cell itself (its template, proof delta and digest). admit-all/v1 relaxes
// admission, not the cell, and none (or the absent member) is the signed base cell.
func commandCellDeltaFor(role, pDelta string) (piruntime.D09CellDelta, error) {
	switch pDelta {
	case "", evalcorpus.PDeltaNone, evalcorpus.PDeltaAdmitAll:
		return piruntime.D09CellDelta{}, nil
	case evalcorpus.PDeltaNetSink, evalcorpus.PDeltaNoReap, evalcorpus.PDeltaNetSinkAdmitAll:
		if !pCorpusRole(role) {
			return piruntime.D09CellDelta{}, fmt.Errorf("%w: the command-cell delta %s is for the P half only, not role %q",
				ErrPiServiceCorpus, pDelta, role)
		}
		return piruntime.ParseD09CellDelta(evalcorpus.PDeltaCell(pDelta))
	}
	return piruntime.D09CellDelta{}, fmt.Errorf("%w: %q is not a registered delta", ErrPiServiceCorpus, pDelta)
}

// SurvivorCells is the number of command cells of the run that were left running by no-reap/v1.
func (r *productionCommandRunner) SurvivorCells() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, settlement := range r.settledReceipts {
		if settlement.SurvivorsTolerated {
			count++
		}
	}
	return count
}

// applyCellDeltaReceipt records the command-cell delta of a corpus run in the receipt: the delta
// name is set by applyCorpusReceipt, and this adds the derived template identity and the number of
// command cells the run left running. Both are empty for a run whose cell is the signed base cell.
func applyCellDeltaReceipt(receipt *productionPiReceipt, binding *corpusBinding, runner sessionrepo.CommandRunner) error {
	if binding == nil {
		return nil
	}
	cell, err := commandCellDeltaFor(binding.role, binding.delta)
	if err != nil {
		return err
	}
	if cell.Relaxed() {
		if receipt.DeltaTemplateDigest, err = cell.TemplateDigest(receipt.ContainmentProfileDigest); err != nil {
			return fmt.Errorf("delta template digest for the receipt: %w", err)
		}
	}
	if production, ok := runner.(*productionCommandRunner); ok {
		receipt.CommandSurvivors = production.SurvivorCells()
	}
	return nil
}
