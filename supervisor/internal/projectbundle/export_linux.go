//go:build linux

package projectbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
)

// ErrNoChanges means the current tree equals the baseline, so there is nothing to export.
var ErrNoChanges = errors.New("projectbundle: the current tree equals the baseline")

// BuildExport compares the managed project at current with the baseline manifest it was imported
// from and writes a tbound-project-export/v1 stream to w. It returns the header and the digest of
// the whole export. It runs as the service. A path that changes type, and any symlink or special
// file, is refused. If a file changes while its content is copied, the error is returned and w holds
// a partial stream that the caller must discard.
func BuildExport(baseline Manifest, current string, limits Limits, w io.Writer) (ExportHeader, string, error) {
	if err := baseline.Validate(limits); err != nil {
		return ExportHeader{}, "", fmt.Errorf("baseline manifest: %w", err)
	}
	baselineDigest, err := baseline.Digest()
	if err != nil {
		return ExportHeader{}, "", err
	}
	target, _, err := scanTree(current, scanOptions{Limits: limits})
	if err != nil {
		return ExportHeader{}, "", err
	}
	targetDigest, err := target.Digest()
	if err != nil {
		return ExportHeader{}, "", err
	}
	changes, err := diffManifests(baseline, target)
	if err != nil {
		return ExportHeader{}, "", err
	}
	if len(changes) == 0 {
		return ExportHeader{}, "", ErrNoChanges
	}
	header := ExportHeader{Format: ExportFormat, BaselineDigest: baselineDigest, TargetDigest: targetDigest, Changes: changes}
	if err := header.Validate(limits); err != nil {
		return ExportHeader{}, "", err
	}
	data, err := header.marshal()
	if err != nil {
		return ExportHeader{}, "", err
	}
	if err := writeFramed(w, ExportFormat, data); err != nil {
		return ExportHeader{}, "", err
	}
	var withContent []Entry
	for _, c := range changes {
		if c.hasContent() {
			withContent = append(withContent, Entry{Path: c.Path, Type: TypeFile, Size: c.Size, SHA256: c.SHA256})
		}
	}
	if err := writeContents(current, withContent, w); err != nil {
		return ExportHeader{}, "", err
	}
	return header, digestOf(data), nil
}

func diffManifests(baseline, target Manifest) ([]Change, error) {
	before := make(map[string]Entry, len(baseline.Entries))
	after := make(map[string]Entry, len(target.Entries))
	var paths []string
	for _, e := range baseline.Entries {
		before[e.Path] = e
		paths = append(paths, e.Path)
	}
	for _, e := range target.Entries {
		after[e.Path] = e
		if _, ok := before[e.Path]; !ok {
			paths = append(paths, e.Path)
		}
	}
	sort.Strings(paths)
	var changes []Change
	for _, p := range paths {
		old, inBefore := before[p]
		cur, inAfter := after[p]
		switch {
		case !inBefore:
			changes = append(changes, Change{Path: p, Op: OpCreate, Type: cur.Type, Executable: cur.Executable, Size: cur.Size, SHA256: cur.SHA256})
		case !inAfter:
			changes = append(changes, Change{Path: p, Op: OpDelete, Type: old.Type})
		case old.Type != cur.Type:
			return nil, fmt.Errorf("projectbundle: %s changed type from %s to %s, which is not supported", p, old.Type, cur.Type)
		case cur.Type == TypeFile && (old.SHA256 != cur.SHA256 || old.Size != cur.Size):
			changes = append(changes, Change{Path: p, Op: OpModify, Type: TypeFile, Executable: cur.Executable, Size: cur.Size, SHA256: cur.SHA256})
		case cur.Type == TypeFile && old.Executable != cur.Executable:
			changes = append(changes, Change{Path: p, Op: OpChmod, Type: TypeFile, Executable: cur.Executable, Size: cur.Size, SHA256: cur.SHA256})
		}
	}
	return changes, nil
}

// readExport strictly validates an export stream, including every content digest, and returns the
// header, the digest of the export and the stream offset of each change's content (-1 for none).
func readExport(bundle io.ReadSeeker, limits Limits) (ExportHeader, string, []int64, error) {
	if _, err := bundle.Seek(0, io.SeekStart); err != nil {
		return ExportHeader{}, "", nil, err
	}
	data, err := readFramed(bundle, ExportFormat)
	if err != nil {
		return ExportHeader{}, "", nil, err
	}
	var header ExportHeader
	if err := decodeStrict(data, &header); err != nil {
		return ExportHeader{}, "", nil, err
	}
	if err := header.Validate(limits); err != nil {
		return ExportHeader{}, "", nil, err
	}
	position := int64(len(ExportFormat) + 1 + 4 + len(data))
	offsets := make([]int64, len(header.Changes))
	for i, c := range header.Changes {
		offsets[i] = -1
		if !c.hasContent() {
			continue
		}
		offsets[i] = position
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(bundle, c.Size))
		if err != nil {
			return ExportHeader{}, "", nil, err
		}
		if n != c.Size {
			return ExportHeader{}, "", nil, invalid("truncated stream: %q ends after %d of %d bytes", c.Path, n, c.Size)
		}
		if hex.EncodeToString(h.Sum(nil)) != c.SHA256 {
			return ExportHeader{}, "", nil, invalid("content of %q does not match its sha256", c.Path)
		}
		position += c.Size
	}
	if err := requireEOF(bundle); err != nil {
		return ExportHeader{}, "", nil, err
	}
	return header, digestOf(data), offsets, nil
}

// manifestAfter returns the manifest that applying the header's changes to before produces. It
// refuses a change that does not fit before and a result that is not a valid manifest.
func manifestAfter(before Manifest, header ExportHeader, limits Limits) (Manifest, error) {
	entries := make(map[string]Entry, len(before.Entries))
	for _, e := range before.Entries {
		entries[e.Path] = e
	}
	for _, c := range header.Changes {
		old, exists := entries[c.Path]
		switch c.Op {
		case OpCreate:
			if exists {
				return Manifest{}, fmt.Errorf("projectbundle: %s already exists", c.Path)
			}
			entries[c.Path] = Entry{Path: c.Path, Type: c.Type, Executable: c.Executable, Size: c.Size, SHA256: c.SHA256}
		case OpModify:
			if !exists || old.Type != TypeFile {
				return Manifest{}, fmt.Errorf("projectbundle: %s is not an existing file", c.Path)
			}
			entries[c.Path] = Entry{Path: c.Path, Type: TypeFile, Executable: c.Executable, Size: c.Size, SHA256: c.SHA256}
		case OpChmod:
			if !exists || old.Type != TypeFile || old.SHA256 != c.SHA256 || old.Size != c.Size || old.Executable == c.Executable {
				return Manifest{}, fmt.Errorf("projectbundle: %s does not fit an executable-bit change", c.Path)
			}
			old.Executable = c.Executable
			entries[c.Path] = old
		case OpDelete:
			if !exists || old.Type != c.Type {
				return Manifest{}, fmt.Errorf("projectbundle: %s is not an existing %s", c.Path, c.Type)
			}
			delete(entries, c.Path)
		}
	}
	result := Manifest{Format: BundleFormat, Entries: make([]Entry, 0, len(entries))}
	for _, e := range entries {
		result.Entries = append(result.Entries, e)
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	if err := result.Validate(limits); err != nil {
		return Manifest{}, err
	}
	return result, nil
}
