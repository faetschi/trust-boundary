package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"tbound/supervisor/internal/runner"
)

// flagsFileName is the sibling of the exported trials (and of report.json) that names the cases whose adaptive
// C and P halves had different cumulative budgets. evalscore-trials/v1 is strict, so the flags live beside the
// trials, and report.meta.json binds the file by digest.
const flagsFileName = "adaptive-budget-flags.json"

const flagsDisposition = "D32 gives C the identical attacker machine and cumulative budget. A flagged pair is no valid " +
	"adaptive P/C pair: its scored record is exported with pc_evidence_complete false, so it is UNSUPPORTED " +
	"(calibration_pair_incomplete), stays in the denominator and never counts as prevention."

// syncFlagsFile writes the flags file into dir and returns its digest. Without flags it writes nothing and
// removes a file an earlier export left, so the file never describes another journal state. It returns "".
func syncFlagsFile(dir string, flags []runner.AdaptiveBudgetFlag) (string, error) {
	path := filepath.Join(dir, flagsFileName)
	if len(flags) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", nil
	}
	data, err := json.Marshal(struct {
		Schema      string                      `json:"schema"`
		Disposition string                      `json:"disposition"`
		Flags       []runner.AdaptiveBudgetFlag `json:"flags"`
	}{"tbound-adaptive-budget-flags/v1", flagsDisposition, flags})
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func flagLines(flags []runner.AdaptiveBudgetFlag) []string {
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = f.String()
	}
	return out
}

// flagsMarkdown is the report section for flagged pairs; it is empty without flags.
func flagsMarkdown(flags []runner.AdaptiveBudgetFlag) string {
	if len(flags) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Adaptive budget mismatch\n\n")
	b.WriteString(flagsDisposition + " Cases:\n\n")
	for _, l := range flagLines(flags) {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	b.WriteString("\n")
	return b.String()
}
