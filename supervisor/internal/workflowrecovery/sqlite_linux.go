//go:build linux

package workflowrecovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	sqliteOwnerSchemaVersion = 2
	sqliteOwnerApplicationID = 0x54425257 // "TBRW"
	maxCheckpointJSONBytes   = 16 << 20
)

// TrustedRoot is the mandatory external host/TCB boundary. Implementations
// must authenticate the root identity, protected-store identity, and actual
// process incarnation; bind acquired epochs to that incarnation only after
// prior owners/cells are drained; authorize fresh workflows; and atomically
// compare/advance the authenticated checkpoint head and incarnation. A local
// SQL transaction, SQLite's WAL, and checkpoint digests are not authenticity.
// Production must bind this interface to the protected host boundary, not a
// process-local map, test double, or unkeyed journal hash. VerifyFence alone is
// an epoch query, not permission to mutate; CompareAndSwapCheckpoint must reject
// an incarnation that the root did not acquire for that epoch.
type TrustedRoot interface {
	TrustedRootIdentityDigest() string
	VerifyHostBoundary(ctx context.Context, workflow string, store OwnerStoreIdentity, incarnation OwnerIncarnation) error
	VerifyWorkflowDeadline(ctx context.Context, workflow string, deadline time.Time) error
	VerifyFence(ctx context.Context, workflow string, ownerEpoch uint64) error
	// AcquireOwnerEpoch atomically verifies drain and binds the next epoch to the
	// supplied host-verified process identity. A gap before the ledger CAS is
	// allowed only after every owner/cell in the superseded epoch range is drained.
	AcquireOwnerEpoch(ctx context.Context, workflow string, previousEpoch, nextEpoch uint64, incarnation OwnerIncarnation) error
	VerifyOwnersDrained(ctx context.Context, workflow string, afterEpoch, beforeEpoch uint64) error
	VerifyCheckpoint(ctx context.Context, workflow string, ownerEpoch uint64, digest, ownerIncarnationDigest string) error
	AuthorizeFreshWorkflow(ctx context.Context, workflow string, ownerEpoch uint64, initialDigest string,
		incarnation OwnerIncarnation, deadline time.Time) error
	CompareAndSwapCheckpoint(ctx context.Context, workflow string, expectedOwnerEpoch uint64, expectedDigest string,
		nextOwnerEpoch uint64, nextDigest string, incarnation OwnerIncarnation) error
}

// terminalEffectTrustedRoot is a deliberately narrow extension used only to
// account for the terminal result of an already-consumed Store reservation.
// It authenticates the exact predecessor/target transition and never changes
// the owner fence or workflow allowances.
type terminalEffectTrustedRoot interface {
	VerifyTerminalEffectOwner(ctx context.Context, workflow string, store OwnerStoreIdentity,
		incarnation OwnerIncarnation, ownerEpoch uint64, checkpointDigest string, deadline time.Time) error
	CompareAndSwapTerminalCheckpoint(ctx context.Context, workflow string, ownerEpoch uint64,
		expectedDigest string, previous, next Checkpoint, proof TransitionProof,
		store OwnerStoreIdentity, incarnation OwnerIncarnation) error
}

// SQLiteOwner is the Linux durable owner store. It writes the checkpoint row,
// cumulative D20 counters, owner epoch, and append-only transition row in one
// SQLite WAL transaction with synchronous=FULL. The external TrustedRoot is
// still mandatory and authenticates every loaded/advanced head.
type SQLiteOwner struct {
	mu                    sync.Mutex
	db                    *sql.DB
	root                  TrustedRoot
	path                  string
	storageIdentityDigest string
	rootIdentityDigest    string
	incarnation           OwnerIncarnation
	closed                bool
	poisoned              bool
}

// JournalOwner is kept as a source-compatibility alias. New integrations
// should name the durable implementation SQLiteOwner explicitly.
type JournalOwner = SQLiteOwner

type ownerStateRow struct {
	WorkflowID             string
	OwnerEpoch             int64
	OwnerIncarnationDigest string
	Sequence               int64
	CheckpointDigest       string
	CheckpointJSON         []byte
	RootIdentityDigest     string
	MaxPreEffectRetries    int64
	MaxCleanRestarts       int64
	MaxHostCalls           int64
	MaxTokens              int64
	PreEffectRetries       int64
	CleanRestarts          int64
	HostCalls              int64
	Tokens                 int64
	DeadlineUTC            string
}

type eventRow struct {
	Sequence         int64
	OwnerEpoch       int64
	ExpectedDigest   string
	CheckpointDigest string
	CheckpointJSON   []byte
	ProofJSON        []byte
}

type sqliteQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// OpenExistingSQLiteOwner opens only an existing protected SQLite database.
// It never creates a database or a workflow checkpoint as a recovery fallback.
func OpenExistingSQLiteOwner(path string, root TrustedRoot) (*SQLiteOwner, error) {
	rootIdentity, err := trustedRootIdentity(root)
	if err != nil {
		return nil, err
	}
	cleanPath, storageIdentity, err := inspectSQLitePath(path, false)
	if err != nil {
		return nil, err
	}
	incarnation, err := currentProcessIncarnation()
	if err != nil {
		return nil, err
	}
	db, err := openOwnerDatabase(cleanPath)
	if err != nil {
		return nil, err
	}
	owner := &SQLiteOwner{db: db, root: root, path: cleanPath,
		storageIdentityDigest: storageIdentity, rootIdentityDigest: rootIdentity, incarnation: incarnation}
	if err := owner.verifyStore(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return owner, nil
}

// CreateFreshSQLiteOwner exclusively creates a private SQLite owner database
// and synchronizes both the new database file and its containing directory.
// This bootstraps only the storage container; a workflow still requires the
// explicit, externally authorized InitializeFresh transition.
func CreateFreshSQLiteOwner(path string, root TrustedRoot) (*SQLiteOwner, error) {
	rootIdentity, err := trustedRootIdentity(root)
	if err != nil {
		return nil, err
	}
	cleanPath, err := cleanSQLitePath(path)
	if err != nil {
		return nil, err
	}
	if err := validateSQLiteParent(cleanPath); err != nil {
		return nil, err
	}
	incarnation, err := currentProcessIncarnation()
	if err != nil {
		return nil, err
	}
	if err := createSQLiteFile(cleanPath); err != nil {
		return nil, err
	}
	_, storageIdentity, err := inspectSQLitePath(cleanPath, false)
	if err != nil {
		return nil, err
	}
	db, err := openOwnerDatabase(cleanPath)
	if err != nil {
		return nil, err
	}
	owner := &SQLiteOwner{db: db, root: root, path: cleanPath,
		storageIdentityDigest: storageIdentity, rootIdentityDigest: rootIdentity, incarnation: incarnation}
	if err := owner.createStore(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return owner, nil
}

// OpenExistingJournalOwner and CreateFreshJournalOwner retain the former
// constructor names while using the SQLite implementation and its stronger
// transactional owner store.
func OpenExistingJournalOwner(path string, root TrustedRoot) (*JournalOwner, error) {
	return OpenExistingSQLiteOwner(path, root)
}

func CreateFreshJournalOwner(path string, root TrustedRoot) (*JournalOwner, error) {
	return CreateFreshSQLiteOwner(path, root)
}

func trustedRootIdentity(root TrustedRoot) (string, error) {
	if root == nil {
		return "", fmt.Errorf("%w: trusted external root is required", ErrOwnerFence)
	}
	identity := root.TrustedRootIdentityDigest()
	if !validDigest(identity) {
		return "", fmt.Errorf("%w: trusted root identity is missing or malformed", ErrOwnerFence)
	}
	return identity, nil
}

func openOwnerDatabase(path string) (*sql.DB, error) {
	dsnURL := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "rw")
	query.Set("_txlock", "immediate")
	query.Add("_pragma", "busy_timeout=5000")
	query.Add("_pragma", "foreign_keys=ON")
	query.Add("_pragma", "synchronous=FULL")
	query.Add("_pragma", "trusted_schema=OFF")
	query.Add("_pragma", "wal_autocheckpoint=1000")
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("open workflow recovery SQLite owner: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect workflow recovery SQLite owner: %w", err)
	}
	return db, nil
}

func cleanSQLitePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return "", fmt.Errorf("%w: SQLite owner path must be absolute and clean", ErrOwnerStore)
	}
	return path, nil
}

func inspectSQLitePath(path string, create bool) (string, string, error) {
	clean, err := cleanSQLitePath(path)
	if err != nil {
		return "", "", err
	}
	if err := validateSQLiteParent(clean); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(clean)
	if create {
		if err == nil {
			return "", "", fmt.Errorf("%w: fresh SQLite owner database already exists", ErrOwnerStore)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("%w: inspect fresh SQLite owner path: %v", ErrOwnerStore, err)
		}
		return clean, "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("%w: inspect existing SQLite owner database: %v", ErrOwnerStore, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("%w: SQLite owner database must be an existing regular non-symlink file", ErrOwnerStore)
	}
	uid, err := sqliteFileUID(info)
	if err != nil || uid != uint32(syscall.Geteuid()) || info.Mode().Perm() != 0o600 {
		return "", "", fmt.Errorf("%w: SQLite owner database must be supervisor-owned mode 0600", ErrOwnerStore)
	}
	identity, err := computeSQLiteStorageIdentity(clean, info)
	if err != nil {
		return "", "", err
	}
	return clean, identity, nil
}

func validateSQLiteParent(path string) error {
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return fmt.Errorf("%w: SQLite owner parent must exist without symlink traversal", ErrOwnerStore)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("%w: inspect SQLite owner parent: %v", ErrOwnerStore, err)
	}
	uid, uidErr := sqliteFileUID(info)
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || uidErr != nil ||
		uid != uint32(syscall.Geteuid()) || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: SQLite owner parent must be supervisor-owned mode 0700", ErrOwnerStore)
	}
	return nil
}

func createSQLiteFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create fresh SQLite owner database: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set SQLite owner database mode: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync fresh SQLite owner database: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close fresh SQLite owner database: %w", err)
	}
	if err := syncSQLiteDirectory(path); err != nil {
		return fmt.Errorf("sync fresh SQLite owner directory: %w", err)
	}
	return nil
}

type sqliteFileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	UID    uint32 `json:"uid"`
	Mode   uint32 `json:"mode"`
}

type sqliteStorageIdentity struct {
	Path      string             `json:"path"`
	Directory sqliteFileIdentity `json:"directory"`
	Database  sqliteFileIdentity `json:"database"`
}

func computeSQLiteStorageIdentity(path string, databaseInfo os.FileInfo) (string, error) {
	directoryInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("%w: recheck SQLite owner directory: %v", ErrOwnerStore, err)
	}
	identityOf := func(info os.FileInfo) (sqliteFileIdentity, error) {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat == nil {
			return sqliteFileIdentity{}, fmt.Errorf("%w: Linux file identity is unavailable", ErrOwnerStore)
		}
		return sqliteFileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino,
			UID: stat.Uid, Mode: uint32(info.Mode().Perm())}, nil
	}
	directory, err := identityOf(directoryInfo)
	if err != nil {
		return "", err
	}
	database, err := identityOf(databaseInfo)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(sqliteStorageIdentity{Path: path, Directory: directory, Database: database})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("tbound-workflow-owner-store/v1\n"), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

// currentProcessIncarnation is an OS-derived identity, not a caller-supplied
// session flag. The TrustedRoot independently validates its fields through the
// mandatory host-boundary implementation before admitting writes.
func currentProcessIncarnation() (OwnerIncarnation, error) {
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(bootID)) == "" {
		return OwnerIncarnation{}, fmt.Errorf("%w: read Linux boot identity: %v", ErrOwnerFence, err)
	}
	statData, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return OwnerIncarnation{}, fmt.Errorf("%w: read Linux process identity: %v", ErrOwnerFence, err)
	}
	closeParen := bytes.LastIndexByte(statData, ')')
	if closeParen < 0 || closeParen+1 >= len(statData) {
		return OwnerIncarnation{}, fmt.Errorf("%w: malformed Linux process identity", ErrOwnerFence)
	}
	fields := strings.Fields(string(statData[closeParen+1:]))
	if len(fields) <= 19 {
		return OwnerIncarnation{}, fmt.Errorf("%w: Linux process start time is missing", ErrOwnerFence)
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || startTime == 0 {
		return OwnerIncarnation{}, fmt.Errorf("%w: invalid Linux process start time", ErrOwnerFence)
	}
	namespaceInfo, err := os.Stat("/proc/self/ns/pid")
	if err != nil {
		return OwnerIncarnation{}, fmt.Errorf("%w: read Linux PID namespace identity: %v", ErrOwnerFence, err)
	}
	namespaceStat, ok := namespaceInfo.Sys().(*syscall.Stat_t)
	if !ok || namespaceStat == nil || namespaceStat.Ino == 0 {
		return OwnerIncarnation{}, fmt.Errorf("%w: Linux PID namespace inode is unavailable", ErrOwnerFence)
	}
	cgroupData, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || len(cgroupData) == 0 {
		return OwnerIncarnation{}, fmt.Errorf("%w: read Linux cgroup identity: %v", ErrOwnerFence, err)
	}
	cgroupSum := sha256.Sum256(append([]byte("tbound-owner-cgroup/v1\n"), cgroupData...))
	identity := OwnerIncarnation{
		BootID: strings.TrimSpace(string(bootID)), PID: os.Getpid(), StartTimeTicks: startTime,
		PIDNamespaceInode: namespaceStat.Ino, EffectiveUID: uint32(syscall.Geteuid()),
		CgroupIdentityDigest: hex.EncodeToString(cgroupSum[:]),
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return OwnerIncarnation{}, fmt.Errorf("%w: encode Linux process identity: %v", ErrOwnerFence, err)
	}
	sum := sha256.Sum256(append([]byte("tbound-owner-incarnation/v1\n"), encoded...))
	identity.Digest = hex.EncodeToString(sum[:])
	return identity, nil
}

