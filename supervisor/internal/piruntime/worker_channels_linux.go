//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/ipc"
)

const (
	PiWorkerSchemaVersion    = "governed-pi-sdk-worker/v1"
	PiWorkerModelID          = "nvidia/nemotron-3.5-lightning:free"
	PiWorkerProviderID       = "tbound-go-broker"
	PiWorkerMaxFrameBytes    = 1 << 20
	PiWorkerMaxPromptBytes   = 16 << 10
	PiWorkerMaxTaskIDBytes   = 128
	PiWorkerMaxIdentityBytes = 256
	PiWorkerMaxEventCount    = 1024
	PiWorkerMaxEventBytes    = 16 << 20
	PiWorkerEventQueue       = 16
	PiWorkerControlFD        = 0
)

type PiWorkerBootstrap struct {
	SourceBindingDigest    string
	SourceProvenanceDigest string
	SourceGenerationID     string
	SourceTreeDigest       string
	SourceManifestDigest   string
	WorkerViewIdentity     string
	WorkerMountID          uint64
	SessionID              string
	WorkflowID             string
	ConversationID         string
}

type PiWorkerControl struct {
	Type      string `json:"type"`
	Sequence  uint64 `json:"sequence"`
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id,omitempty"`
	Text      string `json:"text,omitempty"`
}

type PiWorkerEvent struct {
	Type                   string            `json:"type"`
	Sequence               uint64            `json:"sequence,omitempty"`
	RequestID              string            `json:"request_id,omitempty"`
	Code                   string            `json:"code,omitempty"`
	SchemaVersion          string            `json:"schema_version,omitempty"`
	ProfileID              string            `json:"profile_id,omitempty"`
	ProfileDigest          string            `json:"profile_digest,omitempty"`
	SourceBindingDigest    string            `json:"source_binding_digest,omitempty"`
	SourceProvenanceDigest string            `json:"source_provenance_digest,omitempty"`
	SourceGenerationID     string            `json:"source_generation_id,omitempty"`
	SourceTreeDigest       string            `json:"source_tree_digest,omitempty"`
	SourceManifestDigest   string            `json:"source_manifest_digest,omitempty"`
	WorkerViewIdentity     string            `json:"worker_view_identity,omitempty"`
	WorkerMountID          uint64            `json:"worker_mount_id,omitempty"`
	RuntimeBundleDigest    string            `json:"runtime_bundle_digest,omitempty"`
	DescriptorProfile      string            `json:"descriptor_profile,omitempty"`
	DescriptorDigest       string            `json:"descriptor_profile_digest,omitempty"`
	NodeVersion            string            `json:"node_version,omitempty"`
	PiVersion              string            `json:"pi_version,omitempty"`
	ProviderID             string            `json:"provider_id,omitempty"`
	ModelID                string            `json:"model_id,omitempty"`
	Tools                  []string          `json:"tools,omitempty"`
	ToolSchemaSHA256       map[string]string `json:"tool_schema_sha256,omitempty"`
	DroppedEvents          uint64            `json:"dropped_events,omitempty"`
	DroppedBytes           uint64            `json:"dropped_bytes,omitempty"`
}

type PiWorkerChannels struct {
	mu sync.Mutex

	workingDirectory *os.File
	workerExposure   VerifiedWorkerExposure
	exposureEvidence WorkerExposureEvidence
	childBootstrap   *os.File
	bootstrapWriter  *os.File
	childControl     *os.File
	controlWriter    net.Conn
	childIPC         *os.File
	ipcServer        *ipc.Server
	childProvider    *os.File
	providerChannel  net.Conn
	childEvents      *os.File
	eventReader      *os.File
	runtimeBundle    *os.File
	workerAssets     VerifiedWorkerRuntimeAssets
	assetEvidence    WorkerRuntimeAssetEvidence
	ownedSourceFDs   bool

	bindingToken string
	controlSeq   uint64
	closed       bool
	childClosed  bool

	events        chan PiWorkerEvent
	eventsDone    chan struct{}
	eventsMu      sync.Mutex
	eventsErr     error
	eventsDropped uint64
	eventsBytes   uint64
	gapReturned   bool
}

type piWorkerBootstrapWire struct {
	SchemaVersion          string            `json:"schema_version"`
	ProfileID              string            `json:"profile_id"`
	ProfileDigest          string            `json:"profile_digest"`
	SourceBindingDigest    string            `json:"source_binding_digest"`
	SourceProvenanceDigest string            `json:"source_provenance_digest"`
	SourceGenerationID     string            `json:"source_generation_id"`
	SourceTreeDigest       string            `json:"source_tree_digest"`
	SourceManifestDigest   string            `json:"source_manifest_digest"`
	WorkerViewIdentity     string            `json:"worker_view_identity"`
	WorkerMountID          uint64            `json:"worker_mount_id"`
	RuntimeBundleDigest    string            `json:"runtime_bundle_digest"`
	DescriptorProfile      string            `json:"descriptor_profile"`
	DescriptorDigest       string            `json:"descriptor_profile_digest"`
	ChildEnvironmentDigest string            `json:"child_environment_digest"`
	SessionID              string            `json:"session_id"`
	WorkflowID             string            `json:"workflow_id"`
	ConversationID         string            `json:"conversation_id"`
	PrivateCWD             string            `json:"private_cwd"`
	IPCBindingToken        string            `json:"ipc_binding_token"`
	ProviderID             string            `json:"provider_id"`
	ModelID                string            `json:"model_id"`
	NodeVersion            string            `json:"node_version"`
	PiVersion              string            `json:"pi_version"`
	Tools                  []string          `json:"tools"`
	ToolSchemaSHA256       map[string]string `json:"tool_schema_sha256"`
}

// NewPiWorkerChannels creates the per-session FD endpoints. It does not start
// a process or admit a profile; the trusted host composition must bind the
// returned child descriptors into HostRuntimeBindings before production use.
func NewPiWorkerChannels(ctx context.Context, exposure VerifiedWorkerExposure, assets VerifiedWorkerRuntimeAssets) (*PiWorkerChannels, error) {
	if ctx == nil || isNilBinding(exposure) || isNilBinding(assets) {
		return nil, errors.New("Pi worker channels require verified workspace and runtime-asset capabilities")
	}
	workerExposure, exposureEvidence, err := exposure.ExportVerifiedWorkerExposure(ctx)
	if err != nil {
		return nil, fmt.Errorf("export sealed Pi workspace view: %w", err)
	}
	if err := validateWorkerExposureDescriptor(workerExposure, exposureEvidence); err != nil {
		_ = workerExposure.Close()
		return nil, err
	}
	runtimeAssets, assetEvidence, err := assets.ExportVerifiedWorkerRuntimeAssets(ctx)
	if err != nil {
		_ = workerExposure.Close()
		return nil, fmt.Errorf("export read-only Pi runtime assets: %w", err)
	}
	if err := validateRuntimeAssetDescriptor(runtimeAssets, assetEvidence); err != nil {
		_ = workerExposure.Close()
		_ = runtimeAssets.Close()
		return nil, err
	}
	channels, err := newPiWorkerChannels(workerExposure, runtimeAssets)
	if err != nil {
		_ = workerExposure.Close()
		_ = runtimeAssets.Close()
		return nil, err
	}
	channels.workerExposure = exposure
	channels.exposureEvidence = exposureEvidence
	channels.workerAssets = assets
	channels.assetEvidence = assetEvidence
	channels.ownedSourceFDs = true
	return channels, nil
}

