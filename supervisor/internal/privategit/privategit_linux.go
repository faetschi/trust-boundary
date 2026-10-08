//go:build linux

package privategit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unsafe"

	"tbound/supervisor/internal/workspace"
)

const (
	linuxOPath         = 0x200000
	atRemovedir        = 0x200
	maxGitOutputBytes  = 1 << 20
	maxGitFileBytes    = 4 << 20
	maxGitEntries      = 4096
	maxGitCleanupDepth = 512
)

// PinGitExecutable opens one approved executable descriptor and records the
// approved bytes. The descriptor, not the input pathname, is used for every
// later child exec. Approval is an explicit trusted-host dependency; this
// function does not decide which same-UID binary is acceptable.
func PinGitExecutable(path string, approval GitExecutableApproval) (PinnedGit, error) {
	if strings.TrimSpace(path) != path || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return PinnedGit{}, ErrGitExecutable
	}
	if !validSHA256(approval.ExpectedDigest) || approval.Verifier == nil || strings.TrimSpace(approval.Boundary) == "" {
		return PinnedGit{}, fmt.Errorf("%w: missing trusted expected identity or execution boundary", ErrGitExecutable)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved {
		return PinnedGit{}, fmt.Errorf("%w: resolve executable: %v", ErrGitExecutable, err)
	}
	if err := validateTrustedPath(resolved); err != nil {
		return PinnedGit{}, err
	}
	fd, err := syscall.Open(resolved, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return PinnedGit{}, fmt.Errorf("%w: open executable descriptor: %v", ErrGitExecutable, err)
	}
	file := os.NewFile(uintptr(fd), "pinned-git")
	if file == nil {
		_ = syscall.Close(fd)
		return PinnedGit{}, ErrGitExecutable
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()
	stat, err := statFile(file)
	if err != nil {
		return PinnedGit{}, fmt.Errorf("%w: stat executable descriptor: %v", ErrGitExecutable, err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Uid != uint32(syscall.Geteuid()) && stat.Uid != 0 || stat.Mode&0o111 == 0 || stat.Mode&0o022 != 0 {
		return PinnedGit{}, ErrGitExecutable
	}
	digest, err := hashExecutableFile(file, stat.Size)
	if err != nil {
		return PinnedGit{}, fmt.Errorf("%w: hash executable descriptor: %v", ErrGitExecutable, err)
	}
	identity := GitExecutableIdentity{
		Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: stat.Size,
		Mode: stat.Mode, MtimeSec: stat.Mtim.Sec, MtimeNsec: stat.Mtim.Nsec,
		Digest: digest,
	}
	if digest != approval.ExpectedDigest {
		return PinnedGit{}, fmt.Errorf("%w: executable bytes do not match trusted expected digest", ErrGitExecutable)
	}
	if err := approval.Verifier(identity); err != nil {
		return PinnedGit{}, fmt.Errorf("%w: trusted verifier: %v", ErrGitExecutable, err)
	}
	closeOnError = false
	return PinnedGit{path: resolved, file: file, verify: approval.Verifier, identity: executableIdentity{
		Device: identity.Device, Inode: identity.Inode, Size: identity.Size,
		Mode: identity.Mode, MtimeSec: identity.MtimeSec, MtimeNsec: identity.MtimeNsec,
		Digest: identity.Digest,
	}, boundary: approval.Boundary}, nil
}

// Create captures source through the existing descriptor-relative workspace
// importer with its strict reserved-Git admission filter, then initializes a
// new private Git directory beneath parent. A stable source Git administration
// name is rejected before destination creation; recursive entry admission and
// identity checks prevent a race-inserted or renamed Git object from being
// read or promoted.
func Create(ctx context.Context, parent *os.File, source SourceDescriptor, options Options) (*Repository, error) {
	if ctx == nil {
		return nil, ErrInvalidOptions
	}
	if err := validateOptions(source, options); err != nil {
		return nil, err
	}
	if err := validatePrivateDirectory(parent); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUntrustedParent, err)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	sourceInfo, err := statFile(source.Root)
	if err != nil {
		return nil, fmt.Errorf("stat bound source descriptor: %w", err)
	}
	if err := source.Binding.VerifySource(source.Root, source.Generation, source.MetadataPolicyDigest, source.XattrVisibility.ProfileDigest, source.BindingDigest); err != nil {
		return nil, fmt.Errorf("%w: initial source verification: %v", ErrSourceBinding, err)
	}

	wsOptions := workspace.Options{
		Generation: source.Generation, MetadataPolicyDigest: source.MetadataPolicyDigest,
		Limits: workspace.Limits{
			MaxObjects: source.Limits.MaxObjects, MaxDepth: source.Limits.MaxDepth,
			MaxFileBytes: source.Limits.MaxFileBytes, MaxTotalBytes: source.Limits.MaxTotalBytes,
		},
		QuiescentRoot: source.Quiescent,
		XattrVisibility: workspace.XattrVisibilityAttestation{
			ProfileDigest: source.XattrVisibility.ProfileDigest,
			Complete:      source.XattrVisibility.Complete,
		},
		ImportPolicy: workspace.ImportPolicy{
			ReservedMetadata: workspace.ReservedMetadataRejectLiveGit,
		},
	}
	if err := rejectLiveGitNames(source.Root, source.Limits); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}

	name, err := privateName("staging")
	if err != nil {
		return nil, err
	}
	root, err := createPrivateDirectory(parent, name)
	if err != nil {
		return nil, err
	}
	rootInfo, err := statFile(root)
	if err != nil {
		_ = root.Close()
		return nil, errors.Join(err, ErrCleanupIncomplete)
	}
	var template *os.File
	cleanup := func(cause error) (*Repository, error) {
		if template != nil {
			_ = template.Close()
			template = nil
		}
		cleanupErr := cleanupCreated(parent, name, root, rootInfo, source.Limits, options.CleanupBoundary)
		if cleanupErr != nil {
			_ = root.Close()
		}
		if cleanupErr != nil {
			return nil, errors.Join(cause, fmt.Errorf("%w: %v", ErrCleanupIncomplete, cleanupErr))
		}
		return nil, cause
	}

	imported, err := workspace.Import(source.Root, root, wsOptions)
	if err != nil {
		return cleanup(fmt.Errorf("import filtered source snapshot: %w", err))
	}
	if err := source.Binding.VerifySource(source.Root, source.Generation, source.MetadataPolicyDigest, source.XattrVisibility.ProfileDigest, source.BindingDigest); err != nil {
		return cleanup(fmt.Errorf("%w: post-import source verification: %v", ErrSourceBinding, err))
	}
	if err := rejectLiveGitManifest(imported); err != nil {
		return cleanup(err)
	}
	if err := contextErr(ctx); err != nil {
		return cleanup(err)
	}

	templateName, err := privateName("template")
	if err != nil {
		return cleanup(err)
	}
	template, err = createPrivateDirectory(root, templateName)
	if err != nil {
		return cleanup(fmt.Errorf("create empty Git template: %w", err))
	}
	versionOutput, err := runGit(ctx, options.Git, root, template, []string{"--version"})
	if err != nil {
		return cleanup(errors.Join(fmt.Errorf("%w: query pinned Git version", ErrGitInitialization), err))
	}
	gitVersion := strings.TrimSpace(string(versionOutput))
	if !strings.HasPrefix(gitVersion, "git version ") || strings.ContainsAny(gitVersion, "\r\n") {
		return cleanup(fmt.Errorf("%w: unexpected Git version output", ErrGitInitialization))
	}
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "protocol.file.allow=never",
		"init", "--quiet", "--initial-branch=" + options.InitialBranch,
		"--template=/proc/self/fd/5",
	}
	if _, err := runGit(ctx, options.Git, root, template, args); err != nil {
		return cleanup(errors.Join(ErrGitInitialization, err))
	}
	if err := cleanupCreatedChild(root, templateName, template, source.Limits); err != nil {
		return cleanup(fmt.Errorf("remove temporary Git template: %w", err))
	}
	template = nil
	if err := normalizeAndVerifyGitAdmin(root, options.InitialBranch); err != nil {
		return cleanup(err)
	}
	gitAdminInfo, err := openGitAdmin(root)
	if err != nil {
		return cleanup(err)
	}
	finalOptions := wsOptions
	finalOptions.ImportPolicy = workspace.ImportPolicy{ReservedMetadata: workspace.ReservedMetadataExcludeRootPrivateGit}
	final, err := workspace.Scan(root, finalOptions)
	if err != nil {
		return cleanup(fmt.Errorf("verify private worktree snapshot: %w", err))
	}
	if !reflect.DeepEqual(imported.Manifest, final.Manifest) || imported.TreeDigest != final.TreeDigest || imported.Objects != final.Objects || imported.Bytes != final.Bytes || imported.Manifest.MetadataPolicyDigest != final.Manifest.MetadataPolicyDigest {
		return cleanup(fmt.Errorf("%w: private worktree differs from imported source snapshot", ErrGitMetadata))
	}
	if err := root.Sync(); err != nil {
		return cleanup(fmt.Errorf("sync private Git repository: %w", err))
	}
	if err := contextErr(ctx); err != nil {
		return cleanup(err)
	}
	if err := rejectLiveGitNames(source.Root, source.Limits); err != nil {
		return cleanup(err)
	}
	if err := source.Binding.VerifySource(source.Root, source.Generation, source.MetadataPolicyDigest, source.XattrVisibility.ProfileDigest, source.BindingDigest); err != nil {
		return cleanup(fmt.Errorf("%w: final source verification: %v", ErrSourceBinding, err))
	}

	parentCopy, err := duplicateDirectory(parent, "private-git-parent")
	if err != nil {
		return cleanup(fmt.Errorf("retain private Git parent descriptor: %w", err))
	}
	repository := &Repository{
		root: root, parent: parentCopy, name: name,
		snapshot: Snapshot{Manifest: imported.Manifest, TreeDigest: imported.TreeDigest, Objects: imported.Objects, Bytes: imported.Bytes},
		provenance: Provenance{
			SchemaVersion:    "tbound-private-git/v1",
			Transfer:         "descriptor-relative-filtered-byte-copy",
			FilterPolicy:     "workspace-reserved-git/v1",
			SourceGeneration: source.Generation, SourceTreeDigest: imported.TreeDigest,
			SourceManifest: imported.Manifest, MetadataPolicyDigest: source.MetadataPolicyDigest,
			SourceBindingDigest: source.BindingDigest,
			GitVersion:          gitVersion, GitExecutableDigest: options.Git.identity.Digest,
			GitExecutionBoundary: options.Git.boundary,
			InitialBranch:        options.InitialBranch,
			SourceRootDevice:     uint64(sourceInfo.Dev), SourceRootInode: uint64(sourceInfo.Ino),
			PrivateRootDevice: uint64(rootInfo.Dev), PrivateRootInode: uint64(rootInfo.Ino),
			PrivateGitDevice: uint64(gitAdminInfo.Dev), PrivateGitInode: uint64(gitAdminInfo.Ino),
			XattrProfileDigest: source.XattrVisibility.ProfileDigest, CaptureLimits: source.Limits,
		},
		identity: nodeIdentity{Device: uint64(rootInfo.Dev), Inode: uint64(rootInfo.Ino)},
		gitAdmin: nodeIdentity{Device: uint64(gitAdminInfo.Dev), Inode: uint64(gitAdminInfo.Ino)},
		limits:   source.Limits, xattr: source.XattrVisibility, boundary: options.CleanupBoundary,
	}
	return repository, nil
}

