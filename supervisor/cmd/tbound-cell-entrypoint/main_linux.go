//go:build linux

// Command tbound-cell-entrypoint is the signed, frozen in-image entrypoint for
// the governed D09 Pi cell. It is the only process the container starts, and it
// makes only claims it actually implements:
//
//  1. it measures the TCB postconditions and fails closed when any is unmet
//     (uid/gid, empty capabilities, no_new_privs, seccomp filter mode, cgroup-v2
//     membership);
//  2. it reopens the sealed consumer directory handles fd4 (worker exposure)
//     and fd9 (runtime bundle) read-only and rejects anything that is not a
//     directory. The handles are Go-owned consumer descriptors, not raw host
//     source paths; reopening a host directory descriptor through /proc/self/fd
//     does not relocate its f_path, so no raw private-Git or host-admin
//     descriptor is ever exposed to the worker. /proc/self/fd is a magic link
//     and cannot be used to assert O_NOFOLLOW, so it is not treated as a
//     security boundary;
//  3. it applies the Landlock ruleset whose ABI, rights, and canonical digest
//     are bound by the signed template, using LANDLOCK_RESTRICT_SELF_TSYNC so
//     the ruleset covers every thread of the (multi-threaded) Go process. The
//     kernel must provide the registered TSYNC ABI (>= 8); a lower ABI is an
//     explicit unsupported-kernel refusal, never a silent downgrade;
//  4. it emits a strict typed TCB proof on the dedicated fd10 pipe (closed
//     before Node starts, so Node cannot forge it) and execs the held,
//     digest-verified Node binary at fd3 with the fixed argv and exactly the
//     four signed environment fields.
//
// The writable command view, descriptor role mapping, and session composition
// are owned elsewhere (see docs/governed-setup.md and the session composition
// runbook). This entrypoint deliberately does not open a private-Git or host
// admin descriptor.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"tbound/supervisor/internal/piruntime"
)

const (
	cellEntrypointSchema = "tbound-pi-entrypoint/v1"

	exposureFD = 4
	bundleFD   = 9
	proofFD    = 10

	// landlock syscall numbers (linux/amd64).
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	landlockCreateRulesetVersion = 1
	landlockRulePathBeneath      = 1
	landlockRuleNetPort          = 2
	// landlockRestrictSelfTSYNC makes the ruleset cover every thread in the
	// thread group, so the multi-threaded Go runtime is fully confined. It
	// requires the registered TSYNC ABI.
	landlockRestrictSelfTSYNC = 1
	landlockTSYNCABI          = 8
	landlockAccessAllABI8     = piruntime.D09LandlockHandledFS
	landlockAccessAllNet      = piruntime.D09LandlockHandledNet
	landlockReadExecute       = piruntime.D09LandlockReadExec
)

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tbound-entrypoint: "+format+"\n", args...)
	os.Exit(97)
}

