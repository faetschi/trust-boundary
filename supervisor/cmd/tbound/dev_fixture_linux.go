//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// The explicit development fixture route launches the actual pinned Pi SDK
// worker over the real inherited-FD channels and the real provider conversation
// bridge. It is deliberately non-claim-bearing: the provider transport is a
// scripted offline doer, the session repository uses non-claiming development
// callbacks, containment is not established, and settlement is UNKNOWN. The
// Bash step prefers the reviewed sandbox-backed development runner and falls
// back to a bounded, explicitly non-contained runner only when the development
// sandbox cannot be established (see dev_fixture_bash_linux.go). The route never
// reaches for provider credentials, the network, host containment, or the
// production serve --pi admission path, and it does not publish.
const (
	devFixtureNodeEnv        = "TBOUND_DEV_FIXTURE_NODE"
	devFixtureAdapterEnv     = "TBOUND_DEV_FIXTURE_ADAPTER"
	devFixtureNodeModulesEnv = "TBOUND_DEV_FIXTURE_NODE_MODULES"
	devFixtureRootEnv        = "TBOUND_DEV_FIXTURE_ROOT"

	devFixtureSessionID      = "dev-fixture-session"
	devFixtureWorkflowID     = "dev-fixture-workflow"
	devFixtureConversationID = "dev-fixture-conversation"
	devFixtureProfileID      = "dev-fixture-offline-profile"
	devFixtureTaskID         = "dev-fixture-task"
	devFixtureRequestID      = "dev-fixture-request-1"

	devFixtureCallIssuer     = governedCallIssuer
	devFixtureStorePolicy    = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	devFixtureMetadataPolicy = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	devFixtureXattrPolicy    = "sha256:6666666666666666666666666666666666666666666666666666666666666666"

	devFixturePrompt = "Use the read, edit, bash, and read proxy tools on task.txt, then summarize."

	devFixtureReceiptMaxBytes = 64 << 10
	devFixtureLineageLimit    = 16
)