// ownerIncarnationForPID independently reconstructs a process incarnation from
// procfs. It is used by the host root to compare the full boot/PID-starttime/
// PID-namespace/eUID/cgroup tuple instead of trusting the supplied digest.
func ownerIncarnationForPID(pid int) (OwnerIncarnation, error) {
	if pid <= 0 {
		return OwnerIncarnation{}, fmt.Errorf("%w: invalid Linux owner PID", ErrOwnerFence)
	}
	if pid == os.Getpid() {
		return currentProcessIncarnation()
	}
	procPath := filepath.Join("/proc", strconv.Itoa(pid))
	readStartTime := func() (uint64, error) {
		statData, err := os.ReadFile(filepath.Join(procPath, "stat"))
		if err != nil {
			return 0, err
		}
		closeParen := bytes.LastIndexByte(statData, ')')
		if closeParen < 0 || closeParen+1 >= len(statData) {
			return 0, errors.New("malformed Linux process stat")
		}
		fields := strings.Fields(string(statData[closeParen+1:]))
		if len(fields) <= 19 {
			return 0, errors.New("Linux process start time is missing")
		}
		startTime, err := strconv.ParseUint(fields[19], 10, 64)
		if err != nil || startTime == 0 {
			return 0, errors.New("invalid Linux process start time")
		}
		return startTime, nil
	}
	startTime, err := readStartTime()
	if err != nil {
		return OwnerIncarnation{}, fmt.Errorf("%w: read Linux owner process identity: %v", ErrOwnerFence, err)
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(bootID)) == "" {
		return OwnerIncarnation{}, fmt.Errorf("%w: read Linux boot identity: %v", ErrOwnerFence, err)
	}
	namespaceInfo, err := os.Stat(filepath.Join(procPath, "ns", "pid"))
	if err != nil {
		return OwnerIncarnation{}, fmt.Errorf("%w: read Linux owner PID namespace identity: %v", ErrOwnerFence, err)
	}
	namespaceStat, ok := namespaceInfo.Sys().(*syscall.Stat_t)
	if !ok || namespaceStat == nil || namespaceStat.Ino == 0 {
		return OwnerIncarnation{}, fmt.Errorf("%w: Linux owner PID namespace inode is unavailable", ErrOwnerFence)
	}
	status, err := os.ReadFile(filepath.Join(procPath, "status"))
	if err != nil {
		return OwnerIncarnation{}, fmt.Errorf("%w: read Linux owner UID: %v", ErrOwnerFence, err)
	}
	var effectiveUID uint64
	var foundEffectiveUID bool
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 2 && fields[0] == "Uid:" {
			effectiveUID, err = strconv.ParseUint(fields[2], 10, 32)
			foundEffectiveUID = err == nil
			break
		}
	}
	if err != nil || !foundEffectiveUID {
		if err == nil {
			err = errors.New("Linux process status lacks a valid Uid record")
		}
		return OwnerIncarnation{}, fmt.Errorf("%w: parse Linux owner effective UID: %v", ErrOwnerFence, err)
	}
	cgroupData, err := os.ReadFile(filepath.Join(procPath, "cgroup"))
	if err != nil || len(cgroupData) == 0 {
		return OwnerIncarnation{}, fmt.Errorf("%w: read Linux owner cgroup identity: %v", ErrOwnerFence, err)
	}
	confirmedStartTime, err := readStartTime()
	if err != nil || confirmedStartTime != startTime {
		return OwnerIncarnation{}, fmt.Errorf("%w: owner process exited or PID changed while its identity was being read: %v", ErrOwnerFence, err)
	}
	cgroupSum := sha256.Sum256(append([]byte("tbound-owner-cgroup/v1\n"), cgroupData...))
	identity := OwnerIncarnation{
		BootID: strings.TrimSpace(string(bootID)), PID: pid, StartTimeTicks: startTime,
		PIDNamespaceInode: namespaceStat.Ino, EffectiveUID: uint32(effectiveUID),
		CgroupIdentityDigest: hex.EncodeToString(cgroupSum[:]),
	}
	identity.Digest = computeOwnerIncarnationDigest(identity)
	return identity, nil
}

func currentProcessIncarnationDigest() (string, error) {
	incarnation, err := currentProcessIncarnation()
	if err != nil {
		return "", err
	}
	return incarnation.Digest, nil
}