func main() {
	commandMode := os.Getenv("TBOUND_CELL_MODE") == "command"
	if len(os.Args) < 2 && !commandMode {
		fatal("missing exec target")
	}
	delta, deltaErr := cellDeltaFromEnv(os.Getenv, commandMode)
	if deltaErr != nil {
		fatal("command-cell delta: %v", deltaErr)
	}
	var target string
	var argv []string
	if commandMode {
		target = strings.TrimSpace(os.Getenv("TBOUND_CELL_COMMAND"))
		if len(os.Args) < 2 || os.Args[1] != target {
			fatal("command argv target does not match the signed command identity")
		}
		argv = os.Args[2:]
	} else {
		target = os.Args[1]
		if target != "/proc/self/fd/3" {
			fatal("exec target must be the held fd3 binary, got %q", target)
		}
		argv = os.Args[2:]
	}

	uid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TBOUND_CELL_UID")))
	if err != nil {
		fatal("TBOUND_CELL_UID is required and must be an integer")
	}
	gid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TBOUND_CELL_GID")))
	if err != nil {
		fatal("TBOUND_CELL_GID is required and must be an integer")
	}
	if os.Geteuid() != uid || os.Getegid() != gid {
		fatal("uid/gid %d:%d do not match the frozen cell identity %d:%d", os.Geteuid(), os.Getegid(), uid, gid)
	}
	uidMap, gidMap, mapDigest, err := measureD09UserNamespaceMaps(uid, gid)
	if err != nil {
		fatal("verify exact registered D09 user namespace mapping: %v", err)
	}

	capEff, noNewPrivs, seccomp, err := readStatus()
	if err != nil {
		fatal("read /proc/self/status: %v", err)
	}
	if capEff != "0000000000000000" {
		fatal("capability set is not empty: %s", capEff)
	}
	if noNewPrivs != "1" {
		fatal("no_new_privs is not set: %s", noNewPrivs)
	}
	if seccomp != "2" {
		fatal("seccomp is not in filter mode: %s", seccomp)
	}
	if err := requireCgroupV2(); err != nil {
		fatal("%v", err)
	}
	if err := preparePrivateWorkerHome(uid, gid); err != nil {
		fatal("prepare private tmpfs HOME: %v", err)
	}

	var workerMountID, runtimeMountID, workerDevice, workerInode, runtimeDevice, runtimeInode uint64
	if commandMode {
		workerMountID, err = rebindConsumerFD(3, "/work", false)
		if err != nil {
			fatal("verify command-view consumer mount: %v", err)
		}
		runtimeMountID = workerMountID
		workerDevice, workerInode = descriptorDeviceInode(3)
		runtimeDevice, runtimeInode = workerDevice, workerInode
		if workerDevice != envUint64("TBOUND_COMMAND_VIEW_DEVICE") || workerInode != envUint64("TBOUND_COMMAND_VIEW_INODE") {
			fatal("command-view consumer mount identity differs from the authorized view descriptor")
		}
		syscall.CloseOnExec(3)
	} else {
		workerCWD := strings.TrimSpace(os.Getenv("TBOUND_WORKER_CWD"))
		if !filepath.IsAbs(workerCWD) || filepath.Clean(workerCWD) != workerCWD {
			fatal("TBOUND_WORKER_CWD must be a clean absolute consumer mount path")
		}
		workerMountID, err = rebindConsumerFD(exposureFD, workerCWD, true)
		if err != nil {
			fatal("verify worker consumer mount: %v", err)
		}
		runtimeMountID, err = rebindConsumerFD(bundleFD, "/runtime", true)
		if err != nil {
			fatal("verify runtime consumer mount: %v", err)
		}
		workerDevice, workerInode = descriptorDeviceInode(exposureFD)
		runtimeDevice, runtimeInode = descriptorDeviceInode(bundleFD)
		if workerDevice != envUint64("TBOUND_WORKER_DEVICE") || workerInode != envUint64("TBOUND_WORKER_INODE") ||
			runtimeDevice != envUint64("TBOUND_RUNTIME_DEVICE") || runtimeInode != envUint64("TBOUND_RUNTIME_INODE") {
			fatal("consumer mount identities differ from the verified Go-owned source descriptors")
		}
		runtimeBundle := os.NewFile(bundleFD, "consumer-runtime-bundle")
		observedBundleDigest, digestErr := piruntime.VerifyLinuxPiRuntimeBundle(runtimeBundle)
		if digestErr != nil || observedBundleDigest != strings.TrimSpace(os.Getenv("TBOUND_RUNTIME_BUNDLE_DIGEST")) {
			fatal("consumer runtime bundle digest mismatch: observed %s: %v", observedBundleDigest, digestErr)
		}
		if err := os.Chdir(workerCWD); err != nil {
			fatal("chdir to verified worker consumer mount: %v", err)
		}
	}

	requiredABI, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TBOUND_LANDLOCK_ABI")))
	if err != nil || requiredABI <= 0 {
		fatal("TBOUND_LANDLOCK_ABI is required and must be a positive integer")
	}
	expectedDigest := strings.TrimSpace(os.Getenv("TBOUND_LANDLOCK_RULESET_DIGEST"))
	if expectedDigest == "" {
		fatal("TBOUND_LANDLOCK_RULESET_DIGEST is required")
	}
	abi, digest, err := applyLandlockFor(requiredABI, delta.delta)
	if err != nil {
		fatal("apply Landlock: %v", err)
	}
	if digest != expectedDigest {
		fatal("applied Landlock ruleset digest %s does not match the signed template %s", digest, expectedDigest)
	}

	proof := map[string]any{
		"schema": cellEntrypointSchema, "uid": uid, "gid": gid,
		"cap_eff": capEff, "no_new_privs": true, "seccomp": 2,
		"landlock_abi": abi, "landlock_ruleset_digest": digest,
		"landlock_tsync": true, "cgroup_v2": true,
		"container_leader_pid":     os.Getpid(),
		"consumer_mounts_verified": true, "raw_source_fds_closed": true,
		"worker_mount_id": workerMountID, "runtime_mount_id": runtimeMountID,
		"consumer_worker_mount_id": workerMountID, "consumer_runtime_mount_id": runtimeMountID,
		"worker_device": workerDevice, "worker_inode": workerInode,
		"runtime_device": runtimeDevice, "runtime_inode": runtimeInode,
		"uid_map": uidMap, "gid_map": gidMap, "user_namespace_map_digest": mapDigest,
	}
	nsProof := measureNamespaceIsolation(networkIsolationRequired(delta.delta))
	for key, value := range nsProof {
		proof[key] = value
	}
	if os.Getenv("TBOUND_CELL_MODE") == "command" {
		runCommandCell(target, argv, proof, delta)
		return
	}
	proofFile := os.NewFile(uintptr(proofFD), "tcb-proof")
	if proofFile == nil {
		fatal("dedicated TCB proof fd%d is not open", proofFD)
	}
	if encoded, err := json.Marshal(proof); err == nil {
		_, _ = proofFile.Write(append(encoded, '\n'))
	}
	// Close the proof pipe before Node starts so the worker cannot forge it.
	if err := proofFile.Close(); err != nil {
		fatal("close TCB proof fd%d: %v", proofFD, err)
	}

	execTarget := "/proc/self/fd/3"
	if strings.TrimSpace(os.Getenv("TBOUND_CELL_MODE")) == "command" {
		execTarget = strings.TrimSpace(os.Getenv("TBOUND_CELL_COMMAND"))
		if !filepath.IsAbs(execTarget) || strings.HasPrefix(execTarget, "/proc/") {
			fatal("command mode requires an absolute in-image command, got %q", execTarget)
		}
	}
	nodeEnv := signedNodeEnvironment()
	if strings.TrimSpace(os.Getenv("TBOUND_CELL_MODE")) != "command" && len(nodeEnv) != 4 {
		fatal("signed Node environment must contain exactly HOME/TMPDIR/LANG/PI_OFFLINE")
	}
	if err := syscall.Exec(execTarget, append([]string{execTarget}, argv...), nodeEnv); err != nil {
		fatal("exec %s: %v", execTarget, err)
	}
}

