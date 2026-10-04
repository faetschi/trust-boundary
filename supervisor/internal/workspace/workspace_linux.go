//go:build linux

package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"tbound/supervisor/internal/delta"
)

// linuxOPath is Linux O_PATH, which is not exposed by syscall on all Go versions.
const linuxOPath = 0x200000

var (
	ErrInvalidOptions           = errors.New("invalid workspace options")
	ErrUntrustedRoot            = errors.New("workspace root is not supervisor-owned and private")
	ErrNotQuiescent             = errors.New("workspace root was not attested quiescent")
	ErrXattrVisibilityUnproven  = errors.New("complete xattr visibility is not attested")
	ErrUnsupportedMetadata      = errors.New("workspace contains unsupported metadata")
	ErrSymlink                  = errors.New("workspace contains a symbolic link")
	ErrHardLink                 = errors.New("workspace contains a linked regular file")
	ErrSpecialFile              = errors.New("workspace contains a special file")
	ErrNestedMount              = errors.New("workspace crosses a mount point")
	ErrOverlappingRoots         = errors.New("workspace source and destination roots overlap")
	ErrRootAncestryUnverified   = errors.New("workspace root ancestry could not be verified")
	ErrMountIdentityUnavailable = errors.New("workspace mount identity is unavailable")
	ErrSourceChanged            = errors.New("workspace changed during import or scan")
	ErrLimit                    = errors.New("workspace exceeds a configured limit")
	ErrDestinationNotEmpty      = errors.New("workspace import destination is not empty")
	ErrNoncanonicalMode         = errors.New("stored workspace mode is not normalized")
)

const metadataIdentityProfile = "tbound-normalized-metadata/v1"

const copyBufferSize = 64 << 10
const maxRootAncestry = 4096

