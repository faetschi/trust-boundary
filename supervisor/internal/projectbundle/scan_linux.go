//go:build linux

package projectbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const maxRefusals = 64

// Refusal names one path that cannot be part of a bundle and why.
type Refusal struct {
	Path   string
	Reason string
}

// RefusedError lists what a scan refused. Truncated is set when more refusals were not recorded.
type RefusedError struct {
	Refusals  []Refusal
	Truncated bool
}

func (e *RefusedError) Error() string {
	var b strings.Builder
	b.WriteString("project refused:")
	for _, r := range e.Refusals {
		fmt.Fprintf(&b, "\n  %s: %s", r.Path, r.Reason)
	}
	if e.Truncated {
		b.WriteString("\n  (further refusals not listed)")
	}
	return b.String()
}

type scanOptions struct {
	Exclude []string
	Limits  Limits
}

type scanner struct {
	limits   Limits
	exclude  map[string]bool
	rootDev  uint64
	entries  []Entry
	excluded []string
	refusals []Refusal
	total    int64
	stop     bool
}

// scanTree walks root without following symlinks and returns its manifest and the excluded paths.
// Git administration (a ".git" directory or file at any level), the root's fence directory and the
// exclude paths (whole path components) are skipped and listed. Anything the bundle cannot carry is
// refused with its path and reason.
func scanTree(rootPath string, opts scanOptions) (Manifest, []string, error) {
	if err := opts.Limits.validate(); err != nil {
		return Manifest{}, nil, err
	}
	s := &scanner{limits: opts.Limits, exclude: make(map[string]bool, len(opts.Exclude))}
	for _, p := range opts.Exclude {
		if err := validatePath(p); err != nil {
			return Manifest{}, nil, fmt.Errorf("projectbundle: exclude path %q: %v", p, err)
		}
		s.exclude[p] = true
	}
	root, err := openRoot(rootPath)
	if err != nil {
		return Manifest{}, nil, err
	}
	defer root.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &st); err != nil {
		return Manifest{}, nil, err
	}
	s.rootDev = uint64(st.Dev)
	if err := s.walk(root, ""); err != nil {
		return Manifest{}, nil, err
	}
	if len(s.refusals) > 0 {
		return Manifest{}, nil, &RefusedError{Refusals: s.refusals, Truncated: len(s.refusals) >= maxRefusals}
	}
	sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].Path < s.entries[j].Path })
	sort.Strings(s.excluded)
	manifest := Manifest{Format: BundleFormat, Entries: s.entries}
	if manifest.Entries == nil {
		manifest.Entries = []Entry{}
	}
	if err := manifest.Validate(opts.Limits); err != nil {
		return Manifest{}, nil, fmt.Errorf("projectbundle: scan produced an invalid manifest: %w", err)
	}
	return manifest, s.excluded, nil
}

func (s *scanner) refuse(path, reason string) {
	if len(s.refusals) < maxRefusals {
		s.refusals = append(s.refusals, Refusal{Path: path, Reason: reason})
	}
	if len(s.refusals) >= maxRefusals {
		s.stop = true
	}
}

func (s *scanner) isExcluded(rel, name string) bool {
	return name == gitDir || (rel == name && name == fenceDir) || s.exclude[rel]
}

func (s *scanner) walk(dir *os.File, rel string) error {
	// shortcut: Readdirnames loads one whole directory before the entry limit applies; read in batches if a project may hold millions of files in one directory.
	names, err := dir.Readdirnames(-1)
	if err != nil {
		s.refuse(displayPath(rel), fmt.Sprintf("cannot list directory: %v", err))
		return nil
	}
	sort.Strings(names)
	for _, name := range names {
		if s.stop {
			return nil
		}
		path := name
		if rel != "" {
			path = rel + "/" + name
		}
		if s.isExcluded(path, name) {
			s.excluded = append(s.excluded, path)
			continue
		}
		if err := validateManifestPath(path); err != nil {
			s.refuse(path, fmt.Sprintf("path is unsupported: %v", err))
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			s.refuse(path, fmt.Sprintf("cannot stat: %v", err))
			continue
		}
		if len(s.entries) >= s.limits.MaxEntries {
			s.refuse(path, fmt.Sprintf("more than the limit of %d entries", s.limits.MaxEntries))
			s.stop = true
			return nil
		}
		if st.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
			s.refuse(path, "setuid, setgid or sticky bit")
			continue
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := s.directory(dir, name, path, &st); err != nil {
				return err
			}
		case unix.S_IFREG:
			s.file(dir, name, path, &st)
		default:
			s.refuse(path, kindName(st.Mode)+" is not supported")
		}
	}
	return nil
}