func newPiWorkerChannels(workerExposure, runtimeAssets *os.File) (*PiWorkerChannels, error) {
	if workerExposure == nil || runtimeAssets == nil {
		return nil, errors.New("Pi worker channel endpoints require workspace and runtime-asset descriptors")
	}
	info, err := workerExposure.Stat()
	if err != nil || !info.IsDir() {
		return nil, errors.New("Pi worker exposure descriptor is not a directory")
	}
	bundleInfo, err := runtimeAssets.Stat()
	if err != nil || !bundleInfo.IsDir() {
		return nil, errors.New("Pi worker runtime-assets descriptor is not a directory")
	}
	token, err := ipc.NewBindingToken()
	if err != nil {
		return nil, err
	}
	c := &PiWorkerChannels{
		workingDirectory: workerExposure,
		runtimeBundle:    runtimeAssets,
		bindingToken:     token,
		controlSeq:       1,
		events:           make(chan PiWorkerEvent, PiWorkerEventQueue),
		eventsDone:       make(chan struct{}),
	}
	cleanup := func() { _ = c.Close() }
	if c.childBootstrap, c.bootstrapWriter, err = os.Pipe(); err != nil {
		cleanup()
		return nil, fmt.Errorf("create Pi bootstrap pipe: %w", err)
	}
	controlParent, controlChild, err := newPiWorkerSocketPair("pi-worker-control")
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("create Pi control socketpair: %w", err)
	}
	c.controlWriter, c.childControl = controlParent, controlChild
	ipcParent, ipcChild, err := newPiWorkerSocketPair("pi-worker-ipc")
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("create Pi proposal IPC socketpair: %w", err)
	}
	c.childIPC = ipcChild
	c.ipcServer, err = ipc.NewServer(ipcParent, token)
	if err != nil {
		_ = ipcParent.Close()
		cleanup()
		return nil, fmt.Errorf("initialize Pi proposal IPC server: %w", err)
	}
	providerParent, providerChild, err := newPiWorkerSocketPair("pi-worker-provider")
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("create Go provider bridge socketpair: %w", err)
	}
	c.childProvider = providerChild
	c.providerChannel = providerParent
	if c.eventReader, c.childEvents, err = os.Pipe(); err != nil {
		cleanup()
		return nil, fmt.Errorf("create Pi worker event pipe: %w", err)
	}
	go c.readEvents()
	return c, nil
}

func newPiWorkerSocketPair(name string) (net.Conn, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	parentFile := os.NewFile(uintptr(fds[0]), name+"-parent")
	childFile := os.NewFile(uintptr(fds[1]), name+"-child")
	if parentFile == nil || childFile == nil {
		if parentFile != nil {
			_ = parentFile.Close()
		} else {
			_ = syscall.Close(fds[0])
		}
		if childFile != nil {
			_ = childFile.Close()
		} else {
			_ = syscall.Close(fds[1])
		}
		return nil, nil, errors.New("socketpair returned invalid descriptor")
	}
	parent, err := net.FileConn(parentFile)
	_ = parentFile.Close()
	if err != nil {
		_ = childFile.Close()
		return nil, nil, err
	}
	return parent, childFile, nil
}

// DescriptorBindings returns the logical role map consumed by the launcher.
// FDs 4 and 9 are host-side source handles: only a namespace-backed production
// launcher may mount/reopen them inside the child namespace. The development
// helper intentionally inherits its synthetic sources directly and is not a
// production contract.
func (c *PiWorkerChannels) DescriptorBindings() []InheritedDescriptor {
	if c == nil {
		return nil
	}
	return cloneInheritedDescriptors([]InheritedDescriptor{
		{Role: DescriptorWorkerExposure, ChildFD: 4, File: c.workingDirectory},
		{Role: DescriptorBootstrap, ChildFD: 5, File: c.childBootstrap},
		{Role: DescriptorProposalIPC, ChildFD: 6, File: c.childIPC},
		{Role: DescriptorProviderBridge, ChildFD: 7, File: c.childProvider},
		{Role: DescriptorWorkerEvents, ChildFD: 8, File: c.childEvents},
		{Role: DescriptorRuntimeBundle, ChildFD: 9, File: c.runtimeBundle},
	})
}

func (c *PiWorkerChannels) ChildStdin() *os.File {
	if c == nil {
		return nil
	}
	return c.childControl
}

func (c *PiWorkerChannels) IPCServer() *ipc.Server {
	if c == nil {
		return nil
	}
	return c.ipcServer
}

func (c *PiWorkerChannels) ProviderChannel() io.ReadWriteCloser {
	if c == nil {
		return nil
	}
	return c.providerChannel
}

func (c *PiWorkerChannels) BindingToken() string {
	if c == nil {
		return ""
	}
	return c.bindingToken
}

func piWorkerToolSchemaDigests() map[string]string {
	return map[string]string{
		"read":  "5fad0fa7493528bc4284f5c447781325f67099f5fc018206efdc461d33962e85",
		"write": "7e51d1de0f2ccb5fe8de82d51ee3a1d9065e3c4f7c53497ce12f283d6f2f5aa0",
		"edit":  "394741d0cb4c9a6fe96d3275897405c8b13a1c7ff712426a942c10d484993fa1",
		"bash":  "854beb37435894c8f97dcad8d4610c0ea1458b57a6a8d4bb5b0ea435cdaa5893",
	}
}

