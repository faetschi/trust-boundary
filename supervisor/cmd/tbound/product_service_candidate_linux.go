//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/projectbundle"
	"tbound/supervisor/internal/sessionrepo"
)

// Bounds of a candidate preview. A file that exceeds one of them is listed without a diff.
const (
	productCandidateMaxFileBytes = 64 << 10
	productCandidateMaxLines     = 1000
	productCandidateMaxDiffBytes = 8 << 10
	productCandidateDiffBudget   = 256 << 10
	productCandidateContext      = 3
)

// candidateEntry is one path whose state differs between the baseline and the candidate tree.
// before or after is nil when the path is absent on that side.
type candidateEntry struct {
	path          string
	op            string
	before, after *delta.ObjectState
}

// candidateChanges lists every path whose type, content or metadata differs between two manifests,
// both strictly sorted by path.
func candidateChanges(baseline, tip delta.TreeManifest) []candidateEntry {
	var changes []candidateEntry
	b, t := baseline.Objects, tip.Objects
	for i, j := 0, 0; i < len(b) || j < len(t); {
		var entry candidateEntry
		switch {
		case j == len(t) || (i < len(b) && b[i].Path < t[j].Path):
			entry = candidateEntry{path: b[i].Path, op: string(projectbundle.OpDelete), before: &b[i].State}
			i++
		case i == len(b) || t[j].Path < b[i].Path:
			entry = candidateEntry{path: t[j].Path, op: string(projectbundle.OpCreate), after: &t[j].State}
			j++
		default:
			path, before, after := b[i].Path, &b[i].State, &t[j].State
			i, j = i+1, j+1
			if *before == *after {
				continue
			}
			op := projectbundle.OpModify
			if before.Type == delta.ObjectRegular && after.Type == before.Type && before.ContentDigest == after.ContentDigest {
				op = projectbundle.OpChmod
			}
			entry = candidateEntry{path: path, op: string(op), before: before, after: after}
		}
		changes = append(changes, entry)
	}
	return changes
}

// storeCandidateChanges is the change list between a Store's baseline and tip generations.
func storeCandidateChanges(store *sessionrepo.Store) (baseline, tip *sessionrepo.Generation, _ []candidateEntry, _ error) {
	baseline, tip = store.BaselineGeneration(), store.TipGeneration()
	if baseline == nil || tip == nil {
		return nil, nil, nil, errors.New("the Store has no sealed baseline and tip")
	}
	return baseline, tip, candidateChanges(baseline.Manifest(), tip.Manifest()), nil
}

// buildProductCandidate previews the Store's candidate: every changed path with its operation and a
// bounded text diff. The tip must be the tree the catalog recorded as the candidate.
func buildProductCandidate(turnID string, store *sessionrepo.Store, candidateDigest string) (productprotocol.CandidateView, error) {
	baseline, tip, changes, err := storeCandidateChanges(store)
	if err != nil {
		return productprotocol.CandidateView{}, err
	}
	if sha256DigestFromBarePin(bareStoreTreeDigest(tip.TreeDigest())) != candidateDigest {
		return productprotocol.CandidateView{}, errors.New("the Store tip is not the recorded candidate")
	}
	view := productprotocol.CandidateView{TurnID: turnID, CandidateDigest: candidateDigest,
		Changes: make([]productprotocol.CandidateChange, 0, len(changes))}
	var beforeDir, afterDir *os.File
	defer func() {
		for _, dir := range []*os.File{beforeDir, afterDir} {
			if dir != nil {
				_ = dir.Close()
			}
		}
	}()
	budget := productCandidateDiffBudget
	for _, change := range changes {
		out := productprotocol.CandidateChange{Path: change.path, Op: change.op}
		switch {
		case change.op == string(projectbundle.OpChmod) || isDirectoryChange(change):
			// no content to show
		case regularOrAbsent(change.before) && regularOrAbsent(change.after):
			if beforeDir == nil {
				if beforeDir, err = baseline.ReadOnlyMountSource(); err != nil {
					return productprotocol.CandidateView{}, err
				}
				if afterDir, err = tip.ReadOnlyMountSource(); err != nil {
					return productprotocol.CandidateView{}, err
				}
			}
			if out.Diff, out.Truncated, err = previewChange(beforeDir, afterDir, change, &budget); err != nil {
				return productprotocol.CandidateView{}, err
			}
		default:
			out.Truncated = true // a symlink or special object
		}
		view.Changes = append(view.Changes, out)
	}
	return view, nil
}

func regularOrAbsent(s *delta.ObjectState) bool { return s == nil || s.Type == delta.ObjectRegular }

