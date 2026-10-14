//go:build linux

package workflowrecovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/audit"
)

const (
	d14CapacityHelperEnv   = "TBOUND_D14_CAPACITY_HELPER"
	d14CapacityRunEnv      = "TBOUND_TEST_EXT4_CAPACITY"
	d14CapacityPhaseEnv    = "TBOUND_D14_CAPACITY_PHASE"
	d14CapacityMountEnv    = "TBOUND_D14_CAPACITY_MOUNT"
	d14CapacityStateEnv    = "TBOUND_D14_CAPACITY_STATE"
	d14CapacityMarkerEnv   = "TBOUND_D14_CAPACITY_MARKER"
	d14CapacityWorkflow    = "d14-capacity-experiment"
	d14CapacityEffectID    = "d14-effect"
	d14CapacityReserveSize = 64 << 20
	d14CapacityImageSize   = 512 << 20
)

type d14CapacityState struct {
	Phase                   string `json:"phase"`
	ReservationDigest       string `json:"reservationDigest"`
	TerminalDigest          string `json:"terminalDigest,omitempty"`
	OwnerIncarnation        string `json:"ownerIncarnation"`
	OwnerEpoch              uint64 `json:"ownerEpoch"`
	Marker                  string `json:"marker"`
	MarkerDigest            string `json:"markerDigest"`
	OutcomeDigest           string `json:"outcomeDigest,omitempty"`
	AuditResultDigest       string `json:"auditResultDigest,omitempty"`
	OutcomeBytes            int    `json:"outcomeBytes,omitempty"`
	OutcomeFrameBytes       int    `json:"outcomeFrameBytes,omitempty"`
	ReserveBytes            int64  `json:"reserveBytes"`
	FillerBytes             int64  `json:"fillerBytes,omitempty"`
	ENOSPCObservation       string `json:"enospcObservation,omitempty"`
	OwnerCheckpointB        int    `json:"ownerCheckpointBytes,omitempty"`
	OwnerProofB             int    `json:"ownerProofBytes,omitempty"`
	OwnerPageSize           int    `json:"ownerPageSize,omitempty"`
	OwnerPageCount          int    `json:"ownerPageCount,omitempty"`
	OwnerWALBytes           int64  `json:"ownerWALBytes,omitempty"`
	RootPageSize            int    `json:"rootPageSize,omitempty"`
	RootPageCount           int    `json:"rootPageCount,omitempty"`
	RootWALBytes            int64  `json:"rootWALBytes,omitempty"`
	AuditIntentFrameB       int    `json:"auditIntentFrameBytes,omitempty"`
	AuditOutcomeFrameB      int    `json:"auditOutcomeFrameBytes,omitempty"`
	ReserveAllocatedB       int64  `json:"reserveAllocatedBytes,omitempty"`
	ReserveFreedB           int64  `json:"reserveFreedBytes,omitempty"`
	AvailableBlocksAtENOSPC uint64 `json:"availableBlocksAtENOSPC,omitempty"`
	FreeBlocksAtENOSPC      uint64 `json:"freeBlocksAtENOSPC,omitempty"`
}

// TestReservedCapacityFilesystemExperiment mounts its own bounded ext4 image,
// then exercises the real owner, protected-root, and audit code at actual
// guest-filesystem ENOSPC. This is an experiment, not a product capacity
// guarantee: the current code has no cross-workflow capacity ticket.
func TestReservedCapacityFilesystemExperiment(t *testing.T) {
	if os.Getenv(d14CapacityHelperEnv) != "" {
		return
	}
	if os.Getenv(d14CapacityRunEnv) != "1" {
		t.Skipf("D14 ext4 capacity experiment is opt-in; set %s=1 to run", d14CapacityRunEnv)
	}
	volume := newD14Ext4Volume(t)
	source, err := d14CapacitySourceReceipt()
	if err != nil {
		t.Fatalf("source receipt: %v", err)
	}
	t.Logf("D14_EXT4_RECEIPT source=%s go=%s uuid=%s mount_id=%s device=%s fstype=ext4 options=%s image_bytes=%d reserve_experiment_bytes=%d host_backing_qualification=NOT_ESTABLISHED",
		source, runtime.Version(), volume.uuid, volume.mountID, volume.device, volume.options,
		d14CapacityImageSize, d14CapacityReserveSize)

	stateDir := t.TempDir()
	for _, phase := range []string{"success", "crash-after-release"} {
		markerPath := filepath.Join(stateDir, phase+".marker")
		statePath := filepath.Join(stateDir, phase+".json")
		if phase == "success" {
			output := runD14CapacityChild(t, "run-success", volume.mountPath, statePath, markerPath, 0)
			t.Logf("D14_SUCCESS_CHILD_OUTPUT_BEGIN\n%s\nD14_SUCCESS_CHILD_OUTPUT_END", output)
			runD14CapacityChild(t, "verify-success", volume.mountPath, statePath, markerPath, 0)
		} else {
			output := runD14CapacityChild(t, "run-crash", volume.mountPath, statePath, markerPath, 77)
			t.Logf("D14_CRASH_CHILD_OUTPUT_BEGIN\n%s\nD14_CRASH_CHILD_OUTPUT_END", output)
			runD14CapacityChild(t, "verify-crash", volume.mountPath, statePath, markerPath, 0)
		}
		// The success child removes its filler only after terminal accounting.
		// The crash child intentionally leaves it in place until fresh-process
		// recovery checks that the released reserve blocks are sufficient.
		if phase == "crash-after-release" {
			if err := os.Remove(filepath.Join(volume.mountPath, "run-crash", "unreserved-filler.bin")); err != nil {
				t.Fatalf("remove crash experiment filler after verification: %v", err)
			}
		}
	}
}