// Limits bound directory entries, path depth, per-file contents, and total
// copied/read bytes. Limits are checked while streaming, not after allocation.
type Limits struct {
	MaxObjects    int
	MaxDepth      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// DefaultLimits returns the initial bounded import profile.
func DefaultLimits() Limits {
	return Limits{
		MaxObjects:    delta.MaxObjectsPerManifest,
		MaxDepth:      256,
		MaxFileBytes:  256 << 20,
		MaxTotalBytes: 1 << 30,
	}
}

// XattrVisibilityAttestation must come from the trusted host-profile loader
// after it establishes complete enumeration for the registered filesystem and
// privilege profile. listxattr(2) may omit names the caller cannot access;
// success from listxattr alone is not a complete visibility proof.
type XattrVisibilityAttestation struct {
	ProfileDigest string
	Complete      bool
}

// Options bind a scan to its generation label, selected metadata policy,
// bounded work profile, and trusted source/profile assumptions. The caller
// must establish QuiescentRoot and XattrVisibility; this package cannot prove
// either assertion from the supplied filesystem descriptors.
type Options struct {
	Generation           string
	MetadataPolicyDigest string
	Limits               Limits
	QuiescentRoot        bool
	XattrVisibility      XattrVisibilityAttestation
}

// Snapshot is derived from a complete descriptor-relative scan. Objects and
// TreeDigest are suitable inputs to delta.ValidateChain; the caller must still
// authenticate decisions and persist the transition ledger.
type Snapshot struct {
	Manifest   delta.TreeManifest
	TreeDigest string
	Objects    int
	Bytes      int64
}

// Import copies a quiescent source tree into an already-created, empty private
// destination directory. It streams file bytes, normalizes modes and ownership,
// syncs copied files, then rescans the destination and requires an exact
// manifest match. On error the destination may contain a partial tree; callers
// must discard it and never promote or reuse it.
func Import(sourceRoot, destinationRoot *os.File, options Options) (Snapshot, error) {
	if err := validateOptions(options); err != nil {
		return Snapshot{}, err
	}
	if err := validateSourceRoot(sourceRoot); err != nil {
		return Snapshot{}, err
	}
	if err := validatePrivateRoot(destinationRoot); err != nil {
		return Snapshot{}, err
	}
	if err := rejectOverlappingRoots(sourceRoot, destinationRoot); err != nil {
		return Snapshot{}, err
	}
	if err := checkXattrs(destinationRoot); err != nil {
		return Snapshot{}, fmt.Errorf("destination root: %w", err)
	}
	entries, err := readEntries(destinationRoot, 1)
	if err != nil {
		if errors.Is(err, ErrLimit) {
			return Snapshot{}, ErrDestinationNotEmpty
		}
		return Snapshot{}, fmt.Errorf("inspect import destination: %w", err)
	}
	if len(entries) != 0 {
		return Snapshot{}, ErrDestinationNotEmpty
	}
	sourceMount, err := mountID(sourceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	state := walkState{options: options, rootMountID: sourceMount}
	if err := walkDirectory(sourceRoot, destinationRoot, "", 0, &state, false); err != nil {
		return Snapshot{}, err
	}
	expected, err := makeSnapshot(options, state)
	if err != nil {
		return Snapshot{}, err
	}
	actual, err := Scan(destinationRoot, options)
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify imported workspace: %w", err)
	}
	if !sameManifest(expected.Manifest, actual.Manifest) || expected.TreeDigest != actual.TreeDigest {
		return Snapshot{}, errors.New("imported workspace does not match its copied manifest")
	}
	return actual, nil
}

// Scan describes a normalized supervisor-owned tree rooted at an already-open
// directory. It hashes regular-file bytes incrementally and verifies canonical
// modes, ownership, link counts, mount identity, and visible xattrs.
func Scan(root *os.File, options Options) (Snapshot, error) {
	if err := validateOptions(options); err != nil {
		return Snapshot{}, err
	}
	if err := validatePrivateRoot(root); err != nil {
		return Snapshot{}, err
	}
	if err := checkXattrs(root); err != nil {
		return Snapshot{}, fmt.Errorf("workspace root: %w", err)
	}
	rootMount, err := mountID(root)
	if err != nil {
		return Snapshot{}, err
	}
	state := walkState{options: options, rootMountID: rootMount}
	if err := walkDirectory(root, nil, "", 0, &state, true); err != nil {
		return Snapshot{}, err
	}
	return makeSnapshot(options, state)
}

type walkState struct {
	options     Options
	rootMountID uint64
	objects     []delta.ManifestObject
	bytes       int64
	seen        int
}

func validateOptions(options Options) error {
	if options.Generation == "" || strings.TrimSpace(options.Generation) != options.Generation ||
		len(options.Generation) > delta.MaxIdentityBytes || !utf8.ValidString(options.Generation) {
		return fmt.Errorf("%w: invalid generation label", ErrInvalidOptions)
	}
	for _, r := range options.Generation {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: generation label contains a control character", ErrInvalidOptions)
		}
	}
	if !validSHA256(options.MetadataPolicyDigest) {
		return fmt.Errorf("%w: metadata-policy digest must be sha256:<lowercase hex>", ErrInvalidOptions)
	}
	if !options.QuiescentRoot {
		return ErrNotQuiescent
	}
	if !options.XattrVisibility.Complete || !validSHA256(options.XattrVisibility.ProfileDigest) {
		return ErrXattrVisibilityUnproven
	}
	limits := options.Limits
	if limits.MaxObjects <= 0 || limits.MaxObjects > delta.MaxObjectsPerManifest ||
		limits.MaxDepth <= 0 || limits.MaxDepth > 4096 ||
		limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 ||
		limits.MaxFileBytes > limits.MaxTotalBytes {
		return fmt.Errorf("%w: invalid limits", ErrInvalidOptions)
	}
	return nil
}

