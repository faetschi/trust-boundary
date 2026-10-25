//go:build linux

package taskaccept

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"syscall"

	"golang.org/x/sys/unix"
	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/wsoracle"
)

// MaxCapturedTreeBytes bounds the private copy used by trial acceptance.
const MaxCapturedTreeBytes = 256 << 20

// EvaluateCapturedRootWith judges a private in-memory copy. The digest and every check consume
// the same copied bytes and metadata; no check can reopen the live tree after capture.
func EvaluateCapturedRootWith(caseID string, spec AcceptSpec, fixture trialfix.Spec, root *RootFS, runner NodeRunner) (AcceptDoc, error) {
	before, err := treeOfFixture(fixture)
	if err != nil {
		return AcceptDoc{}, err
	}
	after, err := treeOfFS(root)
	if err != nil {
		return AcceptDoc{}, err
	}
	m := wsoracle.Manifest{Complete: true}
	var total int64
	for path, n := range after {
		info, err := root.Stat(path)
		if err != nil {
			return AcceptDoc{}, err
		}
		if info.Mode().Type() != n.mode.Type() {
			return AcceptDoc{}, errors.New("capture: entry changed type")
		}
		if n.mode.IsRegular() {
			f, err := root.Open(path)
			if err != nil {
				return AcceptDoc{}, err
			}
			info, err = f.Stat()
			if err != nil {
				f.Close()
				return AcceptDoc{}, err
			}
			if !info.Mode().IsRegular() {
				f.Close()
				return AcceptDoc{}, errors.New("capture: file changed type")
			}
			limit := min(int64(MaxReadBytes), int64(MaxCapturedTreeBytes)-total)
			data, rerr := io.ReadAll(io.LimitReader(f, limit+1))
			cerr := f.Close()
			if err := errors.Join(rerr, cerr); err != nil {
				return AcceptDoc{}, err
			}
			if int64(len(data)) > limit {
				return AcceptDoc{}, errors.New("capture: tree or file exceeds private copy limit")
			}
			total += int64(len(data))
			n.content, n.size = data, int64(len(data))
		}
		n.mode, n.fsys, n.name = info.Mode(), nil, ""
		after[path] = n
		e, err := capturedEntry(path, info)
		if err != nil {
			return AcceptDoc{}, err
		}
		switch {
		case n.mode.IsRegular():
			e.Type, e.Size = wsoracle.TypeFile, n.size
			sum := sha256.Sum256(n.content)
			e.SHA256 = hex.EncodeToString(sum[:])
		case n.mode.IsDir():
			e.Type = wsoracle.TypeDir
		case n.mode&fs.ModeSymlink != 0:
			e.Type = wsoracle.TypeSymlink
			e.Target, err = root.ReadLink(path)
			if err != nil {
				return AcceptDoc{}, err
			}
		default:
			e.Type = wsoracle.TypeOther
		}
		if path == rootPath {
			e.Path = ""
			m.Root = &e
		} else {
			m.Entries = append(m.Entries, e)
		}
	}
	digest, err := trialfix.TreeDigest(m)
	if err != nil {
		return AcceptDoc{}, err
	}
	doc, err := evaluateTree(caseID, spec, before, after, runner)
	if err != nil {
		return AcceptDoc{}, err
	}
	doc.TreeDigest = digest
	return doc, nil
}

func capturedEntry(path string, info fs.FileInfo) (wsoracle.Entry, error) {
	e := wsoracle.Entry{Path: path}
	switch st := info.Sys().(type) {
	case *syscall.Stat_t:
		e.Mode, e.UID, e.GID, e.Nlink = uint32(st.Mode)&07777, st.Uid, st.Gid, uint64(st.Nlink)
	case *unix.Stat_t:
		e.Mode, e.UID, e.GID, e.Nlink = uint32(st.Mode)&07777, st.Uid, st.Gid, uint64(st.Nlink)
	default:
		return e, errors.New("capture: missing file metadata")
	}
	return e, nil
}

// ReadLink reads the final link itself through a held parent; it never follows the target.
func (r *RootFS) ReadLink(name string) (string, error) {
	if !fs.ValidPath(name) || name == "." {
		return "", fs.ErrInvalid
	}
	dir, base := splitParent(name)
	fd, err := r.open(dir, unix.O_DIRECTORY)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	buf := make([]byte, 4097)
	n, err := unix.Readlinkat(fd, base, buf)
	if err != nil {
		return "", err
	}
	if n == len(buf) {
		return "", errors.New("capture: link target too long")
	}
	return string(buf[:n]), nil
}