// TestReservedCapacityLinuxHelper is invoked in a fresh process by the outer
// experiment. The crash phase deliberately uses os.Exit immediately after
// syncing the reserve truncation, before an audit outcome or terminal SQLite
// transition can run.
func TestReservedCapacityLinuxHelper(t *testing.T) {
	if os.Getenv(d14CapacityHelperEnv) != "1" {
		return
	}
	phase := os.Getenv(d14CapacityPhaseEnv)
	mountPath := os.Getenv(d14CapacityMountEnv)
	statePath := os.Getenv(d14CapacityStateEnv)
	markerPath := os.Getenv(d14CapacityMarkerEnv)
	switch phase {
	case "run-success", "run-crash":
		runD14CapacityPhase(t, phase, mountPath, statePath, markerPath)
	case "verify-success":
		verifyD14CapacitySuccess(t, mountPath, statePath, markerPath)
	case "verify-crash":
		verifyD14CapacityCrash(t, mountPath, statePath, markerPath)
	default:
		t.Fatalf("unknown D14 capacity helper phase %q", phase)
	}
}

type d14Ext4Volume struct {
	t         *testing.T
	base      string
	imagePath string
	mountPath string
	uuid      string
	mountID   string
	device    string
	options   string
	mounted   bool
}

func newD14Ext4Volume(t *testing.T) *d14Ext4Volume {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("D14 real-ENOSPC experiment requires root to create and mount a private loopback ext4 image; not verified")
	}
	for _, tool := range []string{"dd", "mkfs.ext4", "mount", "umount", "blkid", "findmnt", "losetup"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("D14 real-ENOSPC experiment requires %s; not verified: %v", tool, err)
		}
	}
	// Keep the image and mount below root's private home. The audit journal
	// deliberately rejects writable ancestors such as /tmp and /mnt/c.
	base, err := os.MkdirTemp("/root", "tbound-d14-capacity-*")
	if err != nil {
		t.Fatalf("create private bounded-image directory: %v", err)
	}
	volume := &d14Ext4Volume{t: t, base: base,
		imagePath: filepath.Join(base, "ext4-512m.img"), mountPath: filepath.Join(base, "mnt")}
	if err := os.Mkdir(volume.mountPath, 0o700); err != nil {
		t.Fatalf("create private mount path: %v", err)
	}
	t.Cleanup(volume.cleanup)
	if output, err := exec.Command("dd", "if=/dev/zero", "of="+volume.imagePath,
		"bs=1M", "count=512", "conv=fsync", "status=none").CombinedOutput(); err != nil {
		t.Fatalf("create fully written 512 MiB ext4 image: %v: %s", err, output)
	}
	if output, err := exec.Command("mkfs.ext4", "-q", "-m", "0", "-b", "4096", "-F",
		"-E", "lazy_itable_init=0,lazy_journal_init=0", volume.imagePath).CombinedOutput(); err != nil {
		t.Fatalf("format bounded ext4 image: %v: %s", err, output)
	}
	if output, err := exec.Command("mount", "-o", "loop,noatime,nodiratime", volume.imagePath, volume.mountPath).CombinedOutput(); err != nil {
		t.Skipf("kernel does not support mounting the private loopback ext4 experiment; not verified: %v: %s", err, output)
	}
	volume.mounted = true
	info, err := os.Stat(volume.imagePath)
	if err != nil || info.Size() != d14CapacityImageSize {
		t.Fatalf("bounded ext4 image size=%v err=%v", info, err)
	}
	fs := syscall.Statfs_t{}
	if err := syscall.Statfs(volume.mountPath, &fs); err != nil {
		t.Fatalf("stat mounted ext4 image: %v", err)
	}
	if uint64(fs.Type) != uint64(0xEF53) || fs.Bsize != 4096 {
		t.Fatalf("mounted volume is not 4 KiB ext4: fstype_magic=%#x block_size=%d", fs.Type, fs.Bsize)
	}
	findmnt, err := exec.Command("findmnt", "-n", "-o", "SOURCE,FSTYPE,OPTIONS", "--target", volume.mountPath).Output()
	if err != nil {
		t.Fatalf("identify mounted ext4 source and options: %v", err)
	}
	parts := strings.Fields(string(findmnt))
	if len(parts) != 3 || parts[1] != "ext4" {
		t.Fatalf("unexpected findmnt identity %q", strings.TrimSpace(string(findmnt)))
	}
	volume.device, volume.options = parts[0], parts[2]
	if !strings.Contains(","+volume.options+",", ",rw,") ||
		!strings.Contains(","+volume.options+",", ",noatime,") ||
		!strings.Contains(","+volume.options+",", ",nodiratime,") {
		t.Fatalf("unexpected ext4 mount options %q", volume.options)
	}
	uuid, err := exec.Command("blkid", "-s", "UUID", "-o", "value", volume.imagePath).Output()
	if err != nil {
		t.Fatalf("read ext4 image UUID: %v", err)
	}
	volume.uuid = strings.TrimSpace(string(uuid))
	volume.mountID, err = findD14MountID(volume.mountPath)
	if err != nil {
		t.Fatalf("read ext4 mount ID: %v", err)
	}
	return volume
}