// developmentFixtureTurns is the fixed offline provider script. It drives the
// real Pi SDK through the E05-shaped four proxy-tool turns
// (read(g0) -> edit(g1) -> bash(g2) -> read(g2)) and one final assistant text
// turn. The arguments are exactly what the durable session repository will
// apply to its synthetic source. The bash command mutates the private command
// view so the promoted g2 differs from g1 and the final read observes it.
var developmentFixtureTurns = []providerbridge.DevelopmentProviderTurn{
	{ResponseID: "dev-response-read-g0", CallID: "dev-call-read-g0", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	{ResponseID: "dev-response-edit-g0", CallID: "dev-call-edit-g0", Tool: "edit", Arguments: json.RawMessage(`{"path":"task.txt","edits":[{"oldText":"old\n","newText":"new\n"}]}`)},
	{ResponseID: "dev-response-bash-g1", CallID: "dev-call-bash-g1", Tool: "bash", Arguments: json.RawMessage(`{"command":"umask 022; printf bash > task.txt"}`)},
	{ResponseID: "dev-response-read-g2", CallID: "dev-call-read-g2", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
	{ResponseID: "dev-response-final", Text: "The offline durable development fixture is complete."},
}

type developmentFixtureConfig struct {
	nodePath       string
	adapterRoot    string
	dependencyRoot string
	privateRoot    string
}

type developmentFixtureReceipt struct {
	Mode              string                         `json:"mode"`
	ClaimBearing      bool                           `json:"claim_bearing"`
	ProviderExchange  bool                           `json:"provider_exchange"`
	Containment       string                         `json:"containment"`
	Settlement        string                         `json:"settlement"`
	Publication       string                         `json:"publication"`
	WorkerReady       bool                           `json:"worker_ready"`
	PromptAdmitted    bool                           `json:"prompt_admitted"`
	TurnCompleted     bool                           `json:"turn_completed"`
	ProposalCount     uint64                         `json:"proposal_count"`
	ProviderTurns     int                            `json:"provider_turns"`
	ToolCallCount     int                            `json:"tool_call_count"`
	ToolResultCount   int                            `json:"tool_result_count"`
	InitialGeneration string                         `json:"initial_generation"`
	FinalGeneration   string                         `json:"final_generation"`
	Generations       []string                       `json:"generations"`
	ToolLineage       []developmentFixtureResult     `json:"tool_lineage"`
	BashSettlement    *developmentFixtureBashSummary `json:"bash_settlement,omitempty"`
}

type developmentFixtureResult struct {
	Sequence       uint64 `json:"sequence"`
	Tool           string `json:"tool"`
	ToolCallID     string `json:"tool_call_id"`
	GenerationFrom string `json:"generation_from"`
	GenerationTo   string `json:"generation_to"`
	TransitionID   string `json:"transition_id,omitempty"`
	EffectID       string `json:"effect_id,omitempty"`
}

// developmentFixtureBashSummary is the observed settlement of the one Bash step
// the development workflow performs. It is bounded to the exact fields the
// durable result exposes: the reported exit status, the fixed
// "not-established" containment status, and the bounded development runner's
// descriptive profile label. It makes no containment claim.
type developmentFixtureBashSummary struct {
	ToolCallID               string `json:"tool_call_id"`
	GenerationTo             string `json:"generation_to"`
	ExitCode                 int    `json:"exit_code"`
	ExitObserved             bool   `json:"exit_observed"`
	CommandContainmentStatus string `json:"command_containment_status"`
	CommandRunnerProfile     string `json:"command_runner_profile"`
}

// runDevelopmentPiFixture is the explicit, non-claim-bearing development launch
// route. It requires four environment variables (Node binary, adapter source
// root, pinned dependency root, private mode-0700 root) and refuses if any
// is absent; it never falls back to the Go-only fixture or a stock Pi CLI.
func runDevelopmentPiFixture(ctx context.Context, output io.Writer) error {
	if ctx == nil {
		return errors.New("development fixture context is required")
	}
	config, err := loadDevelopmentFixtureConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	base, err := os.MkdirTemp(config.privateRoot, ".dev-fixture-")
	if err != nil {
		return fmt.Errorf("create development fixture root: %w", err)
	}
	defer os.RemoveAll(base)
	if err := os.Chmod(base, 0o700); err != nil {
		return err
	}
	workerHome := filepath.Join(base, "worker-home")
	workerTemp := filepath.Join(base, "worker-tmp")
	workingDirectory := filepath.Join(base, "session-root")
	for _, directory := range []string{workerHome, workerTemp, workingDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return err
		}
	}
	bundleRoot, err := piruntime.StageDevelopmentPiWorkerBundle(base, config.adapterRoot, config.dependencyRoot)
	if err != nil {
		return err
	}
	nodeVersion, err := piruntime.DevelopmentNodeVersion(config.nodePath, workerHome, workerTemp)
	if err != nil {
		return err
	}
	if nodeVersion != piruntime.DevelopmentPiNodeVersion {
		return fmt.Errorf("development fixture requires pinned Node %s; found %q", piruntime.DevelopmentPiNodeVersion, nodeVersion)
	}

	workingHandle, err := os.Open(workingDirectory)
	if err != nil {
		return err
	}
	defer workingHandle.Close()
	bundleHandle, err := os.Open(bundleRoot)
	if err != nil {
		return err
	}
	defer bundleHandle.Close()
	worker, err := piruntime.StartDevelopmentPiSDKWorker(ctx, piruntime.DeveloperPiWorkerPlan{
		NodePath: config.nodePath, NodeVersion: nodeVersion,
		BundleRoot: bundleRoot, DependencyRoot: config.dependencyRoot, BundleHandle: bundleHandle,
		WorkerPath: filepath.Join(bundleRoot, "src", "governed-pi-worker.ts"),
		WorkingDir: workingDirectory, WorkingHandle: workingHandle,
		Home: workerHome, TempDir: workerTemp,
		SessionID: devFixtureSessionID, WorkflowID: devFixtureWorkflowID, ConversationID: devFixtureConversationID,
	})
	if err != nil {
		return fmt.Errorf("launch actual pinned Pi SDK worker: %w", err)
	}
	var settlement piruntime.Settlement
	workerClosed := false
	closeWorker := func() piruntime.Settlement {
		if workerClosed {
			return settlement
		}
		workerClosed = true
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelClose()
		settlement, _ = worker.Close(closeCtx)
		return settlement
	}
	defer func() { _ = closeWorker() }()

	commands := newDevelopmentFixtureCommandRunner()
	_, durable, _, providerJournal, cleanup, err := setupDevelopmentFixtureStore(base, commands)
	if err != nil {
		return err
	}
	defer cleanup()
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow},
		{Tool: "edit", Effect: gate.EffectAllow},
		{Tool: "bash", Effect: gate.EffectAllow},
	}})
	if err != nil {
		return err
	}
	initial := durable.Tip()
	conversation, err := providerbridge.NewDevelopmentOfflineConversation(providerbridge.Config{
		ConversationID: devFixtureConversationID, WorkflowID: devFixtureWorkflowID, ProfileID: devFixtureProfileID,
		InitialGenerationID: initial.ID(), InitialTreeDigest: initial.TreeDigest(),
		SystemPrompt:    "You are an offline, non-claim-bearing development fixture.",
		DeveloperPrompt: "Use only the registered read, write, edit, and bash proxy tools.",
	}, providerJournal, developmentFixtureTurns)
	if err != nil {
		return err
	}
	resultingExecutor, err := providerbridge.NewExecutor(conversation, durable)
	if err != nil {
		return err
	}
	supervisor := &Supervisor{
		IPC: worker.Channels.IPCServer(), Broker: conversation, Policy: policy,
		Decisions: conversation, Executor: resultingExecutor,
	}
	initialGeneration := initial.ID()

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	channelsDone := make(chan error, 1)
	go func() {
		channelsDone <- RunGovernedChannels(runCtx, supervisor, worker.Channels.ProviderChannel(), conversation)
	}()

	if err := worker.SendAdmittedPrompt(ctx, conversation, devFixtureTaskID, devFixtureRequestID, devFixturePrompt); err != nil {
		cancelRun()
		return fmt.Errorf("admit and forward development prompt to actual Pi: %w", err)
	}
	if err := waitDevelopmentTurnEnd(ctx, worker, devFixtureRequestID); err != nil {
		cancelRun()
		return err
	}

	settlement = closeWorker()
	// The uncontained development settler reports UNKNOWN by design; a non-nil
	// close error is expected here and is not treated as a route failure. A
	// STOPPED claim would be a bug because no containment proof exists.
	if settlement.State == piruntime.SettlementStopped {
		return errors.New("development fixture reported STOPPED settlement without containment proof")
	}
	if settlement.State != piruntime.SettlementUnknown {
		return fmt.Errorf("unexpected development settlement state %q", settlement.State)
	}

	select {
	case err := <-channelsDone:
		if err != nil {
			return fmt.Errorf("governed provider/supervisor channels did not settle cleanly: %w", err)
		}
	case <-time.After(30 * time.Second):
		cancelRun()
		return errors.New("governed provider/supervisor channels did not settle after the worker exited")
	}

	receipt, err := buildDevelopmentFixtureReceipt(providerJournal, supervisor.HandledProposals(), initialGeneration, durable.Tip().ID(), settlement.State, commands.Profile())
	if err != nil {
		return err
	}
	return writeDevelopmentFixtureReceipt(output, receipt)
}

