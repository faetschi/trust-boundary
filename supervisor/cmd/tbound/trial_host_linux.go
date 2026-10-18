//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/trialfix"
)

// What `tbound trial reset` does to the guest besides the live root, both before the tree is
// touched and both fail closed:
//
//   - ensureNetSink makes the net-sink/v1 confinement current. nft resolves the cgroup path of the
//     rule to a cgroup id when it loads, so a reboot, an unloaded table or a recreated parent
//     directory silently ends the confinement; the pinned rule file alone proves nothing about the
//     kernel. The reset creates the net-sink parent cgroup if it is absent (owned by the service
//     identity), loads the pinned file as one transaction, reads the table back and compares it to
//     the exact rules, and only then writes the root-owned attestation the service checks
//     (piruntime.D09NetSinkAttestation).
//   - cleanNoReap removes what no-reap/v1 cells leave behind: their containers (no --rm; labelled
//     piruntime.D09NoReapContainerLabel) through the service's own podman context, and their
//     empty tbound-nr-* cgroups. The persistent net-sink parent is never touched.
//
// Both run after cgroup.kill and the wait for populated 0, so no process is left in those cgroups.
type trialHost struct {
	// nft runs the nft binary with stdin; podman runs the service's podman (arguments after the
	// storage flags). Tests replace them.
	nft    func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
	podman func(ctx context.Context, args ...string) ([]byte, error)
	// ruleFile and attestFile are piruntime.D09NetSinkRulePath and D09NetSinkAttestPath; rootUID is
	// the owner the rule file must have.
	ruleFile, attestFile string
	rootUID              uint32
	// owner is the service identity: it owns the net-sink parent cgroup.
	owner  trialfix.Owner
	bootID func() (string, error)
	// openCells opens the registered cells cgroup directory; checkFS requires a directory to be on
	// the cgroup2 filesystem.
	openCells func(cell evidenceCell) (*os.File, error)
	checkFS   func(dir *os.File) error
	// runtimeDir is the service's runtime directory; without it the service has no podman state.
	runtimeDir string
}

const (
	trialNftPath    = "/usr/sbin/nft"
	trialPodmanPath = "/usr/bin/podman"
	trialHostWait   = 60 * time.Second
	// trialNoReapPrefix names the command cgroups of no-reap/v1 (piruntime.d09DetachedScopePrefix).
	trialNoReapPrefix = "tbound-nr-"
)

func productionTrialHost() trialHost {
	owner := trialfix.Canonical
	return trialHost{
		nft: runTrialNft,
		podman: func(ctx context.Context, args ...string) ([]byte, error) {
			return runTrialCommand(servicePodmanCommand(ctx, owner.UID, owner.GID, args...), nil)
		},
		ruleFile: piruntime.D09NetSinkRulePath, attestFile: piruntime.D09NetSinkAttestPath, rootUID: 0, owner: owner,
		bootID: piruntime.ReadD09BootID, openCells: openCellsCgroup, checkFS: requireCgroup2Dir,
		runtimeDir: piruntime.D09ManagedPiRuntimeDir,
	}
}

func requireCgroup2Dir(dir *os.File) error {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(dir.Fd()), &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return fmt.Errorf("%s is not on a cgroup2 filesystem (%v)", dir.Name(), err)
	}
	return nil
}

func runTrialNft(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, trialHostWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, trialNftPath, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	return runTrialCommand(cmd, stdin)
}

func runTrialCommand(cmd *exec.Cmd, stdin []byte) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", cmd.Path, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// servicePodmanCommand is podman in the service's context, as the provisioning steps run it
// (PROVISIONING-SIGNING.md I1, PODMAN_SVC): the service uid and gid with no supplementary groups,
// the unit's environment, and the unit's storage and runroot.
func servicePodmanCommand(ctx context.Context, uid, gid uint32, args ...string) *exec.Cmd {
	full := append([]string{"--root", piruntime.D09ManagedPiPodmanGraphroot, "--runroot", piruntime.D09ManagedPiPodmanRunroot}, args...)
	ctx, cancel := context.WithTimeout(ctx, trialHostWait)
	cmd := exec.CommandContext(ctx, trialPodmanPath, full...)
	cmd.Cancel = func() error { defer cancel(); return cmd.Process.Kill() }
	cmd.Dir = "/"
	cmd.Env = []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + piruntime.D09ManagedPiPodmanHome, "USER=" + piruntime.D09ManagedPiServiceName, "LOGNAME=" + piruntime.D09ManagedPiServiceName,
		"XDG_RUNTIME_DIR=" + piruntime.D09ManagedPiRuntimeDir, "XDG_CONFIG_HOME=" + piruntime.D09ManagedPiPodmanConfigHome,
		"XDG_DATA_HOME=" + piruntime.D09ManagedPiPodmanDataHome, "XDG_CACHE_HOME=" + piruntime.D09ManagedPiPodmanCacheHome,
		"TMPDIR=" + piruntime.D09ManagedPiTempDir, "CONTAINERS_CONF=" + piruntime.D09ManagedPiContainersConfPath,
		"CONTAINERS_STORAGE_CONF=" + piruntime.D09ManagedPiStorageConfPath, "LANG=C", "LC_ALL=C",
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
	return cmd
}

