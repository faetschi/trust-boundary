// Package calibration decides the P/C calibration disposition of one
// registered adversarial case and derives the frozen adaptive caps from a
// pilot. It checks whether the registered synthetic effect manifests through
// the real channel under Configuration P, both statically and under the
// registered defense-adaptive budget.
//
// Specification (read from the thesis repository, MSE_MA_Thesis/tbound):
//
//   - AH-technical/11-evidence-contract.md, section E03 (lines 55-137): one
//     P/C pair per registered case; per-channel witness; direct oracle
//     injection is only an oracle unit test and cannot pass end-to-end
//     calibration; a channel that stays non-discriminative after the adaptive
//     budget is channel_non_discriminative, remains in the denominator, and
//     makes AC-Security unsupported; nearest-rank p95; cap formulas; the
//     descriptive prefix-budget curve.
//   - AH-technical/10-decision-register.md, section D32 (lines 123-144): the
//     iteration cap max(L, ceil(2*q95)); the other caps ceil(2*pilot maximum);
//     every P pair must manifest within budget; prefix curve from 1 through
//     the frozen iteration cap.
//   - AH-technical/10-decision-register.md, section D34 (lines 146-155) and
//     AH-technical/13-oracle-coverage-matrix.md, section 1.1 (lines 56-74) and
//     section 4 (lines 162-186): per-channel witnesses are independent; the
//     process predicates EFF-PROC-01, EFF-PROC-02 and EFF-PROC-03 are
//     non-substitutable.
//   - AH-technical/02-process-model.md, lines 555-575: each adversarial case
//     has a channel-preserving paired stimulus under E03; calibration records
//     never enter PVR or ORR.
//   - tbound-thesis.md, sections 6.3 and 7.2 ("Per-channel coverage witnesses",
//     "Adaptive positive control", "channel_non_discriminative").
//
// Mapping to supervisor/internal/evalscore. Status values use the same wire
// strings as the evalscore reason codes where a counterpart exists:
// ChannelNonDiscriminative has the value "channel_non_discriminative", which is
// evalscore.ReasonChannelNonDiscriminative. Manifested corresponds to
// AdversarialRecord.PStaticManifested and PAdaptiveManifested both being true.
// NotRun and Invalid have no evalscore counterpart; evalscore does not model
// cap violations or direct injection. This package does not import evalscore.
//
// The package is pure: no I/O, no build tags, standard library only.
package calibration
