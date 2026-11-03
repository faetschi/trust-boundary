//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	productQualificationEvidenceFile   = "evidence.json"
	productQualificationEvidenceSchema = "tbound-product-qualification-evidence/v1"
	productQualificationSeedFile       = "qualification-seed.txt"
	productQualificationOutputFile     = "qualification-output.txt"
	// The qualification Store has its own policy identities, so no turn's Store or evidence can be mistaken for it.
	productQualificationStorePolicy    = "tbound-product-qualification-store-policy/v1"
	productQualificationMetadataPolicy = "tbound-product-qualification-metadata-policy/v1"
	productQualificationCallIssuer     = "tbound-product-qualification/v1"
	productQualificationCommandTimeout = 90 * time.Second
	productQualificationSettleTimeout  = 30 * time.Second
	productQualificationEvidenceBound  = 1 << 20
)

// The fixed commands a start runs in the signed base cell. They are shell text for /bin/bash -lc, the argv the
// product's Bash tool maps to, and take nothing from a client or a model.
const (
	productQualificationProofScript    = `printf 'qualification-proof-ack\n'`
	productQualificationListScript     = `ls -1A`
	productQualificationWritePrefix    = `printf '%s\n' `
	productQualificationReadbackScript = `cat ` + productQualificationOutputFile
	productQualificationRootfsScript   = `for target in /qualification-rootfs-probe /etc/qualification-rootfs-probe /usr/qualification-rootfs-probe; do
  if { : > "$target"; } 2>/dev/null; then printf 'rootfs-write-allowed %s\n' "$target"; exit 3; fi
  if [ -e "$target" ]; then printf 'rootfs-file-appeared %s\n' "$target"; exit 4; fi
done
printf 'rootfs-write-denied\n'`
	productQualificationLimitsScript = `printf 'uid_map=%s\n' "$(cat /proc/self/uid_map)"
printf 'gid_map=%s\n' "$(cat /proc/self/gid_map)"
printf 'id=%s:%s\n' "$(id -u)" "$(id -g)"`
)

func productQualificationWriteScript(token string) string {
	return productQualificationWritePrefix + token + " > " + productQualificationOutputFile
}

// productQualificationNodeScript runs the probe program through the image's own Node, named by the signed host profile.
func productQualificationNodeScript(node, program string) string {
	return "exec " + productShellQuote(node) + " -e " + productShellQuote(program)
}

func productShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// productQualificationTarget is what a qualification run needs from the verified command host.
type productQualificationTarget struct {
	Profile          piruntime.HostProfile
	Template         piruntime.D09CellTemplate
	ProviderHostname string
}

// productQualificationNetwork holds the service-side controls of the network probe. Tests replace them to run offline.
var productQualificationNetwork = struct {
	listener productNetworkControl
	connect  productNetworkConnect
	resolve  productNetworkResolve
}{dialProductListener, connectProductDestination, net.DefaultResolver.LookupIPAddr}

func dialProductListener(ctx context.Context, address string) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

// connectProductDestination performs the provider TCP positive control from the service context.
func connectProductDestination(ctx context.Context, network, address string) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return err
	}
	return conn.Close()
}

type productQualificationPhaseResult struct {
	Phase  productQualificationPhase `json:"phase"`
	Status string                    `json:"status"`
	Detail string                    `json:"detail,omitempty"`
}

type productQualificationCommandRecord struct {
	Phase            productQualificationPhase `json:"phase"`
	Name             string                    `json:"name"`
	LeaseID          string                    `json:"lease_id"`
	ViewID           string                    `json:"view_id"`
	ExitCode         int                       `json:"exit_code"`
	StdoutDigest     string                    `json:"stdout_digest"`
	ReceiptDigest    string                    `json:"receipt_digest"`
	InputGeneration  string                    `json:"input_generation"`
	OutputGeneration string                    `json:"output_generation"`
	Changed          []string                  `json:"changed"`
}