func computeOwnerIncarnationDigest(incarnation OwnerIncarnation) string {
	incarnation.Digest = ""
	encoded, _ := json.Marshal(incarnation)
	sum := sha256.Sum256(append([]byte("tbound-owner-incarnation/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func sqliteFileUID(info os.FileInfo) (uint32, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, fmt.Errorf("%w: Linux inode owner is unavailable", ErrOwnerStore)
	}
	return stat.Uid, nil
}

func (o *SQLiteOwner) verifyPathLocked() error {
	clean, identity, err := inspectSQLitePath(o.path, false)
	if err != nil {
		return err
	}
	if clean != o.path || identity != o.storageIdentityDigest {
		return fmt.Errorf("%w: protected SQLite owner store identity changed", ErrOwnerStore)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		path := o.path + suffix
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: inspect SQLite sidecar %s: %v", ErrOwnerStore, suffix, err)
		}
		uid, uidErr := sqliteFileUID(info)
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || uidErr != nil || uid != uint32(syscall.Geteuid()) {
			return fmt.Errorf("%w: SQLite sidecar %s is not a supervisor-owned regular file", ErrOwnerStore, suffix)
		}
	}
	return nil
}

func (o *SQLiteOwner) TrustedRootIdentityDigest() string {
	if o == nil {
		return ""
	}
	return o.rootIdentityDigest
}

func (o *SQLiteOwner) OwnerIncarnationDigest() string {
	if o == nil {
		return ""
	}
	return o.incarnation.Digest
}

// OwnerIncarnation returns the host-observed incarnation snapshot that this
// owner was opened under. Callers may persist it with an epoch/cgroup catalog;
// the protected root independently revalidates it before granting authority.
func (o *SQLiteOwner) OwnerIncarnation() OwnerIncarnation {
	if o == nil {
		return OwnerIncarnation{}
	}
	return o.incarnation
}

// OwnerStoreIdentity returns the owner database path and inode identity after
// rechecking the local file and host boundary. The digest may be pinned by a
// caller, but the protected root remains the authority for this identity.
func (o *SQLiteOwner) OwnerStoreIdentity(ctx context.Context, workflow string) (OwnerStoreIdentity, error) {
	if ctx == nil || !validID(workflow) || o == nil {
		return OwnerStoreIdentity{}, ErrOwnerStore
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return OwnerStoreIdentity{}, err
	}
	if err := o.verifyHostBoundaryLocked(ctx, workflow); err != nil {
		return OwnerStoreIdentity{}, err
	}
	return OwnerStoreIdentity{Path: o.path, Digest: o.storageIdentityDigest}, nil
}

// VerifyWorkflowDeadline checks the immutable original deadline against the
// protected host root's persisted UTC/boot-time anchor. A boot change or
// backward wall-clock observation pauses the workflow.
func (o *SQLiteOwner) VerifyWorkflowDeadline(ctx context.Context, workflow string, deadline time.Time) error {
	if ctx == nil || !validID(workflow) || deadline.IsZero() || o == nil {
		return ErrClockUncertain
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return err
	}
	if err := o.verifyPathLocked(); err != nil {
		return err
	}
	current, err := currentProcessIncarnation()
	if err != nil || !reflect.DeepEqual(current, o.incarnation) {
		return fmt.Errorf("%w: deadline query process incarnation changed: %v", ErrOwnerFence, err)
	}
	return o.root.VerifyWorkflowDeadline(ctx, workflow, deadline)
}

// VerifyTerminalEffectOwner revalidates the actual live process, protected
// owner database, current fence and checkpoint head while permitting only the
// sticky expiry/clock-uncertainty state for terminal accounting.
func (o *SQLiteOwner) VerifyTerminalEffectOwner(ctx context.Context, workflow string,
	ownerEpoch uint64, checkpointDigest string, deadline time.Time) error {
	if ctx == nil || !validID(workflow) || ownerEpoch == 0 || !validDigest(checkpointDigest) || deadline.IsZero() || o == nil {
		return ErrOwnerFence
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return err
	}
	if err := o.verifyPathLocked(); err != nil {
		return err
	}
	current, err := currentProcessIncarnation()
	if err != nil || !reflect.DeepEqual(current, o.incarnation) {
		return fmt.Errorf("%w: terminal accounting process incarnation changed: %v", ErrOwnerFence, err)
	}
	root, ok := o.root.(terminalEffectTrustedRoot)
	if !ok {
		return fmt.Errorf("%w: trusted root has no terminal-only accounting capability", ErrOwnerFence)
	}
	return root.VerifyTerminalEffectOwner(ctx, workflow,
		OwnerStoreIdentity{Path: o.path, Digest: o.storageIdentityDigest}, o.incarnation,
		ownerEpoch, checkpointDigest, deadline)
}

// NextOwnerEpoch reads the protected root's monotonic fence high-water mark.
// The result only selects an advisory candidate; AcquireOwnerEpoch remains the
// authority check and binds it to this process after all superseded owners drain.
func (o *SQLiteOwner) NextOwnerEpoch(ctx context.Context, workflow string) (uint64, error) {
	if ctx == nil || !validID(workflow) || o == nil {
		return 0, ErrOwnerFence
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return 0, err
	}
	if err := o.verifyHostBoundaryLocked(ctx, workflow); err != nil {
		return 0, err
	}
	allocator, ok := o.root.(interface {
		NextOwnerEpoch(context.Context, string) (uint64, error)
	})
	if !ok {
		return 0, fmt.Errorf("%w: trusted root cannot select its durable next owner epoch", ErrOwnerFence)
	}
	return allocator.NextOwnerEpoch(ctx, workflow)
}

// AcquireOwnerEpoch binds the actual process incarnation to a new fence. The
// trusted root must reject an epoch gap unless every superseded owner/cell is
// drained; its CAS then binds the same incarnation to the durable checkpoint.
func (o *SQLiteOwner) AcquireOwnerEpoch(ctx context.Context, workflow string, previousEpoch, nextEpoch uint64) error {
	if ctx == nil || !validID(workflow) || nextEpoch == 0 || nextEpoch <= previousEpoch {
		return fmt.Errorf("%w: invalid owner epoch acquisition inputs", ErrOwnerFence)
	}
	if o == nil {
		return ErrOwnerFence
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return err
	}
	if err := o.verifyHostBoundaryLocked(ctx, workflow); err != nil {
		return err
	}
	if err := o.root.AcquireOwnerEpoch(ctx, workflow, previousEpoch, nextEpoch, o.incarnation); err != nil {
		return fmt.Errorf("%w: trusted host rejected owner epoch acquisition: %v", ErrOwnerFence, err)
	}
	return nil
}

func (o *SQLiteOwner) VerifyFence(ctx context.Context, workflow string, ownerEpoch uint64) error {
	if ctx == nil || !validID(workflow) || ownerEpoch == 0 {
		return fmt.Errorf("%w: invalid fence verification inputs", ErrOwnerFence)
	}
	if o == nil {
		return ErrOwnerFence
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return err
	}
	if err := o.verifyHostBoundaryLocked(ctx, workflow); err != nil {
		return err
	}
	if err := o.root.VerifyFence(ctx, workflow, ownerEpoch); err != nil {
		return fmt.Errorf("%w: %v", ErrOwnerFence, err)
	}
	return nil
}

func (o *SQLiteOwner) VerifyOwnersDrained(ctx context.Context, workflow string, afterEpoch, beforeEpoch uint64) error {
	if ctx == nil || !validID(workflow) || afterEpoch == 0 || beforeEpoch <= afterEpoch {
		return fmt.Errorf("%w: invalid owner drain inputs", ErrOwnerFence)
	}
	if o == nil {
		return ErrOwnerFence
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return err
	}
	if err := o.verifyHostBoundaryLocked(ctx, workflow); err != nil {
		return err
	}
	if err := o.root.VerifyOwnersDrained(ctx, workflow, afterEpoch, beforeEpoch); err != nil {
		return fmt.Errorf("%w: prior owner/cell drain: %v", ErrOwnerFence, err)
	}
	return nil
}

func (o *SQLiteOwner) InitializeFreshCheckpoint(ctx context.Context, workflow string, ownerEpoch uint64, initial Checkpoint) (string, error) {
	if ctx == nil || !validID(workflow) || ownerEpoch == 0 || initial.Pins.WorkflowID != workflow ||
		initial.Pins.OwnerEpoch != ownerEpoch || initial.Pins.RootIdentityDigest != o.TrustedRootIdentityDigest() ||
		initial.Pins.OwnerIncarnationDigest != o.OwnerIncarnationDigest() {
		return "", fmt.Errorf("%w: invalid fresh checkpoint inputs", ErrFreshAuthorization)
	}
	if err := validateCheckpoint(initial, initial.ComputedCheckpointDigest); err != nil {
		return "", err
	}
	if err := validateOwnerTransition(Checkpoint{}, "", false, initial, "", TransitionProof{}); err != nil {
		return "", err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return "", err
	}
	if err := o.verifyHostBoundaryLocked(ctx, workflow); err != nil {
		return "", err
	}
	if err := o.root.AcquireOwnerEpoch(ctx, workflow, 0, ownerEpoch, o.incarnation); err != nil {
		return "", fmt.Errorf("%w: fresh owner epoch acquisition: %v", ErrOwnerFence, err)
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin fresh workflow recovery transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	_, _, exists, _, err := loadLedger(ctx, tx, workflow)
	if err != nil {
		return "", err
	}
	if exists {
		return "", ErrCAS
	}
	if err := o.root.VerifyCheckpoint(ctx, workflow, 0, "", ""); err != nil {
		return "", fmt.Errorf("%w: trusted root already has a workflow checkpoint: %v", ErrRollback, err)
	}
	if err := o.root.VerifyFence(ctx, workflow, ownerEpoch); err != nil {
		return "", fmt.Errorf("%w: %v", ErrOwnerFence, err)
	}
	// Fresh authorization is called exactly once and is bound to the exact
	// checkpoint digest that the following root CAS will publish.
	if err := o.root.AuthorizeFreshWorkflow(ctx, workflow, ownerEpoch,
		initial.ComputedCheckpointDigest, o.incarnation, initial.Limits.Deadline); err != nil {
		return "", fmt.Errorf("%w: %v", ErrFreshAuthorization, err)
	}
	if err := writeFreshCheckpoint(ctx, tx, initial); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		o.poisoned = true
		return "", fmt.Errorf("%w: commit fresh workflow recovery checkpoint: %v", ErrOwnerStore, err)
	}
	committed = true
	if err := syncSQLiteDirectory(o.path); err != nil {
		o.poisoned = true
		return "", fmt.Errorf("%w: sync owner directory after fresh checkpoint: %v", ErrOwnerStore, err)
	}
	if err := o.root.CompareAndSwapCheckpoint(ctx, workflow, 0, "", ownerEpoch, initial.ComputedCheckpointDigest,
		o.incarnation); err != nil {
		o.poisoned = true
		return "", fmt.Errorf("%w: synchronized fresh checkpoint could not be anchored by the trusted root: %v", ErrCAS, err)
	}
	if err := o.root.VerifyCheckpoint(ctx, workflow, ownerEpoch, initial.ComputedCheckpointDigest, o.incarnation.Digest); err != nil {
		o.poisoned = true
		return "", fmt.Errorf("%w: trusted root did not confirm fresh checkpoint: %v", ErrCAS, err)
	}
	return initial.ComputedCheckpointDigest, nil
}

func (o *SQLiteOwner) LoadCheckpoint(ctx context.Context, workflow string) (Checkpoint, string, bool, error) {
	if ctx == nil || !validID(workflow) {
		return Checkpoint{}, "", false, fmt.Errorf("%w: invalid checkpoint lookup", ErrInvalid)
	}
	if o == nil {
		return Checkpoint{}, "", false, ErrOwnerFence
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return Checkpoint{}, "", false, err
	}
	if err := o.verifyHostBoundaryLocked(ctx, workflow); err != nil {
		return Checkpoint{}, "", false, err
	}
	checkpoint, digest, exists, _, err := loadLedger(ctx, o.db, workflow)
	if err != nil {
		return Checkpoint{}, "", false, err
	}
	if !exists {
		if err := o.root.VerifyCheckpoint(ctx, workflow, 0, "", ""); err != nil {
			return Checkpoint{}, "", false, fmt.Errorf("%w: local workflow checkpoint is absent but the trusted root has a head: %v", ErrRollback, err)
		}
		return Checkpoint{}, "", false, nil
	}
	if err := o.root.VerifyCheckpoint(ctx, workflow, checkpoint.Pins.OwnerEpoch, digest,
		checkpoint.Pins.OwnerIncarnationDigest); err != nil {
		return Checkpoint{}, "", false, fmt.Errorf("%w: checkpoint head is not authenticated by the trusted root: %v", ErrRollback, err)
	}
	return checkpoint, digest, true, nil
}

func (o *SQLiteOwner) CompareAndSwapCheckpoint(ctx context.Context, workflow string, ownerEpoch uint64, expectedDigest string, next Checkpoint, proof TransitionProof) (string, error) {
	if ctx == nil || !validID(workflow) || ownerEpoch == 0 ||
		(expectedDigest != "" && !validDigest(expectedDigest)) || next.Pins.WorkflowID != workflow || next.Pins.OwnerEpoch != ownerEpoch {
		return "", fmt.Errorf("%w: invalid checkpoint CAS inputs", ErrInvalid)
	}
	if o == nil {
		return "", ErrOwnerFence
	}
	if !validDigest(next.Pins.RootIdentityDigest) || next.Pins.RootIdentityDigest != o.rootIdentityDigest ||
		!validDigest(next.Pins.OwnerIncarnationDigest) || next.Pins.OwnerIncarnationDigest != o.incarnation.Digest {
		return "", fmt.Errorf("%w: checkpoint root or owner-incarnation identity differs from the trusted owner", ErrOwnerFence)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(); err != nil {
		return "", err
	}
	terminalAccounting := proof.kind == transitionTerminalEffect
	if terminalAccounting {
		if err := o.verifyPathLocked(); err != nil {
			return "", err
		}
		current, err := currentProcessIncarnation()
		if err != nil || !reflect.DeepEqual(current, o.incarnation) {
			return "", fmt.Errorf("%w: terminal accounting process incarnation changed: %v", ErrOwnerFence, err)
		}
		if _, ok := o.root.(terminalEffectTrustedRoot); !ok {
			return "", fmt.Errorf("%w: trusted root has no terminal-only accounting capability", ErrOwnerFence)
		}
	} else if err := o.verifyHostBoundaryLocked(ctx, workflow); err != nil {
		return "", err
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin workflow recovery compare-and-swap: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	previous, previousDigest, exists, sequence, err := loadLedger(ctx, tx, workflow)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("%w: missing workflow must use explicit fresh initialization", ErrFreshAuthorization)
	}
	if previousDigest != expectedDigest {
		return "", ErrCAS
	}
	if terminalAccounting {
		root := o.root.(terminalEffectTrustedRoot)
		if ownerEpoch != previous.Pins.OwnerEpoch || next.Pins.OwnerEpoch != previous.Pins.OwnerEpoch {
			return "", fmt.Errorf("%w: terminal bookkeeping cannot acquire or change an owner epoch", ErrOwnerFence)
		}
		if err := root.VerifyTerminalEffectOwner(ctx, workflow,
			OwnerStoreIdentity{Path: o.path, Digest: o.storageIdentityDigest}, o.incarnation,
			ownerEpoch, previousDigest, previous.Limits.Deadline); err != nil {
			return "", fmt.Errorf("%w: predecessor terminal-accounting head: %v", ErrOwnerFence, err)
		}
	} else {
		if err := o.root.VerifyCheckpoint(ctx, workflow, previous.Pins.OwnerEpoch, previousDigest,
			previous.Pins.OwnerIncarnationDigest); err != nil {
			return "", fmt.Errorf("%w: predecessor checkpoint is not authenticated by the trusted root: %v", ErrRollback, err)
		}
		if err := o.root.VerifyFence(ctx, workflow, ownerEpoch); err != nil {
			return "", fmt.Errorf("%w: %v", ErrOwnerFence, err)
		}
		if ownerEpoch != previous.Pins.OwnerEpoch {
			if ownerEpoch < previous.Pins.OwnerEpoch {
				return "", ErrOwnerFence
			}
			if err := o.root.VerifyOwnersDrained(ctx, workflow, previous.Pins.OwnerEpoch, ownerEpoch); err != nil {
				return "", fmt.Errorf("%w: prior owner/cell drain: %v", ErrOwnerFence, err)
			}
		}
	}
	if err := validateCheckpoint(next, next.ComputedCheckpointDigest); err != nil {
		return "", err
	}
	if err := validateOwnerTransition(previous, previousDigest, true, next, expectedDigest, proof); err != nil {
		return "", err
	}
	if sequence >= int64(maxSQLiteInteger) {
		return "", fmt.Errorf("%w: checkpoint sequence exhausted", ErrOwnerStore)
	}
	if err := writeNextCheckpoint(ctx, tx, previous, previousDigest, sequence+1, next, expectedDigest, proof); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		o.poisoned = true
		return "", fmt.Errorf("%w: commit workflow recovery compare-and-swap: %v", ErrOwnerStore, err)
	}
	committed = true
	if err := syncSQLiteDirectory(o.path); err != nil {
		o.poisoned = true
		return "", fmt.Errorf("%w: sync owner directory after checkpoint: %v", ErrOwnerStore, err)
	}
	if terminalAccounting {
		root := o.root.(terminalEffectTrustedRoot)
		if err := root.CompareAndSwapTerminalCheckpoint(ctx, workflow, ownerEpoch, previousDigest,
			previous, next, proof, OwnerStoreIdentity{Path: o.path, Digest: o.storageIdentityDigest}, o.incarnation); err != nil {
			o.poisoned = true
			return "", fmt.Errorf("%w: terminal Store accounting could not be anchored by the trusted root: %v", ErrCAS, err)
		}
	} else {
		if err := o.root.CompareAndSwapCheckpoint(ctx, workflow, previous.Pins.OwnerEpoch, previousDigest,
			ownerEpoch, next.ComputedCheckpointDigest, o.incarnation); err != nil {
			o.poisoned = true
			return "", fmt.Errorf("%w: synchronized checkpoint could not be anchored by the trusted root: %v", ErrCAS, err)
		}
		if err := o.root.VerifyCheckpoint(ctx, workflow, ownerEpoch, next.ComputedCheckpointDigest, o.incarnation.Digest); err != nil {
			o.poisoned = true
			return "", fmt.Errorf("%w: trusted root did not confirm committed checkpoint: %v", ErrCAS, err)
		}
	}
	return next.ComputedCheckpointDigest, nil
}

func (o *SQLiteOwner) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	if o.db == nil {
		return nil
	}
	return o.db.Close()
}