func loadDevelopmentFixtureConfig() (developmentFixtureConfig, error) {
	config := developmentFixtureConfig{
		nodePath:       os.Getenv(devFixtureNodeEnv),
		adapterRoot:    os.Getenv(devFixtureAdapterEnv),
		dependencyRoot: os.Getenv(devFixtureNodeModulesEnv),
		privateRoot:    os.Getenv(devFixtureRootEnv),
	}
	missing := make([]string, 0, 4)
	for name, value := range map[string]string{
		devFixtureNodeEnv: config.nodePath, devFixtureAdapterEnv: config.adapterRoot,
		devFixtureNodeModulesEnv: config.dependencyRoot, devFixtureRootEnv: config.privateRoot,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return config, fmt.Errorf("development fixture is opt-in: set %s", strings.Join(missing, ", "))
	}
	for name, value := range map[string]string{
		devFixtureNodeEnv: config.nodePath, devFixtureAdapterEnv: config.adapterRoot,
		devFixtureNodeModulesEnv: config.dependencyRoot, devFixtureRootEnv: config.privateRoot,
	} {
		if !filepath.IsAbs(value) {
			return config, fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if err := piruntime.ValidateDevelopmentPrivateDirectory(config.privateRoot); err != nil {
		return config, fmt.Errorf("%s: %w", devFixtureRootEnv, err)
	}
	return config, nil
}

// setupDevelopmentFixtureStore builds the synthetic, non-claim-bearing durable
// session repository and executor the development route drives. It uses the
// explicit dev callbacks (no SessionOperationAuthority, no D06 private-Git
// provenance) because D06 requires the sealed read-only exposure view the
// production launcher does not yet provide. Read/edit are accepted as the
// development seam; the Bash lease is registered with and settled by the
// bounded, non-contained development runner. It therefore makes no source,
// containment, or publication claim.
func setupDevelopmentFixtureStore(base string, commands devFixtureRunner) (*sessionrepo.Store, *DurableExecutor, *audit.Journal, *audit.Journal, func(), error) {
	if commands == nil {
		return nil, nil, nil, nil, nil, errors.New("development fixture requires a bounded command runner")
	}
	sourcePath := filepath.Join(base, "source")
	storePath := filepath.Join(base, "session")
	for _, path := range []string{sourcePath, storePath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			return nil, nil, nil, nil, nil, err
		}
	}
	if err := writeNativeFixtureWorkspace(sourcePath, "old\n"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	storeJournal, err := audit.Open(filepath.Join(base, "store-audit.jsonl"))
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	providerJournal, err := audit.Open(filepath.Join(base, "provider-audit.jsonl"))
	if err != nil {
		_ = storeJournal.Close()
		return nil, nil, nil, nil, nil, err
	}
	cleanup := func() {
		_ = storeJournal.Close()
		_ = providerJournal.Close()
	}
	authority := developmentFixtureAuthority{commands: commands}
	storeRoot, err := nativeFixtureOpenRoot(storePath)
	if err != nil {
		cleanup()
		return nil, nil, nil, nil, nil, err
	}
	store, err := sessionrepo.Create(storeRoot, sessionrepo.Options{
		PolicyDigest: devFixtureStorePolicy, MetadataPolicyDigest: devFixtureMetadataPolicy,
		Limits:          workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: devFixtureXattrPolicy, Complete: true},
		Journal:         storeJournal, AuthorizeOperation: authority.authorize,
		VerifyDecision: authority.verifyDecision, VerifySettlement: authority.verifySettlement,
	})
	_ = storeRoot.Close()
	if err != nil {
		cleanup()
		return nil, nil, nil, nil, nil, fmt.Errorf("create development session repository: %w", err)
	}
	sourceRoot, err := nativeFixtureOpenRoot(sourcePath)
	if err != nil {
		_ = store.Close()
		cleanup()
		return nil, nil, nil, nil, nil, err
	}
	g0, err := store.Seed(sourceRoot, sessionrepo.RootAttestation{Quiescent: true})
	_ = sourceRoot.Close()
	if err != nil {
		_ = store.Close()
		cleanup()
		return nil, nil, nil, nil, nil, fmt.Errorf("seed development generation: %w", err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: devFixtureCallIssuer,
		PolicyDigest: devFixtureStorePolicy, MetadataPolicyDigest: devFixtureMetadataPolicy,
		CommandRunner: commands,
	})
	if err != nil {
		_ = store.Close()
		cleanup()
		return nil, nil, nil, nil, nil, fmt.Errorf("construct development durable executor: %w", err)
	}
	closeAll := func() {
		_ = store.Close()
		cleanup()
	}
	return store, durable, storeJournal, providerJournal, closeAll, nil
}

func waitDevelopmentTurnEnd(ctx context.Context, worker *piruntime.PiSDKWorker, requestID string) error {
	for {
		event, err := worker.NextEvent(ctx)
		if err != nil {
			return fmt.Errorf("wait for development Pi turn: %w", err)
		}
		switch event.Type {
		case "worker_error":
			return fmt.Errorf("actual Pi worker failed with bounded code %s", event.Code)
		case "turn_end":
			if event.RequestID != requestID {
				return fmt.Errorf("development Pi turn ended for unexpected request %q", event.RequestID)
			}
			return nil
		}
	}
}

func buildDevelopmentFixtureReceipt(journal *audit.Journal, proposals uint64, initialGeneration, finalGeneration string, settlement piruntime.SettlementState, bashRunnerProfile string) (developmentFixtureReceipt, error) {
	trace, err := journal.Trace()
	if err != nil {
		return developmentFixtureReceipt{}, fmt.Errorf("read development provider audit: %w", err)
	}
	if strings.TrimSpace(bashRunnerProfile) == "" {
		return developmentFixtureReceipt{}, errors.New("development fixture receipt requires the selected runner profile")
	}
	receipt := developmentFixtureReceipt{
		Mode: "dev-fixture", ClaimBearing: false, ProviderExchange: false,
		Containment: "not-established", Settlement: string(settlement),
		Publication: "not-attempted",
		WorkerReady: true, PromptAdmitted: true, TurnCompleted: true,
		ProposalCount: proposals, InitialGeneration: initialGeneration, FinalGeneration: finalGeneration,
	}
	seen := make(map[string]struct{})
	addGeneration := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		if len(receipt.Generations) >= devFixtureLineageLimit {
			return
		}
		seen[id] = struct{}{}
		receipt.Generations = append(receipt.Generations, id)
	}
	addGeneration(initialGeneration)
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "provider_exchange":
			var exchange struct {
				ToolCall *struct {
					Name string `json:"name"`
				} `json:"tool_call,omitempty"`
			}
			if err := json.Unmarshal(record.Event.Data, &exchange); err != nil {
				return developmentFixtureReceipt{}, fmt.Errorf("decode development provider exchange: %w", err)
			}
			receipt.ProviderTurns++
			if exchange.ToolCall != nil {
				receipt.ToolCallCount++
			}
		case "provider_tool_result":
			var result struct {
				ToolCallID       string `json:"tool_call_id"`
				Tool             string `json:"tool"`
				Sequence         uint64 `json:"sequence"`
				GenerationFrom   string `json:"generation_from"`
				GenerationTo     string `json:"generation_to"`
				TransitionID     string `json:"transition_id"`
				EffectID         string `json:"effect_id"`
				Result           []byte `json:"result"`
				ResponseIDIssuer string `json:"response_id_issuer"`
			}
			if err := json.Unmarshal(record.Event.Data, &result); err != nil {
				return developmentFixtureReceipt{}, fmt.Errorf("decode development provider result: %w", err)
			}
			if result.ResponseIDIssuer != governedResponseIssuer {
				return developmentFixtureReceipt{}, errors.New("development provider result has an unexpected response issuer")
			}
			receipt.ToolResultCount++
			addGeneration(result.GenerationFrom)
			addGeneration(result.GenerationTo)
			if len(receipt.ToolLineage) < devFixtureLineageLimit {
				receipt.ToolLineage = append(receipt.ToolLineage, developmentFixtureResult{
					Sequence: result.Sequence, Tool: result.Tool, ToolCallID: result.ToolCallID,
					GenerationFrom: result.GenerationFrom, GenerationTo: result.GenerationTo,
					TransitionID: result.TransitionID, EffectID: result.EffectID,
				})
			}
			if result.Tool == "bash" {
				if receipt.BashSettlement != nil {
					return developmentFixtureReceipt{}, errors.New("development provider audit repeats a bash settlement")
				}
				summary, err := developmentFixtureBashSettlement(result.ToolCallID, result.GenerationTo, bashRunnerProfile, result.Result)
				if err != nil {
					return developmentFixtureReceipt{}, err
				}
				receipt.BashSettlement = summary
			}
		}
	}
	if receipt.ToolCallCount == 0 || receipt.ToolCallCount != receipt.ToolResultCount {
		return developmentFixtureReceipt{}, fmt.Errorf("development lineage is incomplete: calls=%d results=%d", receipt.ToolCallCount, receipt.ToolResultCount)
	}
	if receipt.ProposalCount != uint64(receipt.ToolResultCount) {
		return developmentFixtureReceipt{}, fmt.Errorf("development proposal/result mismatch: proposals=%d results=%d", receipt.ProposalCount, receipt.ToolResultCount)
	}
	if receipt.BashSettlement == nil {
		return developmentFixtureReceipt{}, errors.New("development workflow produced no bash settlement")
	}
	return receipt, nil
}