// productQualificationEvidence is the diagnostic record of one start's qualification. It is retained whether the run
// passed or failed, and grants nothing: the next start qualifies again.
type productQualificationEvidence struct {
	Schema        string                              `json:"schema"`
	RunID         string                              `json:"run_id"`
	Status        string                              `json:"status"`
	Failure       string                              `json:"failure,omitempty"`
	StartedAt     string                              `json:"started_at"`
	FinishedAt    string                              `json:"finished_at"`
	ProfileDigest string                              `json:"profile_digest"`
	ImageDigest   string                              `json:"image_digest"`
	Phases        []productQualificationPhaseResult   `json:"phases"`
	Commands      []productQualificationCommandRecord `json:"commands"`
	Limits        *piruntime.D09ProductCommandLimits  `json:"limits,omitempty"`
	Network       *productNetworkReport               `json:"network,omitempty"`
}

// productQualificationCell is one disposable qualification run. Production opens it over the exact per-turn runner;
// a test of the startup order replaces it.
type productQualificationCell interface {
	runPhase(ctx context.Context, phase productQualificationPhase) error
	// finish settles what the run owns, retains its evidence and closes its handles. cause is the failure that ended
	// the run, or nil.
	finish(ctx context.Context, cause error) error
}

type productQualificationRunner interface {
	productTurnCommandRunner
	productCommandLifecycle
	productionBashEvidenceBinder
}

type productQualificationRun struct {
	target           productQualificationTarget
	env              *productTurnEnv
	runner           productQualificationRunner
	owner            *productTurnWorkflow
	store            *sessionrepo.Store
	tip              *sessionrepo.Generation
	dir, runID       string
	storeJournal     *audit.Journal
	commandJournal   *audit.Journal
	next             int
	expectLease      string
	expectDecision   string
	expectScope      []string
	evidence         productQualificationEvidence
	settled, closed  bool
	settlementFailed bool
}

type productQualificationCommand struct {
	record           productQualificationCommandRecord
	stdout, stderr   []byte
	receipt          piruntime.D09CommandReceipt
	changed          []string
	outputGeneration string
}

