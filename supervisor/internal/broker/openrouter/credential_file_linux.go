//go:build linux

package openrouter

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// credentialOPath is Linux O_PATH (not exposed by syscall on every Go version).
const credentialOPath = 0x200000

// credentialAncestorPolicy states who may own and write the directories that
// lead to a service credential file. Every ancestor, including "/", must be a
// real directory owned by trustedUID and not group- or other-writable.
// exemptThrough is empty in production; tests use it to exempt the temp base
// (and everything above it) from the owner/mode rule while still refusing
// symlinks and non-directories on every component.
type credentialAncestorPolicy struct {
	trustedUID    uint32
	allowRoot     bool
	exemptThrough string
}

// serviceCredentialPaths are the two code-defined key files of the production
// service (engineering route and research route). Reads of exactly these paths
// walk every ancestor from "/" with no symlink following; every other path
// (the dev/env _FILE route) keeps the final-component-only O_NOFOLLOW rule.
var serviceCredentialPaths = []string{
	"/etc/tbound/provider/opencode-go.key",
	"/etc/tbound/provider/openrouter.key",
}

// serviceCredentialPolicy is the production ancestor rule: root-owned,
// not group- or other-writable, nothing exempt.
var serviceCredentialPolicy = credentialAncestorPolicy{trustedUID: 0}

// ServiceCredentialPaths returns the fixed service key paths that are read
// through the strict ancestor walk.
func ServiceCredentialPaths() []string {
	return append([]string(nil), serviceCredentialPaths...)
}

func isServiceCredentialPath(path string) bool {
	return slices.Contains(serviceCredentialPaths, filepath.Clean(path))
}

func readSecureCredentialFile(path string) ([]byte, error) {
	if isServiceCredentialPath(path) {
		return readServiceCredentialFile(path, serviceCredentialPolicy)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return readCredentialDescriptor(fd)
}

// readServiceCredentialFile opens path fd-relatively from "/" one component at a
// time with openat(O_PATH|O_NOFOLLOW|O_DIRECTORY), checking every ancestor
// against policy, then opens the file itself relative to the last verified
// directory with O_NOFOLLOW. A non-canonical spelling of a fixed path is refused.
func readServiceCredentialFile(path string, policy credentialAncestorPolicy) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, os.ErrInvalid
	}
	directory, name := filepath.Split(path)
	directory = filepath.Clean(directory)
	if name == "" || name == "." || name == ".." {
		return nil, os.ErrInvalid
	}
	current, err := syscall.Open("/", credentialOPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := checkCredentialAncestor(current, "/", policy); err != nil {
		_ = syscall.Close(current)
		return nil, err
	}
	walked := ""
	for _, component := range strings.Split(strings.TrimPrefix(directory, "/"), "/") {
		if component == "" {
			continue
		}
		walked += "/" + component
		next, err := syscall.Openat(current, component, credentialOPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		_ = syscall.Close(current)
		if err != nil {
			return nil, err
		}
		current = next
		if err := checkCredentialAncestor(current, walked, policy); err != nil {
			_ = syscall.Close(current)
			return nil, err
		}
	}
	fd, err := syscall.Openat(current, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	_ = syscall.Close(current)
	if err != nil {
		return nil, err
	}
	return readCredentialDescriptor(fd)
}

func checkCredentialAncestor(fd int, path string, policy credentialAncestorPolicy) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return os.ErrPermission
	}
	if credentialAncestorExempt(path, policy.exemptThrough) {
		return nil
	}
	if (stat.Uid != policy.trustedUID && !(policy.allowRoot && stat.Uid == 0)) || stat.Mode&0o022 != 0 {
		return os.ErrPermission
	}
	return nil
}

// credentialAncestorExempt reports whether path is exemptThrough or one of its
// ancestors. It is never true in production, where exemptThrough is empty.
func credentialAncestorExempt(path, exemptThrough string) bool {
	if exemptThrough == "" {
		return false
	}
	return path == exemptThrough || path == "/" || strings.HasPrefix(exemptThrough, path+"/")
}

// readCredentialDescriptor takes ownership of fd and applies the file rules:
// regular file, owner = euid, mode 0400/0600, no setuid/setgid/sticky, bounded.
func readCredentialDescriptor(fd int) ([]byte, error) {
	file := os.NewFile(uintptr(fd), "openrouter-credential")
	if file == nil {
		_ = syscall.Close(fd)
		return nil, os.ErrInvalid
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&^os.FileMode(0o600) != 0 ||
		info.Mode().Perm()&0o400 == 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, os.ErrPermission
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return nil, os.ErrPermission
	}
	content, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(content) > maxCredentialBytes {
		return nil, os.ErrInvalid
	}
	return content, nil
}

// ReadCredentialSourceFile pins every ancestor without following symlinks. Source
// ancestors must belong to root or the caller, with no group or other write access.
func ReadCredentialSourceFile(path string) ([]byte, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return readServiceCredentialFile(absolute, credentialAncestorPolicy{trustedUID: uint32(os.Geteuid()), allowRoot: true})
}