func (c *PiWorkerChannels) writeBootstrap(profile HostProfile, input PiWorkerBootstrap) error {
	if c == nil || c.bootstrapWriter == nil || c.closed || c.childClosed {
		return errors.New("Pi worker bootstrap channel is unavailable")
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	if !validDigest(input.SourceBindingDigest) || !validDigest(input.SourceProvenanceDigest) ||
		!workerValidIdentity(input.SourceGenerationID, 256) || !validDigest(input.SourceTreeDigest) ||
		!validDigest(input.SourceManifestDigest) || !validDigest(input.WorkerViewIdentity) || input.WorkerMountID == 0 ||
		!workerValidIdentity(input.SessionID, 256) ||
		!workerValidIdentity(input.WorkflowID, 256) || !workerValidIdentity(input.ConversationID, 256) {
		return errors.New("Pi worker bootstrap source/session binding is incomplete")
	}
	wire := piWorkerBootstrapWire{
		SchemaVersion:          PiWorkerSchemaVersion,
		ProfileID:              profile.ID,
		ProfileDigest:          profile.Digest,
		SourceBindingDigest:    input.SourceBindingDigest,
		SourceProvenanceDigest: input.SourceProvenanceDigest,
		SourceGenerationID:     input.SourceGenerationID,
		SourceTreeDigest:       input.SourceTreeDigest,
		SourceManifestDigest:   input.SourceManifestDigest,
		WorkerViewIdentity:     input.WorkerViewIdentity,
		WorkerMountID:          input.WorkerMountID,
		RuntimeBundleDigest:    profile.PiRuntimeBundleDigest,
		DescriptorProfile:      profile.DescriptorProfile,
		DescriptorDigest:       profile.DescriptorProfileDigest,
		ChildEnvironmentDigest: profile.ChildEnvironmentDigest,
		SessionID:              input.SessionID,
		WorkflowID:             input.WorkflowID,
		ConversationID:         input.ConversationID,
		PrivateCWD:             profile.WorkingDirectory,
		IPCBindingToken:        c.bindingToken,
		ProviderID:             PiWorkerProviderID,
		ModelID:                PiWorkerModelID,
		NodeVersion:            profile.NodeVersion,
		PiVersion:              "0.87.1",
		Tools:                  []string{"read", "write", "edit", "bash"},
		ToolSchemaSHA256:       cloneToolSchemaDigests(),
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("encode Pi worker bootstrap: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > 16<<10 {
		return errors.New("Pi worker bootstrap exceeds bound")
	}
	if err := writePiFrame(c.bootstrapWriter, encoded); err != nil {
		return err
	}
	err = c.bootstrapWriter.Close()
	c.bootstrapWriter = nil
	return err
}

func cloneToolSchemaDigests() map[string]string {
	return map[string]string{
		"read":  "5fad0fa7493528bc4284f5c447781325f67099f5fc018206efdc461d33962e85",
		"write": "7e51d1de0f2ccb5fe8de82d51ee3a1d9065e3c4f7c53497ce12f283d6f2f5aa0",
		"edit":  "394741d0cb4c9a6fe96d3275897405c8b13a1c7ff712426a942c10d484993fa1",
		"bash":  "854beb37435894c8f97dcad8d4610c0ea1458b57a6a8d4bb5b0ea435cdaa5893",
	}
}

func writePiFrame(writer io.Writer, body []byte) error {
	if len(body) == 0 || len(body) > PiWorkerMaxFrameBytes {
		return errors.New("Pi worker frame length is invalid")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
	if err := writePiAll(writer, prefix[:]); err != nil {
		return err
	}
	return writePiAll(writer, body)
}

func writePiAll(writer io.Writer, value []byte) error {
	for len(value) != 0 {
		written, err := writer.Write(value)
		if written < 0 || written > len(value) {
			return errors.New("Pi worker writer returned invalid count")
		}
		value = value[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (c *PiWorkerChannels) sendControl(ctx context.Context, frame PiWorkerControl) error {
	if c == nil || ctx == nil {
		return errors.New("Pi worker control channel is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.controlWriter == nil {
		return errors.New("Pi worker control channel is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.controlSeq == math.MaxUint64 {
		return errors.New("Pi worker control sequence exhausted")
	}
	frame.Sequence = c.controlSeq
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(encoded) == 0 || len(encoded) > PiWorkerMaxFrameBytes {
		return errors.New("Pi worker control frame exceeds bound")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(encoded)))
	writer := c.controlWriter
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if err := writer.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = writer.SetWriteDeadline(time.Now()) })
	defer stopCancellation()
	defer writer.SetWriteDeadline(time.Time{})
	if err := writePiAll(writer, prefix[:]); err != nil {
		_ = writer.Close()
		c.controlWriter = nil
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if err := writePiAll(writer, encoded); err != nil {
		_ = writer.Close()
		c.controlWriter = nil
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	c.controlSeq++
	return nil
}

func (c *PiWorkerChannels) Abort(ctx context.Context, requestID string) error {
	if !workerValidIdentity(requestID, 256) {
		return errors.New("Pi worker abort request ID is invalid")
	}
	return c.sendControl(ctx, PiWorkerControl{Type: "abort", RequestID: requestID})
}

func (c *PiWorkerChannels) RequestStop(ctx context.Context, requestID string) error {
	if !workerValidIdentity(requestID, 256) {
		return errors.New("Pi worker stop request ID is invalid")
	}
	return c.sendControl(ctx, PiWorkerControl{Type: "stop", RequestID: requestID})
}

func (c *PiWorkerChannels) NextEvent(ctx context.Context) (PiWorkerEvent, error) {
	if c == nil || ctx == nil {
		return PiWorkerEvent{}, errors.New("Pi worker event stream is unavailable")
	}
	select {
	case event, ok := <-c.events:
		if ok {
			return event, nil
		}
		c.eventsMu.Lock()
		defer c.eventsMu.Unlock()
		if c.eventsDropped > 0 && !c.gapReturned {
			c.gapReturned = true
			return PiWorkerEvent{Type: "observation_gap", DroppedEvents: c.eventsDropped, DroppedBytes: c.eventsBytes}, nil
		}
		if c.eventsErr != nil {
			return PiWorkerEvent{}, c.eventsErr
		}
		return PiWorkerEvent{}, io.EOF
	case <-ctx.Done():
		return PiWorkerEvent{}, ctx.Err()
	}
}

func (c *PiWorkerChannels) readEvents() {
	defer close(c.eventsDone)
	defer close(c.events)
	var totalBytes uint64
	var sequence uint64 = 1
	for count := 0; count < PiWorkerMaxEventCount; count++ {
		var prefix [4]byte
		if _, err := io.ReadFull(c.eventReader, prefix[:]); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				c.setEventError(fmt.Errorf("read Pi event prefix: %w", err))
			}
			return
		}
		length := binary.BigEndian.Uint32(prefix[:])
		if length == 0 || length > PiWorkerMaxFrameBytes {
			c.setEventError(errors.New("Pi worker event frame length is invalid"))
			return
		}
		body := make([]byte, int(length))
		if _, err := io.ReadFull(c.eventReader, body); err != nil {
			c.setEventError(fmt.Errorf("read Pi worker event: %w", err))
			return
		}
		totalBytes = saturatingAdd(totalBytes, uint64(length))
		if totalBytes > PiWorkerMaxEventBytes {
			c.setEventError(errors.New("Pi worker event stream byte budget exhausted"))
			return
		}
		if err := protocol.ValidateStrictJSON(body); err != nil {
			c.setEventError(fmt.Errorf("worker event is not strict JSON: %w", err))
			return
		}
		event, err := decodePiWorkerEvent(body)
		if err != nil {
			c.setEventError(err)
			return
		}
		if event.Sequence != sequence || sequence == math.MaxUint64 {
			c.setEventError(errors.New("worker event sequence is duplicate, stale, or exhausted"))
			return
		}
		sequence++
		select {
		case c.events <- event:
		default:
			c.eventsMu.Lock()
			c.eventsDropped++
			c.eventsBytes = saturatingAdd(c.eventsBytes, uint64(length))
			c.eventsMu.Unlock()
		}
	}
	c.setEventError(errors.New("Pi worker event frame budget exhausted"))
}

func developmentWorkerSourceEvidence(directory *os.File) (PiWorkerBootstrap, error) {
	if directory == nil {
		return PiWorkerBootstrap{}, errors.New("development worker source evidence requires the selected fixture directory")
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return PiWorkerBootstrap{}, errors.New("development worker source handle is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return PiWorkerBootstrap{}, errors.New("development worker source directory identity is unavailable")
	}
	device := uint64(stat.Dev)
	if device == 0 {
		return PiWorkerBootstrap{}, errors.New("development worker source directory device is unavailable")
	}
	mountID, err := descriptorMountID(directory.Fd())
	if err != nil {
		return PiWorkerBootstrap{}, fmt.Errorf("read development fixture directory mount identity: %w", err)
	}
	manifestDigest, err := digestDevelopmentFixtureDirectory(directory, info)
	if err != nil {
		return PiWorkerBootstrap{}, err
	}
	treeDigest := DigestBytes([]byte("DEVELOPMENT/UNKNOWN/fixture-tree/v1\x00" + manifestDigest))
	viewIdentity := DigestBytes([]byte(fmt.Sprintf("DEVELOPMENT/UNKNOWN/fixture-view/v1\x00%d:%d:%d", device, stat.Ino, mountID)))
	generationID := fmt.Sprintf("DEVELOPMENT/UNKNOWN/fixture-%d-%d", device, stat.Ino)
	return PiWorkerBootstrap{
		SourceBindingDigest:    DigestBytes([]byte("DEVELOPMENT/UNKNOWN/source-binding/v1\x00" + treeDigest)),
		SourceProvenanceDigest: DigestBytes([]byte("DEVELOPMENT/UNKNOWN/source-provenance/v1\x00" + treeDigest)),
		SourceGenerationID:     generationID,
		SourceTreeDigest:       treeDigest,
		SourceManifestDigest:   manifestDigest,
		WorkerViewIdentity:     viewIdentity,
		WorkerMountID:          mountID,
	}, nil
}

func digestDevelopmentFixtureDirectory(directory *os.File, rootInfo os.FileInfo) (string, error) {
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", directory.Fd()))
	if err != nil || !filepath.IsAbs(rootPath) || strings.HasSuffix(rootPath, " (deleted)") {
		return "", errors.New("development worker fixture directory path is unavailable")
	}
	pathInfo, err := os.Lstat(rootPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(rootInfo, pathInfo) {
		return "", errors.New("development worker fixture directory path differs from its held descriptor")
	}
	var manifest strings.Builder
	var entries uint64
	var contentBytes int64
	err = filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 4096 {
			return errors.New("development worker fixture entry budget exhausted")
		}
		relative, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if name == "." {
			name = ""
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			_, err = fmt.Fprintf(&manifest, "d\x00%s\x00%o\n", name, info.Mode().Perm())
		case info.Mode().IsRegular():
			remaining := (16 << 20) - contentBytes
			if remaining < 0 {
				return errors.New("development worker fixture byte budget exhausted")
			}
			data, readErr := readRegularBounded(path, remaining)
			if readErr != nil {
				return readErr
			}
			contentBytes += int64(len(data))
			_, err = fmt.Fprintf(&manifest, "f\x00%s\x00%o\x00%d\x00%s\n", name, info.Mode().Perm(), len(data), DigestBytes(data))
		case info.Mode()&os.ModeSymlink != 0:
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			if len(target) > 4096 {
				return errors.New("development worker fixture symlink target exceeds bound")
			}
			_, err = fmt.Fprintf(&manifest, "l\x00%s\x00%s\n", name, target)
		default:
			return errors.New("development worker fixture contains an unsupported filesystem object")
		}
		if err != nil {
			return err
		}
		if manifest.Len() > 16<<20 {
			return errors.New("development worker fixture manifest exceeds bound")
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest development worker fixture directory: %w", err)
	}
	return DigestBytes([]byte("DEVELOPMENT/UNKNOWN/fixture-manifest/v1\x00" + manifest.String())), nil
}

func (c *PiWorkerChannels) setEventError(err error) {
	c.eventsMu.Lock()
	if c.eventsErr == nil {
		c.eventsErr = err
	}
	c.eventsMu.Unlock()
}

func decodePiWorkerEvent(raw []byte) (PiWorkerEvent, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return PiWorkerEvent{}, err
	}
	var event PiWorkerEvent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return event, err
	}
	if event.Sequence == 0 || !workerValidIdentity(event.Type, 32) {
		return event, errors.New("worker event has invalid sequence or type")
	}
	allowed := map[string][]string{
		"ready":        {"type", "sequence", "schema_version", "profile_id", "profile_digest", "source_binding_digest", "source_provenance_digest", "source_generation_id", "source_tree_digest", "source_manifest_digest", "worker_view_identity", "worker_mount_id", "runtime_bundle_digest", "descriptor_profile", "descriptor_profile_digest", "node_version", "pi_version", "provider_id", "model_id", "tools", "tool_schema_sha256"},
		"turn_end":     {"type", "sequence", "request_id"},
		"worker_error": {"type", "sequence", "request_id", "code"},
		"stopped":      {"type", "sequence", "request_id"},
	}
	keys, ok := allowed[event.Type]
	if !ok {
		return event, fmt.Errorf("unsupported Pi worker event type %q", event.Type)
	}
	if len(fields) != len(keys) {
		for _, optional := range []string{"request_id"} {
			if event.Type == "worker_error" || event.Type == "stopped" {
				if len(fields) == len(keys)-1 && fields[optional] == nil {
					break
				}
			}
		}
		if event.Type != "worker_error" && event.Type != "stopped" || len(fields) < len(keys)-1 || len(fields) > len(keys) {
			return event, errors.New("worker event has unexpected or missing fields")
		}
	}
	for _, key := range keys {
		if fields[key] == nil && !(key == "request_id" && (event.Type == "worker_error" || event.Type == "stopped")) {
			return event, fmt.Errorf("worker event is missing %q", key)
		}
	}
	if (event.Type == "turn_end" && !workerValidIdentity(event.RequestID, 256)) ||
		(event.Type == "worker_error" && !workerValidIdentity(event.Code, 64)) ||
		(event.Type == "stopped" && event.RequestID != "" && !workerValidIdentity(event.RequestID, 256)) {
		return event, errors.New("worker lifecycle event has invalid request identity or error code")
	}
	return event, nil
}

func (c *PiWorkerChannels) CloseChildEnds() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.childClosed {
		return nil
	}
	c.childClosed = true
	var joined error
	for _, descriptor := range c.DescriptorBindings() {
		if descriptor.File == nil {
			continue
		}
		if descriptor.Role == DescriptorWorkerExposure || descriptor.Role == DescriptorRuntimeBundle {
			if c.ownedSourceFDs {
				joined = errors.Join(joined, descriptor.File.Close())
			}
			continue
		}
		joined = errors.Join(joined, descriptor.File.Close())
	}
	if c.childControl != nil {
		joined = errors.Join(joined, c.childControl.Close())
		c.childControl = nil
	}
	return joined
}

func (c *PiWorkerChannels) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	var joined error
	joined = errors.Join(joined, c.CloseChildEnds())
	if c.bootstrapWriter != nil {
		joined = errors.Join(joined, c.bootstrapWriter.Close())
	}
	if c.controlWriter != nil {
		joined = errors.Join(joined, c.controlWriter.Close())
	}
	if c.ipcServer != nil {
		joined = errors.Join(joined, c.ipcServer.Close())
	}
	if c.providerChannel != nil {
		joined = errors.Join(joined, c.providerChannel.Close())
	}
	if c.eventReader != nil {
		joined = errors.Join(joined, c.eventReader.Close())
	}
	return joined
}

func workerValidIdentity(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func equalToolSchemas(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func minimalWorkerEnvironment(home, temp string) []string {
	values := []string{"HOME=" + home, "LANG=C.UTF-8", "PI_OFFLINE=1", "TMPDIR=" + temp}
	sort.Strings(values)
	return values
}

func validateMinimalWorkerEnvironment(environment []string) error {
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		name, value, ok := strings.Cut(item, "=")
		if !ok || (name != "HOME" && name != "TMPDIR" && name != "LANG" && name != "PI_OFFLINE") {
			return errors.New("Pi worker environment contains a non-allowlisted variable")
		}
		if _, duplicate := values[name]; duplicate {
			return fmt.Errorf("Pi worker environment repeats %s", name)
		}
		values[name] = value
	}
	if len(values) != 4 || values["PI_OFFLINE"] != "1" || values["LANG"] != "C.UTF-8" || !filepath.IsAbs(values["HOME"]) || !filepath.IsAbs(values["TMPDIR"]) {
		return errors.New("Pi worker environment must be exactly HOME/TMPDIR/LANG/PI_OFFLINE")
	}
	for _, name := range []string{"HOME", "TMPDIR"} {
		info, err := os.Lstat(values[name])
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("Pi worker %s is not an existing private mode-0700 directory", name)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("Pi worker %s is not owned by the launching UID", name)
		}
	}
	return nil
}

type PiSDKWorker struct {
	Session     *Session
	Channels    *PiWorkerChannels
	development bool

	mu          sync.Mutex
	leases      map[string]*workerPromptLease
	closed      bool
	events      chan PiWorkerEvent
	readerDone  chan struct{}
	eventsErr   error
	eventsDrop  uint64
	eventsBytes uint64
	gapReturned bool
}

type workerPromptLease struct {
	lease *Lease
	stop  func() bool
}

// Admission returns the session's close-and-drain gate. Effect supervisors
// should acquire a lease before executing a proposal and release it on result.
func (w *PiSDKWorker) Admission() *Admission {
	if w == nil || w.Session == nil {
		return nil
	}
	return w.Session.Admission()
}

// PID is the actual direct Node worker pid; it is diagnostic/process-scope
// binding only and does not establish descendant settlement.
func (w *PiSDKWorker) PID() int {
	if w == nil || w.Session == nil || w.Session.child == nil {
		return 0
	}
	return w.Session.child.PID()
}

func LaunchPiSDKWorker(ctx context.Context, capability *AdmissionCapability, exposure VerifiedWorkerExposure, assets VerifiedWorkerRuntimeAssets, channels *PiWorkerChannels, bootstrap PiWorkerBootstrap) (*PiSDKWorker, error) {
	if ctx == nil || capability == nil || !capability.valid() || capability.profile.Mode != ModeProduction || channels == nil {
		return nil, ErrChildCapabilityMissing
	}
	if isNilBinding(exposure) || isNilBinding(assets) ||
		!sameBinding(exposure, capability.runtime.WorkerExposure) || !sameBinding(assets, capability.runtime.WorkerRuntimeAssets) ||
		!sameBinding(exposure, channels.workerExposure) || !sameBinding(assets, channels.workerAssets) {
		return nil, errors.New("production Pi worker requires the capability-bound sealed workspace and runtime-asset views")
	}
	if err := exposure.VerifyWorkerExposure(ctx, capability.runtime.WorkerExposureEvidence); err != nil {
		return nil, fmt.Errorf("reverify sealed worker exposure and D06 provenance: %w", err)
	}
	if err := assets.VerifyWorkerRuntimeAssets(ctx, capability.runtime.RuntimeAssetEvidence); err != nil {
		return nil, fmt.Errorf("reverify worker runtime-asset exposure: %w", err)
	}
	if err := validateWorkerExposureDescriptor(channels.workingDirectory, capability.runtime.WorkerExposureEvidence); err != nil {
		return nil, err
	}
	if err := validateRuntimeAssetDescriptor(channels.runtimeBundle, capability.runtime.RuntimeAssetEvidence); err != nil {
		return nil, err
	}
	bootstrap.SourceBindingDigest = capability.runtime.WorkerExposureEvidence.BindingDigest
	bootstrap.SourceProvenanceDigest = capability.runtime.WorkerExposureEvidence.ProvenanceDigest
	bootstrap.SourceGenerationID = capability.runtime.WorkerExposureEvidence.GenerationID
	bootstrap.SourceTreeDigest = capability.runtime.WorkerExposureEvidence.TreeDigest
	bootstrap.SourceManifestDigest = capability.runtime.WorkerExposureEvidence.ManifestDigest
	bootstrap.WorkerViewIdentity = capability.runtime.WorkerExposureEvidence.ViewIdentity
	bootstrap.WorkerMountID = capability.runtime.WorkerExposureEvidence.MountID
	if !sameInheritedDescriptors(channels.DescriptorBindings(), capability.runtime.Descriptors) ||
		channels.ChildStdin() != capability.runtime.Stdin || capability.runtime.DescriptorProfile != PiWorkerDescriptorProfile ||
		!sameBinding(channels.workingDirectory, capability.runtime.WorkerExposureHandle) ||
		!sameBinding(channels.runtimeBundle, capability.runtime.PiRuntimeBundleHandle) {
		return nil, ErrHostBindingsMissing
	}
	if _, directHostExec := capability.runtime.Launcher.(*LinuxExecutableLauncher); directHostExec {
		return nil, ErrProductionLauncherUnavailable
	}
	return launchPiSDKWorker(ctx, capability.profile, capability.runtime, capability, channels, bootstrap, false, "", "")
}

type DeveloperPiWorkerPlan struct {
	NodePath       string
	NodeVersion    string
	BundleRoot     string
	DependencyRoot string
	BundleDigest   string
	BundleHandle   *os.File
	WorkerPath     string
	WorkingDir     string
	WorkingHandle  *os.File
	Home           string
	TempDir        string
	SessionID      string
	WorkflowID     string
	ConversationID string
}

// StartDevelopmentPiSDKWorker runs the actual Pi SDK and inherited Go/Node
// channels with the explicit direct Linux Node binary and existing dependency
// assets. It bypasses production admission, has no containment claim, and its
// settler always reports UNKNOWN after direct-child exit.
func StartDevelopmentPiSDKWorker(ctx context.Context, plan DeveloperPiWorkerPlan) (*PiSDKWorker, error) {
	if ctx == nil || !filepath.IsAbs(plan.NodePath) || !filepath.IsAbs(plan.BundleRoot) || !filepath.IsAbs(plan.DependencyRoot) ||
		!filepath.IsAbs(plan.WorkerPath) || !filepath.IsAbs(plan.WorkingDir) ||
		!filepath.IsAbs(plan.Home) || !filepath.IsAbs(plan.TempDir) || plan.BundleHandle == nil || plan.WorkingHandle == nil {
		return nil, errors.New("developer Pi worker requires explicit absolute Node/source/HOME/CWD paths and held directory handles")
	}
	if filepath.Clean(plan.WorkerPath) != filepath.Join(filepath.Clean(plan.BundleRoot), "src", "governed-pi-worker.ts") ||
		plan.NodeVersion == "" || !validIdentity(plan.SessionID) || !validIdentity(plan.WorkflowID) || !validIdentity(plan.ConversationID) {
		return nil, errors.New("developer Pi worker plan is incomplete")
	}
	if err := validateMinimalWorkerEnvironment(minimalWorkerEnvironment(plan.Home, plan.TempDir)); err != nil {
		return nil, err
	}
	channels, err := newPiWorkerChannels(plan.WorkingHandle, plan.BundleHandle)
	if err != nil {
		return nil, err
	}
	bundleDigest, err := DigestDeveloperPiSDKBundle(plan.BundleHandle, plan.DependencyRoot)
	if err != nil {
		_ = channels.Close()
		return nil, err
	}
	if plan.BundleDigest != "" && plan.BundleDigest != bundleDigest {
		_ = channels.Close()
		return nil, errors.New("developer Pi bundle digest differs from explicit plan")
	}
	nodeDigest, err := digestLinuxExecutable(plan.NodePath)
	if err != nil {
		_ = channels.Close()
		return nil, err
	}
	profile := developmentPiWorkerProfile(plan, nodeDigest, bundleDigest)
	bootstrap := PiWorkerBootstrap{
		SessionID:      plan.SessionID,
		WorkflowID:     plan.WorkflowID,
		ConversationID: plan.ConversationID,
	}
	sourceEvidence, err := developmentWorkerSourceEvidence(plan.WorkingHandle)
	if err != nil {
		_ = channels.Close()
		return nil, err
	}
	bootstrap.SourceBindingDigest = sourceEvidence.SourceBindingDigest
	bootstrap.SourceProvenanceDigest = sourceEvidence.SourceProvenanceDigest
	bootstrap.SourceGenerationID = sourceEvidence.SourceGenerationID
	bootstrap.SourceTreeDigest = sourceEvidence.SourceTreeDigest
	bootstrap.SourceManifestDigest = sourceEvidence.SourceManifestDigest
	bootstrap.WorkerViewIdentity = sourceEvidence.WorkerViewIdentity
	bootstrap.WorkerMountID = sourceEvidence.WorkerMountID
	runtime := developmentPiWorkerBindings(profile, plan, channels)
	return launchPiSDKWorker(ctx, profile, runtime, nil, channels, bootstrap, true, plan.NodePath, plan.DependencyRoot)
}

func developmentPiWorkerProfile(plan DeveloperPiWorkerPlan, nodeDigest, bundleDigest string) HostProfile {
	environment := minimalWorkerEnvironment(plan.Home, plan.TempDir)
	profile := HostProfile{
		ID: "developer-uncontained-nonclaiming", Digest: DigestBytes([]byte("developer-uncontained-nonclaiming")),
		Mode: ModeFixture, ChildExecutable: plan.NodePath, ExecutableDigest: nodeDigest, NodeVersion: plan.NodeVersion,
		ChildArgumentsProfile: "developer-governed-pi-sdk-worker-v1",
		ChildArgumentsDigest:  DigestArguments([]string{"--experimental-strip-types", "/proc/self/fd/9/src/governed-pi-worker.ts"}),
		WorkingDirectory:      plan.WorkingDir, WorkingDirectoryDigest: DigestBytes([]byte(plan.WorkingDir)),
		StdinProfile: "governed-session-control/fd0/v1", StdinDigest: DigestBytes([]byte("governed-session-control/fd0/v1")),
		ChildEnvironment: "developer-minimal-offline/v1", ChildEnvironmentDigest: DigestEnvironment(environment),
		IPCProfile: "tbound-ipc/v1/fd6", IPCProfileDigest: DigestBytes([]byte("tbound-ipc/v1/fd6")),
		TerminalProfile: "sdk-worker-no-terminal/v1", TerminalProfileDigest: DigestBytes([]byte("sdk-worker-no-terminal/v1")),
		ContainmentProfile: "developer-uncontained", ContainmentProfileDigest: DigestBytes([]byte("developer-uncontained")),
		SettlementProfile: "direct-child-only-unknown", SettlementProfileDigest: DigestBytes([]byte("direct-child-only-unknown")),
		ObservationProfile: "worker-events/fd8/v1", ObservationProfileDigest: DigestBytes([]byte("worker-events/fd8/v1")),
		DescriptorProfile: PiWorkerDescriptorProfile, DescriptorProfileDigest: DigestBytes([]byte(PiWorkerDescriptorProfile)),
		PiRuntimeBundleRoot: plan.BundleRoot, PiRuntimeBundleDigest: bundleDigest,
		SourceProvenance: "DEVELOPMENT/UNKNOWN", SourceProvenanceDigest: DigestBytes([]byte("DEVELOPMENT/UNKNOWN")),
		OfflineVerifier: "none-developer-only", OfflineVerifierDigest: DigestBytes([]byte("none-developer-only")),
		ProviderProfile: "go-providerbridge-synthetic-peer-nonclaiming", ProviderProfileDigest: DigestBytes([]byte("go-providerbridge-synthetic-peer-nonclaiming")),
	}
	return profile
}

func developmentPiWorkerBindings(profile HostProfile, plan DeveloperPiWorkerPlan, channels *PiWorkerChannels) HostRuntimeBindings {
	return HostRuntimeBindings{
		ChildExecutable: profile.ChildExecutable, NodeVersion: profile.NodeVersion,
		ChildArgumentsProfile: profile.ChildArgumentsProfile,
		WorkingDirectory:      profile.WorkingDirectory, WorkerExposureHandle: plan.WorkingHandle,
		PiRuntimeBundleRoot: profile.PiRuntimeBundleRoot, PiRuntimeBundleDigest: profile.PiRuntimeBundleDigest,
		PiRuntimeBundleHandle: plan.BundleHandle, StdinProfile: profile.StdinProfile,
		ChildEnvironment: profile.ChildEnvironment, IPCProfile: profile.IPCProfile,
		TerminalProfile: profile.TerminalProfile, ContainmentProfile: profile.ContainmentProfile,
		SettlementProfile: profile.SettlementProfile, ObservationProfile: profile.ObservationProfile,
		DescriptorProfile: profile.DescriptorProfile, SourceProvenance: profile.SourceProvenance,
		OfflineVerifier: profile.OfflineVerifier, ProviderProfile: profile.ProviderProfile,
		Arguments:   []string{"--experimental-strip-types", "/proc/self/fd/9/src/governed-pi-worker.ts"},
		Environment: minimalWorkerEnvironment(plan.Home, plan.TempDir), Stdin: channels.ChildStdin(),
		Terminal: io.Discard, Observation: discardObservationSink{}, Descriptors: channels.DescriptorBindings(),
	}
}

func developmentPiWorkerRuntimeLauncher(ctx context.Context, nodePath, dependencyRoot string, profile HostProfile, runtime HostRuntimeBindings) (*Process, error) {
	if ctx == nil || !filepath.IsAbs(nodePath) || nodePath != runtime.ChildExecutable {
		return nil, errors.New("developer Node path differs from explicit worker plan")
	}
	if err := validateMinimalWorkerEnvironment(runtime.Environment); err != nil {
		return nil, err
	}
	bundleDigest, err := DigestDeveloperPiSDKBundle(runtime.PiRuntimeBundleHandle, dependencyRoot)
	if err != nil || bundleDigest != profile.PiRuntimeBundleDigest {
		return nil, errors.Join(ErrRuntimeBundleRefused, err)
	}
	node, err := openVerifiedExecutable(nodePath, profile.ExecutableDigest)
	if err != nil {
		return nil, err
	}
	info, err := node.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		_ = node.Close()
		return nil, errors.New("developer Node is not an explicit regular executable")
	}
	if !tryAcquireSettlementWorker() {
		_ = node.Close()
		return nil, ErrSettlementCapacity
	}
	command := exec.CommandContext(ctx, "/proc/self/fd/3", runtime.Arguments...)
	// os/exec applies Dir before ExtraFiles are installed. The governed worker
	// compares actual cwd identity to inherited FD 4 before announcing ready.
	command.Dir = runtime.WorkingDirectory
	command.Env = append([]string{}, runtime.Environment...)
	command.Stdin = runtime.Stdin
	command.Stdout, command.Stderr = io.Discard, io.Discard
	command.ExtraFiles = []*os.File{node}
	for _, descriptor := range cloneInheritedDescriptors(runtime.Descriptors) {
		command.ExtraFiles = append(command.ExtraFiles, descriptor.File)
	}
	if err := command.Start(); err != nil {
		_ = node.Close()
		releaseSettlementWorker()
		return nil, err
	}
	_ = node.Close()
	return newProcess(command, developmentUnknownSettler{}, nil, true), nil
}

func launchPiSDKWorker(ctx context.Context, profile HostProfile, runtime HostRuntimeBindings, capability *AdmissionCapability, channels *PiWorkerChannels, bootstrap PiWorkerBootstrap, development bool, nodePath, dependencyRoot string) (resultWorker *PiSDKWorker, resultErr error) {
	if ctx == nil || channels == nil {
		return nil, errors.New("governed Pi worker requires context and channels")
	}
	keepChannels := false
	defer func() {
		if !keepChannels {
			resultErr = errors.Join(resultErr, channels.Close())
		}
	}()
	if !validDigest(bootstrap.SourceBindingDigest) || !validDigest(bootstrap.SourceProvenanceDigest) || !validIdentity(bootstrap.SessionID) || !validIdentity(bootstrap.WorkflowID) || !validIdentity(bootstrap.ConversationID) {
		return nil, errors.New("Pi worker bootstrap identity/source binding is invalid")
	}
	if profile.DescriptorProfile != PiWorkerDescriptorProfile || profile.DescriptorProfileDigest != DigestBytes([]byte(PiWorkerDescriptorProfile)) ||
		profile.PiRuntimeBundleRoot == "" || !validDigest(profile.PiRuntimeBundleDigest) ||
		profile.ChildArgumentsDigest != DigestArguments([]string{"--experimental-strip-types", "/proc/self/fd/9/src/governed-pi-worker.ts"}) ||
		!equalStrings(runtime.Arguments, []string{"--experimental-strip-types", "/proc/self/fd/9/src/governed-pi-worker.ts"}) ||
		!sameInheritedDescriptors(channels.DescriptorBindings(), runtime.Descriptors) || channels.ChildStdin() != runtime.Stdin {
		return nil, ErrHostBindingsMissing
	}
	if err := channels.writeBootstrap(profile, bootstrap); err != nil {
		_ = channels.Close()
		return nil, err
	}
	var process *Process
	var err error
	if development {
		process, err = developmentPiWorkerRuntimeLauncher(ctx, nodePath, dependencyRoot, profile, runtime)
	} else {
		if capability == nil || !capability.valid() || capability.profile.Mode != ModeProduction {
			_ = channels.Close()
			return nil, ErrChildCapabilityMissing
		}
		bound, bindErr := capability.BindChild(ChildSpec{
			Executable: profile.ChildExecutable, Args: append([]string(nil), runtime.Arguments...),
			Dir: runtime.WorkingDirectory, Env: append([]string{}, runtime.Environment...), Stdin: runtime.Stdin,
			Mode:       IOModeSilent,
			Descendant: runtime.Descendant, Descriptors: cloneInheritedDescriptors(runtime.Descriptors),
		}, runtime.Launcher)
		if bindErr != nil {
			_ = channels.Close()
			return nil, bindErr
		}
		process, err = StartChild(ctx, bound)
	}
	if err != nil {
		_ = channels.Close()
		return nil, err
	}
	_ = channels.CloseChildEnds()
	startupCtx, cancel := boundedStartupContext(ctx)
	defer cancel()
	for {
		event, eventErr := channels.NextEvent(startupCtx)
		if eventErr != nil {
			return nil, stopFailedPiLaunch(process, channels, fmt.Errorf("wait for Pi worker readiness: %w", eventErr))
		}
		if event.Type == "worker_error" {
			return nil, stopFailedPiLaunch(process, channels, fmt.Errorf("Pi worker startup failed: %s", event.Code))
		}
		if event.Type != "ready" {
			continue
		}
		if err := validateReadyEvent(event, profile, bootstrap); err != nil {
			return nil, stopFailedPiLaunch(process, channels, err)
		}
		break
	}
	session, err := NewSession(process)
	if err != nil {
		return nil, stopFailedPiLaunch(process, channels, err)
	}
	worker := &PiSDKWorker{Session: session, Channels: channels, development: development, leases: make(map[string]*workerPromptLease), events: make(chan PiWorkerEvent, PiWorkerEventQueue), readerDone: make(chan struct{})}
	go worker.pumpEvents()
	keepChannels = true
	return worker, nil
}

// SendAdmittedPrompt commits host/session admission in Go before writing the
// prompt over FD 0. The Pi SDK never sends prompt/history/results to FD 7.
type GoPromptConversation interface {
	AdmitPrompt(taskID, prompt string) error
	Close()
}

func (w *PiSDKWorker) SendAdmittedPrompt(ctx context.Context, conversation GoPromptConversation, taskID, requestID, prompt string) error {
	if w == nil || w.Session == nil || w.Channels == nil || conversation == nil || ctx == nil {
		return errors.New("governed Pi prompt requires an active session, Go conversation, and context")
	}
	if !workerValidIdentity(taskID, PiWorkerMaxTaskIDBytes) || !workerValidIdentity(requestID, PiWorkerMaxIdentityBytes) ||
		!utf8.ValidString(prompt) || len([]byte(prompt)) == 0 || len([]byte(prompt)) > PiWorkerMaxPromptBytes {
		return errors.New("governed Pi prompt is invalid or exceeds the broker byte bound")
	}
	return w.sendPromptControl(ctx, conversation, taskID, requestID, prompt)
}

// SendDevelopmentPrompt is a development-only process-test seam. It has no Go
// provider Conversation and therefore makes no provider-history or G1 claim.
func (w *PiSDKWorker) SendDevelopmentPrompt(ctx context.Context, taskID, requestID, prompt string) error {
	if w == nil || !w.development || ctx == nil {
		return errors.New("direct prompt control is available only in the explicit developer worker")
	}
	if !workerValidIdentity(taskID, 128) || !workerValidIdentity(requestID, 256) ||
		!utf8.ValidString(prompt) || len([]byte(prompt)) == 0 || len([]byte(prompt)) > PiWorkerMaxPromptBytes {
		return errors.New("development prompt is invalid or exceeds its byte bound")
	}
	return w.sendPromptControl(ctx, nil, taskID, requestID, prompt)
}

func (w *PiSDKWorker) sendPromptControl(ctx context.Context, conversation GoPromptConversation, taskID, requestID, prompt string) error {
	lease, err := w.Session.Acquire(ctx)
	if err != nil {
		return err
	}
	w.mu.Lock()
	if w.closed || len(w.leases) != 0 {
		w.mu.Unlock()
		lease.Release()
		return errors.New("governed Pi worker is closed or already has an active prompt")
	}
	w.leases[requestID] = &workerPromptLease{lease: lease}
	w.mu.Unlock()
	if conversation != nil {
		if err := conversation.AdmitPrompt(taskID, prompt); err != nil {
			w.releasePromptLease(requestID)
			return fmt.Errorf("Go provider conversation denied prompt admission: %w", err)
		}
	} else if !w.development {
		w.releasePromptLease(requestID)
		return errors.New("production Pi prompt has no Go-owned conversation admission")
	}
	stopCancel := context.AfterFunc(lease.Context(), func() {
		abortCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = w.Channels.Abort(abortCtx, requestID)
	})
	w.mu.Lock()
	if active := w.leases[requestID]; active != nil {
		active.stop = stopCancel
	}
	w.mu.Unlock()
	controlCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := w.Channels.sendControl(controlCtx, PiWorkerControl{Type: "prompt", RequestID: requestID, TaskID: taskID, Text: prompt}); err != nil {
		if conversation != nil {
			conversation.Close()
		}
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		w.releasePromptLease(requestID)
		return fmt.Errorf("write admitted Pi prompt control: %w", err)
	}
	return nil
}

func (w *PiSDKWorker) releasePromptLease(requestID string) {
	w.mu.Lock()
	active := w.leases[requestID]
	delete(w.leases, requestID)
	w.mu.Unlock()
	if active == nil {
		return
	}
	if active.stop != nil {
		active.stop()
	}
	active.lease.Release()
}

func (w *PiSDKWorker) pumpEvents() {
	defer close(w.readerDone)
	defer close(w.events)
	for {
		event, err := w.Channels.NextEvent(context.Background())
		if err != nil {
			w.mu.Lock()
			w.eventsErr = err
			w.mu.Unlock()
			return
		}
		if event.Type == "turn_end" || event.Type == "worker_error" {
			if event.RequestID != "" {
				w.releasePromptLease(event.RequestID)
			}
		}
		select {
		case w.events <- event:
		default:
			w.mu.Lock()
			w.eventsDrop++
			encoded, _ := json.Marshal(event)
			w.eventsBytes = saturatingAdd(w.eventsBytes, uint64(len(encoded)))
			w.mu.Unlock()
		}
	}
}

func (w *PiSDKWorker) NextEvent(ctx context.Context) (PiWorkerEvent, error) {
	if w == nil || ctx == nil {
		return PiWorkerEvent{}, errors.New("Pi worker event channel is unavailable")
	}
	select {
	case event, ok := <-w.events:
		if ok {
			return event, nil
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.eventsDrop > 0 && !w.gapReturned {
			w.gapReturned = true
			return PiWorkerEvent{Type: "observation_gap", DroppedEvents: w.eventsDrop, DroppedBytes: w.eventsBytes}, nil
		}
		if w.eventsErr != nil {
			return PiWorkerEvent{}, w.eventsErr
		}
		return PiWorkerEvent{}, io.EOF
	case <-ctx.Done():
		return PiWorkerEvent{}, ctx.Err()
	}
}

// Close cancels/drains active prompt leases, kills the direct worker if needed,
// and returns only the host scope's measured settlement. Missing or unavailable
// descendant proof remains UNKNOWN.
func (w *PiSDKWorker) Close(ctx context.Context) (Settlement, error) {
	if w == nil || w.Session == nil || ctx == nil {
		return Settlement{State: SettlementUnknown, Detail: "Pi worker session is not configured"}, ErrUnknownSettlement
	}
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	settlement, err := w.Session.Close(ctx)
	if w.Channels != nil {
		err = errors.Join(err, w.Channels.Close())
	}
	select {
	case <-w.readerDone:
	case <-ctx.Done():
		settlement.State = SettlementUnknown
		settlement.Detail = errors.Join(errors.New("worker event pump did not settle"), ctx.Err()).Error()
		err = errors.Join(err, ctx.Err())
	}
	return settlement, err
}

type PiWorkerLaunchError struct {
	Cause      error
	Settlement Settlement
	CleanupErr error
}

func (e *PiWorkerLaunchError) Error() string {
	return fmt.Sprintf("launch governed Pi worker: %v (settlement=%s)", e.Cause, e.Settlement.State)
}
func (e *PiWorkerLaunchError) Unwrap() error { return e.Cause }

func boundedStartupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) < piWorkerStartupTimeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, piWorkerStartupTimeout)
}