func validateSourceRoot(root *os.File) error {
	if root == nil {
		return ErrUntrustedRoot
	}
	info, err := statFile(root)
	if err != nil {
		return fmt.Errorf("stat source root descriptor: %w", err)
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR ||
		info.Uid != uint32(syscall.Geteuid()) || info.Mode&0o022 != 0 {
		return ErrUntrustedRoot
	}
	return nil
}

func validatePrivateRoot(root *os.File) error {
	if root == nil {
		return ErrUntrustedRoot
	}
	info, err := statFile(root)
	if err != nil {
		return fmt.Errorf("stat private root descriptor: %w", err)
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR ||
		info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) ||
		info.Mode&0o7777 != 0o700 {
		return ErrUntrustedRoot
	}
	return nil
}

func rejectOverlappingRoots(source, destination *os.File) error {
	sourceInfo, err := statFile(source)
	if err != nil {
		return fmt.Errorf("stat source root for overlap check: %w", err)
	}
	destinationInfo, err := statFile(destination)
	if err != nil {
		return fmt.Errorf("stat destination root for overlap check: %w", err)
	}
	if sameNode(sourceInfo, destinationInfo) {
		return ErrOverlappingRoots
	}

	overlap, err := ancestorContains(source, destinationInfo)
	if err != nil {
		return err
	}
	if overlap {
		return ErrOverlappingRoots
	}
	overlap, err = ancestorContains(destination, sourceInfo)
	if err != nil {
		return err
	}
	if overlap {
		return ErrOverlappingRoots
	}
	return nil
}

func ancestorContains(root *os.File, target syscall.Stat_t) (bool, error) {
	fd, err := syscall.Dup(int(root.Fd()))
	if err != nil {
		return false, fmt.Errorf("%w: duplicate root descriptor: %v", ErrRootAncestryUnverified, err)
	}
	syscall.CloseOnExec(fd)
	current := os.NewFile(uintptr(fd), "workspace-root-ancestor")
	if current == nil {
		_ = syscall.Close(fd)
		return false, fmt.Errorf("%w: invalid duplicated descriptor", ErrRootAncestryUnverified)
	}
	defer func() { _ = current.Close() }()

	for depth := 0; depth <= maxRootAncestry; depth++ {
		currentInfo, err := statFile(current)
		if err != nil {
			return false, fmt.Errorf("%w: stat ancestor: %v", ErrRootAncestryUnverified, err)
		}
		if sameNode(currentInfo, target) {
			return true, nil
		}
		currentMount, err := mountID(current)
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrRootAncestryUnverified, err)
		}
		parent, err := openDirectory(current, "..")
		if err != nil {
			return false, fmt.Errorf("%w: open parent: %v", ErrRootAncestryUnverified, err)
		}
		parentInfo, err := statFile(parent)
		if err != nil {
			_ = parent.Close()
			return false, fmt.Errorf("%w: stat parent: %v", ErrRootAncestryUnverified, err)
		}
		parentMount, err := mountID(parent)
		if err != nil {
			_ = parent.Close()
			return false, fmt.Errorf("%w: %v", ErrRootAncestryUnverified, err)
		}
		if sameNode(currentInfo, parentInfo) && currentMount == parentMount {
			_ = parent.Close()
			return false, nil
		}
		if depth == maxRootAncestry {
			_ = parent.Close()
			return false, ErrRootAncestryUnverified
		}
		_ = current.Close()
		current = parent
	}
	return false, ErrRootAncestryUnverified
}