// ---- net-sink/v1 ----

// netSinkCgroupControlFiles are the files of a cgroup directory a delegatee must own to manage it.
var netSinkCgroupControlFiles = []string{"cgroup.procs", "cgroup.subtree_control", "cgroup.threads"}

func (h trialHost) ensureNetSink(ctx context.Context, cell evidenceCell) (err error) {
	// A guest that has no rule installed has no net-sink; the service refuses net-sink for it.
	if _, statErr := os.Lstat(h.ruleFile); os.IsNotExist(statErr) {
		return h.removeAttestation()
	}
	// Whatever attestation exists is void until this load is verified.
	if err := h.removeAttestation(); err != nil {
		return err
	}
	rule, err := h.readRuleFile()
	if err != nil {
		return err
	}
	path, level, err := piruntime.D09NetSinkNftMatch(cell.CgroupPath)
	if err != nil {
		return err
	}
	bootID, err := h.bootID()
	if err != nil {
		return fmt.Errorf("the boot id cannot be read: %w", err)
	}
	parent, err := h.ensureNetSinkParent(cell)
	if err != nil {
		return err
	}
	defer parent.Close()
	attestation, err := piruntime.NewD09NetSinkAttestation(bootID, parent)
	if err != nil {
		return err
	}
	// One transaction: the table is replaced, never absent, and a failed load changes nothing.
	if out, err := h.nft(ctx, rule, "-f", "-"); err != nil {
		return fmt.Errorf("nft cannot load %s: %w (%s)", h.ruleFile, err, bytes.TrimSpace(out))
	}
	listing, err := h.nft(ctx, nil, "-j", "list", "table", "inet", piruntime.D09NetSinkTable)
	if err != nil {
		return fmt.Errorf("nft cannot read the table back: %w", err)
	}
	if err := verifyNetSinkRuleset(listing, path, level); err != nil {
		return fmt.Errorf("the loaded table is not the pinned rule: %w", err)
	}
	return h.writeAttestation(attestation)
}

func (h trialHost) removeAttestation() error {
	if err := os.Remove(h.attestFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove the net-sink attestation: %w", err)
	}
	return nil
}

// readRuleFile reads the installed rule file (a regular file owned by rootUID that nobody else can
// write) and requires the pinned digest. The bytes that pass are the bytes nft loads.
func (h trialHost) readRuleFile() ([]byte, error) {
	file, err := os.OpenFile(h.ruleFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("net-sink rule %s: %w", h.ruleFile, err)
	}
	defer file.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG ||
		st.Uid != h.rootUID || st.Mode&0o022 != 0 || st.Size > 8<<10 {
		return nil, fmt.Errorf("net-sink rule %s is not a small regular file owned by uid %d and not writable by others", h.ruleFile, h.rootUID)
	}
	data, err := io.ReadAll(io.LimitReader(file, 8<<10+1))
	if err != nil {
		return nil, err
	}
	if piruntime.DigestBytes(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))) != piruntime.D09NetSinkRuleDigest {
		return nil, fmt.Errorf("net-sink rule %s differs from the pinned one", h.ruleFile)
	}
	return data, nil
}

// ensureNetSinkParent opens the net-sink parent below the registered cells cgroup, creating it
// if it is absent, and makes the service identity its owner (and the owner of the files the
// service writes to manage it: the delegation contract of cgroup v2).
func (h trialHost) ensureNetSinkParent(cell evidenceCell) (*os.File, error) {
	cells, err := h.openCells(cell)
	if err != nil {
		return nil, fmt.Errorf("refusing: %w", err)
	}
	defer cells.Close()
	open := func() (*os.File, error) {
		fd, err := syscall.Openat(int(cells.Fd()), piruntime.D09NetSinkCgroupName, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), piruntime.D09NetSinkCgroupName), nil
	}
	parent, err := open()
	if errors.Is(err, syscall.ENOENT) {
		if err = syscall.Mkdirat(int(cells.Fd()), piruntime.D09NetSinkCgroupName, 0o755); err == nil {
			parent, err = open()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("net-sink parent cgroup: %w", err)
	}
	fail := func(err error) (*os.File, error) {
		parent.Close()
		return nil, fmt.Errorf("net-sink parent cgroup: %w", err)
	}
	if err := h.checkFS(parent); err != nil {
		return fail(err)
	}
	if err := unix.Fchown(int(parent.Fd()), int(h.owner.UID), int(h.owner.GID)); err != nil {
		return fail(err)
	}
	for _, name := range netSinkCgroupControlFiles {
		if err := unix.Fchownat(int(parent.Fd()), name, int(h.owner.UID), int(h.owner.GID), unix.AT_SYMLINK_NOFOLLOW); err != nil && !errors.Is(err, syscall.ENOENT) {
			return fail(err)
		}
	}
	return parent, nil
}

