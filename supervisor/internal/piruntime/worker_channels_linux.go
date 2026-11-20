//go:build linux

// worker_channels_linux.go: Pi worker channel set: wire types, socketpair construction, accessors and close.

package piruntime

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/piinstall"
)

const (
	PiWorkerSchemaVersion    = "governed-pi-cli/v1"
	PiWorkerModelID          = piinstall.PinnedPiModel
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
	controlInput     *closeOnceWriter
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

	bindingToken  string
	controlSeq    uint64
	closed        bool
	childClosed   bool
	childCloseErr error
	closeDone     chan struct{}
	closeErr      error

	events        chan PiWorkerEvent
	eventsDone    chan struct{}
	eventsMu      sync.Mutex
	eventsErr     error
	eventsDropped uint64
	eventsBytes   uint64
	gapReturned   bool
}

// closeOnceWriter is the shared close owner for the RPC client and channels.
// Closing the underlying connection interrupts concurrent blocked writes.
type closeOnceWriter struct {
	writer io.Writer
	closer io.Closer
	once   sync.Once
	err    error
}

func (w *closeOnceWriter) Write(p []byte) (int, error) {
	return w.writer.Write(p)
}

func (w *closeOnceWriter) Close() error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		if w.closer == nil {
			w.err = errors.New("Pi worker control input does not support close")
			return
		}
		w.err = w.closer.Close()
	})
	return w.err
}

// closeOnceConn makes Close idempotent with a sticky result. The provider channel is closed by
// whoever finishes first: the governed channel loop when it ends and PiWorkerChannels.Close at
// worker teardown, which both reach it through ProviderChannel. A second Close of a net.Conn
// returns net.ErrClosed, which teardown recorded as a worker close error and which then blocked
// publication of an otherwise cleanly STOPPED worker.
type closeOnceConn struct {
	net.Conn
	once sync.Once
	err  error
}

func (c *closeOnceConn) Close() error {
	c.once.Do(func() { c.err = c.Conn.Close() })
	return c.err
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
	cleanup := func(cause error) error { return errors.Join(cause, c.Close()) }
	if c.childBootstrap, c.bootstrapWriter, err = os.Pipe(); err != nil {
		return nil, cleanup(fmt.Errorf("create Pi bootstrap pipe: %w", err))
	}
	controlParent, controlChild, err := newPiWorkerSocketPair("pi-worker-control")
	if err != nil {
		return nil, cleanup(fmt.Errorf("create Pi control socketpair: %w", err))
	}
	c.controlWriter, c.childControl = controlParent, controlChild
	c.controlInput = &closeOnceWriter{writer: controlParent, closer: controlParent}
	ipcParent, ipcChild, err := newPiWorkerSocketPair("pi-worker-ipc")
	if err != nil {
		return nil, cleanup(fmt.Errorf("create Pi proposal IPC socketpair: %w", err))
	}
	c.childIPC = ipcChild
	c.ipcServer, err = ipc.NewServer(ipcParent, token)
	if err != nil {
		return nil, cleanup(errors.Join(fmt.Errorf("initialize Pi proposal IPC server: %w", err), ipcParent.Close()))
	}
	providerParent, providerChild, err := newPiWorkerSocketPair("pi-worker-provider")
	if err != nil {
		return nil, cleanup(fmt.Errorf("create Go provider bridge socketpair: %w", err))
	}
	c.childProvider = providerChild
	c.providerChannel = &closeOnceConn{Conn: providerParent}
	if c.eventReader, c.childEvents, err = os.Pipe(); err != nil {
		return nil, cleanup(fmt.Errorf("create Pi worker event pipe: %w", err))
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

func (c *PiWorkerChannels) RPCWriter() io.Writer {
	if c == nil {
		return nil
	}
	return c.controlInput
}

func (c *PiWorkerChannels) BindingToken() string {
	if c == nil {
		return ""
	}
	return c.bindingToken
}

func piWorkerToolSchemaDigests() map[string]string {
	return map[string]string{
		"read":  "26c3f02a4f951a21b617ceff8b62afa46ff0d9057167704892abe55958fdd52a",
		"write": "76c984b0e1e931a557039182d3e1af618ba9baf280594232b0cbe8fca274554d",
		"edit":  "99bb7745bb97f8c842f14c785870271d7fa3bba0b12d64be4dd5e481b1baa8bf",
		"bash":  "4a9cd3d690c6bc974325530add2c213d9240a4db2e027f898d0eac40c653f4c9",
	}
}

func (c *PiWorkerChannels) CloseChildEnds() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.childClosed {
		return c.childCloseErr
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
	c.childCloseErr = joined
	return c.childCloseErr
}

func (c *PiWorkerChannels) Close() error {
	if c == nil {
		return nil
	}

	// Do this before taking c.mu: sendControl holds that mutex during Write,
	// and closing the endpoint is what interrupts a blocked write.
	var inputCloseErr error
	if c.controlInput != nil {
		inputCloseErr = c.controlInput.Close()
	}

	c.mu.Lock()
	if c.closed {
		done := c.closeDone
		c.mu.Unlock()
		if done != nil {
			<-done
		}
		c.mu.Lock()
		err := c.closeErr
		c.mu.Unlock()
		return err
	}
	c.closed = true
	done := make(chan struct{})
	c.closeDone = done
	c.mu.Unlock()

	var joined error
	joined = errors.Join(joined, inputCloseErr, c.CloseChildEnds())
	if c.bootstrapWriter != nil {
		joined = errors.Join(joined, c.bootstrapWriter.Close())
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
	c.mu.Lock()
	c.closeErr = joined
	close(done)
	c.mu.Unlock()
	return joined
}

func closePiWorkerChildEndsForReadiness(channels *PiWorkerChannels) error {
	if err := channels.CloseChildEnds(); err != nil {
		return fmt.Errorf("close Pi worker child descriptor ends before readiness: %w", err)
	}
	return nil
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