func measureD09UserNamespaceMaps(uid, gid int) (uidMap, gidMap, digest string, err error) {
	profile := strings.TrimSpace(os.Getenv("TBOUND_USERNS_MAP_PROFILE"))
	digest = strings.TrimSpace(os.Getenv("TBOUND_USERNS_MAP_DIGEST"))
	serviceUID, uidErr := strconv.Atoi(strings.TrimSpace(os.Getenv("TBOUND_USERNS_HOST_UID")))
	serviceGID, gidErr := strconv.Atoi(strings.TrimSpace(os.Getenv("TBOUND_USERNS_HOST_GID")))
	if uidErr != nil || gidErr != nil || serviceUID <= 0 || serviceGID <= 0 {
		return "", "", "", fmt.Errorf("registered host service UID/GID mapping identity is absent")
	}
	uidRaw, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return "", "", "", err
	}
	gidRaw, err := os.ReadFile("/proc/self/gid_map")
	if err != nil {
		return "", "", "", err
	}
	if err := piruntime.VerifyD09InsideIDMaps(profile, digest, uid, gid, serviceUID, serviceGID, uidRaw, gidRaw); err != nil {
		return "", "", "", err
	}
	return strings.TrimSpace(string(uidRaw)), strings.TrimSpace(string(gidRaw)), digest, nil
}

func envUint64(name string) uint64 {
	value, err := strconv.ParseUint(strings.TrimSpace(os.Getenv(name)), 10, 64)
	if err != nil || value == 0 {
		fatal("%s must be a nonzero decimal identity", name)
	}
	return value
}