func (o *SQLiteOwner) checkLocked() error {
	if o == nil || o.db == nil || o.root == nil {
		return ErrOwnerFence
	}
	if o.closed {
		return fmt.Errorf("%w: SQLite owner is closed", ErrOwnerStore)
	}
	if o.poisoned {
		return fmt.Errorf("%w: SQLite owner is poisoned after an ambiguous durable commit", ErrCAS)
	}
	return nil
}

func (o *SQLiteOwner) verifyHostBoundaryLocked(ctx context.Context, workflow string) error {
	if err := o.verifyPathLocked(); err != nil {
		return err
	}
	currentIncarnation, err := currentProcessIncarnation()
	if err != nil {
		return fmt.Errorf("%w: revalidate owner process incarnation: %v", ErrOwnerFence, err)
	}
	if !reflect.DeepEqual(currentIncarnation, o.incarnation) {
		return fmt.Errorf("%w: owner process incarnation changed", ErrOwnerFence)
	}
	if err := o.root.VerifyHostBoundary(ctx, workflow,
		OwnerStoreIdentity{Path: o.path, Digest: o.storageIdentityDigest}, o.incarnation); err != nil {
		return fmt.Errorf("%w: protected owner-store boundary: %v", ErrOwnerFence, err)
	}
	return nil
}