func (v *d14Ext4Volume) cleanup() {
	if v.mounted {
		if output, err := exec.Command("umount", "--", v.mountPath).CombinedOutput(); err != nil {
			v.t.Errorf("unmount exact D14 task mount %q: %v: %s", v.mountPath, err, output)
		} else {
			v.mounted = false
		}
	}
	if v.mounted {
		// Do not recursively remove a still-mounted filesystem if kernel or
		// process state prevented the exact task mount from being unmounted.
		return
	}
	output, err := exec.Command("losetup", "-j", v.imagePath).CombinedOutput()
	if err != nil {
		v.t.Errorf("inspect exact D14 task loop association; retain image directory: %v: %s", err, output)
		return
	}
	if len(bytes.TrimSpace(output)) != 0 {
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			device := strings.SplitN(line, ":", 2)[0]
			if !strings.HasPrefix(device, "/dev/loop") {
				v.t.Errorf("refuse unexpected loop detach target for task image: %q", device)
				return
			}
			if detach, detachErr := exec.Command("losetup", "-d", device).CombinedOutput(); detachErr != nil {
				v.t.Errorf("detach exact D14 task loop device %s: %v: %s", device, detachErr, detach)
				return
			}
		}
	}
	output, err = exec.Command("losetup", "-j", v.imagePath).CombinedOutput()
	if err != nil || len(bytes.TrimSpace(output)) != 0 {
		v.t.Errorf("verify exact D14 image has no attached loop; retain image directory: %v: %s", err, output)
		return
	}
	if filepath.Dir(filepath.Clean(v.base)) != "/root" || !strings.HasPrefix(filepath.Base(v.base), "tbound-d14-capacity-") {
		v.t.Errorf("refuse to remove unexpected D14 task image directory %q", v.base)
		return
	}
	if err := os.RemoveAll(v.base); err != nil {
		v.t.Errorf("remove private bounded D14 image directory %q: %v", v.base, err)
	}
}

func findD14MountID(mountPath string) (string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 5 && fields[4] == mountPath {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("mount path %q not found in mountinfo", mountPath)
}

func d14CapacitySourceReceipt() (string, error) {
	_, source, line, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller did not return the test source")
	}
	contents, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	head, headErr := exec.Command("git", "-c", "safe.directory="+repoRoot, "-C", repoRoot,
		"rev-parse", "HEAD").Output()
	if headErr != nil {
		return fmt.Sprintf("sha256=%s:%x source_line=%d git_head_unavailable=%v", source, digest, line, headErr), nil
	}
	return fmt.Sprintf("sha256=%s:%x git_head=%s source_line=%d", source, digest, strings.TrimSpace(string(head)), line), nil
}

func runD14CapacityChild(t *testing.T, phase, mountPath, statePath, markerPath string, wantExit int) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestReservedCapacityLinuxHelper$")
	keys := map[string]string{
		d14CapacityHelperEnv: "1", d14CapacityPhaseEnv: phase,
		d14CapacityMountEnv: mountPath, d14CapacityStateEnv: statePath,
		d14CapacityMarkerEnv: markerPath,
	}
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if _, replaced := keys[name]; !replaced {
			command.Env = append(command.Env, item)
		}
	}
	for key, value := range keys {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	gotExit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("D14 helper %s failed to start: %v\n%s", phase, err, output)
		}
		gotExit = exitErr.ExitCode()
	}
	if gotExit != wantExit {
		t.Fatalf("D14 helper %s exit=%d want=%d: %v\n%s", phase, gotExit, wantExit, err, output)
	}
	return string(output)
}