func displayPath(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func kindName(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return "symlink"
	case unix.S_IFIFO:
		return "FIFO"
	case unix.S_IFSOCK:
		return "socket"
	case unix.S_IFCHR, unix.S_IFBLK:
		return "device"
	}
	return "special file"
}

func (s *scanner) directory(parent *os.File, name, path string, st *unix.Stat_t) error {
	if uint64(st.Dev) != s.rootDev {
		s.refuse(path, "mount point or other filesystem")
		return nil
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		s.refuse(path, fmt.Sprintf("cannot open directory: %v", err))
		return nil
	}
	child := os.NewFile(uintptr(fd), path)
	defer child.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !sameNode(st, &opened) {
		s.refuse(path, "changed while being scanned")
		return nil
	}
	s.entries = append(s.entries, Entry{Path: path, Type: TypeDir})
	return s.walk(child, path)
}

func (s *scanner) file(parent *os.File, name, path string, st *unix.Stat_t) {
	switch {
	case st.Nlink > 1:
		s.refuse(path, fmt.Sprintf("hard link (link count %d)", st.Nlink))
		return
	case st.Size > s.limits.MaxFileBytes:
		s.refuse(path, fmt.Sprintf("size %d exceeds the limit of %d bytes", st.Size, s.limits.MaxFileBytes))
		return
	case s.total+st.Size > s.limits.MaxTotalBytes:
		s.refuse(path, fmt.Sprintf("total size exceeds the limit of %d bytes", s.limits.MaxTotalBytes))
		s.stop = true
		return
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		s.refuse(path, fmt.Sprintf("cannot open: %v", err))
		return
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !sameNode(st, &opened) || opened.Size != st.Size {
		s.refuse(path, "changed while being scanned")
		return
	}
	sum, err := hashFile(f, st.Size)
	if err != nil {
		s.refuse(path, err.Error())
		return
	}
	s.total += st.Size
	s.entries = append(s.entries, Entry{Path: path, Type: TypeFile, Executable: st.Mode&0o111 != 0, Size: st.Size, SHA256: sum})
}

func sameNode(a, b *unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode&unix.S_IFMT == b.Mode&unix.S_IFMT
}

// hashFile returns the hex SHA-256 of exactly size bytes and fails if the file is longer or shorter.
func hashFile(f *os.File, size int64) (string, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, size+1))
	if err != nil {
		return "", fmt.Errorf("cannot read: %v", err)
	}
	if n != size {
		return "", fmt.Errorf("changed while being read")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func openRoot(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// openBeneath opens rel below root one component at a time with O_NOFOLLOW, so a symlink anywhere in
// the path fails the open. flags apply to the last component; an empty rel opens root itself.
func openBeneath(root *os.File, rel string, flags int) (*os.File, error) {
	parts := []string{"."}
	if rel != "" {
		parts = strings.Split(rel, "/")
	}
	dirfd, owned := int(root.Fd()), false
	for i, part := range parts {
		f := unix.O_RDONLY | unix.O_DIRECTORY
		if i == len(parts)-1 {
			f = flags
		}
		fd, err := unix.Openat(dirfd, part, f|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if owned {
			unix.Close(dirfd)
		}
		if err != nil {
			return nil, &os.PathError{Op: "openat", Path: rel, Err: err}
		}
		dirfd, owned = fd, true
	}
	return os.NewFile(uintptr(dirfd), rel), nil
}

// copyFile streams the content of the file at e.Path below root to w and verifies it against e.
func copyFile(root *os.File, e Entry, w io.Writer) error {
	f, err := openBeneath(root, e.Path, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return err
	}
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink > 1 || st.Size != e.Size {
		return fmt.Errorf("projectbundle: %s changed while the bundle was written", e.Path)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(f, e.Size+1))
	if err != nil {
		return err
	}
	if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return fmt.Errorf("projectbundle: %s changed while the bundle was written", e.Path)
	}
	return nil
}

// writeContents streams the content of every file entry in order.
func writeContents(rootPath string, entries []Entry, w io.Writer) error {
	root, err := openRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, e := range entries {
		if e.Type != TypeFile {
			continue
		}
		if err := copyFile(root, e, w); err != nil {
			return err
		}
	}
	return nil
}