// openProductQualificationCell builds the private Store, view source and journals under
// <state>/qualification/<run-id>, and the per-turn command runner over them. It runs no command.
func openProductQualificationCell(ctx context.Context, target productQualificationTarget, env *productTurnEnv,
	newRunner func(productTurnCommandContext) (productTurnCommandRunner, error)) (_ productQualificationCell, err error) {
	if env == nil || env.StateRoot == "" || newRunner == nil || !filepath.IsAbs(target.Template.CommandExecutable) ||
		!filepath.IsAbs(target.Profile.ChildExecutable) {
		return nil, errors.New("the qualification cell needs the state root, a command runner and the signed Bash and Node paths")
	}
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	runID := "q-" + time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random[:])
	base := filepath.Join(env.StateRoot, productQualificationDir)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	r := &productQualificationRun{target: target, env: env, runID: runID, dir: filepath.Join(base, runID)}
	r.evidence = productQualificationEvidence{Schema: productQualificationEvidenceSchema, RunID: runID, Status: "failed",
		StartedAt: time.Now().UTC().Format(time.RFC3339), ProfileDigest: target.Profile.Digest, ImageDigest: target.Template.ExpectedImageDigest}
	for _, phase := range productQualificationPhases {
		r.evidence.Phases = append(r.evidence.Phases, productQualificationPhaseResult{Phase: phase, Status: "not-run"})
	}
	storeDir, sourceDir := filepath.Join(r.dir, productTurnStoreDir), filepath.Join(r.dir, "source")
	for _, path := range []string{r.dir, storeDir, sourceDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			return nil, err
		}
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.finish(context.Background(), err))
		}
	}()
	if err := os.WriteFile(filepath.Join(sourceDir, productQualificationSeedFile), []byte("tbound product qualification seed\n"), 0o600); err != nil {
		return nil, err
	}
	if r.storeJournal, err = audit.Open(filepath.Join(r.dir, productTurnStoreJournal)); err != nil {
		return nil, err
	}
	if r.commandJournal, err = audit.Open(filepath.Join(r.dir, productTurnCommandJournal)); err != nil {
		return nil, err
	}
	runner, runnerErr := newRunner(productTurnCommandContext{Journal: r.commandJournal, TurnDir: r.dir, WorkflowID: "qualification-workflow-" + runID,
		RunID: "qualification-run-" + runID, SessionID: "qualification-session-" + runID, ConversationID: "qualification-conversation-" + runID})
	if lifecycle, ok := runner.(productCommandLifecycle); ok && lifecycle != nil {
		r.owner = &productTurnWorkflow{env: env, taskID: runID, commandLifecycle: lifecycle, closers: []func() error{r.release}}
	}
	qualified, ok := runner.(productQualificationRunner)
	if ok && qualified != nil {
		r.runner = qualified
	}
	if runnerErr != nil {

		return nil, runnerErr
	}
	if r.runner == nil {
		return nil, errors.New("the command runner has no lifecycle, evidence binding or lease API")
	}
	storeRoot, err := os.Open(storeDir)
	if err != nil {
		return nil, err
	}
	r.store, err = sessionrepo.Create(storeRoot, sessionrepo.Options{
		PolicyDigest: digestBytes([]byte(productQualificationStorePolicy)), MetadataPolicyDigest: digestBytes([]byte(productQualificationMetadataPolicy)),
		Limits: workspace.DefaultLimits(), XattrVisibility: env.Xattr, Journal: r.storeJournal,
		AuthorizeOperation: r.authorize, VerifyDecision: r.verifyDecision, VerifySettlement: r.runner.VerifySettlement})
	_ = storeRoot.Close()
	if err != nil {
		return nil, err
	}
	source, err := os.Open(sourceDir)
	if err != nil {
		return nil, err
	}
	r.tip, err = r.store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	_ = source.Close()
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (r *productQualificationRun) authorize(request sessionrepo.OperationRequest) error {
	if request.Tool != "bash" || request.Operation.Kind != delta.OperationLease || request.Operation.LeaseID != r.expectLease ||
		request.Decision.ID != r.expectDecision || request.ViewID == "" || request.EffectID == "" ||
		!slices.Equal(request.OutputScopePaths, r.expectScope) {
		return errors.New("the qualification Store refused a command it did not issue")
	}
	return r.runner.RegisterLease(request.ViewID, request.Operation.LeaseID)
}

func (r *productQualificationRun) verifyDecision(decision delta.PolicyDecision, _ delta.TransitionBinding) error {
	if decision.Outcome != delta.PolicyAllow || decision.PolicyDigest != digestBytes([]byte(productQualificationStorePolicy)) ||
		decision.MetadataPolicyDigest != digestBytes([]byte(productQualificationMetadataPolicy)) || !strings.HasPrefix(decision.ID, "decision-") {
		return errors.New("the qualification Store does not recognize this decision")
	}
	return nil
}

func (r *productQualificationRun) identity(domain string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + r.runID + "\x00" + strconv.Itoa(r.next)))
	return domain + "-" + hex.EncodeToString(sum[:])
}