// writeAttestation writes the attestation atomically: a temporary file in the same directory,
// synced, then renamed over the old name. It is root-owned (the reset runs as root) and 0644.
func (h trialHost) writeAttestation(a piruntime.D09NetSinkAttestation) error {
	data, err := a.Marshal()
	if err != nil {
		return err
	}
	temp := h.attestFile + ".tmp"
	_ = os.Remove(temp)
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("net-sink attestation: %w", err)
	}
	_, werr := file.Write(data)
	cerr := file.Chmod(0o644)
	serr := file.Sync()
	if err := errors.Join(werr, cerr, serr, file.Close()); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("net-sink attestation: %w", err)
	}
	if err := os.Rename(temp, h.attestFile); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("net-sink attestation: %w", err)
	}
	return nil
}

// verifyNetSinkRuleset requires the output of `nft -j list table inet tbound_net_sink` to hold
// exactly the pinned rule: one table, one base chain `output` (filter, hook output, policy
// accept) and two rules in order, an accept of TCP to the sink address and port and a counted
// drop, both matching the net-sink cgroup path and level. Anything else, extra rules included,
// is refused. The comparison is over the semantic fields, not over the text.
func verifyNetSinkRuleset(listing []byte, cgroupPath string, level int) error {
	var doc struct {
		Items []map[string]any `json:"nftables"`
	}
	if err := json.Unmarshal(listing, &doc); err != nil {
		return fmt.Errorf("nft output is not JSON: %w", err)
	}
	var objects []map[string]any
	for _, item := range doc.Items {
		if _, meta := item["metainfo"]; !meta {
			objects = append(objects, item)
		}
	}
	if len(objects) != 4 {
		return fmt.Errorf("the table has %d objects, want its table, one chain and two rules", len(objects))
	}
	object := func(i int, kind string) (map[string]any, error) {
		inner, ok := objects[i][kind].(map[string]any)
		if !ok || len(objects[i]) != 1 {
			return nil, fmt.Errorf("object %d is not a %s", i, kind)
		}
		return inner, nil
	}
	table, err := object(0, "table")
	if err != nil {
		return err
	}
	chain, err := object(1, "chain")
	if err != nil {
		return err
	}
	if table["family"] != "inet" || table["name"] != piruntime.D09NetSinkTable {
		return errors.New("the table is not inet " + piruntime.D09NetSinkTable)
	}
	if chain["family"] != "inet" || chain["table"] != piruntime.D09NetSinkTable || chain["name"] != "output" || chain["type"] != "filter" ||
		chain["hook"] != "output" || chain["prio"] != float64(0) || chain["policy"] != "accept" {
		return errors.New("the chain is not the base chain output (filter, hook output, priority 0, policy accept)")
	}
	cgroup := fmt.Sprintf("match socket.cgroupv2.%d == %s", level, cgroupPath)
	want := [][]string{
		{cgroup, "match payload.ip.daddr == " + piruntime.D09NetSinkAddress, "match payload.tcp.dport == " + strconv.Itoa(piruntime.D09NetSinkPort), "accept"},
		{cgroup, "counter", "drop"},
	}
	for i := range want {
		rule, err := object(2+i, "rule")
		if err != nil {
			return err
		}
		if rule["family"] != "inet" || rule["table"] != piruntime.D09NetSinkTable || rule["chain"] != "output" {
			return fmt.Errorf("rule %d is not in inet %s output", i, piruntime.D09NetSinkTable)
		}
		exprs, _ := rule["expr"].([]any)
		var got []string
		for _, expr := range exprs {
			text, err := nftExpression(expr)
			if err != nil {
				return fmt.Errorf("rule %d: %w", i, err)
			}
			got = append(got, text)
		}
		if strings.Join(got, "; ") != strings.Join(want[i], "; ") {
			return fmt.Errorf("rule %d is %q, want %q", i, strings.Join(got, "; "), strings.Join(want[i], "; "))
		}
	}
	return nil
}