func (o *SQLiteOwner) createStore(ctx context.Context) error {
	var journalMode string
	if err := o.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("%w: enable SQLite WAL: %v", ErrOwnerStore, err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("%w: SQLite refused WAL mode (got %q)", ErrOwnerStore, journalMode)
	}
	if err := o.verifyConnectionPragmas(ctx); err != nil {
		return err
	}
	if _, err := o.db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d", sqliteOwnerApplicationID)); err != nil {
		return fmt.Errorf("%w: set SQLite owner application id: %v", ErrOwnerStore, err)
	}
	if _, err := o.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", sqliteOwnerSchemaVersion)); err != nil {
		return fmt.Errorf("%w: set SQLite owner schema version: %v", ErrOwnerStore, err)
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin SQLite owner schema transaction: %v", ErrOwnerStore, err)
	}
	statements := []string{
		`CREATE TABLE workflow_recovery_metadata (
			singleton INTEGER PRIMARY KEY CHECK(singleton=1),
			schema_version INTEGER NOT NULL CHECK(schema_version=2),
			root_identity_digest TEXT NOT NULL CHECK(length(root_identity_digest)=64),
			storage_identity_digest TEXT NOT NULL CHECK(length(storage_identity_digest)=64)
		) STRICT`,
		`CREATE TABLE workflow_recovery_state (
			workflow_id TEXT PRIMARY KEY NOT NULL,
			owner_epoch INTEGER NOT NULL CHECK(owner_epoch>0),
			owner_incarnation_digest TEXT NOT NULL CHECK(length(owner_incarnation_digest)=64),
			sequence INTEGER NOT NULL CHECK(sequence>0),
			checkpoint_digest TEXT NOT NULL CHECK(length(checkpoint_digest)=64),
			checkpoint_json BLOB NOT NULL CHECK(length(checkpoint_json) BETWEEN 1 AND 16777216),
			root_identity_digest TEXT NOT NULL CHECK(length(root_identity_digest)=64),
			max_pre_effect_retries INTEGER NOT NULL CHECK(max_pre_effect_retries BETWEEN 0 AND 2),
			max_clean_restarts INTEGER NOT NULL CHECK(max_clean_restarts BETWEEN 0 AND 2),
			max_host_calls INTEGER NOT NULL CHECK(max_host_calls>0),
			max_tokens INTEGER NOT NULL CHECK(max_tokens>0),
			pre_effect_retries INTEGER NOT NULL CHECK(pre_effect_retries BETWEEN 0 AND max_pre_effect_retries),
			clean_restarts INTEGER NOT NULL CHECK(clean_restarts BETWEEN 0 AND max_clean_restarts),
			host_calls INTEGER NOT NULL CHECK(host_calls BETWEEN 0 AND max_host_calls),
			tokens INTEGER NOT NULL CHECK(tokens BETWEEN 0 AND max_tokens),
			deadline_utc TEXT NOT NULL
		) STRICT`,
		`CREATE TABLE workflow_recovery_events (
			workflow_id TEXT NOT NULL,
			sequence INTEGER NOT NULL CHECK(sequence>0),
			owner_epoch INTEGER NOT NULL CHECK(owner_epoch>0),
			expected_digest TEXT NOT NULL CHECK(expected_digest='' OR length(expected_digest)=64),
			checkpoint_digest TEXT NOT NULL CHECK(length(checkpoint_digest)=64),
			checkpoint_json BLOB NOT NULL CHECK(length(checkpoint_json) BETWEEN 1 AND 16777216),
			proof_json BLOB NOT NULL CHECK(length(proof_json) BETWEEN 1 AND 16777216),
			PRIMARY KEY(workflow_id,sequence),
			UNIQUE(workflow_id,checkpoint_digest),
			FOREIGN KEY(workflow_id) REFERENCES workflow_recovery_state(workflow_id) ON DELETE RESTRICT
		) STRICT`,
		`CREATE TRIGGER workflow_recovery_events_no_update
			BEFORE UPDATE ON workflow_recovery_events BEGIN SELECT RAISE(ABORT,'recovery events are append-only'); END`,
		`CREATE TRIGGER workflow_recovery_events_no_delete
			BEFORE DELETE ON workflow_recovery_events BEGIN SELECT RAISE(ABORT,'recovery events are append-only'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%w: create SQLite owner schema: %v", ErrOwnerStore, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO workflow_recovery_metadata(singleton,schema_version,root_identity_digest,storage_identity_digest) VALUES(1,?,?,?)",
		sqliteOwnerSchemaVersion, o.rootIdentityDigest, o.storageIdentityDigest); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%w: bind SQLite owner identity: %v", ErrOwnerStore, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit SQLite owner schema: %v", ErrOwnerStore, err)
	}
	if err := syncSQLiteDirectory(o.path); err != nil {
		return fmt.Errorf("%w: sync SQLite owner directory after schema creation: %v", ErrOwnerStore, err)
	}
	return o.verifyStore(ctx)
}

func (o *SQLiteOwner) verifyStore(ctx context.Context) error {
	if err := o.verifyPathLocked(); err != nil {
		return err
	}
	if err := o.verifyConnectionPragmas(ctx); err != nil {
		return err
	}
	var applicationID, schemaVersion int64
	if err := o.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return fmt.Errorf("%w: read SQLite owner application id: %v", ErrOwnerStore, err)
	}
	if err := o.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schemaVersion); err != nil {
		return fmt.Errorf("%w: read SQLite owner schema version: %v", ErrOwnerStore, err)
	}
	if applicationID != sqliteOwnerApplicationID || schemaVersion != sqliteOwnerSchemaVersion {
		return fmt.Errorf("%w: SQLite file is not the expected workflow recovery schema", ErrOwnerStore)
	}
	var integrity string
	rows, err := o.db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("%w: SQLite quick_check failed: %v", ErrRollback, err)
	}
	quickCheckRows := 0
	for rows.Next() {
		if err := rows.Scan(&integrity); err != nil || integrity != "ok" {
			_ = rows.Close()
			return fmt.Errorf("%w: SQLite quick_check reported %q: %v", ErrRollback, integrity, err)
		}
		quickCheckRows++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("%w: read SQLite quick_check results: %v", ErrRollback, err)
	}
	if err := rows.Close(); err != nil || quickCheckRows != 1 {
		return fmt.Errorf("%w: SQLite quick_check returned %d rows: %v", ErrRollback, quickCheckRows, err)
	}
	var metadataVersion int64
	var rootIdentity, storeIdentity string
	if err := o.db.QueryRowContext(ctx, `SELECT schema_version,root_identity_digest,storage_identity_digest
		FROM workflow_recovery_metadata WHERE singleton=1`).Scan(&metadataVersion, &rootIdentity, &storeIdentity); err != nil {
		return fmt.Errorf("%w: read SQLite owner identity metadata: %v", ErrOwnerStore, err)
	}
	if metadataVersion != sqliteOwnerSchemaVersion || rootIdentity != o.rootIdentityDigest || storeIdentity != o.storageIdentityDigest ||
		!validDigest(rootIdentity) || !validDigest(storeIdentity) {
		return fmt.Errorf("%w: SQLite owner identity or schema metadata differs", ErrRollback)
	}
	return nil
}

func (o *SQLiteOwner) verifyConnectionPragmas(ctx context.Context) error {
	var engineVersion string
	if err := o.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&engineVersion); err != nil {
		return fmt.Errorf("%w: read SQLite engine version: %v", ErrOwnerStore, err)
	}
	if err := requireWALResetFix(engineVersion); err != nil {
		return err
	}
	var journalMode string
	if err := o.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("%w: read SQLite journal mode: %v", ErrOwnerStore, err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("%w: SQLite owner is not in WAL mode", ErrOwnerStore)
	}
	var synchronous, foreignKeys, busyTimeout, walAutocheckpoint int64
	if err := o.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("%w: read SQLite synchronous mode: %v", ErrOwnerStore, err)
	}
	if err := o.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("%w: read SQLite foreign key mode: %v", ErrOwnerStore, err)
	}
	if err := o.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("%w: read SQLite busy timeout: %v", ErrOwnerStore, err)
	}
	if err := o.db.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&walAutocheckpoint); err != nil {
		return fmt.Errorf("%w: read SQLite WAL checkpoint threshold: %v", ErrOwnerStore, err)
	}
	if synchronous != 2 || foreignKeys != 1 || busyTimeout != 5000 || walAutocheckpoint != 1000 {
		return fmt.Errorf("%w: SQLite PRAGMAs differ from required WAL/FULL owner settings", ErrOwnerStore)
	}
	return nil
}

func requireWALResetFix(version string) error {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: unrecognized SQLite engine version %q", ErrSQLiteEngine, version)
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	patch, errPatch := strconv.Atoi(parts[2])
	if errMajor != nil || errMinor != nil || errPatch != nil || major != 3 ||
		(minor < 51 || (minor == 51 && patch < 3)) || version == "3.52.0" {
		return fmt.Errorf("%w: SQLite %s predates or lacks the WAL-reset fix (require 3.51.3 or later)", ErrSQLiteEngine, version)
	}
	return nil
}

func writeFreshCheckpoint(ctx context.Context, tx *sql.Tx, checkpoint Checkpoint) error {
	encodedCheckpoint, encodedProof, err := encodeCheckpointAndProof(checkpoint, TransitionProof{})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_recovery_state(
		workflow_id,owner_epoch,owner_incarnation_digest,sequence,checkpoint_digest,checkpoint_json,root_identity_digest,
		max_pre_effect_retries,max_clean_restarts,max_host_calls,max_tokens,
		pre_effect_retries,clean_restarts,host_calls,tokens,deadline_utc
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		checkpoint.Pins.WorkflowID, int64(checkpoint.Pins.OwnerEpoch), checkpoint.Pins.OwnerIncarnationDigest,
		int64(1), checkpoint.ComputedCheckpointDigest,
		encodedCheckpoint, checkpoint.Pins.RootIdentityDigest,
		int64(checkpoint.Limits.MaxPreEffectRetries), int64(checkpoint.Limits.MaxCleanRestarts),
		int64(checkpoint.Limits.MaxHostCalls), int64(checkpoint.Limits.MaxTokens),
		int64(checkpoint.Counters.PreEffectRetries), int64(checkpoint.Counters.CleanRestarts),
		int64(checkpoint.Counters.HostCalls), int64(checkpoint.Counters.Tokens), checkpoint.Limits.Deadline.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("%w: insert fresh workflow recovery state: %v", ErrOwnerStore, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_recovery_events(
		workflow_id,sequence,owner_epoch,expected_digest,checkpoint_digest,checkpoint_json,proof_json
	) VALUES(?,?,?,?,?,?,?)`, checkpoint.Pins.WorkflowID, int64(1), int64(checkpoint.Pins.OwnerEpoch), "",
		checkpoint.ComputedCheckpointDigest, encodedCheckpoint, encodedProof); err != nil {
		return fmt.Errorf("%w: append fresh workflow recovery event: %v", ErrOwnerStore, err)
	}
	return nil
}