// exec runs one fixed command in a fresh cell through the Store, binds the settled receipt to the Store's operation and
// verifies it the way publication does. A command that exited nonzero is a result, not an error.
func (r *productQualificationRun) exec(ctx context.Context, phase productQualificationPhase, name, script string, outputs []string) (productQualificationCommand, error) {
	var out productQualificationCommand
	r.next++
	leaseID, decisionID := r.identity("lease"), r.identity("decision")
	r.expectLease, r.expectDecision, r.expectScope = leaseID, decisionID, outputs
	spec := sessionrepo.CommandSpec{Executable: r.target.Template.CommandExecutable, Args: []string{"-lc", script},
		DeclaredOutputPaths: slices.Clone(outputs)}
	operation := delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: leaseID}
	decision := delta.PolicyDecision{ID: decisionID, Outcome: delta.PolicyAllow,
		PolicyDigest: digestBytes([]byte(productQualificationStorePolicy)), MetadataPolicyDigest: digestBytes([]byte(productQualificationMetadataPolicy))}
	commandCtx, cancel := context.WithTimeout(ctx, productQualificationCommandTimeout)
	defer cancel()
	input := r.tip
	mutation, err := r.store.RunBash(commandCtx, input, operation, decision, spec, r.runner)
	if err != nil {
		return out, fmt.Errorf("run %s: %w", name, err)
	}
	if mutation.Generation == nil || mutation.Command == nil {
		return out, fmt.Errorf("run %s: the Store returned no settled generation", name)
	}
	r.tip = mutation.Generation
	bundle, err := r.store.EvidenceBundle()
	if err != nil {
		return out, fmt.Errorf("run %s: read the Store evidence: %w", name, err)
	}
	var operationEvidence *sessionrepo.OperationEvidence
	for index := range bundle.Operations {
		if bundle.Operations[index].EffectID == mutation.EffectID {
			operationEvidence = &bundle.Operations[index]
		}
	}
	var transition *delta.Transition
	for index := range bundle.ApprovedDeltaLedger {
		if bundle.ApprovedDeltaLedger[index].ID == mutation.Transition.ID {
			transition = &bundle.ApprovedDeltaLedger[index]
		}
	}
	if operationEvidence == nil || transition == nil {
		return out, fmt.Errorf("run %s: the Store evidence does not hold the command", name)
	}
	providerCall := delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: r.identity("proposal"),
		CallIssuer: productQualificationCallIssuer, CallID: r.identity("call")}
	if err := r.runner.BindProductionBashEvidence(commandCtx, providerCall, bundle, *transition, *operationEvidence); err != nil {
		return out, fmt.Errorf("run %s: bind the command receipt: %w", name, err)
	}
	if err := verifyProductionBashEvidence(commandCtx, r.runner, *operationEvidence); err != nil {
		return out, fmt.Errorf("run %s: verify the command receipt: %w", name, err)
	}
	receipt, err := r.receiptOf(leaseID)
	if err != nil {
		return out, fmt.Errorf("run %s: %w", name, err)
	}
	for _, change := range transition.Changes {
		out.changed = append(out.changed, change.Path)
	}
	out.stdout, out.stderr, out.receipt, out.outputGeneration = mutation.Command.Stdout, mutation.Command.Stderr, receipt, transition.OutputGeneration
	out.record = productQualificationCommandRecord{Phase: phase, Name: name, LeaseID: leaseID, ViewID: transition.ViewID,
		ExitCode: mutation.Command.ExitCode, StdoutDigest: digestBytes(mutation.Command.Stdout), ReceiptDigest: receipt.ReceiptDigest,
		InputGeneration: input.ID(), OutputGeneration: transition.OutputGeneration, Changed: slices.Clone(out.changed)}
	r.evidence.Commands = append(r.evidence.Commands, out.record)
	r.env.crash("qualification-command-settled")
	return out, nil
}

// receiptOf reads the sealed receipt the command journal bound to the lease.
func (r *productQualificationRun) receiptOf(leaseID string) (piruntime.D09CommandReceipt, error) {
	trace, err := r.commandJournal.Trace()
	if err != nil {
		return piruntime.D09CommandReceipt{}, err
	}
	for _, record := range trace.Records {
		if record.Event.Kind != productionD09SettledKind {
			continue
		}
		var event productionD09CommandSettledEvent
		if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil || event.Operation.Operation.LeaseID != leaseID {
			continue
		}
		var receipt piruntime.D09CommandReceipt
		if err := unmarshalGovernedJSON(event.Receipt, &receipt); err != nil {
			return piruntime.D09CommandReceipt{}, err
		}
		if err := receipt.ValidateSnapshot(); err != nil {
			return piruntime.D09CommandReceipt{}, fmt.Errorf("the retained receipt does not validate: %w", err)
		}
		return receipt, nil
	}
	return piruntime.D09CommandReceipt{}, errors.New("the command journal holds no receipt for the lease")
}