func walkDirectory(source, destination *os.File, parent string, depth int, state *walkState, requireCanonical bool) error {
	if depth > state.options.Limits.MaxDepth {
		return fmt.Errorf("%w: directory depth", ErrLimit)
	}
	before, err := statFile(source)
	if err != nil {
		return fmt.Errorf("stat directory %q: %w", parent, err)
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return fmt.Errorf("%w: %q is not a directory", ErrSpecialFile, parent)
	}
	if err := checkMount(source, state.rootMountID, parent); err != nil {
		return err
	}
	if requireCanonical && !ownedBySupervisor(before) {
		return fmt.Errorf("%w: directory %q", ErrUntrustedRoot, parent)
	}
	if err := checkXattrs(source); err != nil {
		return fmt.Errorf("directory %q: %w", parent, err)
	}
	if requireCanonical && parent != "" && uint32(before.Mode&0o7777) != 0o755 {
		return fmt.Errorf("%w: directory %q", ErrNoncanonicalMode, parent)
	}
	remainingObjects := state.options.Limits.MaxObjects - state.seen
	entries, err := readEntries(source, remainingObjects)
	if err != nil {
		return fmt.Errorf("read directory %q: %w", parent, err)
	}
	for _, entry := range entries {
		if state.seen >= state.options.Limits.MaxObjects {
			return fmt.Errorf("%w: object count", ErrLimit)
		}
		state.seen++
		name := entry.Name()
		if err := validateSegment(name); err != nil {
			return fmt.Errorf("directory %q: %w", parent, err)
		}
		path := name
		if parent != "" {
			path = parent + "/" + name
		}
		if len(path) > delta.MaxPathBytes {
			return fmt.Errorf("%w: path length", ErrLimit)
		}
		node, err := openPathNode(source, name)
		if err != nil {
			return fmt.Errorf("open %q without following links: %w", path, err)
		}
		nodeInfo, err := statFile(node)
		if err != nil {
			_ = node.Close()
			return fmt.Errorf("stat %q: %w", path, err)
		}
		if err := checkMount(node, state.rootMountID, path); err != nil {
			_ = node.Close()
			return err
		}
		switch nodeInfo.Mode & syscall.S_IFMT {
		case syscall.S_IFLNK:
			_ = node.Close()
			return fmt.Errorf("%w: %q", ErrSymlink, path)
		case syscall.S_IFDIR:
			err = walkSubdirectory(source, destination, name, path, node, nodeInfo, depth+1, state, requireCanonical)
		case syscall.S_IFREG:
			err = walkRegularFile(source, destination, name, path, node, nodeInfo, state, requireCanonical)
		default:
			err = fmt.Errorf("%w: %q", ErrSpecialFile, path)
		}
		if err != nil {
			_ = node.Close()
			return err
		}
		if err := verifyNameStillMatches(source, name, nodeInfo, state.rootMountID, path); err != nil {
			_ = node.Close()
			return fmt.Errorf("%w: %q: %w", ErrSourceChanged, path, err)
		}
		if err := verifyStable(node, nodeInfo); err != nil {
			_ = node.Close()
			return fmt.Errorf("%w: %q: %v", ErrSourceChanged, path, err)
		}
		_ = node.Close()
	}
	afterEntries, err := readEntries(source, len(entries))
	if err != nil {
		if errors.Is(err, ErrLimit) {
			return fmt.Errorf("%w: directory entries changed in %q", ErrSourceChanged, parent)
		}
		return fmt.Errorf("recheck directory %q: %w", parent, err)
	}
	if !sameEntryNames(entries, afterEntries) {
		return fmt.Errorf("%w: directory entries changed in %q", ErrSourceChanged, parent)
	}
	if err := verifyStable(source, before); err != nil {
		return fmt.Errorf("%w: directory %q: %v", ErrSourceChanged, parent, err)
	}
	return nil
}