func writeNextCheckpoint(ctx context.Context, tx *sql.Tx, previous Checkpoint, previousDigest string, sequence int64,
	next Checkpoint, expectedDigest string, proof TransitionProof) error {
	encodedCheckpoint, encodedProof, err := encodeCheckpointAndProof(next, proof)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_recovery_state SET
		owner_epoch=?,owner_incarnation_digest=?,sequence=?,checkpoint_digest=?,checkpoint_json=?,root_identity_digest=?,
		max_pre_effect_retries=?,max_clean_restarts=?,max_host_calls=?,max_tokens=?,
		pre_effect_retries=?,clean_restarts=?,host_calls=?,tokens=?,deadline_utc=?
		WHERE workflow_id=? AND owner_epoch=? AND checkpoint_digest=?`,
		int64(next.Pins.OwnerEpoch), next.Pins.OwnerIncarnationDigest, sequence,
		next.ComputedCheckpointDigest, encodedCheckpoint, next.Pins.RootIdentityDigest,
		int64(next.Limits.MaxPreEffectRetries), int64(next.Limits.MaxCleanRestarts),
		int64(next.Limits.MaxHostCalls), int64(next.Limits.MaxTokens),
		int64(next.Counters.PreEffectRetries), int64(next.Counters.CleanRestarts),
		int64(next.Counters.HostCalls), int64(next.Counters.Tokens), next.Limits.Deadline.Format(time.RFC3339Nano),
		next.Pins.WorkflowID, int64(previous.Pins.OwnerEpoch), expectedDigest)
	if err != nil {
		return fmt.Errorf("%w: update workflow recovery checkpoint row: %v", ErrOwnerStore, err)
	}
	rowsChanged, err := result.RowsAffected()
	if err != nil || rowsChanged != 1 {
		return fmt.Errorf("%w: SQLite owner state row compare-and-swap did not change exactly one row", ErrCAS)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_recovery_events(
		workflow_id,sequence,owner_epoch,expected_digest,checkpoint_digest,checkpoint_json,proof_json
	) VALUES(?,?,?,?,?,?,?)`, next.Pins.WorkflowID, sequence, int64(next.Pins.OwnerEpoch), previousDigest,
		next.ComputedCheckpointDigest, encodedCheckpoint, encodedProof); err != nil {
		return fmt.Errorf("%w: append workflow recovery event: %v", ErrOwnerStore, err)
	}
	return nil
}

func encodeCheckpointAndProof(checkpoint Checkpoint, proof TransitionProof) ([]byte, []byte, error) {
	checkpointJSON, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode workflow checkpoint: %v", ErrOwnerStore, err)
	}
	proofJSON, err := json.Marshal(proof.record())
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode workflow transition proof: %v", ErrOwnerStore, err)
	}
	if len(checkpointJSON) == 0 || len(checkpointJSON) > maxCheckpointJSONBytes || len(proofJSON) == 0 || len(proofJSON) > maxCheckpointJSONBytes {
		return nil, nil, fmt.Errorf("%w: checkpoint or transition proof exceeds the bounded SQLite record size", ErrInvalid)
	}
	return checkpointJSON, proofJSON, nil
}

