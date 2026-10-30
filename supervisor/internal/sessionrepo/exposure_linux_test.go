//go:build linux

package sessionrepo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

const exposureMountChildEnv = "TBOUND_SESSIONREPO_EXPOSURE_MOUNT_CHILD"

func init() {
	if os.Getenv(exposureMountChildEnv) != "1" {
		return
	}
	runExposureMountChild()
	os.Exit(0)
}

// TestSessionRepositoryGenerationExposureModeE01Style proves the mode's
// supervisor-side selection and explicit-refresh behavior with an injected
// binder. It is only the generation-visibility fragment: it is not an E05
// provider transcript, does not show that Pi observed the view, and does not
// establish containment.
func TestSessionRepositoryGenerationExposureModeE01Style(t *testing.T) {
	for _, mode := range []ViewMode{ViewRecreated, ViewIndirection} {
		t.Run(string(mode), func(t *testing.T) {
			fx := newReachabilityFixture(t)
			binder := newMemoryExposureBinder()
			fx.store.options.ExposureBinder = binder
			sealedDigest := fx.g0.TreeDigest()

			view, err := fx.g0.Expose(mode)
			if err != nil {
				t.Fatalf("expose g0: %v", err)
			}
			defer view.Close()
			stale, err := fx.g0.Expose(mode)
			if err != nil {
				t.Fatalf("expose stale g0 view: %v", err)
			}
			defer stale.Close()

			if got := view.Observed(); got.ID != "g0" || got.TreeDigest != fx.g0.TreeDigest() {
				t.Fatalf("initial selected evidence = %+v, want g0", got)
			}
			readme, err := binder.ReadFile(view, "README.md")
			if err != nil || string(readme) != "baseline text\n" {
				t.Fatalf("consumer read of g0 = %q, err=%v", readme, err)
			}
			if err := binder.WriteFile(view, "README.md", []byte("must-not-write\n")); !errors.Is(err, syscall.EROFS) {
				t.Fatalf("write through read-only fixture view = %v, want EROFS", err)
			}

			if err := view.Refresh(fx.g1); err != nil {
				t.Fatalf("explicit refresh to g1: %v", err)
			}
			if got := view.Observed(); got.ID != "g1" || got.TreeDigest != fx.g1.TreeDigest() {
				t.Fatalf("refreshed selected evidence = %+v, want g1", got)
			}
			created, err := binder.ReadFile(view, "created.txt")
			if err != nil || string(created) != "created\n" {
				t.Fatalf("consumer read after Refresh = %q, err=%v; want g1 bytes", created, err)
			}
			if _, err := binder.ReadFile(stale, "created.txt"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unrefreshed view read created.txt = %v, want absent g0 path", err)
			}
			staleReadme, err := binder.ReadFile(stale, "README.md")
			if err != nil || string(staleReadme) != "baseline text\n" {
				t.Fatalf("unrefreshed view stopped observing g0: bytes=%q err=%v", staleReadme, err)
			}

			if got := fx.g0.TreeDigest(); got != sealedDigest {
				t.Fatalf("sealed g0 digest changed across exposure/refresh: got %s want %s", got, sealedDigest)
			}
			if _, err := fx.store.Verify(); err != nil {
				t.Fatalf("verify sealed input after exposure/refresh: %v", err)
			}
			assertNoGitReachability(t, fx.store.rootPath)
			if err := mountReachabilityError(readMountInfo(t), fx.sourcePath, fx.store.rootPath,
				[]string{fx.g0.testPath(), fx.g1.testPath()}); err != nil {
				t.Fatalf("fixture binder exposed live/Git or generation mount reachability: %v", err)
			}
			artifact, err := fx.store.Evidence()
			if err != nil {
				t.Fatalf("evidence after exposure proof: %v", err)
			}
			if artifact.RealProviderExchange || artifact.PiAdapterWired || artifact.CommandContainmentStatus != "not-established" {
				t.Fatalf("exposure proof upgraded unrelated evidence: %+v", artifact)
			}
		})
	}
}