func validateOptions(source SourceDescriptor, options Options) error {
	if source.Root == nil || source.Binding == nil || !source.Quiescent || source.Generation == "" || strings.TrimSpace(source.Generation) != source.Generation || !validSHA256(source.MetadataPolicyDigest) || !validSHA256(source.BindingDigest) || !source.XattrVisibility.Complete || !validSHA256(source.XattrVisibility.ProfileDigest) {
		return ErrInvalidOptions
	}
	limits := source.Limits
	if limits.MaxObjects <= 0 || limits.MaxObjects > workspace.DefaultLimits().MaxObjects || limits.MaxDepth <= 0 || limits.MaxDepth > 4096 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 || limits.MaxFileBytes > limits.MaxTotalBytes {
		return ErrInvalidOptions
	}
	if options.Git.path == "" || options.Git.file == nil || options.Git.identity.Digest == "" || options.Git.verify == nil || options.InitialBranch != "main" {
		return ErrInvalidOptions
	}
	if !options.CleanupBoundary.ExclusiveParent || strings.TrimSpace(options.CleanupBoundary.Description) == "" {
		return ErrInvalidOptions
	}
	return nil
}

func rejectLiveGitManifest(snapshot workspace.Snapshot) error {
	for _, object := range snapshot.Manifest.Objects {
		for _, segment := range strings.Split(object.Path, "/") {
			if strings.EqualFold(segment, ".git") {
				return fmt.Errorf("%w: %q", ErrSourceContainsGit, object.Path)
			}
		}
	}
	return nil
}