func walkSubdirectory(parentSource, parentDestination *os.File, name, path string, node *os.File, nodeInfo syscall.Stat_t, depth int, state *walkState, requireCanonical bool) error {
	source, err := openDirectory(parentSource, name)
	if err != nil {
		return fmt.Errorf("open directory %q: %w", path, err)
	}
	defer source.Close()
	actual, err := statFile(source)
	if err != nil || !sameNode(nodeInfo, actual) {
		return fmt.Errorf("%w: directory identity changed at %q", ErrSourceChanged, path)
	}
	var output *os.File
	if parentDestination != nil {
		if err := syscall.Mkdirat(int(parentDestination.Fd()), name, 0o700); err != nil {
			return fmt.Errorf("create imported directory %q: %w", path, err)
		}
		output, err = openDirectory(parentDestination, name)
		if err != nil {
			return fmt.Errorf("open imported directory %q: %w", path, err)
		}
		defer output.Close()
	}
	if err := walkDirectory(source, output, path, depth, state, requireCanonical); err != nil {
		return err
	}
	if output != nil {
		if err := syscall.Fchmod(int(output.Fd()), 0o755); err != nil {
			return fmt.Errorf("normalize directory mode %q: %w", path, err)
		}
		info, err := statFile(output)
		if err != nil || !ownedBySupervisor(info) || uint32(info.Mode&0o7777) != 0o755 {
			return fmt.Errorf("%w: imported directory %q", ErrUntrustedRoot, path)
		}
	}
	return addObject(state, path, delta.ObjectDirectory, "", 0o755)
}

func walkRegularFile(parentSource, parentDestination *os.File, name, path string, node *os.File, nodeInfo syscall.Stat_t, state *walkState, requireCanonical bool) error {
	if nodeInfo.Nlink != 1 {
		return fmt.Errorf("%w: %q has link count %d", ErrHardLink, path, nodeInfo.Nlink)
	}
	if err := checkXattrs(node); err != nil {
		return fmt.Errorf("regular file %q: %w", path, err)
	}
	source, err := openRegularFile(parentSource, name)
	if err != nil {
		return fmt.Errorf("open regular file %q: %w", path, err)
	}
	defer source.Close()
	actual, err := statFile(source)
	if err != nil || !sameNode(nodeInfo, actual) {
		return fmt.Errorf("%w: regular-file identity changed at %q", ErrSourceChanged, path)
	}
	if err := checkMount(source, state.rootMountID, path); err != nil {
		return err
	}
	mode := normalizedFileMode(nodeInfo.Mode)
	if requireCanonical && (uint32(nodeInfo.Mode&0o7777) != mode || !ownedBySupervisor(nodeInfo)) {
		return fmt.Errorf("%w: regular file %q", ErrNoncanonicalMode, path)
	}
	if nodeInfo.Size < 0 || nodeInfo.Size > state.options.Limits.MaxFileBytes {
		return fmt.Errorf("%w: file size at %q", ErrLimit, path)
	}
	var destination *os.File
	if parentDestination != nil {
		fd, err := syscall.Openat(int(parentDestination.Fd()), name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return fmt.Errorf("create imported file %q: %w", path, err)
		}
		destination = os.NewFile(uintptr(fd), path)
		if destination == nil {
			_ = syscall.Close(fd)
			return fmt.Errorf("open imported file %q: invalid file descriptor", path)
		}
		defer destination.Close()
	}
	contentHash := sha256.New()
	copied, err := transferFile(source, destination, contentHash, state, path)
	if err != nil {
		return err
	}
	if copied != nodeInfo.Size {
		return fmt.Errorf("%w: size changed at %q", ErrSourceChanged, path)
	}
	if err := verifyStable(source, nodeInfo); err != nil {
		return fmt.Errorf("%w: regular file %q: %v", ErrSourceChanged, path, err)
	}
	if err := checkMount(source, state.rootMountID, path); err != nil {
		return err
	}
	if destination != nil {
		if err := syscall.Fchmod(int(destination.Fd()), mode); err != nil {
			return fmt.Errorf("normalize file mode %q: %w", path, err)
		}
		if err := destination.Sync(); err != nil {
			return fmt.Errorf("sync imported file %q: %w", path, err)
		}
		info, err := statFile(destination)
		if err != nil || !ownedBySupervisor(info) || uint32(info.Mode&0o7777) != mode {
			return fmt.Errorf("%w: imported file %q", ErrUntrustedRoot, path)
		}
	}
	return addObject(state, path, delta.ObjectRegular, digest(contentHash), mode)
}