func (r *productQualificationRun) runPhase(ctx context.Context, phase productQualificationPhase) error {
	var run func(context.Context) error
	switch phase {
	case productPhaseProofAck:
		run = r.proofAck
	case productPhaseListing:
		run = r.listing
	case productPhaseScopedWrite:
		run = r.scopedWrite
	case productPhaseRootfsDenial:
		run = r.rootfsDenial
	case productPhaseLimitsMapping:
		run = r.limitsMapping
	case productPhaseNetworkProbe:
		run = r.networkProbe
	case productPhaseSettlement:
		run = r.settle
	default:
		return fmt.Errorf("unknown qualification phase %q", phase)
	}
	err := run(ctx)
	for index := range r.evidence.Phases {
		if r.evidence.Phases[index].Phase == phase {
			r.evidence.Phases[index].Status = "passed"
			if err != nil {
				r.evidence.Phases[index].Status, r.evidence.Phases[index].Detail = "failed", err.Error()
			}
		}
	}
	return err
}

// requireCleanExit refuses a command that failed or changed the view when the phase expects neither.
func requireCleanExit(command productQualificationCommand, stdout string) error {
	switch {
	case command.record.ExitCode != 0:
		return fmt.Errorf("%s exited %d: %s", command.record.Name, command.record.ExitCode, strings.TrimSpace(string(command.stderr)))
	case string(command.stdout) != stdout:
		return fmt.Errorf("%s printed %q, want %q", command.record.Name, command.stdout, stdout)
	case len(command.changed) != 0:
		return fmt.Errorf("%s changed %v in a read-only phase", command.record.Name, command.changed)
	}
	return nil
}

// proofAck runs the smallest command and holds the cell's own proof to the signed template: the entrypoint was ready
// before the command started, and the command ran as the registered identity with no capability.
func (r *productQualificationRun) proofAck(ctx context.Context) error {
	command, err := r.exec(ctx, productPhaseProofAck, "proof-ack", productQualificationProofScript, []string{})
	if err != nil {
		return err
	}
	if err := requireCleanExit(command, "qualification-proof-ack\n"); err != nil {
		return err
	}
	proof, template := command.receipt.Proof, r.target.Template
	switch {
	case !proof.CommandReady || !proof.CommandStarted || !proof.CommandExitObserved:
		return errors.New("the entrypoint proof does not show ready, started and exited")
	case proof.UID != template.CellUID || proof.GID != template.CellGID || proof.CapEff != "0000000000000000" || !proof.NoNewPrivs ||
		proof.Seccomp != 2 || !proof.CgroupV2 || !proof.LandlockTSYNC:
		return errors.New("the entrypoint proof differs from the signed credentials, capabilities, seccomp or Landlock profile")
	case proof.ImageDigest != template.ExpectedImageDigest || proof.CommandPath != template.CommandExecutable:
		return errors.New("the entrypoint proof names another image or executable than the signed template")
	}
	return nil
}

func (r *productQualificationRun) listing(ctx context.Context) error {
	command, err := r.exec(ctx, productPhaseListing, "ls", productQualificationListScript, []string{})
	if err != nil {
		return err
	}
	if command.record.ExitCode != 0 || len(command.changed) != 0 || !slices.Contains(strings.Fields(string(command.stdout)), productQualificationSeedFile) {
		return fmt.Errorf("ls printed %q, which does not list %s", command.stdout, productQualificationSeedFile)
	}
	return nil
}