// rejectLiveGitNames is a narrow Git-specific descriptor walk. It does not
// replace workspace's type, link, mount, ownership, xattr, or byte validators;
// it only lets the package reject a .git name before copying when the source
// has non-canonical source modes that workspace.Scan intentionally does not
// accept as a sealed destination.
func rejectLiveGitNames(root *os.File, limits Limits) error {
	state := gitNameWalkState{remaining: limits.MaxObjects, maxDepth: limits.MaxDepth}
	if state.remaining <= 0 || state.maxDepth <= 0 {
		return ErrInvalidOptions
	}
	return walkGitNames(root, 0, &state)
}

type gitNameWalkState struct {
	remaining int
	maxDepth  int
}

func walkGitNames(directory *os.File, depth int, state *gitNameWalkState) error {
	if depth > state.maxDepth {
		return fmt.Errorf("%w: Git-name preflight depth", workspace.ErrLimit)
	}
	entries, err := readBoundedEntries(directory, state.remaining)
	if err != nil {
		return fmt.Errorf("Git-name preflight: %w", err)
	}
	for _, entry := range entries {
		if state.remaining == 0 {
			return fmt.Errorf("%w: Git-name preflight object count", workspace.ErrLimit)
		}
		state.remaining--
		if strings.EqualFold(entry.Name(), ".git") {
			return fmt.Errorf("%w: %q", ErrSourceContainsGit, entry.Name())
		}
		node, err := openPathAt(directory, entry.Name())
		if err != nil {
			// workspace.Import owns the definitive no-follow/link error. The
			// preflight must not turn a symlink into a Git-specific result.
			if errors.Is(err, syscall.ELOOP) {
				continue
			}
			return fmt.Errorf("Git-name preflight open %q: %w", entry.Name(), err)
		}
		info, statErr := statFile(node)
		if statErr != nil {
			_ = node.Close()
			return statErr
		}
		if info.Mode&syscall.S_IFMT == syscall.S_IFDIR {
			subdirectory, openErr := openDirectoryAt(directory, entry.Name())
			_ = node.Close()
			if openErr != nil {
				return openErr
			}
			err = walkGitNames(subdirectory, depth+1, state)
			closeErr := subdirectory.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
			continue
		}
		if err := node.Close(); err != nil {
			return err
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
}

func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func privateName(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate private Git name: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(random[:]), nil
}

func validatePrivateDirectory(directory *os.File) error {
	if directory == nil {
		return ErrUntrustedParent
	}
	info, err := statFile(directory)
	if err != nil {
		return err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) || info.Mode&0o7777 != 0o700 {
		return ErrUntrustedParent
	}
	return nil
}

func createPrivateDirectory(parent *os.File, name string) (*os.File, error) {
	if err := syscall.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		return nil, fmt.Errorf("create private Git root: %w", err)
	}
	directory, err := openDirectoryAt(parent, name)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open private Git root: %w", err), ErrCleanupIncomplete)
	}
	if err := syscall.Fchmod(int(directory.Fd()), 0o700); err != nil {
		_ = directory.Close()
		return nil, errors.Join(fmt.Errorf("set private Git root mode: %w", err), ErrCleanupIncomplete)
	}
	if err := validatePrivateDirectory(directory); err != nil {
		_ = directory.Close()
		return nil, errors.Join(err, ErrCleanupIncomplete)
	}
	return directory, nil
}