func transferFile(source, destination *os.File, contentHash hash.Hash, state *walkState, path string) (int64, error) {
	var buffer [copyBufferSize]byte
	var copied int64
	for {
		n, readErr := source.Read(buffer[:])
		if n > 0 {
			chunk := int64(n)
			if chunk > state.options.Limits.MaxFileBytes-copied ||
				chunk > state.options.Limits.MaxTotalBytes-state.bytes {
				return copied, fmt.Errorf("%w: file contents at %q", ErrLimit, path)
			}
			if destination != nil {
				if err := writeFull(destination, buffer[:n]); err != nil {
					return copied, fmt.Errorf("write imported file %q: %w", path, err)
				}
			}
			if _, err := contentHash.Write(buffer[:n]); err != nil {
				return copied, err
			}
			copied += chunk
			state.bytes += chunk
		}
		if readErr == io.EOF {
			return copied, nil
		}
		if readErr != nil {
			return copied, fmt.Errorf("read file %q: %w", path, readErr)
		}
	}
}

func addObject(state *walkState, path string, objectType delta.ObjectType, contentDigest string, mode uint32) error {
	if len(state.objects) >= state.options.Limits.MaxObjects {
		return fmt.Errorf("%w: object count", ErrLimit)
	}
	metadata := sha256.Sum256([]byte(fmt.Sprintf("%s/type=%s/mode=%04o", metadataIdentityProfile, objectType, mode)))
	state.objects = append(state.objects, delta.ManifestObject{
		Path: path,
		State: delta.ObjectState{
			Exists: true, Type: objectType, ContentDigest: contentDigest,
			MetadataIdentity: "sha256:" + hex.EncodeToString(metadata[:]),
		},
	})
	return nil
}

func makeSnapshot(options Options, state walkState) (Snapshot, error) {
	if state.seen != len(state.objects) {
		return Snapshot{}, errors.New("workspace scan did not account for every object")
	}
	sort.Slice(state.objects, func(i, j int) bool { return state.objects[i].Path < state.objects[j].Path })
	manifest := delta.TreeManifest{
		Generation: options.Generation, MetadataPolicyDigest: options.MetadataPolicyDigest,
		Objects: append([]delta.ManifestObject(nil), state.objects...),
	}
	treeDigest, err := delta.ComputeTreeDigest(manifest)
	if err != nil {
		return Snapshot{}, fmt.Errorf("build canonical workspace manifest: %w", err)
	}
	return Snapshot{Manifest: manifest, TreeDigest: treeDigest, Objects: len(manifest.Objects), Bytes: state.bytes}, nil
}