func runD14CapacityPhase(t *testing.T, phase, mountPath, statePath, markerPath string) {
	t.Helper()
	ctx := context.Background()
	workflowRoot := filepath.Join(mountPath, phase)
	rootDir := filepath.Join(workflowRoot, "root")
	ownerStoreRoot := filepath.Join(workflowRoot, "owners")
	auditDir := filepath.Join(workflowRoot, "audit")
	for _, directory := range []string{workflowRoot, rootDir, ownerStoreRoot, auditDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("create private protected-volume directory %s: %v", directory, err)
		}
	}
	workflow := d14CapacityWorkflow
	rootPath := filepath.Join(rootDir, "workflow-root.sqlite")
	ownerPath := filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")
	auditPath := filepath.Join(auditDir, "workflow.audit.jsonl")
	marker := []byte("one synchronized effect marker: " + d14CapacityEffectID + "\n")
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		t.Fatalf("create marker directory outside protected volume: %v", err)
	}
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("effect marker already exists before dispatch: %v", err)
	}

	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create real protected-root SQLite database: %v", err)
	}
	owner, err := CreateFreshSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("create real owner SQLite database: %v", err)
	}
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(),
		workflow, "session-d14-capacity", 1, 1)
	if _, err := InitializeFresh(ctx, owner, pins, testLimits()); err != nil {
		t.Fatalf("initialize real owner/root checkpoint: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(ctx, workflow)
	if err != nil || !exists {
		t.Fatalf("load initialized owner checkpoint: exists=%v err=%v", exists, err)
	}
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits})
	if err != nil {
		t.Fatalf("open real workflow recovery ledger: %v", err)
	}
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("d14-capacity-transcript")); err != nil {
		t.Fatalf("register test transcript: %v", err)
	}
	settleInitialTranscript(t, ledger)
	journal, err := audit.Open(auditPath)
	if err != nil {
		t.Fatalf("open real append-only audit journal: %v", err)
	}

	// This is a test-owned global allocator lock, not a production mechanism.
	lock, err := os.OpenFile(filepath.Join(workflowRoot, "allocator-test.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open experiment allocator lock: %v", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("acquire exclusive experiment writer lock: %v", err)
	}
	defer func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}()
	reservePath := filepath.Join(workflowRoot, "terminalization.reserve")
	reserve, err := os.OpenFile(reservePath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create protected reserve file: %v", err)
	}
	if err := unix.Fallocate(int(reserve.Fd()), 0, 0, d14CapacityReserveSize); err != nil {
		t.Fatalf("physically allocate experimental reserve: %v", err)
	}
	if err := reserve.Sync(); err != nil {
		t.Fatalf("sync experimental reserve before effect reservation: %v", err)
	}
	reserveDir, err := os.Open(workflowRoot)
	if err != nil {
		t.Fatalf("open reserve parent for directory sync: %v", err)
	}
	if err := reserveDir.Sync(); err != nil {
		_ = reserveDir.Close()
		t.Fatalf("sync reserve directory entry before effect reservation: %v", err)
	}
	if err := reserveDir.Close(); err != nil {
		t.Fatalf("close reserve parent after directory sync: %v", err)
	}
	reservedInfo, err := reserve.Stat()
	if err != nil || reservedInfo.Size() != d14CapacityReserveSize || reservedInfo.Sys().(*syscall.Stat_t).Blocks*512 < d14CapacityReserveSize {
		t.Fatalf("experimental reserve lacks physical allocation: info=%v err=%v", reservedInfo, err)
	}
	reserveAllocatedBytes := reservedInfo.Sys().(*syscall.Stat_t).Blocks * 512

	binding := testEffectBinding(d14CapacityEffectID)
	reservation, err := ledger.ReserveEffect(ctx, binding, nil)
	if err != nil {
		t.Fatalf("persist owner effect reservation before callback: %v", err)
	}
	reservedCheckpoint, reservationDigest := ledger.Snapshot()
	if reservation.CheckpointDigest() != reservationDigest || reservedCheckpoint.Pins.EffectReservation == nil ||
		reservedCheckpoint.Pins.EffectReservation.Disposition != EffectReservationReserved {
		t.Fatalf("workflow effect reservation mismatch: receipt=%s checkpoint=%s", reservation.CheckpointDigest(), reservationDigest)
	}
	if err := root.VerifyCheckpoint(ctx, workflow, reservedCheckpoint.Pins.OwnerEpoch, reservationDigest,
		reservedCheckpoint.Pins.OwnerIncarnationDigest); err != nil {
		t.Fatalf("protected root did not authenticate pre-effect reservation: %v", err)
	}

	state := d14CapacityState{Phase: phase, ReservationDigest: reservationDigest,
		OwnerIncarnation: reservedCheckpoint.Pins.OwnerIncarnationDigest,
		OwnerEpoch:       reservedCheckpoint.Pins.OwnerEpoch, Marker: string(marker),
		MarkerDigest: testDigest(string(marker)), ReserveBytes: d14CapacityReserveSize,
		ReserveAllocatedB: reserveAllocatedBytes}
	if err := writeD14CapacityState(statePath, state); err != nil {
		t.Fatalf("persist experiment recovery receipt before effect: %v", err)
	}

	var fillerBytes int64
	var enospcObservation string
	var availableBlocksAtENOSPC uint64
	var freeBlocksAtENOSPC uint64
	var reserveFreedBytes int64
	var outcomeFrameBytes int
	result, err := journal.RunEffect(d14CapacityEffectID, []byte("test-only ext4 capacity fault experiment"), func() ([]byte, error) {
		writeD14CapacityMarker(t, markerPath, marker)
		var fillErr error
		fillerBytes, enospcObservation, availableBlocksAtENOSPC, freeBlocksAtENOSPC, fillErr = fillD14UnreservedBlocksUntilENOSPC(t,
			filepath.Join(workflowRoot, "unreserved-filler.bin"))
		if fillErr != nil {
			return nil, fillErr
		}
		if availableBlocksAtENOSPC != 0 {
			return nil, fmt.Errorf("kernel reported ENOSPC but ext4 still reports %d available blocks (free=%d)",
				availableBlocksAtENOSPC, freeBlocksAtENOSPC)
		}
		if phase == "run-crash" {
			if err := reserve.Truncate(0); err != nil {
				fmt.Printf("D14_CRASH_BEFORE_TERMINAL truncate_error=%v\n", err)
				os.Exit(78)
			}
			if err := reserve.Sync(); err != nil {
				fmt.Printf("D14_CRASH_BEFORE_TERMINAL reserve_sync_error=%v\n", err)
				os.Exit(78)
			}
			freed, err := reserve.Stat()
			if err != nil || freed.Size() != 0 || freed.Sys().(*syscall.Stat_t).Blocks != 0 {
				fmt.Printf("D14_CRASH_BEFORE_TERMINAL reserve_stat=%v error=%v\n", freed, err)
				os.Exit(78)
			}
			reserveFreedBytes = reserveAllocatedBytes - freed.Sys().(*syscall.Stat_t).Blocks*512
			if reserveFreedBytes < d14CapacityReserveSize {
				fmt.Printf("D14_CRASH_BEFORE_TERMINAL reserve_freed_bytes=%d want_at_least=%d\n", reserveFreedBytes, d14CapacityReserveSize)
				os.Exit(78)
			}
			var afterRelease syscall.Statfs_t
			if err := syscall.Statfs(mountPath, &afterRelease); err != nil || uint64(afterRelease.Bavail)*uint64(afterRelease.Bsize)+uint64(1<<20) < uint64(reserveAllocatedBytes) {
				fmt.Printf("D14_CRASH_BEFORE_TERMINAL post_release_statfs=%+v error=%v reserve_allocated_bytes=%d\n",
					afterRelease, err, reserveAllocatedBytes)
				os.Exit(78)
			}
			fmt.Printf("D14_CRASH_BOUNDARY enospc_observation=%s filler_bytes=%d available_blocks_at_enospc=%d free_blocks_at_enospc=%d reserve_allocated_bytes=%d reserve_size=%d reserve_blocks=%d reserve_freed_bytes=%d terminal_records=none global_ticket=absent\n",
				enospcObservation, fillerBytes, availableBlocksAtENOSPC, freeBlocksAtENOSPC, reserveAllocatedBytes,
				freed.Size(), freed.Sys().(*syscall.Stat_t).Blocks, reserveFreedBytes)
			os.Exit(77)
		}
		if err := reserve.Truncate(0); err != nil {
			return nil, fmt.Errorf("truncate reserve after real ENOSPC: %w", err)
		}
		if err := reserve.Sync(); err != nil {
			return nil, fmt.Errorf("sync released reserve after real ENOSPC: %w", err)
		}
		freed, err := reserve.Stat()
		if err != nil || freed.Size() != 0 || freed.Sys().(*syscall.Stat_t).Blocks != 0 {
			return nil, fmt.Errorf("reserve did not release all extent blocks: stat=%v err=%v", freed, err)
		}
		reserveFreedBytes = reserveAllocatedBytes - freed.Sys().(*syscall.Stat_t).Blocks*512
		if reserveFreedBytes < d14CapacityReserveSize {
			return nil, fmt.Errorf("reserve truncate released %d physical bytes; wanted at least %d", reserveFreedBytes, d14CapacityReserveSize)
		}
		outcome := bytes.Repeat([]byte("x"), d14MaxOutcomePayloadBytes())
		return outcome, nil
	})
	if phase == "run-crash" {
		t.Fatal("crash phase callback returned without the expected process exit")
	}
	if err != nil {
		t.Fatalf("real audit RunEffect did not persist the post-ENOSPC outcome: %v", err)
	}
	if len(result) != d14MaxOutcomePayloadBytes() {
		t.Fatalf("audit effect result length=%d want=%d", len(result), d14MaxOutcomePayloadBytes())
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("verify actual audit journal after post-ENOSPC outcome: %v", err)
	}
	if len(trace.Records) != 2 || len(trace.Effects) != 1 || trace.Effects[0].Unresolved ||
		len(trace.Effects[0].Result) != len(result) || testDigest(string(trace.Effects[0].Result)) != testDigest(string(result)) {
		t.Fatalf("audit trace does not contain the exact completed effect: records=%d effects=%+v",
			len(trace.Records), trace.Effects)
	}
	lines, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read exact audit frames for serialized size: %v", err)
	}
	firstNL := bytes.IndexByte(lines, '\n')
	secondNL := bytes.IndexByte(lines[firstNL+1:], '\n')
	if firstNL < 0 || secondNL < 0 {
		t.Fatalf("actual audit journal has incomplete frames")
	}
	outcomeFrameBytes = secondNL + 1
	if outcomeFrameBytes > audit.MaxRecordBytes || audit.MaxRecordBytes-outcomeFrameBytes > 3 {
		t.Fatalf("outcome frame bytes=%d max=%d; expected the largest fitting encoded frame", outcomeFrameBytes, audit.MaxRecordBytes)
	}

	terminalPins := clonePins(reservedCheckpoint.Pins)
	terminalPins.EffectReservation.Disposition = EffectReservationTerminal
	terminalPins.EffectReservation.OutcomeDigest = testDigest(string(marker))
	terminalPins.StoreJournalCount++
	terminalPins.StoreJournalHead = testDigest("terminal store journal: " + d14CapacityEffectID)
	terminalPins.StoreTipGenerationID = "g1-d14-capacity"
	terminalPins.StoreTipTreeDigest = testDigest("terminal store tree: " + d14CapacityEffectID)
	terminalPins.AdmissionJournalCount++
	terminalPins.AdmissionJournalHead = testDigest("terminal admission journal: " + d14CapacityEffectID)
	verifier := &sqliteTerminalMarkerVerifier{markerPath: markerPath, marker: marker}
	terminalReceipt, err := ledger.RecordTerminalEffectOutcome(ctx, reservation, verifier, terminalPins)
	if err != nil {
		t.Fatalf("real owner terminal transaction and protected-root CAS failed after reserve release: %v", err)
	}
	terminal, terminalDigest := ledger.Snapshot()
	if terminalReceipt.CheckpointDigest() != terminalDigest || terminal.Pins.EffectReservation.Disposition != EffectReservationTerminal {
		t.Fatalf("terminal receipt/checkpoint mismatch: receipt=%s checkpoint=%s pins=%+v",
			terminalReceipt.CheckpointDigest(), terminalDigest, terminal.Pins.EffectReservation)
	}
	if err := root.VerifyCheckpoint(ctx, workflow, terminal.Pins.OwnerEpoch, terminalDigest,
		terminal.Pins.OwnerIncarnationDigest); err != nil {
		t.Fatalf("protected root did not durably anchor terminal owner digest: %v", err)
	}
	ownerPageSize, ownerPageCount, ownerWALBytes, ownerCheckpointBytes, ownerProofBytes := d14SQLiteOwnerGeometry(t, owner, ownerPath, workflow)
	rootPageSize, rootPageCount, rootWALBytes := d14SQLiteGeometry(t, root.db, rootPath)
	state.TerminalDigest = terminalDigest
	state.OutcomeDigest = testDigest(string(marker))
	state.AuditResultDigest = testDigest(string(result))
	state.OutcomeBytes = len(result)
	state.OutcomeFrameBytes = outcomeFrameBytes
	state.FillerBytes = fillerBytes
	state.ENOSPCObservation = enospcObservation
	state.AvailableBlocksAtENOSPC = availableBlocksAtENOSPC
	state.FreeBlocksAtENOSPC = freeBlocksAtENOSPC
	state.ReserveFreedB = reserveFreedBytes
	state.OwnerCheckpointB = ownerCheckpointBytes
	state.OwnerProofB = ownerProofBytes
	state.OwnerPageSize = ownerPageSize
	state.OwnerPageCount = ownerPageCount
	state.OwnerWALBytes = ownerWALBytes
	state.RootPageSize = rootPageSize
	state.RootPageCount = rootPageCount
	state.RootWALBytes = rootWALBytes
	state.AuditIntentFrameB = firstNL + 1
	state.AuditOutcomeFrameB = outcomeFrameBytes
	if err := writeD14CapacityState(statePath, state); err != nil {
		t.Fatalf("persist successful experiment receipt: %v", err)
	}
	fmt.Printf("D14_ENOSPC_TERMINALIZED enospc_observation=%s filler_bytes=%d available_blocks_at_enospc=%d free_blocks_at_enospc=%d reserve_allocated_bytes=%d reserve_freed_bytes=%d audit_intent_frame_bytes=%d audit_outcome_frame_bytes=%d owner_page_size=%d owner_pages=%d owner_terminal_checkpoint_json=%d owner_terminal_proof_json=%d owner_wal_bytes=%d root_page_size=%d root_pages=%d root_wal_bytes=%d outcome_payload_bytes=%d synthetic_store_pins=true global_ticket=absent\n",
		enospcObservation, fillerBytes, availableBlocksAtENOSPC, freeBlocksAtENOSPC, reserveAllocatedBytes, reserveFreedBytes, state.AuditIntentFrameB, outcomeFrameBytes,
		ownerPageSize, ownerPageCount, ownerCheckpointBytes, ownerProofBytes, ownerWALBytes,
		rootPageSize, rootPageCount, rootWALBytes, len(result))

	if err := journal.Close(); err != nil {
		t.Errorf("close successful audit journal: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Errorf("close successful owner SQLite database: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Errorf("close successful protected-root SQLite database: %v", err)
	}
	if err := os.Remove(filepath.Join(workflowRoot, "unreserved-filler.bin")); err != nil {
		t.Fatalf("reclaim experiment-only filler after terminal records: %v", err)
	}
	if dir, err := os.Open(workflowRoot); err != nil {
		t.Fatalf("open protected volume directory after filler removal: %v", err)
	} else {
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			t.Fatalf("sync protected volume directory after filler removal: %v", err)
		}
	}
}

