//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

var productOperatorEUID = os.Geteuid
var productOperatorStopped = checkProductOperatorStopped
var productOperatorDelegate = sandboxProductOperator
var productOperatorSandboxCommand = exec.Command

// The signed scanner calibration is measured inside this filesystem sandbox.
// Start as root to authenticate the socket, then drop to the journal's owner.
func sandboxProductOperator(turn, reason string, dry bool, out, stderr io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := productOperatorSandboxCommand("systemd-run", "--wait", "--collect", "--pipe", "--quiet",
		"-p", "NoNewPrivileges=yes", "-p", "ProtectSystem=strict", "-p", "ProtectHome=yes", "-p", "PrivateTmp=yes",
		"-p", "UMask=0077", "-p", "ReadWritePaths="+productsetup.StateDir,
		"--", executable, "product", "turns", "resolve-sandbox")
	data, err := json.Marshal(productOperatorRequest{turn, reason, dry})
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(string(data))
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		return adminRefusal("operator sandbox refused (see diagnostic above): %v", err)
	}
	return nil
}

func runProductOperatorSandbox(in io.Reader, out, stderr io.Writer) error {
	if os.Geteuid() != 0 {
		return adminRefusal("operator sandbox must run as root")
	}
	var request productOperatorRequest
	if err := json.NewDecoder(io.LimitReader(in, 4096)).Decode(&request); err != nil {
		return adminRefusal("invalid operator request: %v", err)
	}
	if err := productsession.ValidateOperatorReason(request.Reason); err != nil {
		return err
	}
	if err := productOperatorStopped(); err != nil {
		return adminRefusal("worker exclusion: %v", err)
	}
	return delegateProductOperator(request.Turn, request.Reason, request.DryRun, out, stderr)
}

func runProductOperator(args []string, out, stderr io.Writer) error {
	flags := flag.NewFlagSet("product turns resolve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	turn := flags.String("turn", "", "unknown turn ID")
	reason := flags.String("reason", "", "operator reason (1-256 UTF-8 bytes)")
	dry := flags.Bool("dry-run", false, "check evidence without writing")
	if err := flags.Parse(args); err != nil {
		return adminRefusal("%v", err)
	}
	if flags.NArg() != 0 || *turn == "" {
		return adminRefusal("usage: tbound product turns resolve --turn ID --reason TEXT [--dry-run]")
	}
	if err := productsession.ValidateOperatorReason(*reason); err != nil {
		return adminRefusal("%v", err)
	}
	if productOperatorEUID() != 0 {
		return adminRefusal("operator resolution must run as root, including --dry-run")
	}
	if err := productOperatorStopped(); err != nil {
		return adminRefusal("worker exclusion: %v", err)
	}
	return productOperatorDelegate(*turn, *reason, *dry, out, stderr)
}

// productOperatorHostState is the narrow host seam of worker exclusion: the unit's
// systemd state, its cgroup, and the process inventory. Production wires the real
// reads; tests replace only this struct.
type productOperatorHostState struct {
	systemctlShow        func() ([]byte, error)
	lookupServiceAccount func(string) (*user.User, error)
	requireCgroupV2      func() error
	emptyUnitCgroup      func(string) error
	readProcessDirectory func(string) ([]os.DirEntry, error)
	readProcessStatus    func(string) ([]byte, error)
}

var productOperatorHost = productOperatorHostState{
	systemctlShow: func() ([]byte, error) {
		return exec.Command("systemctl", "show", productsetup.UnitName, "--property=LoadState", "--property=ActiveState", "--property=MainPID", "--property=ControlGroup").Output()
	},
	lookupServiceAccount: func(name string) (*user.User, error) {
		return productAdminLookupUser(name)
	},
	requireCgroupV2: func() error {
		var stat unix.Statfs_t
		if err := unix.Statfs("/sys/fs/cgroup", &stat); err != nil || stat.Type != unix.CGROUP2_SUPER_MAGIC {
			return errors.New("cannot establish the cgroup v2 process inventory")
		}
		return nil
	},
	emptyUnitCgroup:      productOperatorEmptyCgroup,
	readProcessDirectory: os.ReadDir,
	readProcessStatus:    os.ReadFile,
}

// Require a stopped service and an empty cgroup tree, including delegated descendants.
func checkProductOperatorStopped() error {
	data, err := productOperatorHost.systemctlShow()
	if err != nil {
		return fmt.Errorf("cannot inspect %s: %w", productsetup.UnitName, err)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			fields[key] = value
		}
	}
	if fields["LoadState"] != "loaded" || fields["ActiveState"] != "inactive" || fields["MainPID"] != "0" {
		return errors.New("stop tbound-product.service before resolving a turn")
	}
	scope := "/sys/fs/cgroup/system.slice/" + productsetup.UnitName
	if group := fields["ControlGroup"]; group != "" && group != "/system.slice/"+productsetup.UnitName {
		return errors.New("service has an unrecognized command/cgroup scope")
	}
	if err := productOperatorHost.requireCgroupV2(); err != nil {
		return err
	}
	if err := productOperatorHost.emptyUnitCgroup(scope); err != nil {
		return err
	}
	// Catch a manually launched supervisor outside the installed service scope too.
	account, err := productOperatorHost.lookupServiceAccount(productsetup.ServiceUser)
	if err != nil {
		return err
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || uid == 0 {
		return errors.New("service account must have a non-root numeric UID")
	}
	entries, err := productOperatorHost.readProcessDirectory("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		status, err := productOperatorHost.readProcessStatus(filepath.Join("/proc", entry.Name(), "status"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot inspect process %s: %w", entry.Name(), err)
		}
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "Uid:") {
				for _, value := range strings.Fields(strings.TrimPrefix(line, "Uid:")) {
					if value == account.Uid {
						return fmt.Errorf("service worker process %s is alive", entry.Name())
					}
				}
			}
		}
	}
	return nil
}