func duplicateDirectory(source *os.File, name string) (*os.File, error) {
	fd, err := syscall.Dup(int(source.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), name), nil
}

func openDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openPathAt(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openRegularAt(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func statFile(file *os.File) (syscall.Stat_t, error) {
	if file == nil {
		return syscall.Stat_t{}, ErrUntrustedParent
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &info); err != nil {
		return syscall.Stat_t{}, err
	}
	return info, nil
}

func mountID(file *os.File) (uint64, error) {
	if file == nil {
		return 0, ErrCleanupIncomplete
	}
	info, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil {
		return 0, err
	}
	defer info.Close()
	data, err := io.ReadAll(io.LimitReader(info, 16<<10))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "mnt_id" {
			continue
		}
		id, parseErr := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if parseErr == nil {
			return id, nil
		}
	}
	return 0, ErrCleanupIncomplete
}

func checkMount(file *os.File, rootMount uint64, path string) error {
	id, err := mountID(file)
	if err != nil {
		return fmt.Errorf("%w: mount identity at %s: %v", ErrCleanupIncomplete, path, err)
	}
	return verifyCleanupMountIdentity(id, rootMount, path)
}

func verifyCleanupMountIdentity(id, rootMount uint64, path string) error {
	if id != rootMount {
		return fmt.Errorf("%w: nested mount at %s", ErrCleanupIncomplete, path)
	}
	return nil
}

func procPath(file *os.File) string {
	return fmt.Sprintf("/proc/self/fd/%d", file.Fd())
}

func validateTrustedPath(path string) error {
	current := path
	for {
		info, err := os.Stat(current)
		if err != nil {
			return fmt.Errorf("%w: stat executable parent: %v", ErrGitExecutable, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 && stat.Uid != uint32(syscall.Geteuid()) || stat.Mode&0o022 != 0 {
			return ErrGitExecutable
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func hashExecutableFile(file *os.File, size int64) (string, error) {
	if file == nil {
		return "", ErrGitExecutable
	}
	if size < 0 || size > 512<<20 {
		return "", ErrGitExecutable
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, 512<<20+1)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyPinnedGit(git PinnedGit) error {
	if git.file == nil || git.verify == nil {
		return ErrGitExecutable
	}
	stat, err := statFile(git.file)
	if err != nil {
		return fmt.Errorf("%w: stat pinned executable descriptor: %v", ErrGitExecutable, err)
	}
	if uint64(stat.Dev) != git.identity.Device || uint64(stat.Ino) != git.identity.Inode || stat.Size != git.identity.Size || stat.Mode != git.identity.Mode || stat.Mtim.Sec != git.identity.MtimeSec || stat.Mtim.Nsec != git.identity.MtimeNsec {
		return ErrGitExecutable
	}
	digest, err := hashExecutableFile(git.file, stat.Size)
	if err != nil || digest != git.identity.Digest {
		return ErrGitExecutable
	}
	identity := GitExecutableIdentity{
		Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: stat.Size,
		Mode: stat.Mode, MtimeSec: stat.Mtim.Sec, MtimeNsec: stat.Mtim.Nsec,
		Digest: digest,
	}
	if err := git.verify(identity); err != nil {
		return fmt.Errorf("%w: trusted verifier: %v", ErrGitExecutable, err)
	}
	return nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		return 0, errors.New("Git output exceeded bound")
	}
	return b.Buffer.Write(data)
}

var beforeGitStartForTests func(root, template *os.File)
var beforeCleanupUnlinkForTests func(parent *os.File, name string)

func runGit(ctx context.Context, git PinnedGit, root, template *os.File, args []string) ([]byte, error) {
	if err := verifyPinnedGit(git); err != nil {
		return nil, err
	}
	if err := validatePrivateDirectory(root); err != nil {
		return nil, fmt.Errorf("validate private Git root descriptor: %w", err)
	}
	if err := validatePrivateDirectory(template); err != nil {
		return nil, fmt.Errorf("validate private Git template descriptor: %w", err)
	}
	rootMount, err := mountID(root)
	if err != nil {
		return nil, err
	}
	if err := checkMount(template, rootMount, "Git template"); err != nil {
		return nil, err
	}
	if beforeGitStartForTests != nil {
		beforeGitStartForTests(root, template)
	}
	gitChild, err := duplicateForExec(git.file, "pinned-git-child")
	if err != nil {
		return nil, fmt.Errorf("duplicate pinned Git descriptor: %w", err)
	}
	defer gitChild.Close()
	rootChild, err := duplicateForExec(root, "private-root-child")
	if err != nil {
		return nil, fmt.Errorf("duplicate private root descriptor: %w", err)
	}
	defer rootChild.Close()
	templateChild, err := duplicateForExec(template, "private-template-child")
	if err != nil {
		return nil, fmt.Errorf("duplicate private template descriptor: %w", err)
	}
	defer templateChild.Close()
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/proc/self/fd/4",
		"XDG_CONFIG_HOME=/proc/self/fd/4",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_COUNT=0",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TEMPLATE_DIR=/proc/self/fd/5",
		"GIT_CEILING_DIRECTORIES=/proc/self/fd/4",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
		"LC_ALL=C",
		"LANG=C",
	}
	command := exec.CommandContext(ctx, "/proc/self/fd/3", args...)
	// os/exec performs chdir before it remaps ExtraFiles. Use the original
	// held root descriptor for that one pre-remap operation; after remapping,
	// Git sees root and template as child descriptors 4 and 5.
	command.Dir = procPath(root)
	command.Env = env
	command.ExtraFiles = []*os.File{gitChild, rootChild, templateChild}
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = maxGitOutputBytes, maxGitOutputBytes
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%w (stderr %q): %v", ErrGitInitialization, stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

func duplicateForExec(source *os.File, name string) (*os.File, error) {
	if source == nil {
		return nil, ErrGitExecutable
	}
	fd, err := syscall.Dup(int(source.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), name), nil
}

func normalizeAndVerifyGitAdmin(root *os.File, branch string) error {
	rootMount, err := mountID(root)
	if err != nil {
		return fmt.Errorf("%w: root mount identity: %v", ErrGitMetadata, err)
	}
	if err := normalizeGitDirectory(root, ".git", rootMount); err != nil {
		return fmt.Errorf("%w: normalize Git administration: %v", ErrGitMetadata, err)
	}
	if err := verifyGitAdminWithMount(root, branch, rootMount); err != nil {
		return err
	}
	return nil
}

func normalizeGitDirectory(parent *os.File, name string, rootMount uint64) error {
	directory, err := openDirectoryAt(parent, name)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := checkMount(directory, rootMount, name); err != nil {
		return err
	}
	if err := checkGitXattrs(directory); err != nil {
		return err
	}
	if err := syscall.Fchmod(int(directory.Fd()), 0o700); err != nil {
		return err
	}
	entries, err := readBoundedEntries(directory, maxGitEntries)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		node, info, err := openNodeAt(directory, entry.Name())
		if err != nil {
			return err
		}
		if err := checkMount(node, rootMount, entry.Name()); err != nil {
			_ = node.Close()
			return err
		}
		if err := checkGitXattrs(node); err != nil {
			_ = node.Close()
			return err
		}
		switch info.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			if err := normalizeGitDirectory(directory, entry.Name(), rootMount); err != nil {
				_ = node.Close()
				return err
			}
		case syscall.S_IFREG:
			if info.Nlink != 1 {
				_ = node.Close()
				return ErrGitMetadata
			}
			if err := syscall.Fchmod(int(node.Fd()), 0o600); err != nil {
				_ = node.Close()
				return err
			}
		default:
			_ = node.Close()
			return ErrGitMetadata
		}
		if err := node.Close(); err != nil {
			return err
		}
	}
	return nil
}

func verifyGitAdmin(root *os.File, branch string) error {
	rootMount, err := mountID(root)
	if err != nil {
		return fmt.Errorf("%w: root mount identity: %v", ErrGitMetadata, err)
	}
	return verifyGitAdminWithMount(root, branch, rootMount)
}

func openGitAdmin(root *os.File) (syscall.Stat_t, error) {
	rootMount, err := mountID(root)
	if err != nil {
		return syscall.Stat_t{}, fmt.Errorf("%w: root mount identity: %v", ErrGitMetadata, err)
	}
	directory, err := openDirectoryAt(root, ".git")
	if err != nil {
		return syscall.Stat_t{}, fmt.Errorf("%w: open new .git: %v", ErrGitMetadata, err)
	}
	defer directory.Close()
	if err := checkMount(directory, rootMount, ".git"); err != nil {
		return syscall.Stat_t{}, fmt.Errorf("%w: .git mount identity: %v", ErrGitMetadata, err)
	}
	info, err := statFile(directory)
	if err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) || info.Mode&0o7777 != 0o700 {
		return syscall.Stat_t{}, ErrGitMetadata
	}
	return info, nil
}

func verifyGitAdminWithMount(root *os.File, branch string, rootMount uint64) error {
	gitDir, err := openDirectoryAt(root, ".git")
	if err != nil {
		return fmt.Errorf("%w: .git is not a private directory: %v", ErrGitMetadata, err)
	}
	defer gitDir.Close()
	if err := checkMount(gitDir, rootMount, ".git"); err != nil {
		return fmt.Errorf("%w: .git mount identity: %v", ErrGitMetadata, err)
	}
	if err := checkGitXattrs(gitDir); err != nil {
		return err
	}
	info, err := statFile(gitDir)
	if err != nil || info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) || info.Mode&0o7777 != 0o700 {
		return ErrGitMetadata
	}
	if err := inspectGitDirectory(gitDir, "", rootMount); err != nil {
		return err
	}
	config, err := readGitFile(gitDir, "config")
	if err != nil || unsafeGitConfig(config) {
		return fmt.Errorf("%w: missing or non-allowlisted private config", ErrGitMetadata)
	}
	head, err := readGitFile(gitDir, "HEAD")
	if err != nil || string(head) != "ref: refs/heads/"+branch+"\n" {
		return fmt.Errorf("%w: unexpected private HEAD", ErrGitMetadata)
	}
	return nil
}

func inspectGitDirectory(directory *os.File, relative string, rootMount uint64) error {
	if err := checkMount(directory, rootMount, relative); err != nil {
		return fmt.Errorf("%w: mount identity at %s: %v", ErrGitMetadata, relative, err)
	}
	entries, err := readBoundedEntries(directory, maxGitEntries)
	if err != nil {
		return fmt.Errorf("%w: read %s: %v", ErrGitMetadata, relative, err)
	}
	if relative == "hooks" && len(entries) != 0 {
		return fmt.Errorf("%w: initialized hooks directory is not empty", ErrGitMetadata)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !validEntryName(name) || forbiddenGitName(name) {
			return fmt.Errorf("%w: %s/%s", ErrGitMetadata, relative, name)
		}
		node, info, err := openNodeAt(directory, name)
		if err != nil {
			return fmt.Errorf("%w: open %s/%s: %v", ErrGitMetadata, relative, name, err)
		}
		if err := checkMount(node, rootMount, childPath(relative, name)); err != nil {
			_ = node.Close()
			return fmt.Errorf("%w: mount identity at %s/%s: %v", ErrGitMetadata, relative, name, err)
		}
		if err := checkGitXattrs(node); err != nil {
			_ = node.Close()
			return err
		}
		if info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) || info.Mode&0o022 != 0 {
			_ = node.Close()
			return ErrGitMetadata
		}
		child := childPath(relative, name)
		switch info.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			if info.Mode&0o7777 != 0o700 {
				_ = node.Close()
				return ErrGitMetadata
			}
			childDirectory, err := openDirectoryAt(directory, name)
			if err != nil {
				_ = node.Close()
				return fmt.Errorf("%w: open directory %s: %v", ErrGitMetadata, child, err)
			}
			err = inspectGitDirectory(childDirectory, child, rootMount)
			_ = childDirectory.Close()
		case syscall.S_IFREG:
			if info.Nlink != 1 {
				err = ErrGitMetadata
			} else if info.Size > maxGitFileBytes {
				err = ErrGitMetadata
			} else if name == "config" && relative == "" {
				content, readErr := readOpenFile(node)
				if readErr != nil {
					err = readErr
				} else if unsafeGitConfig(content) {
					err = ErrGitMetadata
				}
			}
		default:
			err = ErrGitMetadata
		}
		closeErr := node.Close()
		if err != nil || closeErr != nil {
			return fmt.Errorf("%w: inspect %s: %v", ErrGitMetadata, child, errors.Join(err, closeErr))
		}
	}
	return nil
}