func fillD14UnreservedBlocksUntilENOSPC(t *testing.T, path string) (int64, string, uint64, uint64, error) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", 0, 0, err
	}
	defer file.Close()
	chunkSize := 16 << 20
	chunk := bytes.Repeat([]byte{0x5a}, chunkSize)
	var total int64
	for attempt := 0; attempt < 4096; attempt++ {
		n, writeErr := file.Write(chunk)
		total += int64(n)
		if writeErr == nil && n == len(chunk) {
			continue
		}
		if !errors.Is(writeErr, syscall.ENOSPC) {
			return total, "", 0, 0, fmt.Errorf("filler write returned n=%d err=%v; want full write or kernel ENOSPC", n, writeErr)
		}
		syncErr := file.Sync()
		if errors.Is(syncErr, syscall.ENOSPC) {
			if err := file.Close(); err != nil {
				return total, "", 0, 0, fmt.Errorf("close full fault filler after fsync ENOSPC: %w", err)
			}
			return d14FillerReceipt(path, total, "write=ENOSPC,file_sync=ENOSPC")
		}
		if syncErr != nil {
			return total, "", 0, 0, fmt.Errorf("filler fsync returned %v after write ENOSPC", syncErr)
		}
		var stats syscall.Statfs_t
		if err := syscall.Statfs(filepath.Dir(path), &stats); err != nil {
			return total, "", 0, 0, fmt.Errorf("stat filesystem after filler write ENOSPC: %w", err)
		}
		if stats.Bavail == 0 {
			syncfsErr := unix.Syncfs(int(file.Fd()))
			if errors.Is(syncfsErr, syscall.ENOSPC) {
				if err := file.Close(); err != nil {
					return total, "", 0, 0, fmt.Errorf("close full fault filler after syncfs ENOSPC: %w", err)
				}
				return d14FillerReceipt(path, total, "write=ENOSPC,file_sync=success,syncfs=ENOSPC")
			}
			if syncfsErr != nil {
				return total, "", 0, 0, fmt.Errorf("syncfs returned %v after write ENOSPC", syncfsErr)
			}
			if err := file.Close(); err != nil {
				return total, "", 0, 0, fmt.Errorf("close full fault filler: %w", err)
			}
			return d14FillerReceipt(path, total, "write=ENOSPC,file_sync=success,syncfs=success")
		}
		if n == 0 && chunkSize <= 4096 {
			return total, "", uint64(stats.Bavail), uint64(stats.Bfree), fmt.Errorf("kernel returned ENOSPC at 4 KiB write while ext4 reports %d available and %d free blocks", stats.Bavail, stats.Bfree)
		}
		freeBytes := int64(stats.Bfree) * stats.Bsize
		nextSize := chunkSize / 2
		if fit := int(freeBytes / 2); fit > 0 && fit < nextSize {
			nextSize = fit
		}
		nextSize -= nextSize % 4096
		if nextSize < 4096 {
			nextSize = 4096
		}
		if nextSize >= chunkSize {
			nextSize = chunkSize / 2
		}
		chunkSize = nextSize
		chunk = bytes.Repeat([]byte{0x5a}, chunkSize)
	}
	return total, "", 0, 0, errors.New("bounded filler loop did not reach kernel ENOSPC")
}