func measureNamespaceIsolation(privateNetwork bool) map[string]any {
	names := []struct{ file, key, env string }{
		{"user", "user_ns_inode", "TBOUND_HOST_NS_USER"},
		{"mnt", "mount_ns_inode", "TBOUND_HOST_NS_MOUNT"},
		{"pid", "pid_ns_inode", "TBOUND_HOST_NS_PID"},
		{"ipc", "ipc_ns_inode", "TBOUND_HOST_NS_IPC"},
		{"uts", "uts_ns_inode", "TBOUND_HOST_NS_UTS"},
		{"net", "network_ns_inode", "TBOUND_HOST_NS_NETWORK"},
	}
	proof := make(map[string]any, len(names)+1)
	for _, item := range names {
		info, err := os.Stat("/proc/self/ns/" + item.file)
		if err != nil {
			fatal("stat consumer %s namespace: %v", item.file, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Ino == 0 {
			fatal("consumer %s namespace inode is unavailable", item.file)
		}
		host := envUint64(item.env)
		if item.file == "net" && !privateNetwork {
			// net-sink/v1: the cell shares the host network namespace on purpose.
			if stat.Ino != host {
				fatal("net-sink cell is not in the host network namespace")
			}
		} else if stat.Ino == host {
			fatal("consumer %s namespace is not private", item.file)
		}
		proof[item.key] = stat.Ino
	}
	if privateNetwork {
		if err := requireNoExternalRoutes(); err != nil {
			fatal("network namespace contains an external route: %v", err)
		}
	}
	proof["no_external_routes"] = privateNetwork
	return proof
}

func requireNoExternalRoutes() error {
	ipv4, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return err
	}
	for index, line := range strings.Split(strings.TrimSpace(string(ipv4)), "\n") {
		if index == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[0] != "lo" {
			return fmt.Errorf("unexpected IPv4 route %q", line)
		}
		if fields[1] == "00000000" {
			return fmt.Errorf("default IPv4 route is present")
		}
	}
	ipv6, err := os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(ipv6)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[len(fields)-1] != "lo" {
			return fmt.Errorf("unexpected IPv6 route %q", line)
		}
		if fields[0] == strings.Repeat("0", 32) && fields[1] == "00" {
			return fmt.Errorf("default IPv6 route is present")
		}
	}
	return nil
}

type commandProof struct {
	Schema                 string `json:"schema"`
	UID                    int    `json:"uid"`
	GID                    int    `json:"gid"`
	CapEff                 string `json:"cap_eff"`
	NoNewPrivs             bool   `json:"no_new_privs"`
	Seccomp                int    `json:"seccomp"`
	LandlockABI            int    `json:"landlock_abi"`
	LandlockRulesetDigest  string `json:"landlock_ruleset_digest"`
	LandlockTSYNC          bool   `json:"landlock_tsync"`
	CgroupV2               bool   `json:"cgroup_v2"`
	ConsumerMountsVerified bool   `json:"consumer_mounts_verified"`
	RawSourceFDsClosed     bool   `json:"raw_source_fds_closed"`
	WorkerMountID          uint64 `json:"worker_mount_id"`
	RuntimeMountID         uint64 `json:"runtime_mount_id"`
	WorkerDevice           uint64 `json:"worker_device"`
	WorkerInode            uint64 `json:"worker_inode"`
	RuntimeDevice          uint64 `json:"runtime_device"`
	RuntimeInode           uint64 `json:"runtime_inode"`
	UserNSInode            uint64 `json:"user_ns_inode"`
	MountNSInode           uint64 `json:"mount_ns_inode"`
	PIDNSInode             uint64 `json:"pid_ns_inode"`
	IPCNSInode             uint64 `json:"ipc_ns_inode"`
	UTSNSInode             uint64 `json:"uts_ns_inode"`
	NetworkNSInode         uint64 `json:"network_ns_inode"`
	NoExternalRoutes       bool   `json:"no_external_routes"`
	UIDMap                 string `json:"uid_map"`
	GIDMap                 string `json:"gid_map"`
	UserNamespaceMapDigest string `json:"user_namespace_map_digest"`
	ContainerLeaderPID     int    `json:"container_leader_pid"`
	LeaseID                string `json:"lease_id"`
	ViewID                 string `json:"view_id"`
	CommandStarted         bool   `json:"command_started"`
	CommandReady           bool   `json:"command_ready"`
	CommandExitObserved    bool   `json:"command_exit_observed"`
	CommandExitCode        int    `json:"command_exit_code"`
	CommandPath            string `json:"command_path"`
	CommandDigest          string `json:"command_digest"`
	CommandWorkdir         string `json:"command_workdir"`
	ContainerName          string `json:"container_name"`
	ImageRef               string `json:"image_ref"`
	ImageDigest            string `json:"image_digest"`
	TemplateDigest         string `json:"template_digest"`
	Delta                  string `json:"delta,omitempty"`
	DeltaTemplateDigest    string `json:"delta_template_digest,omitempty"`
}