func (r *productQualificationRun) scopedWrite(ctx context.Context) error {
	token := r.runID
	written, err := r.exec(ctx, productPhaseScopedWrite, "write", productQualificationWriteScript(token), []string{productQualificationOutputFile})
	if err != nil {
		return err
	}
	if written.record.ExitCode != 0 || !slices.Equal(written.changed, []string{productQualificationOutputFile}) {
		return fmt.Errorf("the scoped write exited %d and changed %v, want exactly %s", written.record.ExitCode, written.changed, productQualificationOutputFile)
	}
	readback, err := r.exec(ctx, productPhaseScopedWrite, "readback", productQualificationReadbackScript, []string{})
	if err != nil {
		return err
	}
	return requireCleanExit(readback, token+"\n")
}

func (r *productQualificationRun) rootfsDenial(ctx context.Context) error {
	command, err := r.exec(ctx, productPhaseRootfsDenial, "rootfs", productQualificationRootfsScript, []string{})
	if err != nil {
		return err
	}
	return requireCleanExit(command, "rootfs-write-denied\n")
}

// limitsMapping holds the cell to the signed template's one-ID mapping (from its proof and from inside) and to the
// live limits observed by the supervisor through its held cgroup descriptor; Landlock denies in-cell cgroup reads.
func (r *productQualificationRun) limitsMapping(ctx context.Context) error {
	command, err := r.exec(ctx, productPhaseLimitsMapping, "limits", productQualificationLimitsScript, []string{})
	if err != nil {
		return err
	}
	if command.record.ExitCode != 0 || len(command.changed) != 0 {
		return fmt.Errorf("the limits probe exited %d and changed %v", command.record.ExitCode, command.changed)
	}
	template := r.target.Template
	proof := command.receipt.Proof
	if err := piruntime.VerifyD09InsideIDMaps(template.UserNamespaceMappingProfile, template.UserNamespaceMappingDigest, template.CellUID, template.CellGID,
		os.Geteuid(), os.Getegid(), []byte(proof.UIDMap), []byte(proof.GIDMap)); err != nil {
		return fmt.Errorf("the proof's ID maps differ from the signed single-ID mapping: %w", err)
	}
	observed := map[string]string{}
	for _, line := range strings.Split(string(command.stdout), "\n") {
		if key, value, found := strings.Cut(line, "="); found {
			observed[key] = value
		}
	}
	observer, ok := r.runner.(interface {
		productCommandLimits(string) (piruntime.D09ProductCommandLimits, error)
	})
	if !ok {
		return errors.New("no supervisor cgroup observer")
	}
	limits, err := observer.productCommandLimits(command.record.LeaseID)
	if err != nil {
		return err
	}
	r.evidence.Limits = &limits
	for key, pair := range map[string][2]string{"pids.max": {limits.PidsMax, strconv.Itoa(template.PidsMax)}, "memory.max": {limits.MemoryMax, strconv.FormatInt(int64(template.MemoryMB)<<20, 10)}} {
		if pair[0] != pair[1] {
			return fmt.Errorf("live cgroup %s=%q, signed template sets %q", key, pair[0], pair[1])
		}
	}
	want := map[string]string{"id": fmt.Sprintf("%d:%d", template.CellUID, template.CellGID)}
	for key, value := range want {
		if observed[key] != value {
			return fmt.Errorf("the cell reports %s=%q, the signed template sets %q", key, observed[key], value)
		}
	}
	for key, inside := range map[string]string{"uid_map": piruntime.D09InsideUIDMap(template.CellUID), "gid_map": piruntime.D09InsideGIDMap(template.CellGID)} {
		if strings.Join(strings.Fields(observed[key]), " ") != inside {
			return fmt.Errorf("the cell reports %s %q, the signed mapping is %q", key, observed[key], inside)
		}
	}
	return nil
}