func validateReadyEvent(event PiWorkerEvent, profile HostProfile, bootstrap PiWorkerBootstrap) error {
	if event.SchemaVersion != PiWorkerSchemaVersion || event.ProfileID != profile.ID || event.ProfileDigest != profile.Digest ||
		event.SourceBindingDigest != bootstrap.SourceBindingDigest || event.SourceProvenanceDigest != bootstrap.SourceProvenanceDigest ||
		event.SourceGenerationID != bootstrap.SourceGenerationID || event.SourceTreeDigest != bootstrap.SourceTreeDigest ||
		event.SourceManifestDigest != bootstrap.SourceManifestDigest || event.WorkerViewIdentity != bootstrap.WorkerViewIdentity ||
		event.WorkerMountID != bootstrap.WorkerMountID ||
		event.RuntimeBundleDigest != profile.PiRuntimeBundleDigest ||
		event.DescriptorProfile != profile.DescriptorProfile || event.DescriptorDigest != profile.DescriptorProfileDigest ||
		event.NodeVersion != profile.NodeVersion || event.PiVersion != "0.87.1" ||
		event.ProviderID != PiWorkerProviderID || event.ModelID != PiWorkerModelID ||
		strings.Join(event.Tools, "\x00") != "read\x00write\x00edit\x00bash" || !equalToolSchemas(event.ToolSchemaSHA256, piWorkerToolSchemaDigests()) {
		return fmt.Errorf("Pi SDK worker ready receipt differs from the admitted fixed profile (schema=%t profile=%t source_binding=%t source_provenance=%t bundle=%t descriptor=%t node=%t pi=%t provider=%t model=%t tools=%t schemas=%t)",
			event.SchemaVersion == PiWorkerSchemaVersion,
			event.ProfileID == profile.ID && event.ProfileDigest == profile.Digest,
			event.SourceBindingDigest == bootstrap.SourceBindingDigest,
			event.SourceProvenanceDigest == bootstrap.SourceProvenanceDigest,
			event.RuntimeBundleDigest == profile.PiRuntimeBundleDigest,
			event.DescriptorProfile == profile.DescriptorProfile && event.DescriptorDigest == profile.DescriptorProfileDigest,
			event.NodeVersion == profile.NodeVersion,
			event.PiVersion == "0.87.1",
			event.ProviderID == PiWorkerProviderID,
			event.ModelID == PiWorkerModelID,
			strings.Join(event.Tools, "\x00") == "read\x00write\x00edit\x00bash",
			equalToolSchemas(event.ToolSchemaSHA256, piWorkerToolSchemaDigests()))
	}
	return nil
}

func stopFailedPiLaunch(process *Process, channels *PiWorkerChannels, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), piWorkerStopTimeout)
	defer cancel()
	settlement, cleanupErr := process.Stop(cleanupCtx)
	closeErr := channels.Close()
	return &PiWorkerLaunchError{Cause: cause, Settlement: settlement, CleanupErr: errors.Join(cleanupErr, closeErr)}
}

type developmentUnknownSettler struct{}

func (developmentUnknownSettler) Settle(context.Context) (bool, error) {
	return false, errors.New("development launch has no admitted descendant settlement profile")
}

type discardObservationSink struct{}

func (discardObservationSink) Observe(context.Context, Observation) error { return nil }