func loadLedger(ctx context.Context, queryer sqliteQueryer, workflow string) (Checkpoint, string, bool, int64, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT sequence,owner_epoch,expected_digest,checkpoint_digest,checkpoint_json,proof_json
		FROM workflow_recovery_events WHERE workflow_id=? ORDER BY sequence`, workflow)
	if err != nil {
		return Checkpoint{}, "", false, 0, fmt.Errorf("%w: load workflow recovery event history: %v", ErrOwnerStore, err)
	}
	var latest Checkpoint
	var latestDigest string
	var sequence int64
	priorExists := false
	for rows.Next() {
		var event eventRow
		if err := rows.Scan(&event.Sequence, &event.OwnerEpoch, &event.ExpectedDigest,
			&event.CheckpointDigest, &event.CheckpointJSON, &event.ProofJSON); err != nil {
			_ = rows.Close()
			return Checkpoint{}, "", false, 0, fmt.Errorf("%w: scan workflow recovery event: %v", ErrRollback, err)
		}
		if event.Sequence != sequence+1 || event.Sequence <= 0 || event.OwnerEpoch <= 0 ||
			(event.ExpectedDigest != "" && !validDigest(event.ExpectedDigest)) || !validDigest(event.CheckpointDigest) {
			_ = rows.Close()
			return Checkpoint{}, "", false, 0, fmt.Errorf("%w: workflow recovery event sequence or digest is invalid", ErrRollback)
		}
		var checkpoint Checkpoint
		if err := decodeOwnerJSON(event.CheckpointJSON, &checkpoint); err != nil {
			_ = rows.Close()
			return Checkpoint{}, "", false, 0, fmt.Errorf("%w: decode workflow recovery checkpoint: %v", ErrRollback, err)
		}
		var record transitionRecord
		if err := decodeOwnerJSON(event.ProofJSON, &record); err != nil {
			_ = rows.Close()
			return Checkpoint{}, "", false, 0, fmt.Errorf("%w: decode workflow transition proof: %v", ErrRollback, err)
		}
		if checkpoint.Pins.WorkflowID != workflow || checkpoint.Pins.OwnerEpoch != uint64(event.OwnerEpoch) ||
			checkpoint.ComputedCheckpointDigest != event.CheckpointDigest || event.ExpectedDigest != latestDigest {
			_ = rows.Close()
			return Checkpoint{}, "", false, 0, fmt.Errorf("%w: workflow recovery event does not follow its exact predecessor", ErrRollback)
		}
		if err := validateOwnerTransition(latest, latestDigest, priorExists, checkpoint, event.ExpectedDigest, record.proof()); err != nil {
			_ = rows.Close()
			return Checkpoint{}, "", false, 0, fmt.Errorf("%w: workflow %q event %d: %v", ErrRollback, workflow, event.Sequence, err)
		}
		if err := validateCheckpoint(checkpoint, event.CheckpointDigest); err != nil {
			_ = rows.Close()
			return Checkpoint{}, "", false, 0, err
		}
		latest = checkpoint
		latest.Pins = clonePins(latest.Pins)
		latestDigest = event.CheckpointDigest
		sequence = event.Sequence
		priorExists = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Checkpoint{}, "", false, 0, fmt.Errorf("%w: read workflow recovery event history: %v", ErrOwnerStore, err)
	}
	if err := rows.Close(); err != nil {
		return Checkpoint{}, "", false, 0, fmt.Errorf("%w: close workflow recovery event history: %v", ErrOwnerStore, err)
	}
	var state ownerStateRow
	err = queryer.QueryRowContext(ctx, `SELECT workflow_id,owner_epoch,owner_incarnation_digest,sequence,checkpoint_digest,checkpoint_json,root_identity_digest,
		max_pre_effect_retries,max_clean_restarts,max_host_calls,max_tokens,
		pre_effect_retries,clean_restarts,host_calls,tokens,deadline_utc
		FROM workflow_recovery_state WHERE workflow_id=?`, workflow).Scan(
		&state.WorkflowID, &state.OwnerEpoch, &state.OwnerIncarnationDigest, &state.Sequence,
		&state.CheckpointDigest, &state.CheckpointJSON,
		&state.RootIdentityDigest, &state.MaxPreEffectRetries, &state.MaxCleanRestarts, &state.MaxHostCalls, &state.MaxTokens,
		&state.PreEffectRetries, &state.CleanRestarts, &state.HostCalls, &state.Tokens, &state.DeadlineUTC)
	if errors.Is(err, sql.ErrNoRows) {
		if priorExists {
			return Checkpoint{}, "", false, 0, fmt.Errorf("%w: recovery history exists without its current state row", ErrRollback)
		}
		return Checkpoint{}, "", false, 0, nil
	}
	if err != nil {
		return Checkpoint{}, "", false, 0, fmt.Errorf("%w: read current workflow recovery row: %v", ErrOwnerStore, err)
	}
	if !priorExists || state.Sequence != sequence || state.OwnerEpoch <= 0 || state.CheckpointDigest != latestDigest ||
		state.WorkflowID != workflow || state.RootIdentityDigest != latest.Pins.RootIdentityDigest ||
		!reflect.DeepEqual(state.CheckpointJSON, mustMarshalCheckpoint(latest)) {
		return Checkpoint{}, "", false, 0, fmt.Errorf("%w: current workflow recovery row diverges from its append-only history", ErrRollback)
	}
	if err := validateStateColumns(state, latest); err != nil {
		return Checkpoint{}, "", false, 0, err
	}
	return latest, latestDigest, true, sequence, nil
}

func validateStateColumns(state ownerStateRow, checkpoint Checkpoint) error {
	if uint64(state.OwnerEpoch) != checkpoint.Pins.OwnerEpoch || state.OwnerIncarnationDigest != checkpoint.Pins.OwnerIncarnationDigest ||
		state.MaxPreEffectRetries < 0 || state.MaxCleanRestarts < 0 || state.MaxHostCalls < 0 || state.MaxTokens < 0 ||
		state.PreEffectRetries < 0 || state.CleanRestarts < 0 || state.HostCalls < 0 || state.Tokens < 0 {
		return fmt.Errorf("%w: SQLite owner columns contain invalid counters or epoch", ErrRollback)
	}
	if uint64(state.MaxPreEffectRetries) != checkpoint.Limits.MaxPreEffectRetries ||
		uint64(state.MaxCleanRestarts) != checkpoint.Limits.MaxCleanRestarts ||
		uint64(state.MaxHostCalls) != checkpoint.Limits.MaxHostCalls || uint64(state.MaxTokens) != checkpoint.Limits.MaxTokens ||
		uint64(state.PreEffectRetries) != checkpoint.Counters.PreEffectRetries ||
		uint64(state.CleanRestarts) != checkpoint.Counters.CleanRestarts ||
		uint64(state.HostCalls) != checkpoint.Counters.HostCalls || uint64(state.Tokens) != checkpoint.Counters.Tokens {
		return fmt.Errorf("%w: SQLite cumulative D20 columns diverge from the checkpoint", ErrRollback)
	}
	deadline, err := time.Parse(time.RFC3339Nano, state.DeadlineUTC)
	if err != nil || !deadline.Equal(checkpoint.Limits.Deadline) {
		return fmt.Errorf("%w: SQLite original workflow deadline diverges from the checkpoint", ErrRollback)
	}
	return nil
}

func mustMarshalCheckpoint(checkpoint Checkpoint) []byte {
	encoded, _ := json.Marshal(checkpoint)
	return encoded
}

func decodeOwnerJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > maxCheckpointJSONBytes {
		return errors.New("empty or oversized SQLite recovery JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func syncSQLiteDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