func openNodeAt(parent *os.File, name string) (*os.File, syscall.Stat_t, error) {
	pathNode, err := openPathAt(parent, name)
	if err != nil {
		return nil, syscall.Stat_t{}, err
	}
	info, err := statFile(pathNode)
	_ = pathNode.Close()
	if err != nil {
		return nil, syscall.Stat_t{}, err
	}
	if info.Mode&syscall.S_IFMT == syscall.S_IFLNK {
		return nil, syscall.Stat_t{}, ErrGitMetadata
	}
	var node *os.File
	switch info.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		node, err = openDirectoryAt(parent, name)
	case syscall.S_IFREG:
		node, err = openRegularAt(parent, name)
	default:
		return nil, syscall.Stat_t{}, ErrGitMetadata
	}
	if err != nil {
		return nil, syscall.Stat_t{}, err
	}
	actual, err := statFile(node)
	if err != nil || !sameGitNode(info, actual) {
		_ = node.Close()
		return nil, syscall.Stat_t{}, ErrGitMetadata
	}
	return node, actual, nil
}

func sameGitNode(left, right syscall.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode&syscall.S_IFMT == right.Mode&syscall.S_IFMT
}

func readGitFile(directory *os.File, name string) ([]byte, error) {
	file, err := openRegularAt(directory, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readOpenFile(file)
}

func readOpenFile(file *os.File) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, maxGitFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxGitFileBytes {
		return nil, ErrGitMetadata
	}
	return data, nil
}

