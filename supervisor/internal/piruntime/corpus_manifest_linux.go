//go:build linux

package piruntime

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// corpusOPath is Linux O_PATH (not exposed by syscall on every Go version).
const corpusOPath = 0x200000

// corpusAncestorPolicy states who may own the directories that lead to the corpus
// manifest and the file itself. Every ancestor, including "/", must be a real
// directory owned by trustedUID and not group- or other-writable. exemptThrough is
// empty in production; tests use it to exempt their temp base (and everything above
// it) from the owner and mode rule while still refusing symlinks and non-directories
// on every component. It is the same rule as the provider key files
// (internal/broker/openrouter/credential_file_linux.go), applied to a root-owned file.
type corpusAncestorPolicy struct {
	trustedUID    uint32
	exemptThrough string
}

// corpusManifestPolicy is the production rule: root-owned, nothing exempt.
var corpusManifestPolicy = corpusAncestorPolicy{trustedUID: 0}

// ReadD09ManagedPiCorpusManifest reads the frozen corpus manifest from its fixed
// path. It walks every ancestor from "/" with no symlink following and refuses a
// manifest that is not a regular, root-owned file that no group or other can write.
// The caller still has to match the content against the digest in the signed config.
func ReadD09ManagedPiCorpusManifest() ([]byte, error) {
	return readCorpusManifestFile(D09ManagedPiCorpusManifestPath, D09ManagedPiCorpusManifestMaxBytes, corpusManifestPolicy)
}

// readCorpusManifestFile opens path fd-relatively from "/" one component at a time
// with openat(O_PATH|O_NOFOLLOW|O_DIRECTORY), checking every ancestor against policy,
// then opens the file relative to the last verified directory with O_NOFOLLOW. A
// non-canonical spelling of the path is refused.
func readCorpusManifestFile(path string, maximum int64, policy corpusAncestorPolicy) ([]byte, error) {
	if maximum <= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrD09ManagedLiveRootRefused
	}
	directory, name := filepath.Split(path)
	directory = filepath.Clean(directory)
	if name == "" || name == "." || name == ".." {
		return nil, ErrD09ManagedLiveRootRefused
	}
	current, err := syscall.Open("/", corpusOPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := checkCorpusAncestor(current, "/", policy); err != nil {
		_ = syscall.Close(current)
		return nil, err
	}
	walked := ""
	for _, component := range strings.Split(strings.TrimPrefix(directory, "/"), "/") {
		if component == "" {
			continue
		}
		walked += "/" + component
		next, err := syscall.Openat(current, component, corpusOPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		_ = syscall.Close(current)
		if err != nil {
			return nil, err
		}
		current = next
		if err := checkCorpusAncestor(current, walked, policy); err != nil {
			_ = syscall.Close(current)
			return nil, err
		}
	}
	fd, err := syscall.Openat(current, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	_ = syscall.Close(current)
	if err != nil {
		return nil, err
	}
	return readCorpusDescriptor(fd, maximum, policy)
}

func checkCorpusAncestor(fd int, path string, policy corpusAncestorPolicy) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return ErrD09ManagedLiveRootRefused
	}
	if corpusAncestorExempt(path, policy.exemptThrough) {
		return nil
	}
	if stat.Uid != policy.trustedUID || stat.Mode&0o022 != 0 {
		return ErrD09ManagedLiveRootRefused
	}
	return nil
}

// corpusAncestorExempt reports whether path is exemptThrough or one of its
// ancestors. It is never true in production, where exemptThrough is empty.
func corpusAncestorExempt(path, exemptThrough string) bool {
	if exemptThrough == "" {
		return false
	}
	return path == exemptThrough || path == "/" || strings.HasPrefix(exemptThrough, path+"/")
}

// readCorpusDescriptor takes ownership of fd and applies the file rules: regular
// file, owned by the trusted uid, no group or other write, no setuid, setgid or
// sticky bit, non-empty and at most maximum bytes.
func readCorpusDescriptor(fd int, maximum int64, policy corpusAncestorPolicy) ([]byte, error) {
	file := os.NewFile(uintptr(fd), "corpus-manifest")
	if file == nil {
		_ = syscall.Close(fd)
		return nil, ErrD09ManagedLiveRootRefused
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, ErrD09ManagedLiveRootRefused
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != policy.trustedUID {
		return nil, ErrD09ManagedLiveRootRefused
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) > maximum || int64(len(content)) != info.Size() {
		return nil, ErrD09ManagedLiveRootRefused
	}
	return content, nil
}
