//go:build linux

package publication

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

const restoreRecordKind = "tbound-disposable-checkpoint-restore/v1"

type RestoreResult struct {
	RestoreID    string
	CheckpointID string
	Root         *os.File
	Snapshot     workspace.Snapshot
	Status       Status
}

type restoreIntent struct {
	SchemaVersion string             `json:"schema_version"`
	RestoreID     string             `json:"restore_id"`
	WorkflowID    string             `json:"workflow_id"`
	OwnerEpoch    uint64             `json:"owner_epoch"`
	CheckpointID  string             `json:"checkpoint_id"`
	TreeDigest    string             `json:"tree_digest"`
	Manifest      delta.TreeManifest `json:"manifest"`
	SourceRoot    fileIdentity       `json:"source_root"`
	DirectoryName string             `json:"directory_name"`
}

type restoreProgress struct {
	RestoreID string       `json:"restore_id"`
	Step      string       `json:"step"`
	Identity  fileIdentity `json:"identity"`
}

type restoreTerminal struct {
	RestoreID string `json:"restore_id"`
	Status    Status `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type restoreView struct {
	intent   restoreIntent
	identity *fileIdentity
	terminal *restoreTerminal
}

// RestoreDisposableCheckpoint materializes a caller-verified, supervisor-owned
// checkpoint into a fresh private directory beneath StateRoot. It never accepts
// a destination pathname or writes into the live repository. The generated
// destination must be empty and is populated by verified ordinary copying;
// partial materializations remain private and can only be inspected or removed
// by RecoverDisposableCheckpoint.
func (r *Repository) RestoreDisposableCheckpoint(sourceRoot *os.File, checkpointID string, manifest delta.TreeManifest, treeDigest string, inject FaultInjector) (RestoreResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpenLocked(); err != nil {
		return RestoreResult{}, err
	}
	if sourceRoot == nil || !validIdentity(checkpointID) {
		return RestoreResult{}, ErrInvalidRequest
	}
	trace, err := r.options.Journal.Trace()
	if err != nil {
		return RestoreResult{}, err
	}
	prior, err := readRestores(trace, r.options.WorkflowID)
	if err != nil {
		return RestoreResult{}, err
	}
	for _, restore := range prior {
		if restore.terminal == nil || restore.terminal.Status == StatusUnknown {
			return RestoreResult{}, fmt.Errorf("%w: disposable restore %s requires reconciliation", ErrQuarantined, restore.intent.RestoreID)
		}
	}
	options := r.options.Workspace
	options.Generation = manifest.Generation
	snapshot, err := workspace.Scan(sourceRoot, options)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("verify owned checkpoint source: %w", err)
	}
	if snapshot.TreeDigest != treeDigest || !sameManifest(snapshot.Manifest, manifest) {
		return RestoreResult{}, errors.New("checkpoint source does not match its retained complete manifest and digest")
	}
	sourceID, err := identifyDirectory(sourceRoot)
	if err != nil {
		return RestoreResult{}, err
	}
	restoreID, err := randomLabel("restore-")
	if err != nil {
		return RestoreResult{}, err
	}
	directoryName := ".tbound-checkpoint-" + strings.TrimPrefix(restoreID, "restore-")
	intent := restoreIntent{SchemaVersion: restoreRecordKind, RestoreID: restoreID, WorkflowID: r.options.WorkflowID,
		OwnerEpoch: r.options.OwnerEpoch, CheckpointID: checkpointID, TreeDigest: treeDigest, Manifest: manifest,
		SourceRoot: sourceID, DirectoryName: directoryName}
	encoded, err := jsonMarshalBounded(intent)
	if err != nil {
		return RestoreResult{}, err
	}
	if _, err := r.options.Journal.Append(audit.Event{Kind: "publication_restore_intent", ID: restoreID, Data: encoded}); err != nil {
		return RestoreResult{}, fmt.Errorf("persist disposable checkpoint restore intent: %w", err)
	}
	pending := RestoreResult{RestoreID: restoreID, CheckpointID: checkpointID, Status: StatusUnknown}
	if err := injectFault(inject, "after-restore-intent-checkpoint", -1); err != nil {
		return pending, err
	}
	if err := syscall.Mkdirat(int(r.options.StateRoot.Fd()), directoryName, 0o700); err != nil {
		return pending, fmt.Errorf("create disposable checkpoint directory: %w", err)
	}
	fd, err := syscall.Openat(int(r.options.StateRoot.Fd()), directoryName, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return pending, err
	}
	destination := os.NewFile(uintptr(fd), directoryName)
	keepDestination := false
	defer func() {
		if !keepDestination {
			_ = destination.Close()
		}
	}()
	if err := syscall.Fchmod(fd, 0o700); err != nil {
		return pending, err
	}
	if err := r.options.StateRoot.Sync(); err != nil {
		return pending, fmt.Errorf("sync disposable checkpoint parent: %w", err)
	}
	destinationID, err := identifyDirectory(destination)
	if err != nil {
		return pending, err
	}
	if err := r.appendRestoreCheckpoint(restoreProgress{RestoreID: restoreID, Step: "directory-ready", Identity: destinationID}); err != nil {
		return pending, err
	}
	if err := injectFault(inject, "after-restore-directory-checkpoint", -1); err != nil {
		return pending, err
	}
	materialized, err := workspace.Import(sourceRoot, destination, options)
	if err != nil {
		return pending, fmt.Errorf("materialize disposable checkpoint: %w", err)
	}
	if materialized.TreeDigest != treeDigest || !sameManifest(materialized.Manifest, manifest) {
		return pending, errors.New("materialized disposable checkpoint differs from the retained checkpoint")
	}
	if err := syncOwnedTree(destination); err != nil {
		return pending, fmt.Errorf("sync disposable checkpoint tree: %w", err)
	}
	if err := r.options.StateRoot.Sync(); err != nil {
		return pending, fmt.Errorf("sync checkpoint parent after materialization: %w", err)
	}
	if err := injectFault(inject, "after-checkpoint-materialization", -1); err != nil {
		return pending, err
	}
	if err := r.appendRestoreTerminal(restoreTerminal{RestoreID: restoreID, Status: StatusSucceeded}); err != nil {
		return pending, err
	}
	keepDestination = true
	return RestoreResult{RestoreID: restoreID, CheckpointID: checkpointID, Root: destination, Snapshot: materialized, Status: StatusSucceeded}, nil
}

// RecoverDisposableCheckpoint verifies or removes an interrupted private
// materialization. It never copies into the live tree or an existing directory.
// A complete exact manifest is synchronized and admitted; an incomplete owned
// disposable tree is removed descriptor-relatively; identity ambiguity pauses.
func (r *Repository) RecoverDisposableCheckpoint(sourceRoot *os.File, restoreID string, inject FaultInjector) (RestoreResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpenLocked(); err != nil {
		return RestoreResult{}, err
	}
	if sourceRoot == nil || !validRestoreID(restoreID) {
		return RestoreResult{}, ErrInvalidRequest
	}
	trace, err := r.options.Journal.Trace()
	if err != nil {
		return RestoreResult{}, err
	}
	restores, err := readRestores(trace, r.options.WorkflowID)
	if err != nil {
		return RestoreResult{}, err
	}
	var restore *restoreView
	for i := range restores {
		if restores[i].intent.RestoreID == restoreID {
			restore = &restores[i]
			break
		}
	}
	if restore == nil {
		return RestoreResult{}, errors.New("disposable checkpoint restore record not found")
	}
	if restore.terminal != nil {
		result := RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: restore.terminal.Status}
		if result.Status == StatusUnknown {
			return result, ErrRecoveryUnknown
		}
		if result.Status == StatusSucceeded {
			root, err := openRestoreRoot(r.options.StateRoot, restore.intent.DirectoryName, restore.identity)
			if err != nil {
				return result, err
			}
			result.Root = root
			result.Snapshot, err = workspace.Scan(root, restoreWorkspaceOptions(r.options.Workspace, restore.intent.Manifest.Generation))
			if err != nil || result.Snapshot.TreeDigest != restore.intent.TreeDigest {
				_ = root.Close()
				result.Root = nil
				return result, ErrRecoveryUnknown
			}
		}
		return result, nil
	}
	if r.options.OwnerEpoch <= restore.intent.OwnerEpoch {
		return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, ErrOwnerEpoch
	}
	sourceID, err := identifyDirectory(sourceRoot)
	if err != nil || !sameDirectoryIdentity(sourceID, restore.intent.SourceRoot) {
		return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, ErrRecoveryUnknown
	}
	options := restoreWorkspaceOptions(r.options.Workspace, restore.intent.Manifest.Generation)
	sourceSnapshot, err := workspace.Scan(sourceRoot, options)
	if err != nil || sourceSnapshot.TreeDigest != restore.intent.TreeDigest || !sameManifest(sourceSnapshot.Manifest, restore.intent.Manifest) {
		return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, ErrRecoveryUnknown
	}
	if restore.identity == nil {
		fd, openErr := syscall.Openat(int(r.options.StateRoot.Fd()), restore.intent.DirectoryName,
			syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if errors.Is(openErr, syscall.ENOENT) {
			terminal := restoreTerminal{RestoreID: restoreID, Status: StatusFailed, Reason: "no identity checkpoint or disposable destination exists"}
			if err := r.appendRestoreTerminal(terminal); err != nil {
				return RestoreResult{}, err
			}
			return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusFailed}, nil
		}
		if openErr != nil {
			return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, ErrRecoveryUnknown
		}
		_ = syscall.Close(fd)
		return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, ErrRecoveryUnknown
	}
	destination, err := openRestoreRoot(r.options.StateRoot, restore.intent.DirectoryName, restore.identity)
	if errors.Is(err, syscall.ENOENT) {
		terminal := restoreTerminal{RestoreID: restoreID, Status: StatusFailed, Reason: "no disposable destination was materialized"}
		if err := r.appendRestoreTerminal(terminal); err != nil {
			return RestoreResult{}, err
		}
		return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusFailed}, nil
	}
	if err != nil {
		return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, errors.Join(ErrRecoveryUnknown, err)
	}
	keepDestination := false
	defer func() {
		if !keepDestination {
			_ = destination.Close()
		}
	}()
	actual, err := workspace.Scan(destination, options)
	if err == nil && actual.TreeDigest == restore.intent.TreeDigest && sameManifest(actual.Manifest, restore.intent.Manifest) {
		if err := syncOwnedTree(destination); err != nil {
			return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, errors.Join(ErrRecoveryUnknown, err)
		}
		if err := r.options.StateRoot.Sync(); err != nil {
			return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, errors.Join(ErrRecoveryUnknown, err)
		}
		if err := injectFault(inject, "after-checkpoint-materialization", -1); err != nil {
			return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, err
		}
		if err := r.appendRestoreTerminal(restoreTerminal{RestoreID: restoreID, Status: StatusSucceeded}); err != nil {
			return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, err
		}
		keepDestination = true
		return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Root: destination, Snapshot: actual, Status: StatusSucceeded}, nil
	}
	if err := removeOwnedTreeAt(int(r.options.StateRoot.Fd()), restore.intent.DirectoryName, *restore.identity); err != nil {
		return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusUnknown}, errors.Join(ErrRecoveryUnknown, err)
	}
	terminal := restoreTerminal{RestoreID: restoreID, Status: StatusFailed, Reason: "partial disposable materialization was discarded; no live path was modified"}
	if err := r.appendRestoreTerminal(terminal); err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{RestoreID: restoreID, CheckpointID: restore.intent.CheckpointID, Status: StatusFailed}, nil
}

func (r *Repository) appendRestoreCheckpoint(progress restoreProgress) error {
	return r.appendRecord("publication_restore_checkpoint", progress.RestoreID+"/restore-checkpoint", progress)
}

func (r *Repository) appendRestoreTerminal(terminal restoreTerminal) error {
	kind := "publication_restore_outcome"
	if terminal.Status == StatusSucceeded {
		kind = "publication_restore_materialized"
	}
	return r.appendRecord(kind, terminal.RestoreID, terminal)
}

func readRestores(trace audit.Trace, workflow string) ([]restoreView, error) {
	byID := map[string]*restoreView{}
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "publication_restore_intent":
			var intent restoreIntent
			if err := decodeCanonical(record.Event.Data, &intent); err != nil {
				return nil, fmt.Errorf("decode disposable restore intent: %w", err)
			}
			if intent.SchemaVersion != restoreRecordKind || intent.RestoreID != record.Event.ID || !validRestoreID(intent.RestoreID) || intent.OwnerEpoch == 0 ||
				!validIdentity(intent.WorkflowID) || !validIdentity(intent.CheckpointID) || intent.DirectoryName != restoreDirectoryName(intent.RestoreID) ||
				len(intent.Manifest.Objects) > delta.MaxObjectsPerManifest {
				return nil, errors.New("invalid disposable restore intent")
			}
			computed, err := delta.ComputeTreeDigest(intent.Manifest)
			if err != nil || computed != intent.TreeDigest {
				return nil, errors.New("disposable restore intent manifest digest mismatch")
			}
			if _, exists := byID[intent.RestoreID]; exists {
				return nil, errors.New("duplicate disposable restore identity")
			}
			byID[intent.RestoreID] = &restoreView{intent: intent}
		case "publication_restore_checkpoint":
			var progress restoreProgress
			if err := decodeCanonical(record.Event.Data, &progress); err != nil {
				return nil, fmt.Errorf("decode disposable restore checkpoint: %w", err)
			}
			view := byID[progress.RestoreID]
			if view == nil || progress.Step != "directory-ready" || view.identity != nil || progress.Identity.Mode&syscall.S_IFMT != syscall.S_IFDIR {
				return nil, errors.New("invalid disposable restore identity checkpoint")
			}
			view.identity = &progress.Identity
		case "publication_restore_materialized", "publication_restore_outcome":
			var terminal restoreTerminal
			if err := decodeCanonical(record.Event.Data, &terminal); err != nil {
				return nil, fmt.Errorf("decode disposable restore outcome: %w", err)
			}
			view := byID[terminal.RestoreID]
			if view == nil || view.terminal != nil || !validStatus(terminal.Status) ||
				(record.Event.Kind == "publication_restore_materialized" && terminal.Status != StatusSucceeded) ||
				(record.Event.Kind == "publication_restore_outcome" && terminal.Status == StatusSucceeded) ||
				(terminal.Status == StatusSucceeded && view.identity == nil) {
				return nil, errors.New("invalid disposable restore terminal record")
			}
			view.terminal = &terminal
		}
	}
	result := make([]restoreView, 0, len(byID))
	for _, view := range byID {
		if view.intent.WorkflowID == workflow {
			result = append(result, *view)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].intent.RestoreID < result[j].intent.RestoreID })
	return result, nil
}

func openRestoreRoot(stateRoot *os.File, name string, expected *fileIdentity) (*os.File, error) {
	fd, err := syscall.Openat(int(stateRoot.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), name)
	if err := validateArtifactDirectory(root); err != nil {
		_ = root.Close()
		return nil, err
	}
	if expected != nil {
		actual, err := identifyDirectory(root)
		if err != nil || !sameDirectoryIdentity(actual, *expected) {
			_ = root.Close()
			return nil, ErrUntrustedArtifact
		}
	}
	return root, nil
}

func syncOwnedTree(root *os.File) error {
	fd, err := syscall.Openat(int(root.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	copyRoot := os.NewFile(uintptr(fd), "checkpoint-sync-root")
	defer copyRoot.Close()
	return syncOwnedDirectory(copyRoot)
}

func syncOwnedDirectory(directory *os.File) error {
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
			return ErrUntrustedArtifact
		}
		fd, err := syscall.Openat(int(directory.Fd()), name, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		var info syscall.Stat_t
		statErr := syscall.Fstat(fd, &info)
		_ = syscall.Close(fd)
		if statErr != nil {
			return statErr
		}
		switch info.Mode & syscall.S_IFMT {
		case syscall.S_IFREG:
			if info.Nlink != 1 || info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) {
				return ErrUntrustedArtifact
			}
			fileFD, err := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return err
			}
			file := os.NewFile(uintptr(fileFD), name)
			err = file.Sync()
			_ = file.Close()
			if err != nil {
				return err
			}
		case syscall.S_IFDIR:
			childFD, err := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			child := os.NewFile(uintptr(childFD), name)
			childErr := syncOwnedDirectory(child)
			if childErr == nil {
				childErr = child.Sync()
			}
			_ = child.Close()
			if childErr != nil {
				return childErr
			}
		default:
			return ErrUntrustedArtifact
		}
	}
	return directory.Sync()
}

func removeOwnedTreeAt(parentFD int, name string, expected fileIdentity) error {
	fd, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	root := os.NewFile(uintptr(fd), name)
	if err := validateArtifactDirectory(root); err != nil {
		_ = root.Close()
		return err
	}
	actual, err := identifyDirectory(root)
	if err != nil || !sameDirectoryIdentity(actual, expected) {
		_ = root.Close()
		return ErrUntrustedArtifact
	}
	if err := removeOwnedContents(root); err != nil {
		_ = root.Close()
		return err
	}
	_ = root.Close()
	if err := unlinkAt(parentFD, name, atRemoveDir); err != nil {
		return err
	}
	return syscall.Fsync(parentFD)
}

func removeOwnedContents(directory *os.File) error {
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
			return ErrUntrustedArtifact
		}
		fd, err := syscall.Openat(int(directory.Fd()), name, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		var info syscall.Stat_t
		statErr := syscall.Fstat(fd, &info)
		_ = syscall.Close(fd)
		if statErr != nil {
			return statErr
		}
		if info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) {
			return ErrUntrustedArtifact
		}
		switch info.Mode & syscall.S_IFMT {
		case syscall.S_IFREG:
			if info.Nlink != 1 {
				return ErrUntrustedArtifact
			}
			if err := unlinkAt(int(directory.Fd()), name, 0); err != nil {
				return err
			}
		case syscall.S_IFDIR:
			childFD, err := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			child := os.NewFile(uintptr(childFD), name)
			childErr := removeOwnedContents(child)
			_ = child.Close()
			if childErr != nil {
				return childErr
			}
			if err := unlinkAt(int(directory.Fd()), name, atRemoveDir); err != nil {
				return err
			}
		default:
			return ErrUntrustedArtifact
		}
	}
	return directory.Sync()
}

func restoreDirectoryName(restoreID string) string {
	return ".tbound-checkpoint-" + strings.TrimPrefix(restoreID, "restore-")
}

func validRestoreID(value string) bool {
	if len(value) != len("restore-")+32 || !strings.HasPrefix(value, "restore-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "restore-"))
	return err == nil
}

func restoreWorkspaceOptions(options workspace.Options, generation string) workspace.Options {
	options.Generation = generation
	return options
}

func jsonMarshalBounded(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > maxRecordBytes {
		return nil, errors.New("checkpoint restore manifest exceeds the durable record bound")
	}
	return data, nil
}