func openPathNode(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openRegularFile(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func readEntries(directory *os.File, maxEntries int) ([]os.DirEntry, error) {
	if maxEntries < 0 {
		return nil, fmt.Errorf("%w: negative directory-entry limit", ErrInvalidOptions)
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind directory descriptor: %w", err)
	}
	const batchSize = 128
	entries := make([]os.DirEntry, 0, min(maxEntries, batchSize))
	for {
		remaining := maxEntries - len(entries)
		request := min(remaining+1, batchSize)
		batch, err := directory.ReadDir(request)
		if len(batch) > remaining {
			return nil, fmt.Errorf("%w: directory entries", ErrLimit)
		}
		entries = append(entries, batch...)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return nil, io.ErrNoProgress
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func sameEntryNames(left, right []os.DirEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Name() != right[i].Name() {
			return false
		}
	}
	return true
}

func verifyNameStillMatches(parent *os.File, name string, expected syscall.Stat_t, rootMountID uint64, path string) error {
	current, err := openPathNode(parent, name)
	if err != nil {
		return err
	}
	defer current.Close()
	actual, err := statFile(current)
	if err != nil {
		return err
	}
	if err := checkMount(current, rootMountID, path); err != nil {
		return err
	}
	if !sameNode(expected, actual) {
		return ErrSourceChanged
	}
	return nil
}

func verifyStable(file *os.File, before syscall.Stat_t) error {
	after, err := statFile(file)
	if err != nil {
		return err
	}
	if !sameVersion(before, after) {
		return ErrSourceChanged
	}
	return nil
}

func sameNode(left, right syscall.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino &&
		left.Mode&syscall.S_IFMT == right.Mode&syscall.S_IFMT
}

func sameVersion(left, right syscall.Stat_t) bool {
	return sameNode(left, right) && left.Mode == right.Mode &&
		left.Nlink == right.Nlink && left.Uid == right.Uid &&
		left.Gid == right.Gid && left.Size == right.Size &&
		left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func statFile(file *os.File) (syscall.Stat_t, error) {
	if file == nil {
		return syscall.Stat_t{}, ErrUntrustedRoot
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &info); err != nil {
		return syscall.Stat_t{}, err
	}
	return info, nil
}

func ownedBySupervisor(info syscall.Stat_t) bool {
	return info.Uid == uint32(syscall.Geteuid()) && info.Gid == uint32(syscall.Getegid())
}

func normalizedFileMode(mode uint32) uint32 {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func checkMount(file *os.File, rootMountID uint64, path string) error {
	id, err := mountID(file)
	if err != nil {
		return err
	}
	return verifyMountIdentity(id, rootMountID, path)
}

func verifyMountIdentity(id, rootMountID uint64, path string) error {
	if id != rootMountID {
		return fmt.Errorf("%w: %q", ErrNestedMount, path)
	}
	return nil
}

// mountID reads the kernel-reported mount ID for an opened object. /proc fdinfo
// is used only with a held descriptor; the input tree cannot supply this path.
func mountID(file *os.File) (uint64, error) {
	info, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrMountIdentityUnavailable, err)
	}
	defer info.Close()
	data, err := io.ReadAll(io.LimitReader(info, 16<<10))
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrMountIdentityUnavailable, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "mnt_id" {
			continue
		}
		id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err == nil {
			return id, nil
		}
	}
	return 0, ErrMountIdentityUnavailable
}

func checkXattrs(file *os.File) error {
	// The procfs descriptor link selects the already-open inode. Source names
	// are never joined into this trusted inspection path.
	attributes, err := syscall.Listxattr(fmt.Sprintf("/proc/self/fd/%d", file.Fd()), nil)
	if err != nil {
		return fmt.Errorf("%w: cannot enumerate attributes: %v", ErrUnsupportedMetadata, err)
	}
	if attributes != 0 {
		return ErrUnsupportedMetadata
	}
	return nil
}

func validateSegment(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > delta.MaxPathBytes ||
		!utf8.ValidString(name) || strings.IndexByte(name, '/') >= 0 ||
		strings.IndexByte(name, '\\') >= 0 || strings.IndexByte(name, 0) >= 0 {
		return errors.New("path component is invalid")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("path component contains a control character")
		}
	}
	return nil
}

func validSHA256(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := value[len(prefix):]
	if strings.ToLower(encoded) != encoded {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func digest(contentHash hash.Hash) string {
	return "sha256:" + hex.EncodeToString(contentHash.Sum(nil))
}

func sameManifest(left, right delta.TreeManifest) bool {
	if left.Generation != right.Generation ||
		left.MetadataPolicyDigest != right.MetadataPolicyDigest ||
		len(left.Objects) != len(right.Objects) {
		return false
	}
	for i := range left.Objects {
		if left.Objects[i] != right.Objects[i] {
			return false
		}
	}
	return true
}

func writeFull(destination *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := destination.Write(data)
		if n < 0 || n > len(data) {
			return errors.New("invalid write count")
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