func isDirectoryChange(c candidateEntry) bool {
	return (c.before != nil && c.before.Type == delta.ObjectDirectory) || (c.after != nil && c.after.Type == delta.ObjectDirectory)
}

// previewChange diffs one regular file. A side that is oversized, binary or not valid UTF-8, or a
// spent budget, gives no diff and truncated.
func previewChange(beforeDir, afterDir *os.File, change candidateEntry, budget *int) (string, bool, error) {
	var before, after []string
	for _, side := range []struct {
		state *delta.ObjectState
		dir   *os.File
		lines *[]string
	}{{change.before, beforeDir, &before}, {change.after, afterDir, &after}} {
		if side.state == nil {
			continue
		}
		text, ok, err := readGenerationText(side.dir, change.path)
		if err != nil {
			return "", false, err
		}
		if !ok {
			return "", true, nil
		}
		*side.lines = strings.SplitAfter(text, "\n")
		if len(*side.lines) > 0 && (*side.lines)[len(*side.lines)-1] == "" {
			*side.lines = (*side.lines)[:len(*side.lines)-1]
		}
		if len(*side.lines) > productCandidateMaxLines {
			return "", true, nil
		}
	}
	diff := renderUnifiedDiff(diffLines(before, after))
	truncated := false
	limit := min(productCandidateMaxDiffBytes, *budget)
	if len(diff) > limit {
		diff, truncated = diff[:strings.LastIndexByte(diff[:limit], '\n')+1], true
	}
	*budget -= len(diff)
	return diff, truncated, nil
}

// readGenerationText reads one regular file below a sealed generation directory without following
// symlinks. ok is false for a file that is too large, binary or not valid UTF-8.
func readGenerationText(root *os.File, rel string) (string, bool, error) {
	file, err := openBeneathNoFollow(root, rel)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return "", false, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size > productCandidateMaxFileBytes {
		return "", false, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, productCandidateMaxFileBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > productCandidateMaxFileBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", false, nil
	}
	return string(data), true, nil
}

// openBeneathNoFollow opens rel below root one component at a time with O_NOFOLLOW.
func openBeneathNoFollow(root *os.File, rel string) (*os.File, error) {
	parts := strings.Split(rel, "/")
	dirfd, owned := int(root.Fd()), false
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_DIRECTORY
		if i == len(parts)-1 {
			flags = unix.O_RDONLY | unix.O_NONBLOCK
		}
		fd, err := unix.Openat(dirfd, part, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if owned {
			_ = unix.Close(dirfd)
		}
		if err != nil {
			return nil, fmt.Errorf("open candidate file: %w", err)
		}
		dirfd, owned = fd, true
	}
	return os.NewFile(uintptr(dirfd), "candidate"), nil
}

type diffOp struct {
	kind byte // ' ', '-' or '+'
	text string
}

// diffLines is a longest-common-subsequence line diff. The inputs are bounded, so the table is too.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]uint16, n+1)
	for i := range lcs {
		lcs[i] = make([]uint16, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// renderUnifiedDiff prints the changes as unified hunks with three lines of context.
func renderUnifiedDiff(ops []diffOp) string {
	var out strings.Builder
	oldBefore, newBefore := 0, 0 // lines of each side before ops[from]
	from := 0
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(i-productCandidateContext, from)
		last := i
		for k := i + 1; k < len(ops) && k <= last+2*productCandidateContext+1; k++ {
			if ops[k].kind != ' ' {
				last = k
			}
		}
		end := min(last+productCandidateContext+1, len(ops))
		for ; from < start; from++ {
			oldBefore, newBefore = advanceLines(ops[from], oldBefore, newBefore)
		}
		oldCount, newCount := 0, 0
		for _, op := range ops[start:end] {
			oldCount, newCount = advanceLines(op, oldCount, newCount)
		}
		oldStart, newStart := oldBefore+1, newBefore+1
		if oldCount == 0 {
			oldStart = oldBefore
		}
		if newCount == 0 {
			newStart = newBefore
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, op := range ops[start:end] {
			out.WriteByte(op.kind)
			out.WriteString(strings.TrimSuffix(op.text, "\n"))
			out.WriteByte('\n')
			if !strings.HasSuffix(op.text, "\n") {
				out.WriteString("\\ No newline at end of file\n")
			}
		}
		for ; from < end; from++ {
			oldBefore, newBefore = advanceLines(ops[from], oldBefore, newBefore)
		}
		i = end
	}
	return out.String()
}

func advanceLines(op diffOp, oldLines, newLines int) (int, int) {
	if op.kind != '+' {
		oldLines++
	}
	if op.kind != '-' {
		newLines++
	}
	return oldLines, newLines
}