func checkGitXattrs(file *os.File) error {
	if file == nil {
		return ErrGitMetadata
	}
	count, err := syscall.Listxattr(procPath(file), nil)
	if err != nil || count != 0 {
		return ErrGitMetadata
	}
	return nil
}

func unsafeGitConfig(content []byte) bool {
	allowed := map[string]string{
		"core.repositoryformatversion": "0",
		"core.filemode":                "true",
		"core.bare":                    "false",
		"core.logallrefupdates":        "true",
	}
	seen := make(map[string]bool, len(allowed))
	section := ""
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section != "core" {
				return true
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "core" {
			return true
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		fullKey := section + "." + key
		expected, ok := allowed[fullKey]
		if !ok || seen[fullKey] || value != expected {
			return true
		}
		seen[fullKey] = true
	}
	return len(seen) != len(allowed)
}

func childPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func forbiddenGitName(name string) bool {
	switch strings.ToLower(name) {
	case "commondir", "alternates", "gitdir", "worktrees":
		return true
	default:
		return false
	}
}

func validEntryName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func cleanupCreated(parent *os.File, name string, heldRoot *os.File, expected syscall.Stat_t, limits Limits, boundary CleanupBoundary) error {
	if !boundary.ExclusiveParent || strings.TrimSpace(boundary.Description) == "" || heldRoot == nil || expected.Mode == 0 {
		return ErrCleanupIncomplete
	}
	actual, err := statFile(heldRoot)
	if err != nil || !sameGitNode(actual, expected) || actual.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return ErrCleanupIncomplete
	}
	rootMount, err := mountID(heldRoot)
	if err != nil {
		return ErrCleanupIncomplete
	}
	if err := checkMount(parent, rootMount, "private parent"); err != nil {
		return ErrCleanupIncomplete
	}
	if err := removeTreeContents(heldRoot, rootMount, limits); err != nil {
		return fmt.Errorf("%w: remove private root contents: %v", ErrCleanupIncomplete, err)
	}
	if err := verifyNamedDirectory(parent, name, expected, rootMount); err != nil {
		return fmt.Errorf("%w: private root name changed: %v", ErrCleanupIncomplete, err)
	}
	if beforeCleanupUnlinkForTests != nil {
		beforeCleanupUnlinkForTests(parent, name)
	}
	if err := verifyNamedDirectory(parent, name, expected, rootMount); err != nil {
		return fmt.Errorf("%w: private root name changed before unlink: %v", ErrCleanupIncomplete, err)
	}
	if err := unlinkAt(parent, name, atRemovedir); err != nil {
		remaining := ""
		if _, seekErr := heldRoot.Seek(0, io.SeekStart); seekErr == nil {
			if entries, readErr := readBoundedEntries(heldRoot, maxGitEntries); readErr == nil {
				remaining = fmt.Sprintf(" remaining=%v", entryNames(entries))
			}
		}
		return fmt.Errorf("%w: unlink private root: %v%s", ErrCleanupIncomplete, err, remaining)
	}
	return heldRoot.Close()
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func cleanupCreatedChild(parent *os.File, name string, heldDirectory *os.File, limits Limits) error {
	if heldDirectory == nil {
		return ErrCleanupIncomplete
	}
	parentMount, err := mountID(parent)
	if err != nil {
		return ErrCleanupIncomplete
	}
	info, err := statFile(heldDirectory)
	if err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return ErrCleanupIncomplete
	}
	if err := removeTreeContents(heldDirectory, parentMount, limits); err != nil {
		return fmt.Errorf("%w: remove child contents: %v", ErrCleanupIncomplete, err)
	}
	if err := verifyNamedDirectory(parent, name, info, parentMount); err != nil {
		return fmt.Errorf("%w: child name changed: %v", ErrCleanupIncomplete, err)
	}
	if beforeCleanupUnlinkForTests != nil {
		beforeCleanupUnlinkForTests(parent, name)
	}
	if err := verifyNamedDirectory(parent, name, info, parentMount); err != nil {
		return fmt.Errorf("%w: child name changed before unlink: %v", ErrCleanupIncomplete, err)
	}
	if err := unlinkAt(parent, name, atRemovedir); err != nil {
		return fmt.Errorf("%w: unlink child: %v", ErrCleanupIncomplete, err)
	}
	return heldDirectory.Close()
}