// runCommandCell uses the authenticated start-ack handshake. It writes a TCB
// proof while PID1 is alive and blocked, waits for the host to verify cgroup
// membership and obtain a pidfd, then launches the authorized command. The
// command cannot inherit the proof descriptor (CLOEXEC); after it exits PID1
// writes a second proof line and exits with the observed command code.
func runCommandCell(commandPath string, args []string, base map[string]any, delta cellDelta) {
	if !filepath.IsAbs(commandPath) || filepath.Clean(commandPath) != commandPath {
		fatal("command mode requires a clean absolute in-image executable: %q", commandPath)
	}
	expectedCommandDigest := strings.TrimSpace(os.Getenv("TBOUND_COMMAND_DIGEST"))
	if !validDigest(expectedCommandDigest) {
		fatal("command mode requires the signed command executable digest")
	}
	observedCommandDigest, err := digestCommandExecutable(commandPath)
	if err != nil || observedCommandDigest != expectedCommandDigest {
		fatal("command executable digest mismatch: observed %s expected %s (%v)", observedCommandDigest, expectedCommandDigest, err)
	}
	leaseID := strings.TrimSpace(os.Getenv("TBOUND_COMMAND_LEASE_ID"))
	viewID := strings.TrimSpace(os.Getenv("TBOUND_COMMAND_VIEW_ID"))
	if leaseID == "" || viewID == "" {
		fatal("command mode requires authenticated lease and view identities")
	}
	workingDirectory := strings.TrimSpace(os.Getenv("TBOUND_COMMAND_WORKDIR"))
	if workingDirectory == "" {
		workingDirectory = "."
	}
	cleanWorkingDirectory := filepath.Clean(workingDirectory)
	if filepath.IsAbs(cleanWorkingDirectory) || cleanWorkingDirectory == ".." || strings.HasPrefix(cleanWorkingDirectory, ".."+string(os.PathSeparator)) {
		fatal("command working directory escapes the authorized command view")
	}
	proofFDValue, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TBOUND_CELL_PROOF_FD")))
	if err != nil || proofFDValue < 3 || proofFDValue > 64 {
		fatal("command mode requires a valid TBOUND_CELL_PROOF_FD")
	}
	ackFDValue, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TBOUND_CELL_ACK_FD")))
	if err != nil || ackFDValue < 3 || ackFDValue > 64 || ackFDValue == proofFDValue {
		fatal("command mode requires a valid distinct TBOUND_CELL_ACK_FD")
	}
	proofFile := os.NewFile(uintptr(proofFDValue), "tcb-proof")
	ackFile := os.NewFile(uintptr(ackFDValue), "tcb-start-ack")
	if proofFile == nil || ackFile == nil {
		fatal("command mode requires proof fd%d and start-ack fd%d", proofFDValue, ackFDValue)
	}
	proof := commandProof{
		Schema: cellEntrypointSchema, UID: base["uid"].(int), GID: base["gid"].(int),
		CapEff: base["cap_eff"].(string), NoNewPrivs: true, Seccomp: 2,
		LandlockABI: base["landlock_abi"].(int), LandlockRulesetDigest: base["landlock_ruleset_digest"].(string),
		LandlockTSYNC: true, CgroupV2: true, ContainerLeaderPID: os.Getpid(),
		ConsumerMountsVerified: base["consumer_mounts_verified"].(bool),
		RawSourceFDsClosed:     base["raw_source_fds_closed"].(bool),
		WorkerMountID:          base["worker_mount_id"].(uint64), RuntimeMountID: base["runtime_mount_id"].(uint64),
		WorkerDevice: base["worker_device"].(uint64), WorkerInode: base["worker_inode"].(uint64),
		RuntimeDevice: base["runtime_device"].(uint64), RuntimeInode: base["runtime_inode"].(uint64),
		UserNSInode: base["user_ns_inode"].(uint64), MountNSInode: base["mount_ns_inode"].(uint64),
		PIDNSInode: base["pid_ns_inode"].(uint64), IPCNSInode: base["ipc_ns_inode"].(uint64),
		UTSNSInode: base["uts_ns_inode"].(uint64), NetworkNSInode: base["network_ns_inode"].(uint64),
		NoExternalRoutes: base["no_external_routes"].(bool),
		UIDMap:           base["uid_map"].(string), GIDMap: base["gid_map"].(string),
		UserNamespaceMapDigest: base["user_namespace_map_digest"].(string),
		LeaseID:                leaseID, ViewID: viewID, CommandReady: true,
		ContainerName:  strings.TrimSpace(os.Getenv("TBOUND_CONTAINER_NAME")),
		ImageRef:       strings.TrimSpace(os.Getenv("TBOUND_IMAGE_REF")),
		ImageDigest:    strings.TrimSpace(os.Getenv("TBOUND_IMAGE_DIGEST")),
		TemplateDigest: strings.TrimSpace(os.Getenv("TBOUND_TEMPLATE_DIGEST")),
	}
	proof.Delta = delta.delta.Name()
	if delta.delta.Relaxed() {
		proof.DeltaTemplateDigest, _ = delta.delta.TemplateDigest(proof.TemplateDigest)
	}
	proof.CommandPath = commandPath
	proof.CommandDigest = observedCommandDigest
	proof.CommandWorkdir = cleanWorkingDirectory
	if proof.ContainerName == "" || !strings.HasSuffix(proof.ImageRef, "@"+proof.ImageDigest) ||
		!validDigest(proof.ImageDigest) || !validDigest(proof.TemplateDigest) {
		fatal("command TCB proof is missing frozen image/template identity")
	}
	writeProofLine(proofFile, proof)
	var start [1]byte
	if _, err := io.ReadFull(ackFile, start[:]); err != nil || start[0] != 1 {
		fatal("host did not acknowledge command TCB proof")
	}
	_ = ackFile.Close()
	proof.CommandStarted = true
	syscall.CloseOnExec(proofFDValue)
	cmd := exec.Command(commandPath, args...)
	cmd.Dir = filepath.Join("/work", cleanWorkingDirectory)
	cmd.Env = []string{"HOME=/tmp/home", "TMPDIR=/tmp/tbound-tmp", "LANG=C.UTF-8", "PI_OFFLINE=1", "PATH=/usr/bin:/bin"}
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// A non-ExitError means the command did not produce an observed
			// process exit (for example, execve denial). Withhold the final proof;
			// the host treats that missing proof as UNKNOWN and releases no result.
			fatal("authorized command did not produce an observed process exit: %v", err)
		}
	}
	proof.CommandExitObserved = true
	proof.CommandExitCode = exitCode
	if delta.endMarker != "" {
		// no-reap/v1: end both output streams at the marker before the final proof, so the host has
		// the complete output even though a surviving descendant may hold the streams open.
		_, _ = os.Stdout.WriteString(delta.endMarker)
		_, _ = os.Stderr.WriteString(delta.endMarker)
	}
	writeProofLine(proofFile, proof)
	_ = proofFile.Close()
	if delta.delta.NoReap() {
		lingerAsInit()
	}
	os.Exit(exitCode)
}