// nftExpression renders one statement of the nft JSON as text, for the kinds the pinned rule uses.
func nftExpression(expr any) (string, error) {
	statement, ok := expr.(map[string]any)
	if !ok || len(statement) != 1 {
		return "", errors.New("a statement is not a single-key object")
	}
	for kind, value := range statement {
		switch kind {
		case "accept", "drop", "counter":
			return kind, nil
		case "match":
			m, _ := value.(map[string]any)
			left, _ := m["left"].(map[string]any)
			operand, err := nftOperand(left)
			if err != nil {
				return "", err
			}
			var right string
			switch r := m["right"].(type) {
			case string:
				right = r
			case float64:
				right = strconv.FormatFloat(r, 'f', -1, 64)
			default:
				return "", errors.New("a match has an operand that is neither a string nor a number")
			}
			return fmt.Sprintf("match %s %v %s", operand, m["op"], right), nil
		}
		return "", fmt.Errorf("unexpected statement %q", kind)
	}
	return "", errors.New("empty statement")
}

func nftOperand(left map[string]any) (string, error) {
	if socket, ok := left["socket"].(map[string]any); ok && len(left) == 1 {
		level, _ := socket["level"].(float64)
		return fmt.Sprintf("socket.%v.%d", socket["key"], int(level)), nil
	}
	if payload, ok := left["payload"].(map[string]any); ok && len(left) == 1 {
		return fmt.Sprintf("payload.%v.%v", payload["protocol"], payload["field"]), nil
	}
	return "", errors.New("a match names an operand other than a socket or a payload field")
}

// ---- no-reap/v1 ----

var trialContainerIDRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// cleanNoReap is the reset's answer to cells that were left running by design: their containers
// are not removed by the lease (no --rm) and their cgroups are detached from it. It runs after
// cgroup.kill, so nothing is alive in them.
func (h trialHost) cleanNoReap(ctx context.Context, cell evidenceCell) error {
	if err := h.removeNoReapContainers(ctx); err != nil {
		return err
	}
	return h.removeNoReapCgroups(cell)
}

func (h trialHost) noReapContainers(ctx context.Context) ([]string, error) {
	out, err := h.podman(ctx, "ps", "-a", "-q", "--no-trunc", "--filter", "label="+piruntime.D09NoReapContainerLabel)
	if err != nil {
		return nil, fmt.Errorf("podman cannot list the no-reap containers: %w", err)
	}
	var ids []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if !trialContainerIDRE.MatchString(line) {
			return nil, fmt.Errorf("podman listed %q, which is not a container id", line)
		}
		ids = append(ids, line)
	}
	return ids, nil
}

func (h trialHost) removeNoReapContainers(ctx context.Context) error {
	// systemd removes the runtime directory with the service: no runroot, no container state.
	if _, err := os.Stat(h.runtimeDir); os.IsNotExist(err) {
		return nil
	}
	ids, err := h.noReapContainers(ctx)
	if err != nil || len(ids) == 0 {
		return err
	}
	if _, err := h.podman(ctx, append([]string{"rm", "-f", "-t", "0"}, ids...)...); err != nil {
		return fmt.Errorf("podman cannot remove the no-reap containers %v: %w", ids, err)
	}
	if left, err := h.noReapContainers(ctx); err != nil || len(left) != 0 {
		return fmt.Errorf("no-reap containers remain after removal: %v %v", left, err)
	}
	return nil
}

// removeNoReapCgroups removes the tbound-nr-* children of the cells cgroup with everything below
// them. rmdir of a cgroup that still holds a process or a child fails; that is a refusal. Nothing
// else is removed: the net-sink parent persists because the nft rule is bound to its id.
func (h trialHost) removeNoReapCgroups(cell evidenceCell) error {
	cells, err := h.openCells(cell)
	if err != nil {
		return fmt.Errorf("refusing: %w", err)
	}
	defer cells.Close()
	entries, err := cells.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), trialNoReapPrefix) {
			continue
		}
		if !entry.IsDir() {
			return fmt.Errorf("%s is not a cgroup directory", entry.Name())
		}
		if err := h.removeCgroupTree(int(cells.Fd()), entry.Name(), 0); err != nil {
			return err
		}
	}
	return nil
}

const trialCgroupTreeDepth = 8

func (h trialHost) removeCgroupTree(parentFD int, name string, depth int) error {
	if depth > trialCgroupTreeDepth {
		return fmt.Errorf("cgroup %s is nested deeper than %d levels", name, trialCgroupTreeDepth)
	}
	fd, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("cgroup %s: %w", name, err)
	}
	dir := os.NewFile(uintptr(fd), name)
	defer dir.Close()
	if err := h.checkFS(dir); err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("cgroup %s: %w", name, err)
	}
	for _, entry := range entries {
		// The regular files of a cgroup directory are its control files.
		if entry.IsDir() {
			if err := h.removeCgroupTree(int(dir.Fd()), entry.Name(), depth+1); err != nil {
				return err
			}
		}
	}
	if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("cgroup %s cannot be removed (still populated or in use): %w", name, err)
	}
	return nil
}