func d14FillerReceipt(path string, bytesWritten int64, observation string) (int64, string, uint64, uint64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &stats); err != nil {
		return bytesWritten, observation, 0, 0, fmt.Errorf("stat full ext4 capacity experiment: %w", err)
	}
	return bytesWritten, observation, uint64(stats.Bavail), uint64(stats.Bfree), nil
}

func writeD14CapacityMarker(t *testing.T, path string, marker []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("write exact-once external effect marker: %v", err)
	}
	if _, err := file.Write(marker); err != nil {
		_ = file.Close()
		t.Fatalf("write external effect marker: %v", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatalf("sync external effect marker: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close external effect marker: %v", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		t.Fatalf("open external marker directory: %v", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		t.Fatalf("sync external marker directory: %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close external marker directory: %v", err)
	}
}

func d14MaxOutcomePayloadBytes() int {
	probe, err := json.Marshal(audit.Record{Version: 1, Sequence: 2,
		PreviousHash: strings.Repeat("0", 64),
		Event:        audit.Event{Kind: "effect_outcome", ID: d14CapacityEffectID, Outcome: "success", Data: []byte{0}},
		Hash:         strings.Repeat("0", 64)})
	if err != nil {
		panic(err)
	}
	// Replacing the one-byte probe's four base64 characters with the largest
	// multiple-of-four payload encoding gives the exact biggest unpadded result
	// that can fit after the terminating LF.
	maxEncoded := audit.MaxRecordBytes - 1 - (len(probe) - 4)
	maxEncoded -= maxEncoded % 4
	return (maxEncoded / 4) * 3
}

func d14SQLiteGeometry(t *testing.T, db *sql.DB, path string) (pageSize, pageCount int, walBytes int64) {
	t.Helper()
	var journalMode string
	var synchronous int
	if err := db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read SQLite journal mode for %s: %v", path, err)
	}
	if err := db.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("read SQLite synchronous mode for %s: %v", path, err)
	}
	if err := db.QueryRowContext(context.Background(), "PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatalf("read SQLite page size for %s: %v", path, err)
	}
	if err := db.QueryRowContext(context.Background(), "PRAGMA page_count").Scan(&pageCount); err != nil {
		t.Fatalf("read SQLite page count for %s: %v", path, err)
	}
	if journalMode != "wal" || synchronous != 2 || pageSize != 4096 {
		t.Fatalf("SQLite geometry for %s mode=%s synchronous=%d page_size=%d; want WAL/FULL/4096", path, journalMode, synchronous, pageSize)
	}
	if info, err := os.Stat(path + "-wal"); err == nil {
		walBytes = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat SQLite WAL %s: %v", path+"-wal", err)
	}
	return pageSize, pageCount, walBytes
}