func digestCommandExecutable(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm()&0o022 != 0 || before.Size() <= 0 || before.Size() > 128<<20 {
		return "", fmt.Errorf("command executable must be a non-symlink immutable regular image file")
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || before.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("command executable must be root-owned and executable")
	}
	if err := requireReadOnlyMount(path); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("command executable is not a regular immutable image file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("command executable must be root-owned and executable in the frozen image")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, before.Size()+1))
	if err != nil {
		return "", err
	}
	if n != before.Size() {
		return "", errors.New("command executable changed while it was hashed")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func requireReadOnlyMount(path string) error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	clean := filepath.Clean(path)
	best, ro := -1, false
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 6 {
			continue
		}
		mount := filepath.Clean(unescapeMountPoint(fields[4]))
		if clean != mount && !strings.HasPrefix(clean, mount+string(os.PathSeparator)) {
			continue
		}
		if len(mount) > best {
			best = len(mount)
			ro = strings.Contains(","+fields[5]+",", ",ro,")
		}
	}
	if best < 0 || !ro {
		return fmt.Errorf("command executable is not on a read-only mount")
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func writeProofLine(writer io.Writer, proof commandProof) {
	encoded, err := json.Marshal(proof)
	if err != nil {
		fatal("encode TCB proof: %v", err)
	}
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		fatal("write TCB proof: %v", err)
	}
}

func descriptorDeviceInode(fd int) (uint64, uint64) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		fatal("stat consumer descriptor fd%d: %v", fd, err)
	}
	if stat.Ino == 0 {
		fatal("consumer descriptor fd%d has no stable inode identity", fd)
	}
	return uint64(stat.Dev), stat.Ino
}

func preparePrivateWorkerHome(uid, gid int) error {
	for _, path := range []string{"/tmp/home", "/tmp/tbound-tmp"} {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("%s is not a private mode-0700 directory", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
			return fmt.Errorf("%s is not owned by the frozen worker uid/gid", path)
		}
	}
	return nil
}

// signedNodeEnvironment returns exactly the four signed worker fields, stripping
// every helper variable (TBOUND_*) before Node is exec'd.
func signedNodeEnvironment() []string {
	wanted := map[string]bool{"HOME": true, "TMPDIR": true, "LANG": true, "PI_OFFLINE": true}
	env := make([]string, 0, 4)
	for _, item := range os.Environ() {
		name, _, ok := strings.Cut(item, "=")
		if ok && wanted[name] {
			env = append(env, item)
		}
	}
	return env
}