type cleanupBudget struct {
	remaining int
	depth     int
}

func removeTreeContents(directory *os.File, rootMount uint64, limits Limits) error {
	budget := cleanupBudget{remaining: limits.MaxObjects + 4096, depth: limits.MaxDepth + maxGitCleanupDepth}
	if budget.remaining <= 0 {
		budget.remaining = 4096
	}
	if budget.depth <= 0 {
		budget.depth = maxGitCleanupDepth
	}
	if err := checkMount(directory, rootMount, "cleanup root"); err != nil {
		return err
	}
	entries, err := readBoundedEntries(directory, budget.remaining)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeTreeAt(directory, entry.Name(), &budget, 1, rootMount); err != nil {
			return err
		}
	}
	remaining, err := readBoundedEntries(directory, 1)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return ErrCleanupIncomplete
	}
	return nil
}

func removeTreeAt(parent *os.File, name string, budget *cleanupBudget, depth int, rootMount uint64) error {
	if !validEntryName(name) || budget.remaining <= 0 || depth > budget.depth {
		return ErrCleanupIncomplete
	}
	budget.remaining--
	node, info, err := openNodeAt(parent, name)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := checkMount(node, rootMount, name); err != nil {
		_ = node.Close()
		return err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		if info.Mode&syscall.S_IFMT != syscall.S_IFREG {
			_ = node.Close()
			return ErrCleanupIncomplete
		}
		if err := verifyNamedNode(parent, name, info, rootMount); err != nil {
			_ = node.Close()
			return err
		}
		err := unlinkAt(parent, name, 0)
		closeErr := node.Close()
		return errors.Join(err, closeErr)
	}
	directory, err := openDirectoryAt(parent, name)
	if err != nil {
		_ = node.Close()
		return err
	}
	if actual, statErr := statFile(directory); statErr != nil || !sameGitNode(actual, info) {
		_ = directory.Close()
		_ = node.Close()
		return ErrCleanupIncomplete
	}
	if err := removeTreeContentsBounded(directory, rootMount, budget, depth); err != nil {
		_ = directory.Close()
		_ = node.Close()
		return err
	}
	closeErr := directory.Close()
	identityErr := verifyNamedNode(parent, name, info, rootMount)
	if identityErr != nil {
		return errors.Join(closeErr, identityErr, node.Close())
	}
	unlinkErr := unlinkAt(parent, name, atRemovedir)
	nodeCloseErr := node.Close()
	return errors.Join(closeErr, unlinkErr, nodeCloseErr)
}

func removeTreeContentsBounded(directory *os.File, rootMount uint64, budget *cleanupBudget, depth int) error {
	if depth > budget.depth {
		return ErrCleanupIncomplete
	}
	if err := checkMount(directory, rootMount, "cleanup directory"); err != nil {
		return err
	}
	entries, err := readBoundedEntries(directory, budget.remaining)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeTreeAt(directory, entry.Name(), budget, depth+1, rootMount); err != nil {
			return err
		}
	}
	remaining, err := readBoundedEntries(directory, 1)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return ErrCleanupIncomplete
	}
	return nil
}

func verifyNamedDirectory(parent *os.File, name string, expected syscall.Stat_t, rootMount uint64) error {
	actual, err := verifyNamedNodeInfo(parent, name, rootMount)
	if err != nil || !sameGitNode(actual, expected) || actual.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return ErrCleanupIncomplete
	}
	return nil
}