func d14SQLiteOwnerGeometry(t *testing.T, owner *SQLiteOwner, path, workflow string) (pageSize, pageCount int, walBytes int64,
	checkpointBytes, proofBytes int) {
	t.Helper()
	pageSize, pageCount, walBytes = d14SQLiteGeometry(t, owner.db, path)
	if err := owner.db.QueryRowContext(context.Background(), `SELECT length(checkpoint_json) FROM workflow_recovery_state WHERE workflow_id=?`, workflow).Scan(&checkpointBytes); err != nil {
		t.Fatalf("measure stored owner checkpoint JSON: %v", err)
	}
	if err := owner.db.QueryRowContext(context.Background(), `SELECT length(checkpoint_json),length(proof_json) FROM workflow_recovery_events WHERE workflow_id=? ORDER BY sequence DESC LIMIT 1`, workflow).Scan(&checkpointBytes, &proofBytes); err != nil {
		t.Fatalf("measure terminal owner event/proof JSON: %v", err)
	}
	return
}

func verifyD14CapacitySuccess(t *testing.T, mountPath, statePath, markerPath string) {
	t.Helper()
	state := readD14CapacityState(t, statePath)
	if state.ReserveAllocatedB < state.ReserveBytes || state.ReserveFreedB < state.ReserveBytes ||
		state.AvailableBlocksAtENOSPC != 0 || !strings.Contains(state.ENOSPCObservation, "write=ENOSPC") {
		t.Fatalf("successful capacity receipt lacks physical reserve/real ENOSPC evidence: %+v", state)
	}
	verifyD14CapacityMarker(t, markerPath, state)
	workflow := d14CapacityWorkflow
	rootPath := filepath.Join(mountPath, "run-success", "root", "workflow-root.sqlite")
	ownerStoreRoot := filepath.Join(mountPath, "run-success", "owners")
	auditPath := filepath.Join(mountPath, "run-success", "audit", "workflow.audit.jsonl")
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
		newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
	if err != nil {
		t.Fatalf("fresh process reopen protected root after reserve-funded terminalization: %v", err)
	}
	defer root.Close()
	owner, err := OpenExistingSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow-owner.sqlite"), root)
	if err != nil {
		t.Fatalf("fresh process reopen owner after reserve-funded terminalization: %v", err)
	}
	defer owner.Close()
	checkpoint, digest, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists || digest != state.TerminalDigest ||
		checkpoint.Pins.EffectReservation == nil || checkpoint.Pins.EffectReservation.Disposition != EffectReservationTerminal ||
		checkpoint.Pins.EffectReservation.OutcomeDigest != state.MarkerDigest {
		t.Fatalf("fresh owner checkpoint is not the exact terminal state: exists=%v digest=%s err=%v pins=%+v",
			exists, digest, err, checkpoint.Pins.EffectReservation)
	}
	if err := root.VerifyCheckpoint(context.Background(), workflow, checkpoint.Pins.OwnerEpoch, digest,
		checkpoint.Pins.OwnerIncarnationDigest); err != nil {
		t.Fatalf("fresh protected root did not authenticate terminal digest: %v", err)
	}
	journal, err := audit.Open(auditPath)
	if err != nil {
		t.Fatalf("fresh process reopen completed audit journal: %v", err)
	}
	defer journal.Close()
	trace, err := journal.Trace()
	if err != nil || len(trace.Effects) != 1 || trace.Effects[0].ID != d14CapacityEffectID || trace.Effects[0].Unresolved ||
		trace.Effects[0].Outcome != "success" || len(trace.Effects[0].Result) != state.OutcomeBytes ||
		testDigest(string(trace.Effects[0].Result)) != state.AuditResultDigest {
		t.Fatalf("fresh audit trace differs from terminal checkpoint: trace=%+v err=%v", trace.Effects, err)
	}
	fmt.Printf("D14_FRESH_SUCCESS_VERIFIED marker_once=true audit_outcome=true owner_terminal=true root_anchor=true outcome_frame_bytes=%d owner_pages=%d root_pages=%d global_ticket=absent\n",
		state.OutcomeFrameBytes, state.OwnerPageCount, state.RootPageCount)
}