// readStatus parses the measured fields from /proc/self/status.
func readStatus() (capEff, noNewPrivs, seccomp string, err error) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "", "", "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "CapEff:":
			capEff = fields[1]
		case "NoNewPrivs:":
			noNewPrivs = fields[1]
		case "Seccomp:":
			seccomp = fields[1]
		}
	}
	if capEff == "" || noNewPrivs == "" || seccomp == "" {
		return "", "", "", fmt.Errorf("status omitted a required measured field")
	}
	return capEff, noNewPrivs, seccomp, nil
}

// requireCgroupV2 fails closed unless the process is a member of a cgroup-v2
// hierarchy.
func requireCgroupV2() error {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return fmt.Errorf("cgroup membership is empty")
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "0::") {
			return fmt.Errorf("unexpected cgroup membership %q", line)
		}
	}
	return nil
}

// reopenReadOnlyDirectory reopens an inherited directory fd read-only without
// following symlinks and refuses anything that is not a directory. The returned
// handle is the only view of the sealed source; no path is accepted.
func reopenReadOnlyDirectory(fd int) (*os.File, error) {
	if fd <= 2 {
		return nil, fmt.Errorf("fd%d is reserved", fd)
	}
	opened, err := syscall.Open("/proc/self/fd/"+strconv.Itoa(fd), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(opened), "sealed-fd-"+strconv.Itoa(fd))
	if file == nil {
		_ = syscall.Close(opened)
		return nil, fmt.Errorf("invalid reopened descriptor")
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		_ = file.Close()
		return nil, fmt.Errorf("reopened fd%d is not a directory", fd)
	}
	return file, nil
}

// rebindConsumerFD proves that the consumer-visible destination is the same
// inode as the inherited verified source, is mounted at the exact namespace
// path, has the required ro/rw mount mode, and then replaces the raw inherited
// descriptor with a descriptor opened through that consumer mount. This is the
// required relocation step: merely reopening /proc/self/fd would retain the
// host source f_path.
func rebindConsumerFD(sourceFD int, destination string, requireReadOnly bool) (uint64, error) {
	if sourceFD < 3 || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return 0, fmt.Errorf("invalid consumer descriptor/path")
	}
	source, err := reopenReadOnlyDirectory(sourceFD)
	if err != nil {
		return 0, err
	}
	defer source.Close()
	consumer, err := os.Open(destination)
	if err != nil {
		return 0, err
	}
	defer consumer.Close()
	sourceInfo, err := source.Stat()
	if err != nil {
		return 0, err
	}
	consumerInfo, err := consumer.Stat()
	if err != nil || !consumerInfo.IsDir() || !os.SameFile(sourceInfo, consumerInfo) {
		return 0, fmt.Errorf("consumer mount %s does not identify the verified source descriptor", destination)
	}
	mountID, readOnly, err := verifyConsumerMountInfo(destination)
	if err != nil {
		return 0, err
	}
	if requireReadOnly && !readOnly {
		return 0, fmt.Errorf("consumer mount %s is not read-only", destination)
	}
	if !requireReadOnly && readOnly {
		return 0, fmt.Errorf("leased command-view mount %s is unexpectedly read-only", destination)
	}
	if err := syscall.Dup3(int(consumer.Fd()), sourceFD, 0); err != nil {
		return 0, fmt.Errorf("replace host source fd%d with consumer-mount descriptor: %w", sourceFD, err)
	}
	return mountID, nil
}

func verifyConsumerMountInfo(destination string) (uint64, bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return 0, false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 6 || filepath.Clean(unescapeMountPoint(fields[4])) != destination {
			continue
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || id == 0 {
			return 0, false, fmt.Errorf("consumer mount ID is invalid")
		}
		options := "," + fields[5] + ","
		return id, strings.Contains(options, ",ro,"), nil
	}
	return 0, false, fmt.Errorf("consumer path %s is not an exact mountpoint", destination)
}

func unescapeMountPoint(value string) string {
	return strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\").Replace(value)
}

// applyLandlock installs the ruleset whose effective rights are derived from the
// signed template and returns the kernel ABI and the canonical ruleset digest. It
// uses LANDLOCK_RESTRICT_SELF_TSYNC so every Go runtime thread is covered, and
// fails closed with an exact ABI prerequisite when the kernel lacks the
// registered TSYNC ABI or the reviewed paths are missing.
func applyLandlock(requiredABI int) (int, string, error) {
	return applyLandlockFor(requiredABI, piruntime.D09CellDelta{})
}