// developmentFixtureBashSettlement decodes the bounded, observed settlement of
// the one development Bash step from the exact durable protocol result the
// provider conversation journaled. It fails closed if the result is missing its
// durable output, is not a settled command, or carries a runner profile other
// than the runner profile selected for this launch. It never upgrades the fixed
// "not-established" containment status.
func developmentFixtureBashSettlement(toolCallID, generationTo, expectedProfile string, rawResult []byte) (*developmentFixtureBashSummary, error) {
	var protocolResult struct {
		Tool   string          `json:"tool"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(rawResult, &protocolResult); err != nil {
		return nil, fmt.Errorf("decode development bash protocol result: %w", err)
	}
	if protocolResult.Tool != "bash" || len(protocolResult.Output) == 0 {
		return nil, errors.New("development bash protocol result is missing its durable output")
	}
	var mutation durableMutationSummaryPayload
	if err := json.Unmarshal(protocolResult.Output, &mutation); err != nil {
		return nil, fmt.Errorf("decode development bash durable summary: %w", err)
	}
	if mutation.Tool != "bash" || mutation.Outcome != "success" || mutation.Command == nil ||
		mutation.Generation.ID != generationTo {
		return nil, errors.New("development bash durable summary is not a settled command for the promoted generation")
	}
	if mutation.Command.CommandRunnerProfile != expectedProfile ||
		mutation.Command.CommandContainmentStatus != "not-established" {
		return nil, errors.New("development bash durable summary reports an unexpected runner or containment status")
	}
	return &developmentFixtureBashSummary{
		ToolCallID: toolCallID, GenerationTo: generationTo,
		ExitCode: mutation.Command.ExitCode, ExitObserved: mutation.Command.ExitObserved,
		CommandContainmentStatus: mutation.Command.CommandContainmentStatus,
		CommandRunnerProfile:     mutation.Command.CommandRunnerProfile,
	}, nil
}

func writeDevelopmentFixtureReceipt(output io.Writer, receipt developmentFixtureReceipt) error {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode development fixture receipt: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > devFixtureReceiptMaxBytes {
		return errors.New("development fixture receipt exceeds its bound")
	}
	if output == nil {
		return nil
	}
	if _, err := output.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write development fixture receipt: %w", err)
	}
	return nil
}