func verifyD14CapacityCrash(t *testing.T, mountPath, statePath, markerPath string) {
	t.Helper()
	state := readD14CapacityState(t, statePath)
	verifyD14CapacityMarker(t, markerPath, state)
	workflow := d14CapacityWorkflow
	workflowRoot := filepath.Join(mountPath, "run-crash", "root")
	rootPath := filepath.Join(workflowRoot, "workflow-root.sqlite")
	ownerStoreRoot := filepath.Join(mountPath, "run-crash", "owners")
	auditPath := filepath.Join(mountPath, "run-crash", "audit", "workflow.audit.jsonl")
	reservePath := filepath.Join(mountPath, "run-crash", "terminalization.reserve")
	reserveInfo, err := os.Stat(reservePath)
	if err != nil || reserveInfo.Size() != 0 || reserveInfo.Sys().(*syscall.Stat_t).Blocks != 0 {
		t.Fatalf("fresh process does not observe the synchronized released reserve: stat=%v err=%v", reserveInfo, err)
	}
	var afterRelease syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(reservePath), &afterRelease); err != nil {
		t.Fatalf("stat filesystem immediately after crash following reserve release: %v", err)
	}
	freeBytes := uint64(afterRelease.Bavail) * uint64(afterRelease.Bsize)
	if state.ReserveAllocatedB < state.ReserveBytes || state.AvailableBlocksAtENOSPC != 0 ||
		freeBytes+uint64(1<<20) < uint64(state.ReserveAllocatedB) {
		t.Fatalf("fresh process cannot verify released physical capacity: receipt=%+v free_bytes=%d", state, freeBytes)
	}
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
		newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
	if err != nil {
		t.Fatalf("fresh process reopen root after release-boundary crash: %v", err)
	}
	defer root.Close()
	owner, err := OpenExistingSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow-owner.sqlite"), root)
	if err != nil {
		t.Fatalf("fresh process reopen owner after release-boundary crash: %v", err)
	}
	defer owner.Close()
	checkpoint, digest, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists || digest != state.ReservationDigest || checkpoint.Pins.EffectReservation == nil ||
		checkpoint.Pins.EffectReservation.Disposition != EffectReservationReserved {
		t.Fatalf("fresh owner process did not recover the pre-effect workflow reservation: exists=%v digest=%s err=%v pins=%+v",
			exists, digest, err, checkpoint.Pins.EffectReservation)
	}
	if err := root.VerifyCheckpoint(context.Background(), workflow, checkpoint.Pins.OwnerEpoch, digest,
		checkpoint.Pins.OwnerIncarnationDigest); err != nil {
		t.Fatalf("root does not authenticate pre-effect reservation after crash: %v", err)
	}
	journal, err := audit.Open(auditPath)
	if err != nil {
		t.Fatalf("fresh process reopen unresolved audit journal after release-boundary crash: %v", err)
	}
	defer journal.Close()
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) != 1 || len(trace.Effects) != 1 || !trace.Effects[0].Unresolved {
		t.Fatalf("fresh audit process did not recover one unresolved intent and no outcome: trace=%+v err=%v", trace, err)
	}
	ledger, err := Open(context.Background(), owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits})
	if err != nil {
		t.Fatalf("open durable unresolved per-workflow reservation: %v", err)
	}
	if _, err := ledger.ReserveEffect(context.Background(), testEffectBinding("must-not-run-after-release-crash"), nil); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("same-workflow effect admission after release-boundary crash=%v; want ErrPendingUnknown", err)
	}
	secondCalls := 0
	if _, err := journal.RunEffect("must-not-run-after-release-crash", nil, func() ([]byte, error) {
		secondCalls++
		return []byte("should not run"), nil
	}); !errors.Is(err, audit.ErrQuarantined) || secondCalls != 0 {
		t.Fatalf("audit retry after unresolved effect err=%v callback_calls=%d; want quarantine and zero calls", err, secondCalls)
	}
	verifyD14CapacityMarker(t, markerPath, state)
	fmt.Printf("D14_FRESH_CRASH_VERIFIED marker_once=true reserve_zero_blocks=true audit_unresolved=true owner_reservation=true root_anchor=true same_workflow_retry=denied global_ticket=absent\n")
}

func verifyD14CapacityMarker(t *testing.T, markerPath string, state d14CapacityState) {
	t.Helper()
	info, err := os.Lstat(markerPath)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("expected one regular actual effect marker: info=%v err=%v", info, err)
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil || string(marker) != state.Marker || testDigest(string(marker)) != state.MarkerDigest {
		t.Fatalf("actual effect marker differs from durable pre-effect receipt: err=%v", err)
	}
}

func readD14CapacityState(t *testing.T, path string) d14CapacityState {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read D14 capacity receipt: %v", err)
	}
	var state d14CapacityState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatalf("decode D14 capacity receipt: %v", err)
	}
	return state
}

func writeD14CapacityState(path string, state d14CapacityState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