// networkProbe runs productNetworkProbe against the cell. The listener is opened here, in the service context, so the
// cell can only reach it through a shared network namespace.
func (r *productQualificationRun) networkProbe(ctx context.Context) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("open the probe listener: %w", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	run := func(ctx context.Context, program string) ([]byte, error) {
		command, err := r.exec(ctx, productPhaseNetworkProbe, "network-probe",
			productQualificationNodeScript(r.target.Profile.ChildExecutable, program), []string{})
		if err != nil {
			return nil, err
		}
		if command.record.ExitCode != 0 || len(command.changed) != 0 {
			return nil, fmt.Errorf("%s exited %d: %s", r.target.Profile.ChildExecutable, command.record.ExitCode, strings.TrimSpace(string(command.stderr)))
		}
		return command.stdout, nil
	}
	report, err := productNetworkProbe(ctx, productNetworkPolicyDenyAll, listener.Addr().String(), r.target.ProviderHostname, run,
		productQualificationNetwork.listener, productQualificationNetwork.connect, productQualificationNetwork.resolve)
	r.evidence.Network = &report
	return err
}

// settle closes the run's ownership: the lifecycle drains the scope and launcher, every command's receipt is bound and
// verified, and the Store's chain reconciles.
func (r *productQualificationRun) settle(ctx context.Context) error {
	r.env.crash("qualification-before-settle")
	bundle, err := r.store.EvidenceBundle()
	if err != nil {
		return err
	}
	if len(bundle.Operations) != len(r.evidence.Commands) {
		return fmt.Errorf("the Store holds %d operations for %d commands", len(bundle.Operations), len(r.evidence.Commands))
	}
	for _, operation := range bundle.Operations {
		if err := verifyProductionBashEvidence(ctx, r.runner, operation); err != nil {
			return err
		}
	}
	if _, err := r.store.Verify(); err != nil {
		return fmt.Errorf("the qualification Store does not verify: %w", err)
	}
	if err := r.owner.settleCommands(); err != nil {
		r.settlementFailed = true
		return fmt.Errorf("settle the command scope: %w", err)
	}
	r.settled = true
	if pending := r.runner.Pending(); pending != 0 {
		return fmt.Errorf("%d command owners remain after settlement", pending)
	}
	return nil
}

func (r *productQualificationRun) finish(ctx context.Context, cause error) error {
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), productQualificationSettleTimeout)
		defer cancel()
	}
	var failures []error
	if r.settlementFailed {
		failures = append(failures, piruntime.ErrUnknownSettlement)
	} else if !r.settled && r.owner != nil {
		if err := r.owner.settleCommands(); err != nil {
			r.settlementFailed = true
			failures = append(failures, fmt.Errorf("settle the qualification cell: %w", err))
		} else {
			r.settled = true
		}
	}
	r.evidence.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if cause == nil && len(failures) == 0 {
		r.evidence.Status = "passed"
	} else if cause != nil {
		r.evidence.Failure = cause.Error()
	} else {
		r.evidence.Failure = errors.Join(failures...).Error()
	}
	if err := r.writeEvidence(); err != nil {
		failures = append(failures, fmt.Errorf("retain the qualification evidence: %w", err))
	}
	var closeErr error
	if r.owner != nil {
		closeErr = r.owner.closeUnlessRetained()
	} else {
		closeErr = r.release()
	}
	if cause == nil && len(failures) == 0 && closeErr == nil {
		closeErr = pruneProductQualifications(ctx, filepath.Dir(r.dir))
	}
	return errors.Join(append(failures, closeErr)...)
}

// release closes the handles this run opened. The runner closes the owner journal, scope and launcher itself, and only
// after they are settled.
func (r *productQualificationRun) release() error {
	if r.closed {
		return nil
	}
	r.closed = true
	var errs []error
	if r.store != nil {
		errs = append(errs, r.store.Close())
	}
	for _, journal := range []*audit.Journal{r.storeJournal, r.commandJournal} {
		if journal != nil {
			errs = append(errs, journal.Close())
		}
	}
	return errors.Join(errs...)
}