func verifyNamedNode(parent *os.File, name string, expected syscall.Stat_t, rootMount uint64) error {
	actual, err := verifyNamedNodeInfo(parent, name, rootMount)
	if err != nil || !sameGitNode(actual, expected) {
		return ErrCleanupIncomplete
	}
	return nil
}

func verifyNamedNodeInfo(parent *os.File, name string, rootMount uint64) (syscall.Stat_t, error) {
	node, err := openPathAt(parent, name)
	if err != nil {
		return syscall.Stat_t{}, err
	}
	defer node.Close()
	actual, err := statFile(node)
	if err != nil {
		return syscall.Stat_t{}, err
	}
	if err := checkMount(node, rootMount, name); err != nil {
		return syscall.Stat_t{}, err
	}
	return actual, nil
}

func readBoundedEntries(directory *os.File, maximum int) ([]os.DirEntry, error) {
	if maximum < 0 {
		return nil, fmt.Errorf("%w: negative directory-entry budget", workspace.ErrLimit)
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	entries := make([]os.DirEntry, 0, minInt(maximum, 128))
	for {
		remaining := maximum - len(entries)
		request := remaining + 1
		if request > 128 {
			request = 128
		}
		batch, err := directory.ReadDir(request)
		if len(batch) > remaining {
			return nil, fmt.Errorf("%w: directory-entry budget", workspace.ErrLimit)
		}
		entries = append(entries, batch...)
		if err == io.EOF {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return nil, io.ErrNoProgress
		}
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func unlinkAt(parent *os.File, name string, flags uintptr) error {
	path, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(parent.Fd()), uintptr(unsafe.Pointer(path)), flags)
	if errno != 0 {
		return errno
	}
	return nil
}

func destroyRepositoryLocked(repository *Repository) error {
	if repository.root == nil || repository.parent == nil {
		return ErrCleanupIncomplete
	}
	info, err := statFile(repository.root)
	if err != nil || uint64(info.Dev) != repository.identity.Device || uint64(info.Ino) != repository.identity.Inode {
		return ErrCleanupIncomplete
	}
	if err := cleanupCreated(repository.parent, repository.name, repository.root, info, repository.limits, repository.boundary); err != nil {
		return fmt.Errorf("%w: %v", ErrCleanupIncomplete, err)
	}
	repository.root = nil
	return repository.parent.Close()
}

func verifyRepositoryLocked(repository *Repository) error {
	info, err := statFile(repository.root)
	if err != nil || uint64(info.Dev) != repository.identity.Device || uint64(info.Ino) != repository.identity.Inode || uint64(info.Dev) != repository.provenance.PrivateRootDevice || uint64(info.Ino) != repository.provenance.PrivateRootInode {
		return ErrGitMetadata
	}
	if err := validatePrivateDirectory(repository.root); err != nil {
		return ErrGitMetadata
	}
	options := workspace.Options{
		Generation:           repository.provenance.SourceGeneration,
		MetadataPolicyDigest: repository.provenance.MetadataPolicyDigest,
		Limits: workspace.Limits{
			MaxObjects: repository.limits.MaxObjects, MaxDepth: repository.limits.MaxDepth,
			MaxFileBytes: repository.limits.MaxFileBytes, MaxTotalBytes: repository.limits.MaxTotalBytes,
		},
		QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{
			ProfileDigest: repository.xattr.ProfileDigest, Complete: repository.xattr.Complete,
		},
		ImportPolicy: workspace.ImportPolicy{ReservedMetadata: workspace.ReservedMetadataExcludeRootPrivateGit},
	}
	actual, err := workspace.Scan(repository.root, options)
	if repository.provenance.FilterPolicy != "workspace-reserved-git/v1" ||
		repository.provenance.SourceTreeDigest != repository.snapshot.TreeDigest ||
		repository.provenance.SourceRootDevice == 0 || repository.provenance.SourceRootInode == 0 ||
		repository.provenance.PrivateRootDevice != repository.identity.Device || repository.provenance.PrivateRootInode != repository.identity.Inode ||
		repository.provenance.PrivateGitDevice != repository.gitAdmin.Device || repository.provenance.PrivateGitInode != repository.gitAdmin.Inode ||
		repository.provenance.XattrProfileDigest != repository.xattr.ProfileDigest ||
		!validSHA256(repository.provenance.SourceBindingDigest) || repository.provenance.GitExecutableDigest == "" ||
		strings.TrimSpace(repository.provenance.GitExecutionBoundary) == "" ||
		repository.provenance.CaptureLimits != repository.limits ||
		err != nil || !reflect.DeepEqual(actual.Manifest, repository.provenance.SourceManifest) ||
		actual.TreeDigest != repository.provenance.SourceTreeDigest || actual.Objects != repository.snapshot.Objects ||
		actual.Bytes != repository.snapshot.Bytes || actual.Manifest.MetadataPolicyDigest != repository.provenance.MetadataPolicyDigest {
		return ErrGitMetadata
	}
	gitAdminInfo, err := openGitAdmin(repository.root)
	if err != nil || uint64(gitAdminInfo.Dev) != repository.gitAdmin.Device || uint64(gitAdminInfo.Ino) != repository.gitAdmin.Inode {
		return ErrGitMetadata
	}
	return verifyGitAdmin(repository.root, repository.provenance.InitialBranch)
}

func nodeIdentityFromStat(info syscall.Stat_t) nodeIdentity {
	return nodeIdentity{Device: uint64(info.Dev), Inode: uint64(info.Ino)}
}

func hashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}