func productOperatorEmptyCgroup(scope string) error {
	if _, err := os.Lstat(scope); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(scope, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("cgroup inventory contains a symlink")
		}
		if entry.Name() != "cgroup.procs" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(data)) != "" {
			return fmt.Errorf("a worker/command scope is alive: %s", path)
		}
		return nil
	})
}

type productOperatorRequest struct {
	Turn, Reason string
	DryRun       bool
}

// A service-owned audit journal must be opened as its owner. SO_PEERCRED on the
// inherited socket authenticates the root front end without trusting CLI claims.
func delegateProductOperator(turn, reason string, dry bool, out, stderr io.Writer) error {
	account, err := productAdminLookupUser(productsetup.ServiceUser)
	if err != nil {
		return err
	}
	uid, uidErr := strconv.ParseUint(account.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(account.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 {
		return adminRefusal("service account IDs are invalid")
	}
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	parent, child := os.NewFile(uintptr(sockets[0]), "root-operator"), os.NewFile(uintptr(sockets[1]), "operator-request")
	defer parent.Close()
	defer child.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "product", "turns", "resolve-worker")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	cmd.ExtraFiles = []*os.File{child}
	cmd.Stdout, cmd.Stderr = out, stderr
	// Set this on the forking thread so every child thread inherits the same
	// scanner privilege profile as the NoNewPrivileges product unit.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("match product scanner privilege profile: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	err = json.NewEncoder(parent).Encode(productOperatorRequest{turn, reason, dry})
	parent.Close()
	waitErr := cmd.Wait()
	if err != nil {
		return err
	}
	if waitErr != nil {
		return adminRefusal("operator evidence check refused (see diagnostic above): %v", waitErr)
	}
	return nil
}

func runProductOperatorWorker(out io.Writer) error {
	socket := os.NewFile(3, "operator-request")
	defer socket.Close()
	peer, err := unix.GetsockoptUcred(3, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || peer.Uid != 0 {
		return adminRefusal("operator request requires an inherited root-authenticated socket")
	}
	var request productOperatorRequest
	if err := json.NewDecoder(io.LimitReader(socket, 4096)).Decode(&request); err != nil {
		return adminRefusal("invalid operator request: %v", err)
	}
	cfg, err := loadProductServiceConfig(productsetup.ServiceConfigPath)
	if err != nil {
		return err
	}
	if err := cfg.validate(nil); err != nil {
		return err
	}
	registration, err := productsession.LoadRegistration(cfg.RegistrationPath, cfg.RegistrationSignaturePath, cfg.RegistrationPublicKeyPath)
	if err != nil {
		return err
	}
	env := &productTurnEnv{Registration: registration, StateRoot: cfg.StateRoot, ProjectRoots: cfg.Projects,
		OpenCommandHistory: openProductD09CommandHistory, StorePolicyDigest: digestBytes([]byte(gate.ProductionSessionPolicyLabel)), MetadataPolicyDigest: digestBytes([]byte(gate.ProductionMetadataPolicyLabel))}
	if _, err := installProductGatePolicies(env, cfg); err != nil {
		return err
	}
	if env.Xattr, err = productHostXattr(cfg, env); err != nil {
		return err
	}
	turn, err := productsession.OperatorResolve(cfg.CatalogPath, registration, request.Turn, request.Reason, request.DryRun,
		func(turn productsession.Turn) (productsession.OperatorEvidence, error) {
			return env.operatorEvidence(context.Background(), turn)
		})
	if err != nil {
		return adminRefusal("operator resolution refused: %v", err)
	}
	if request.DryRun {
		fmt.Fprintf(out, "dry-run: %s can be abandoned; baseline unchanged; full reservation charged; no records written\n", turn.ID)
	} else {
		fmt.Fprintf(out, "resolved %s: abandoned (operator_resolved); baseline unchanged; full reservation charged; session and project unblocked\n", turn.ID)
	}
	return nil
}

func (e *productTurnEnv) operatorEvidence(ctx context.Context, turn productsession.Turn) (evidence productsession.OperatorEvidence, err error) {
	if turn.Link == nil {
		return evidence, errors.New("turn has no workflow link")
	}
	paths, err := productTurnPathsFromLink(*turn.Link)
	if err != nil {
		return evidence, err
	}
	if filepath.Dir(paths.StoreJournalPath) != filepath.Join(e.StateRoot, "turns", turn.Link.WorkflowID) {
		return evidence, errors.New("turn journals are outside the registered state root")
	}
	retained, err := openRetainedProductTurn(paths, e.storeOptions(turn), e.OpenCommandHistory)
	ownerHead := ""
	if errors.Is(err, errHistoricalBashContext) {
		original := err
		defer func() {
			if err != nil {
				err = errors.Join(original, err)
			}
		}()
		retained, ownerHead, err = openInterruptedProductBash(ctx, paths, e.Registration.Digest, e.storeOptions(turn))
	}
	if err != nil {
		return evidence, fmt.Errorf("retained Store/journals: %w", err)
	}
	root, err := e.openProjectRoot(turn.ProjectID)
	if err != nil {
		return evidence, errors.Join(err, retained.Close())
	}
	closeEvidence := func() error { return errors.Join(root.Close(), retained.Close()) }
	// On success the evidence owns the handles until OperatorResolve has appended.
	defer func() {
		if err != nil {
			err = errors.Join(err, closeEvidence())
		}
	}()
	var bundle sessionrepo.EvidenceBundle
	if retained.Store != nil {
		bundle, err = retained.Store.EvidenceBundle()
	}
	if err != nil {
		return evidence, err
	}
	checkpoint, err := loadRetainedProductCheckpoint(ctx, filepath.Dir(paths.StoreJournalPath), turn.Link.WorkflowID)
	if err != nil {
		return evidence, fmt.Errorf("protected checkpoint: %w", err)
	}
	if err := productOperatorCheckpointBindings(turn, paths, retained, checkpoint.Pins); err != nil {
		return evidence, err
	}
	// An abandoned provider request cannot execute after its sole supervisor died.
	// Keep every effect check; only provider-side pending state is allowed to be inert.
	pins := checkpoint.Pins
	pins.HostCallReservation = nil
	pins.PendingCallDisposition = ""
	if err := productTurnReservationsResolved(pins); err != nil {
		return evidence, fmt.Errorf("Store/workflow is not inert: %w", err)
	}
	if retained.Store != nil {
		if err := productTurnChildResolved(bundle, pins); err != nil {
			return evidence, fmt.Errorf("Store/workflow is not inert: %w", err)
		}
	}
	command, err := retained.CommandJournal.Trace()
	if err != nil {
		return evidence, err
	}
	if len(command.Records) != 0 && ownerHead == "" {
		return evidence, errors.New("command activity exists; command-scope recovery is not supported")
	}
	if named, _, err := e.workflowPublication(turn.ProjectID, turn.Link.WorkflowID); err != nil || named {
		return evidence, errors.Join(errors.New("publication evidence is present or unreadable"), err)
	}
	measured, err := measureProductBaseline(turn.ProjectID, root, e.projectWorkspace())
	if err != nil {
		return evidence, fmt.Errorf("baseline scan: %w", err)
	}
	if measured != turn.BaselineDigest {
		return evidence, errProductStaleBaseline
	}
	head := func(j *audit.Journal) (string, error) {
		trace, err := j.Trace()
		if err != nil {
			return "", err
		}
		if len(trace.Records) == 0 {
			return digestBytes(nil), nil
		}
		return "sha256:" + auditHeadHash(trace.Records), nil
	}
	evidence = productsession.OperatorEvidence{WorkflowID: turn.Link.WorkflowID, BaselineDigest: turn.BaselineDigest, ScanDigest: measured, CheckpointDigest: "sha256:" + checkpoint.ComputedCheckpointDigest,
		// OperatorResolve calls Confirm while the retained journals and this project
		// directory are still open, directly before the catalog append.
		Confirm: func() error {
			second, err := measureProductBaseline(turn.ProjectID, root, e.projectWorkspace())
			if err != nil {
				return fmt.Errorf("baseline rescan: %w", err)
			}
			if second != measured || second != turn.BaselineDigest {
				return fmt.Errorf("%w: project changed before the resolution record was appended", errProductStaleBaseline)
			}
			return nil
		},
		Close: closeEvidence}
	if evidence.StoreHead, err = head(retained.StoreJournal); err != nil {
		return evidence, err
	}
	if evidence.ProviderHead, err = head(retained.ProviderJournal); err != nil {
		return evidence, err
	}
	evidence.CommandOwnerHead = ownerHead
	if evidence.CommandHead, err = head(retained.CommandJournal); err != nil {
		return evidence, err
	}
	return evidence, nil
}

func productOperatorCheckpointBindings(turn productsession.Turn, paths productTurnPaths, retained *retainedProductTurn, pins workflowrecovery.Pins) error {
	if pins.SessionID != turn.Link.SessionID || pins.PromptDigest != bareWorkflowDigest(turn.PromptDigest) ||
		pins.StoreBaselineTreeDigest != bareWorkflowDigest(turn.BaselineDigest) || pins.PolicyDigest != bareWorkflowDigest(turn.PolicyDigest) ||
		pins.ImportedPrefixDigest != bareWorkflowDigest(turn.Link.ImportedPrefixDigest) || pins.PredecessorTurnReceiptDigest != bareWorkflowDigest(turn.PredecessorReceiptDigest) {
		return errors.New("protected checkpoint differs from catalog turn bindings")
	}
	root, err := os.Open(paths.StoreRoot)
	if err != nil {
		return err
	}
	identity, err := recoveryDirectoryIdentityDigest(root)
	closeErr := root.Close()
	if err != nil || closeErr != nil || identity != pins.StoreIdentityDigest {
		return errors.Join(errors.New("Store identity differs from protected checkpoint"), err, closeErr)
	}
	commandIdentity, err := recoveryFileIdentityDigest(paths.CommandJournalPath, false)
	if err != nil || commandIdentity != pins.CommandIdentityDigest {
		return errors.Join(errors.New("command journal identity differs from protected checkpoint"), err)
	}
	prefix := func(j *audit.Journal, count uint64, head string) error {
		trace, err := j.Trace()
		if err != nil {
			return err
		}
		if count > uint64(len(trace.Records)) || count == 0 && head != "" || count > 0 && trace.Records[count-1].Hash != head {
			return errors.New("retained journal differs from protected checkpoint prefix")
		}
		return nil
	}
	if err := prefix(retained.StoreJournal, pins.StoreJournalCount, pins.StoreJournalHead); err != nil {
		return err
	}
	if len(pins.ProviderTranscripts) != 1 || pins.ProviderTranscripts[0].ConversationID != turn.Link.ConversationID {
		return errors.New("protected checkpoint does not name exactly this provider conversation")
	}
	transcript := pins.ProviderTranscripts[0]
	providerTrace, err := retained.ProviderJournal.Trace()
	if err != nil {
		return err
	}
	if len(providerTrace.Records) == 0 {
		return errors.New("retained provider journal is empty; turn evidence cannot be established")
	}
	providerIdentity, err := recoveryFileIdentityDigest(paths.ProviderJournalPath, false)
	if err != nil || providerIdentity != transcript.JournalIdentityDigest {
		return errors.Join(errors.New("provider journal identity differs from protected checkpoint"), err)
	}
	return prefix(retained.ProviderJournal, transcript.JournalCount, transcript.JournalHead)
}