// applyLandlockFor is applyLandlock for the Landlock ruleset of a command-cell delta (planLandlock).
func applyLandlockFor(requiredABI int, delta piruntime.D09CellDelta) (int, string, error) {
	if requiredABI != landlockTSYNCABI {
		return 0, "", fmt.Errorf("this entrypoint is frozen to the exact Landlock ABI %d (TSYNC) profile; signed template requested %d", landlockTSYNCABI, requiredABI)
	}
	roPaths := splitPaths(os.Getenv("TBOUND_LANDLOCK_RO_PATH"))
	rwPaths := splitPaths(os.Getenv("TBOUND_LANDLOCK_RW_PATH"))
	if len(roPaths) == 0 && len(rwPaths) == 0 {
		return 0, "", fmt.Errorf("TBOUND_LANDLOCK_RO_PATH/TBOUND_LANDLOCK_RW_PATH are required")
	}
	abiRaw, _, errno := syscall.Syscall(uintptr(sysLandlockCreateRuleset), 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return 0, "", fmt.Errorf("probe landlock ABI: %w", errno)
	}
	abi := int(abiRaw)
	if abi != requiredABI {
		return 0, "", fmt.Errorf("landlock ABI %d (TSYNC) is required exactly, kernel reports %d", requiredABI, abi)
	}
	plan := planLandlock(delta, roPaths, rwPaths)
	digest, err := delta.LandlockRulesetDigest(roPaths, rwPaths)
	if err != nil {
		return 0, "", err
	}
	var fd uintptr
	if plan.scoped == 0 {
		ruleset := struct {
			HandledAccessFS  uint64
			HandledAccessNet uint64
		}{plan.handledFS, plan.handledNet}
		fd, _, errno = syscall.Syscall(uintptr(sysLandlockCreateRuleset), uintptr(unsafe.Pointer(&ruleset)), unsafe.Sizeof(ruleset), 0)
	} else {
		ruleset := struct {
			HandledAccessFS  uint64
			HandledAccessNet uint64
			Scoped           uint64
		}{plan.handledFS, plan.handledNet, plan.scoped}
		fd, _, errno = syscall.Syscall(uintptr(sysLandlockCreateRuleset), uintptr(unsafe.Pointer(&ruleset)), unsafe.Sizeof(ruleset), 0)
	}
	if errno != 0 {
		return 0, "", fmt.Errorf("create landlock ruleset: %w", errno)
	}
	defer syscall.Close(int(fd))
	for _, rule := range plan.pathRules {
		parent, err := os.Open(rule.path)
		if err != nil {
			return 0, "", fmt.Errorf("open landlock path %q: %w", rule.path, err)
		}
		attr := struct {
			AllowedAccess uint64
			ParentFd      int32
		}{rule.access, int32(parent.Fd())}
		_, _, errno := syscall.Syscall6(uintptr(sysLandlockAddRule), fd, landlockRulePathBeneath, uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
		_ = parent.Close()
		if errno != 0 {
			return 0, "", fmt.Errorf("add landlock rule %q: %w", rule.path, errno)
		}
	}
	// The only network right ever allowed: connect to a TCP port of the delta (net-sink/v1: 8080).
	for _, port := range plan.connectPorts {
		attr := struct {
			AllowedAccess uint64
			Port          uint64
		}{piruntime.D09LandlockNetConnectTCP, uint64(port)}
		if _, _, errno := syscall.Syscall6(uintptr(sysLandlockAddRule), fd, landlockRuleNetPort, uintptr(unsafe.Pointer(&attr)), 0, 0, 0); errno != 0 {
			return 0, "", fmt.Errorf("add landlock connect rule for port %d: %w", port, errno)
		}
	}
	if _, _, errno := syscall.Syscall(uintptr(sysLandlockRestrictSelf), fd, landlockRestrictSelfTSYNC, 0); errno != 0 {
		return 0, "", fmt.Errorf("restrict self with landlock TSYNC: %w", errno)
	}
	return abi, digest, nil
}

type landlockRule struct {
	path   string
	access uint64
}

func appendRules(roPaths []string, roAccess uint64, rwPaths []string, rwAccess uint64) []landlockRule {
	rules := make([]landlockRule, 0, len(roPaths)+len(rwPaths))
	for _, path := range roPaths {
		rules = append(rules, landlockRule{path: path, access: roAccess})
	}
	for _, path := range rwPaths {
		rules = append(rules, landlockRule{path: path, access: rwAccess})
	}
	return rules
}

func splitPaths(value string) []string {
	parts := strings.Split(value, ",")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	return paths
}