func (r *productQualificationRun) writeEvidence() error {
	encoded, err := json.MarshalIndent(r.evidence, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(r.dir, productQualificationEvidenceFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(encoded, '\n'))
	syncErr := file.Sync()
	return errors.Join(writeErr, syncErr, file.Close())
}

// productQualificationEvidenceCheck is the doctor's advisory summary of the latest retained qualification. It reports
// what the last start recorded and never that Bash is available now: every start qualifies again.
func productQualificationEvidenceCheck(stateDir string) productsetup.Check {
	check := productsetup.Check{Name: "command qualification", Status: productsetup.Warn}
	base := filepath.Join(stateDir, productQualificationDir)
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		check.Detail = "no qualification evidence: no Bash-enabled start has run on this host"
		return check
	}
	if err != nil {
		check.Detail = "the qualification evidence is unreadable: " + err.Error()
		return check
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	for index := len(names) - 1; index >= 0; index-- {
		file, err := os.Open(filepath.Join(base, names[index], productQualificationEvidenceFile))
		if err != nil {
			continue // a run killed before it retained evidence leaves none
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, productQualificationEvidenceBound))
		_ = file.Close()
		var evidence productQualificationEvidence
		if readErr != nil || json.Unmarshal(raw, &evidence) != nil || evidence.Schema != productQualificationEvidenceSchema {
			check.Detail = "the latest qualification evidence " + names[index] + " is unreadable"
			return check
		}
		passed := 0
		for _, phase := range evidence.Phases {
			if phase.Status == "passed" {
				passed++
			}
		}
		node := ""
		if evidence.Network != nil {
			node = ", Node " + evidence.Network.Node
		}
		summary := fmt.Sprintf("run %s finished %s: %d of %d phases passed%s", evidence.RunID, evidence.FinishedAt, passed, len(evidence.Phases), node)
		if evidence.Status == "passed" {
			check.Status = productsetup.Pass
			check.Detail = summary + "; advisory only, every start qualifies again and the self-test alone establishes readiness"
			return check
		}
		check.Detail = summary + "; failed: " + evidence.Failure
		check.Fix = "read " + filepath.Join(base, names[index], productQualificationEvidenceFile) + ", fix the named prerequisite and restart the service"
		return check
	}
	check.Detail = "no qualification run has retained evidence"
	return check
}

// productErrnoName names a failed connection's errno for the retained evidence.
func productErrnoName(err error) string {
	var errno unix.Errno
	if errors.As(err, &errno) {
		if name := unix.ErrnoName(errno); name != "" {
			return name
		}
	}
	return ""
}

// pruneProductQualifications removes only completed, durably drained disposable runs, keeping the newest five.
func pruneProductQualifications(ctx context.Context, base string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	type completed struct {
		path     string
		modified time.Time
	}
	var runs []completed
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(base, entry.Name())
		raw, err := os.ReadFile(filepath.Join(path, productQualificationEvidenceFile))
		if err != nil {
			continue
		}
		var evidence productQualificationEvidence
		if json.Unmarshal(raw, &evidence) != nil || evidence.Schema != productQualificationEvidenceSchema || evidence.FinishedAt == "" {
			continue
		}
		journal, err := openExistingRecoveryJournal(filepath.Join(path, productCommandOwnerJournal))
		if err != nil {
			continue
		}
		trace, readErr := journal.Trace()
		closeErr := journal.Close()
		if readErr != nil || closeErr != nil || len(trace.Records) < 2 {
			continue
		}
		last := trace.Records[len(trace.Records)-1].Event
		first := trace.Records[0].Event
		var opened, drained productD09ScopeOwner
		if first.Kind != "product_command_scope_opened" || last.Kind != "product_command_scope_drained" || json.Unmarshal(first.Data, &opened) != nil || json.Unmarshal(last.Data, &drained) != nil {
			continue
		}
		drained.State = "opened"
		if drained != opened || opened.CommandOwnershipVersion != 1 || opened.State != "opened" || first.ID != opened.ScopeIdentity || last.ID != opened.ScopeIdentity {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		runs = append(runs, completed{path, info.ModTime()})
	}
	slices.SortFunc(runs, func(a, b completed) int { return b.modified.Compare(a.modified) })
	for i := 5; i < len(runs); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.RemoveAll(runs[i].path); err != nil {
			return err
		}
	}
	return nil
}