func TestSessionRepositoryExposureDirectoryRecovery(t *testing.T) {
	t.Run("clean close reopens with exposed root entry", func(t *testing.T) {
		fx := newReachabilityFixture(t)
		fx.store.options.ExposureBinder = newMemoryExposureBinder()
		view, err := fx.g0.Expose(ViewRecreated)
		if err != nil {
			t.Fatalf("expose g0: %v", err)
		}
		if err := view.Close(); err != nil {
			t.Fatalf("close exposed view: %v", err)
		}
		rootPath, options := fx.store.rootPath, fx.store.options
		if err := fx.store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		reopened, err := Open(openFixtureRoot(t, rootPath), options)
		if err != nil {
			t.Fatalf("reopen clean exposed directory: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
	})

	t.Run("leftover exposure fails closed", func(t *testing.T) {
		fx := newReachabilityFixture(t)
		rootPath, options := fx.store.rootPath, fx.store.options
		if err := os.Mkdir(filepath.Join(rootPath, "exposed", "unfinished"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := fx.store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		if _, err := Open(openFixtureRoot(t, rootPath), options); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("open with unfinished exposure = %v, want %v", err, ErrQuarantined)
		}
	})
}

// TestSessionRepositoryExposureMountBackedReadOnlyRefresh re-executes this
// test binary in an unprivileged user+mount namespace. Only an actual mounted
// view write returning EROFS counts; an O_PATH descriptor is never used as the
// read-only assertion. Namespace or mount unavailability is an explicit skip.
func TestSessionRepositoryExposureMountBackedReadOnlyRefresh(t *testing.T) {
	fx := newReachabilityFixture(t)
	sealedDigest := fx.g0.TreeDigest()
	binder := newNamespaceExposureBinder(t)
	fx.store.options.ExposureBinder = binder

	view, err := fx.g0.Expose(ViewRecreated)
	if err != nil {
		if mountMechanismUnavailable(err) {
			t.Skipf("user-namespace read-only bind unavailable (explicit skip): %v", err)
		}
		t.Fatalf("mount-backed Expose g0: %v", err)
	}
	defer view.Close()
	stale, err := fx.g0.Expose(ViewRecreated)
	if err != nil {
		if mountMechanismUnavailable(err) {
			t.Skipf("user-namespace read-only bind unavailable (explicit skip): %v", err)
		}
		t.Fatalf("mount-backed stale Expose g0: %v", err)
	}
	defer stale.Close()

	readme, err := binder.ReadFile(view.testConsumerPath(), "README.md")
	if err != nil || string(readme) != "baseline text\n" {
		t.Fatalf("mount consumer read of g0 = %q, err=%v", readme, err)
	}
	if err := binder.WriteFile(view.testConsumerPath(), "README.md", []byte("not writable\n")); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("write through real read-only bind = %v, want EROFS", err)
	}
	if err := view.Refresh(fx.g1); err != nil {
		if mountMechanismUnavailable(err) {
			t.Skipf("user-namespace bind refresh unavailable (explicit skip): %v", err)
		}
		t.Fatalf("mount-backed refresh to g1: %v", err)
	}
	created, err := binder.ReadFile(view.testConsumerPath(), "created.txt")
	if err != nil || string(created) != "created\n" {
		t.Fatalf("mount consumer after Refresh = %q, err=%v; want g1 bytes", created, err)
	}
	if _, err := binder.ReadFile(stale.testConsumerPath(), "created.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrefreshed real bind read created.txt = %v, want absent g0 path", err)
	}
	if got := view.Observed(); got.ID != "g1" || got.TreeDigest != fx.g1.TreeDigest() {
		t.Fatalf("mount-backed refreshed evidence = %+v, want g1", got)
	}
	if got := stale.Observed(); got.ID != "g0" || got.TreeDigest != fx.g0.TreeDigest() {
		t.Fatalf("unrefreshed real bind identity = %+v, want g0", got)
	}
	if got := fx.g0.TreeDigest(); got != sealedDigest {
		t.Fatalf("mount-backed exposure changed sealed g0 digest: got %s want %s", got, sealedDigest)
	}
	if _, err := fx.store.Verify(); err != nil {
		t.Fatalf("verify sealed chain after mount-backed exposure: %v", err)
	}
	t.Log("read-only view is supervisor-selected and its generation identity is re-proven after refresh; no Pi observation or containment claim")
}

type memoryExposureBinder struct {
	mu       sync.Mutex
	bindings map[string]string
}

func newMemoryExposureBinder() *memoryExposureBinder {
	return &memoryExposureBinder{bindings: make(map[string]string)}
}

func (b *memoryExposureBinder) BindReadOnly(source *os.File, target string) error {
	if source == nil {
		return ErrInvalidOptions
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bindings[filepath.Clean(target)] = source.Name()
	return nil
}

func (b *memoryExposureBinder) Unmount(target string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.bindings[filepath.Clean(target)]; !exists {
		return fmt.Errorf("no fake bind at %s", target)
	}
	delete(b.bindings, filepath.Clean(target))
	return nil
}

func (b *memoryExposureBinder) ReadFile(view *ExposedView, relative string) ([]byte, error) {
	target, err := filepath.EvalSymlinks(view.testConsumerPath())
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	source := b.bindings[filepath.Clean(target)]
	b.mu.Unlock()
	if source == "" {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(source, filepath.FromSlash(relative)))
}

func (b *memoryExposureBinder) WriteFile(view *ExposedView, relative string, content []byte) error {
	if _, err := b.ReadFile(view, relative); err != nil {
		return err
	}
	return syscall.EROFS
}

func (v *ExposedView) testConsumerPath() string {
	if v.mode == ViewIndirection {
		return filepath.Join(v.rootPath, "current")
	}
	return v.rootPath
}

type exposureMountRequest struct {
	Operation string `json:"operation"`
	Source    string `json:"source,omitempty"`
	Target    string `json:"target,omitempty"`
	Path      string `json:"path,omitempty"`
	Content   []byte `json:"content,omitempty"`
}

type exposureMountResponse struct {
	Ready bool   `json:"ready,omitempty"`
	Data  []byte `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	Errno int    `json:"errno,omitempty"`
}

type namespaceExposureBinder struct {
	mu      sync.Mutex
	command *exec.Cmd
	input   io.WriteCloser
	output  *json.Decoder
	stderr  bytes.Buffer
}

func newNamespaceExposureBinder(t *testing.T) *namespaceExposureBinder {
	t.Helper()
	command := exec.Command("/proc/self/exe")
	command.Env = append(os.Environ(), exposureMountChildEnv+"=1")
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("open exposure-helper stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("open exposure-helper stdout: %v", err)
	}
	command.Stderr = &bytes.Buffer{}
	if err := command.Start(); err != nil {
		if mountMechanismUnavailable(err) {
			t.Skipf("user+mount namespace unavailable (explicit skip): %v", err)
		}
		t.Fatalf("start unprivileged mount helper: %v", err)
	}
	binder := &namespaceExposureBinder{command: command, input: input, output: json.NewDecoder(stdout)}
	var ready exposureMountResponse
	if err := binder.output.Decode(&ready); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("read mount-helper readiness: %v", err)
	}
	if !ready.Ready {
		_ = command.Process.Kill()
		_ = command.Wait()
		if ready.Errno == int(syscall.EPERM) || ready.Errno == int(syscall.EACCES) || ready.Errno == int(syscall.ENOSYS) {
			t.Skipf("private mount namespace setup unavailable (explicit skip): %s", ready.Error)
		}
		t.Fatalf("mount helper did not initialize: %s", ready.Error)
	}
	t.Cleanup(func() { binder.stop() })
	return binder
}

func (b *namespaceExposureBinder) BindReadOnly(source *os.File, target string) error {
	if source == nil {
		return ErrInvalidOptions
	}
	return b.request(exposureMountRequest{Operation: "bind", Source: source.Name(), Target: target}).err()
}

func (b *namespaceExposureBinder) Unmount(target string) error {
	return b.request(exposureMountRequest{Operation: "unmount", Target: target}).err()
}

func (b *namespaceExposureBinder) ReadFile(root, relative string) ([]byte, error) {
	return b.request(exposureMountRequest{Operation: "read", Path: filepath.Join(root, filepath.FromSlash(relative))}).result()
}

func (b *namespaceExposureBinder) WriteFile(root, relative string, content []byte) error {
	return b.request(exposureMountRequest{
		Operation: "write", Path: filepath.Join(root, filepath.FromSlash(relative)), Content: content,
	}).err()
}

func (b *namespaceExposureBinder) request(request exposureMountRequest) exposureMountResponse {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := json.NewEncoder(b.input).Encode(request); err != nil {
		return exposureMountResponse{Error: err.Error()}
	}
	var response exposureMountResponse
	if err := b.output.Decode(&response); err != nil {
		return exposureMountResponse{Error: err.Error()}
	}
	return response
}

func (response exposureMountResponse) err() error {
	if response.Errno != 0 {
		return syscall.Errno(response.Errno)
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

func (response exposureMountResponse) result() ([]byte, error) {
	if err := response.err(); err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (b *namespaceExposureBinder) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.command == nil || b.command.Process == nil {
		return
	}
	_ = json.NewEncoder(b.input).Encode(exposureMountRequest{Operation: "stop"})
	_ = b.input.Close()
	_ = b.command.Wait()
	b.command = nil
}

func runExposureMountChild() {
	encoder := json.NewEncoder(os.Stdout)
	decoder := json.NewDecoder(os.Stdin)
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		_ = encoder.Encode(exposureMountResponse{Error: err.Error(), Errno: errnoNumber(err)})
		return
	}
	if err := encoder.Encode(exposureMountResponse{Ready: true}); err != nil {
		return
	}
	for {
		var request exposureMountRequest
		if err := decoder.Decode(&request); err != nil {
			return
		}
		if request.Operation == "stop" {
			_ = encoder.Encode(exposureMountResponse{})
			return
		}
		response := handleExposureMountRequest(request)
		if err := encoder.Encode(response); err != nil {
			return
		}
	}
}

func handleExposureMountRequest(request exposureMountRequest) exposureMountResponse {
	var err error
	switch request.Operation {
	case "bind":
		var source *os.File
		source, err = os.Open(request.Source)
		if err == nil {
			err = (LinuxReadOnlyBinder{}).BindReadOnly(source, request.Target)
			closeErr := source.Close()
			err = errors.Join(err, closeErr)
		}
	case "unmount":
		err = (LinuxReadOnlyBinder{}).Unmount(request.Target)
	case "read":
		var data []byte
		data, err = os.ReadFile(request.Path)
		if err == nil {
			return exposureMountResponse{Data: data}
		}
	case "write":
		err = os.WriteFile(request.Path, request.Content, 0o600)
	default:
		err = fmt.Errorf("unknown mount-helper operation %q", request.Operation)
	}
	if err == nil {
		return exposureMountResponse{}
	}
	return exposureMountResponse{Error: err.Error(), Errno: errnoNumber(err)}
}

func errnoNumber(err error) int {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return int(errno)
	}
	return 0
}

func mountMechanismUnavailable(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EOPNOTSUPP)
}
