//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
)

// piServiceUnitFixture returns the fixed registration, service configuration,
// writable paths, and a service unit text that satisfies the piruntime R8
// requirements (including DelegateSubgroup=runner).
func piServiceUnitFixture() (piruntime.D09ManagedLiveRootRegistration, piServiceConfig, []string, string) {
	registration := piruntime.D09ManagedLiveRootRegistration{
		ServiceName: "tbound-pi", ServiceGroup: "tbound-pi", RunnerPath: "/usr/local/bin/tbound",
	}
	serviceConfig := piServiceConfig{
		PodmanHome:           "/var/lib/tbound/podman-home",
		XDGRuntimeDir:        "/run/tbound-pi",
		XDGConfigHome:        "/etc/tbound/podman-config",
		XDGDataHome:          "/var/lib/tbound/podman-home/data",
		XDGCacheHome:         "/run/tbound-pi/cache",
		TMPDir:               "/run/tbound-pi/tmp",
		ContainersConfigPath: "/etc/tbound/podman-config/containers.conf",
		StorageConfigPath:    "/etc/tbound/podman-config/storage.conf",
	}
	writablePaths := []string{
		"/run/tbound-pi", "/var/lib/tbound/audit", "/var/lib/tbound/live",
		"/var/lib/tbound/podman-home", "/var/lib/tbound/podman-storage/graphroot",
		"/var/lib/tbound/private-admin", "/var/lib/tbound/publication-state", "/var/lib/tbound/sessions",
	}
	unit := `[Unit]
Description=TBound Pi service
[Service]
User=tbound-pi
Group=tbound-pi
ExecStart=/usr/local/bin/tbound serve --pi --service
KillMode=control-group
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
Delegate=yes
DelegateSubgroup=runner
RuntimeDirectory=tbound-pi
RuntimeDirectoryMode=0700
StateDirectory=tbound/audit tbound/live tbound/podman-home tbound/podman-storage/graphroot tbound/private-admin tbound/publication-state tbound/sessions
StateDirectoryMode=0700
ReadWritePaths=/run/tbound-pi /var/lib/tbound/audit /var/lib/tbound/live /var/lib/tbound/podman-home /var/lib/tbound/podman-storage/graphroot /var/lib/tbound/private-admin /var/lib/tbound/publication-state /var/lib/tbound/sessions
Environment=HOME=/var/lib/tbound/podman-home USER=tbound-pi LOGNAME=tbound-pi XDG_RUNTIME_DIR=/run/tbound-pi XDG_CONFIG_HOME=/etc/tbound/podman-config XDG_DATA_HOME=/var/lib/tbound/podman-home/data XDG_CACHE_HOME=/run/tbound-pi/cache TMPDIR=/run/tbound-pi/tmp CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf LANG=C LC_ALL=C
UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS
`
	return registration, serviceConfig, writablePaths, unit
}

// The handoff validator and the piruntime managed-root validator both read the
// same installed unit. A unit that satisfies the piruntime R8 contract
// (DelegateSubgroup=runner) must therefore pass the handoff validator, and the
// subgroup must be the exact closed runner leaf.
func TestValidatePiServiceUnitRequiresExactRunnerDelegateSubgroup(t *testing.T) {
	registration, serviceConfig, writablePaths, unit := piServiceUnitFixture()
	if !strings.Contains(unit, "DelegateSubgroup="+piruntime.D09ManagedPiRunnerCgroupLeaf+"\n") {
		t.Fatal("fixture does not carry the piruntime R8 DelegateSubgroup requirement")
	}
	if err := validatePiServiceUnit([]byte(unit), registration, serviceConfig, writablePaths); err != nil {
		t.Fatalf("unit satisfying the piruntime R8 contract refused by the handoff validator: %v", err)
	}
	for name, mutated := range map[string]string{
		"missing subgroup":          strings.Replace(unit, "DelegateSubgroup=runner\n", "", 1),
		"empty subgroup":            strings.Replace(unit, "DelegateSubgroup=runner", "DelegateSubgroup=", 1),
		"cell subtree as main leaf": strings.Replace(unit, "DelegateSubgroup=runner", "DelegateSubgroup=cells", 1),
		"dotdot escape":             strings.Replace(unit, "DelegateSubgroup=runner", "DelegateSubgroup=../runner", 1),
		"nested descendant":         strings.Replace(unit, "DelegateSubgroup=runner", "DelegateSubgroup=runner/child", 1),
		"case variant":              strings.Replace(unit, "DelegateSubgroup=runner", "DelegateSubgroup=Runner", 1),
		"duplicate subgroup":        strings.Replace(unit, "DelegateSubgroup=runner\n", "DelegateSubgroup=runner\nDelegateSubgroup=runner\n", 1),
		"other unlisted directive":  strings.Replace(unit, "DelegateSubgroup=runner\n", "DelegateSubgroup=runner\nSlice=system.slice\n", 1),
	} {
		if mutated == unit {
			t.Fatalf("mutation %q did not change the baseline unit", name)
		}
		if err := validatePiServiceUnit([]byte(mutated), registration, serviceConfig, writablePaths); !errors.Is(err, ErrPiServiceUnavailable) {
			t.Fatalf("handoff validator accepted unit with %s or returned unexpected error: %v", name, err)
		}
	}
}

func TestValidatePiServiceUnitPinsServicePolicy(t *testing.T) {
	registration, serviceConfig, writablePaths, unit := piServiceUnitFixture()
	if err := validatePiServiceUnit([]byte(unit), registration, serviceConfig, writablePaths); err != nil {
		t.Fatalf("valid fixed Pi service unit refused: %v", err)
	}

	mutations := []struct {
		name   string
		before string
		after  string
	}{
		{name: "broaden ReadWritePaths", before: "/var/lib/tbound/sessions", after: "/var/lib/tbound"},
		{name: "broaden StateDirectory", before: "StateDirectory=tbound/audit tbound/live", after: "StateDirectory=tbound/audit tbound"},
		{name: "unsafe KillMode", before: "KillMode=control-group", after: "KillMode=process"},
		{name: "EnvironmentFile", before: "[Service]\n", after: "[Service]\nEnvironmentFile=/etc/tbound/serve-pi.env\n"},
		{name: "ExecStartPre", before: "[Service]\n", after: "[Service]\nExecStartPre=/bin/true\n"},
		{name: "ExecCondition", before: "[Service]\n", after: "[Service]\nExecCondition=/bin/true\n"},
		{name: "ExecStop", before: "[Service]\n", after: "[Service]\nExecStop=/bin/true\n"},
		{name: "ExecStopPost", before: "[Service]\n", after: "[Service]\nExecStopPost=/bin/true\n"},
		{name: "duplicate environment key", before: "UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS", after: "Environment=HOME=/tmp\nUnsetEnvironment=DBUS_SESSION_BUS_ADDRESS"},
		{name: "service install section", before: "UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS\n", after: "UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS\n[Install]\nWantedBy=multi-user.target\n"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := strings.Replace(unit, mutation.before, mutation.after, 1)
			if mutated == unit {
				t.Fatalf("test mutation did not match the baseline unit: %q", mutation.before)
			}
			if err := validatePiServiceUnit([]byte(mutated), registration, serviceConfig, writablePaths); !errors.Is(err, ErrPiServiceUnavailable) {
				t.Fatalf("unsafe service unit accepted or returned unexpected error: %v", err)
			}
		})
	}
}

func TestValidatePiServiceSocketUnitPinsActivationPolicy(t *testing.T) {
	socket := piServiceSocketRegistration{
		SocketPath: "/run/tbound/serve-pi.sock", ServiceUnit: "tbound-pi.service",
		SocketUser: "root", SocketGroup: "tbound-pi-clients",
	}
	unit := `[Unit]
Description=TBound Pi socket
[Socket]
ListenStream=/run/tbound/serve-pi.sock
Accept=no
Service=tbound-pi.service
SocketUser=root
SocketGroup=tbound-pi-clients
SocketMode=0660
DirectoryMode=0755
FileDescriptorName=tbound-pi
[Install]
WantedBy=sockets.target
`
	if err := validatePiServiceSocketUnit([]byte(unit), socket); err != nil {
		t.Fatalf("valid fixed Pi socket unit refused: %v", err)
	}
	for _, mutation := range []struct {
		name   string
		before string
		after  string
	}{
		{name: "per-connection activation", before: "Accept=no", after: "Accept=yes"},
		{name: "wrong file descriptor name", before: "FileDescriptorName=tbound-pi", after: "FileDescriptorName=other"},
		{name: "additional datagram listener", before: "Accept=no", after: "Accept=no\nListenDatagram=/run/tbound/serve-pi.sock"},
		{name: "additional stream listener", before: "ListenStream=/run/tbound/serve-pi.sock", after: "ListenStream=/run/tbound/serve-pi.sock\nListenStream=/run/tbound/serve-pi-extra.sock"},
		{name: "unexpected install target", before: "WantedBy=sockets.target", after: "WantedBy=multi-user.target"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := strings.Replace(unit, mutation.before, mutation.after, 1)
			if mutated == unit {
				t.Fatalf("test mutation did not match the baseline unit: %q", mutation.before)
			}
			if err := validatePiServiceSocketUnit([]byte(mutated), socket); !errors.Is(err, ErrPiServiceUnavailable) {
				t.Fatalf("unsafe socket unit accepted or returned unexpected error: %v", err)
			}
		})
	}
}

func TestPiServiceUnixSocketHandoffUsesKernelPeerAndReplaysTerminal(t *testing.T) {
	server := newPiServiceTestServer(t, piServiceSocketRegistration{
		AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid(),
	}, func(_ context.Context, request piServiceRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"schema_version":"tbound-production-pi-receipt/v1","success":true,"request_id":"` + request.RequestID + `"}`), nil
	})
	request := piServiceRequest{RequestID: "request-success-1",
		TaskID: "task-1", Prompt: "read task.txt"}
	first := roundTripPiServiceRequest(t, server.path, request)
	if first.Status != "completed" || first.RequestID != request.RequestID || len(first.Receipt) == 0 || server.calls.Load() != 1 {
		t.Fatalf("first local service request=%+v calls=%d", first, server.calls.Load())
	}
	replay := roundTripPiServiceRequest(t, server.path, request)
	if replay.Status != "completed" || !bytes.Equal(replay.Receipt, first.Receipt) || server.calls.Load() != 1 {
		t.Fatalf("exact retry was not replayed without duplicate execution: first=%+v replay=%+v calls=%d",
			first, replay, server.calls.Load())
	}
	conflict := request
	scopedPaths := []string{"task.txt"}
	scopeConflict := request
	scopeConflict.BashOutputPaths = &scopedPaths
	scopeRefused := roundTripPiServiceRequest(t, server.path, scopeConflict)
	if scopeRefused.Status != "refused" || scopeRefused.ErrorCode != "request-id-conflict" || server.calls.Load() != 1 {
		t.Fatalf("changed Bash scope was not refused: response=%+v calls=%d", scopeRefused, server.calls.Load())
	}
	conflict.Prompt = "different payload"
	refused := roundTripPiServiceRequest(t, server.path, conflict)
	if refused.Status != "refused" || refused.ErrorCode != "request-id-conflict" || server.calls.Load() != 1 {
		t.Fatalf("request ID payload conflict was not refused: response=%+v calls=%d", refused, server.calls.Load())
	}
	trace, err := server.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 2 || trace.Records[0].Event.Kind != "pi_service_request_accepted" ||
		trace.Records[1].Event.Kind != "pi_service_request_terminal" || server.calls.Load() != 1 {
		t.Fatalf("scope conflict changed the journal or reran work: records=%+v calls=%d", trace.Records, server.calls.Load())
	}
	var accepted piServiceRequestAcceptedEvent
	if err := unmarshalGovernedJSON(trace.Records[0].Event.Data, &accepted); err != nil || accepted.PayloadDigest != mustPiServicePayloadDigest(t, request) {
		t.Fatalf("journal lost the accepted payload binding: %+v err=%v", accepted, err)
	}
}

func TestPiServiceRejectsUnauthorizedActualPeerUIDAndGID(t *testing.T) {
	for name, policy := range map[string]piServiceSocketRegistration{
		"wrong uid": {AllowedClientUIDs: []int{os.Geteuid() + 1}, AllowedClientPrimaryGID: os.Getegid()},
		"wrong gid": {AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid() + 1},
	} {
		t.Run(name, func(t *testing.T) {
			server := newPiServiceTestServer(t, policy, func(context.Context, piServiceRequest) (json.RawMessage, error) {
				return json.RawMessage(`{"success":true}`), nil
			})
			// The service checks SO_PEERCRED before parsing a request. Avoid writing
			// after connect: a correct early refusal can close first and cause EPIPE.
			conn := dialPiServiceTest(t, server.path)
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				_ = conn.Close()
				t.Fatal(err)
			}
			response, err := readPiServiceResponse(conn)
			_ = conn.Close()
			if err != nil || response.Status != "refused" || response.ErrorCode != "peer-not-authorized" || server.calls.Load() != 0 {
				t.Fatalf("actual kernel peer credentials were not denied before request parsing: response=%+v err=%v calls=%d", response, err, server.calls.Load())
			}
		})
	}
}

func TestPiServiceRejectsUnknownDuplicateAndOversizedFrames(t *testing.T) {
	server := newPiServiceTestServer(t, piServiceSocketRegistration{
		AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid(),
	}, func(context.Context, piServiceRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"success":true}`), nil
	})
	frames := map[string][]byte{
		"unknown field": []byte(`{"request_id":"r-unknown","task_id":"task","prompt":"x","profile":"/etc/tbound/forged"}`),
		"duplicate key": []byte(`{"request_id":"r-dup","request_id":"r-other","task_id":"task","prompt":"x"}`),
	}
	for name, payload := range frames {
		t.Run(name, func(t *testing.T) {
			response := roundTripPiServiceRaw(t, server.path, payload)
			if response.Status != "refused" || server.calls.Load() != 0 {
				t.Fatalf("invalid strict request was processed: response=%+v calls=%d", response, server.calls.Load())
			}
		})
	}
	conn := dialPiServiceTest(t, server.path)
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(piServiceClientMaxBytes+1))
	if _, err := conn.Write(prefix[:]); err != nil {
		t.Fatal(err)
	}
	response, err := readPiServiceResponse(conn)
	_ = conn.Close()
	if err != nil || response.Status != "refused" || server.calls.Load() != 0 {
		t.Fatalf("oversized frame was not refused: response=%+v err=%v calls=%d", response, err, server.calls.Load())
	}
	conn = dialPiServiceTest(t, server.path)
	binary.BigEndian.PutUint32(prefix[:], 24)
	_, _ = conn.Write(prefix[:])
	_, _ = conn.Write([]byte("partial"))
	_ = conn.CloseWrite()
	response, err = readPiServiceResponse(conn)
	_ = conn.Close()
	if err != nil || response.Status != "refused" || server.calls.Load() != 0 {
		t.Fatalf("partial frame was not refused: response=%+v err=%v calls=%d", response, err, server.calls.Load())
	}
}

func TestReadPiServiceResponseRequestIDRules(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		status  string
		wantErr bool
	}{
		{name: "pre-request refusal may omit ID", status: "refused"},
		{name: "completed requires ID", status: "completed", wantErr: true},
		{name: "busy requires ID", status: "busy", wantErr: true},
		{name: "unknown requires ID", status: "unknown", wantErr: true},
		{name: "refused ID must be bounded", id: strings.Repeat("x", productionRequestMaxBytes+1), status: "refused", wantErr: true},
		{name: "valid refusal ID", id: "request-refused", status: "refused"},
		{name: "valid completed ID", id: "request-completed", status: "completed"},
		{name: "valid busy ID", id: "request-busy", status: "busy"},
		{name: "valid unknown ID", id: "request-unknown", status: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			writeDone := make(chan error, 1)
			go func() {
				writeDone <- writePiServiceFrame(server, piServiceResponse{
					SchemaVersion: piServiceResponseSchema,
					RequestID:     test.id,
					Status:        test.status,
				})
				_ = server.Close()
			}()
			response, err := readPiServiceResponse(client)
			_ = client.Close()
			<-writeDone // closing the other end unblocks a partial frame writer too
			if (err != nil) != test.wantErr {
				t.Fatalf("readPiServiceResponse() error = %v, wantErr %t (response=%+v)", err, test.wantErr, response)
			}
			if err == nil && (response.RequestID != test.id || response.Status != test.status) {
				t.Fatalf("decoded response = %+v, want ID=%q status=%q", response, test.id, test.status)
			}
		})
	}
}

func TestCallPiServiceConnBindsResponseToRequestID(t *testing.T) {
	request := piServiceRequest{RequestID: "request-bound", TaskID: "task-bound", Prompt: "bounded test"}
	socketRoot := t.TempDir()
	tests := []struct {
		name       string
		responseID string
		status     string
		wantErr    bool
	}{
		{name: "mismatched ID refused", responseID: "other-request", status: "completed", wantErr: true},
		{name: "refused without ID", status: "refused", wantErr: true},
		{name: "matching completed", responseID: request.RequestID, status: "completed"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := piServiceResponse{SchemaVersion: piServiceResponseSchema,
				RequestID: test.responseID, Status: test.status}
			got, err := callPiServiceConn(testPiServiceResponseServer(t, filepath.Join(socketRoot, "s"+strconv.Itoa(index)), request, response), request)
			if (err != nil) != test.wantErr {
				t.Fatalf("callPiServiceConn() error = %v, wantErr %t; response=%+v", err, test.wantErr, got)
			}
			if err == nil && (got.RequestID != request.RequestID || got.Status != "completed") {
				t.Fatalf("accepted response = %+v, want matching completed response", got)
			}
		})
	}
}

// testPiServiceResponseServer returns a connected Unix client while a bounded
// test server consumes the request and sends exactly one chosen response.
func testPiServiceResponseServer(t *testing.T, path string, request piServiceRequest, response piServiceResponse) *net.UnixConn {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		gotRequest, err := readPiServiceRequest(conn)
		if err != nil {
			serverDone <- err
			return
		}
		if gotRequest != request {
			serverDone <- errors.New("test response server received a different request")
			return
		}
		serverDone <- writePiServiceFrame(conn, response)
	}()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	// callPiServiceConn owns the exchange; this watcher ensures its server-side
	// goroutine terminates promptly if an assertion or protocol failure occurs.
	t.Cleanup(func() {
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("test response server: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("test response server did not finish within deadline")
		}
	})
	return client
}

func TestPiServiceBusyDisconnectAndUnresolvedReplay(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	registration := piServiceSocketRegistration{
		AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid(),
	}
	runTask := func(ctx context.Context, request piServiceRequest) (json.RawMessage, error) {
		if request.RequestID == "request-blocking" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return json.RawMessage(`{"success":true,"request_id":"` + request.RequestID + `"}`), nil
	}
	server := newPiServiceTestServer(t, registration, runTask)
	firstRequest := piServiceRequest{RequestID: "request-blocking", TaskID: "task-1", Prompt: "work"}
	firstResult := make(chan piServiceResponse, 1)
	go func() { firstResult <- roundTripPiServiceRequest(t, server.path, firstRequest) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first task did not enter the single active slot")
	}
	busy := roundTripPiServiceRequest(t, server.path,
		piServiceRequest{RequestID: "request-busy", TaskID: "task-2", Prompt: "other"})
	if busy.Status != "busy" {
		t.Fatalf("second simultaneous request was queued instead of refused busy: %+v", busy)
	}
	close(release)
	if response := <-firstResult; response.Status != "completed" {
		t.Fatalf("first task did not complete: %+v", response)
	}

	// Production refuses any new admission while a prior request lacks independent settlement
	// (ErrPiServiceUnresolvedPrior). This fixture has no settlement verifier, so the disconnect case
	// runs on a fresh service with no prior work; its accepted, terminal, and replay checks are unchanged.
	server = newPiServiceTestServer(t, registration, runTask)

	// Simulate a client disconnect after the service durably accepts the task.
	conn := dialPiServiceTest(t, server.path)
	request := piServiceRequest{RequestID: "request-disconnect", TaskID: "task-3", Prompt: "finish after disconnect"}
	payload, _ := json.Marshal(request)
	if err := writePiServiceRequest(conn, payload); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, exists, err := server.requests.lookup(piServicePeerCredentials{UID: os.Geteuid(), GID: os.Getegid(), PID: os.Getpid()}, request, mustPiServicePayloadDigest(t, request))
		if err != nil {
			t.Fatal(err)
		}
		if exists && state.terminal != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, exists, err := server.requests.lookup(piServicePeerCredentials{UID: os.Geteuid(), GID: os.Getegid(), PID: os.Getpid()}, request, mustPiServicePayloadDigest(t, request))
	if err != nil || !exists || state.terminal == nil {
		t.Fatalf("disconnect canceled or lost the accepted request: state=%+v exists=%v err=%v", state, exists, err)
	}
	response := roundTripPiServiceRequest(t, server.path, request)
	if response.Status != "completed" || server.calls.Load() != 1 {
		t.Fatalf("disconnect retry duplicated or lost the terminal response: response=%+v calls=%d", response, server.calls.Load())
	}
}

func TestPiServiceJournalBlocksNewIDsAfterRestart(t *testing.T) {
	for _, terminalStatus := range []string{"", "unknown", "completed"} {
		name := terminalStatus
		if name == "" {
			name = "missing-terminal"
		}
		t.Run(name, func(t *testing.T) {
			_, journalPath, _, _, _ := seedPiServiceRecoveryJournal(t, terminalStatus)
			command := exec.Command(os.Args[0], "-test.run=^TestPiServiceJournalRecoveryProcessHelper$")
			command.Env = append(os.Environ(), "TB_TBOUND_PI_RECOVERY_HELPER=begin-new",
				"TB_TBOUND_PI_RECOVERY_JOURNAL="+journalPath,
				"TB_TBOUND_PI_RECOVERY_UID="+strconv.Itoa(os.Geteuid()),
				"TB_TBOUND_PI_RECOVERY_GID="+strconv.Itoa(os.Getegid()))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("fresh-process new-ID admission: %v\n%s", err, output)
			}
			journal, err := audit.Open(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			trace, traceErr := journal.Trace()
			closeErr := journal.Close()
			if traceErr != nil || closeErr != nil {
				t.Fatalf("reopen request journal: trace=%v close=%v", traceErr, closeErr)
			}
			wantRecords := 1
			if terminalStatus != "" {
				wantRecords = 2
			}
			if len(trace.Records) != wantRecords {
				t.Fatalf("refused fresh process appended admission: got %d records, want %d", len(trace.Records), wantRecords)
			}
		})
	}
}

func TestPiServiceRequestJournalHashBindsWorkflowAdmission(t *testing.T) {
	root := privateGovernedTestRoot(t)
	requestAudit, err := audit.Open(filepath.Join(root, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	requests, err := newPiServiceRequestJournal(requestAudit)
	if err != nil {
		_ = requestAudit.Close()
		t.Fatal(err)
	}
	defer requests.close()
	peer := piServicePeerCredentials{PID: 424242, UID: os.Geteuid(), GID: os.Getegid()}
	request := piServiceRequest{RequestID: "request-admission-hash", TaskID: "task-admission-hash", Prompt: "resume bound task"}
	digest := mustPiServicePayloadDigest(t, request)
	acceptedRecord, err := requests.begin(context.Background(), peer, request, digest)
	if err != nil {
		t.Fatal(err)
	}
	state, exists, err := requests.lookup(peer, request, digest)
	if err != nil || !exists || state.acceptedRecord.Hash != acceptedRecord.Hash ||
		!validBareWorkflowDigest(acceptedRecord.Hash) || validDigestText(acceptedRecord.Hash) {
		t.Fatalf("real request-journal hash domain changed: record=%+v state=%+v exists=%v err=%v", acceptedRecord, state, exists, err)
	}

	catalogAudit, err := audit.Open(filepath.Join(root, "workflow-catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := &piServiceWorkflowCatalog{journal: catalogAudit, entries: make(map[string]piServiceWorkflowEntry),
		byHostAdmissionID: make(map[string]string), ownerScopes: make(map[string]piServiceWorkflowOwnerScope)}
	defer catalog.Close()
	config := piServiceConfig{WorkflowAdmissionMode: "fresh", WorkflowAdmissionID: "host-admission-regression",
		WorkflowTaskID: request.TaskID, WorkflowPrompt: request.Prompt, WorkflowOwnerStoreRoot: filepath.Join(root, "owners"),
		WorkflowDurationSeconds: 3600, WorkflowMaxPreEffectRetries: 1, WorkflowMaxCleanRestarts: 1,
		WorkflowMaxHostCalls: 8, WorkflowMaxTokens: 1024}
	configDigest := "sha256:" + strings.Repeat("a", 64)
	registrationID := "sha256:" + strings.Repeat("b", 64)
	admission := piServiceAdmissionContext{Peer: peer, CurrentPeer: peer, Request: request, PayloadDigest: digest,
		Accepted: state.accepted, AcceptedRecord: state.acceptedRecord}
	workflowAdmission, err := catalog.ResolveAdmission(admission, config, configDigest, registrationID)
	if err != nil {
		t.Fatalf("bind real request-journal record to workflow admission: %v", err)
	}
	if workflowAdmission.Entry.AdmissionRecordHash != acceptedRecord.Hash ||
		workflowAdmission.Entry.AdmissionSequence != acceptedRecord.Sequence ||
		!validWorkflowCatalogEntry(workflowAdmission.Entry) {
		t.Fatalf("workflow catalog did not preserve the bare request-journal identity: %+v", workflowAdmission.Entry)
	}
	trace, err := catalogAudit.Trace()
	if err != nil || len(trace.Records) != 1 {
		t.Fatalf("workflow admission journal trace: records=%d err=%v", len(trace.Records), err)
	}
	var event piServiceWorkflowEvent
	if err := unmarshalGovernedJSON(trace.Records[0].Event.Data, &event); err != nil ||
		event.AdmissionHash != acceptedRecord.Hash || event.Entry.AdmissionRecordHash != acceptedRecord.Hash {
		t.Fatalf("persisted workflow admission changed the journal hash domain: %+v err=%v", event, err)
	}
}

func TestPiServiceJournalRecoversExactIDInFreshProcess(t *testing.T) {
	root, journalPath, peer, request, acceptedRecord := seedPiServiceRecoveryJournal(t, "")
	socketPath := filepath.Join(root, "recovery.sock")
	var childOutput bytes.Buffer
	command := exec.Command(os.Args[0], "-test.run=^TestPiServiceJournalRecoveryProcessHelper$")
	command.Env = append(os.Environ(), "TB_TBOUND_PI_RECOVERY_HELPER=serve-replay",
		"TB_TBOUND_PI_RECOVERY_JOURNAL="+journalPath,
		"TB_TBOUND_PI_RECOVERY_SOCKET="+socketPath,
		"TB_TBOUND_PI_RECOVERY_HASH="+acceptedRecord.Hash,
		"TB_TBOUND_PI_RECOVERY_SEQUENCE="+strconv.FormatUint(acceptedRecord.Sequence, 10),
		"TB_TBOUND_PI_RECOVERY_UID="+strconv.Itoa(peer.UID),
		"TB_TBOUND_PI_RECOVERY_GID="+strconv.Itoa(peer.GID),
		"TB_TBOUND_PI_RECOVERY_ORIGINAL_PID="+strconv.Itoa(peer.PID),
		"TB_TBOUND_PI_RECOVERY_CURRENT_PID="+strconv.Itoa(os.Getpid()))
	command.Stdout, command.Stderr = &childOutput, &childOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- command.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		select {
		case err := <-childDone:
			t.Fatalf("fresh-process recovery service exited before listening: %v\n%s", err, childOutput.String())
		case <-time.After(10 * time.Millisecond):
			if time.Now().After(deadline) {
				_ = command.Process.Kill()
				<-childDone
				t.Fatalf("fresh-process recovery service did not create its socket\n%s", childOutput.String())
			}
		}
	}

	newRequest := piServiceRequest{RequestID: "request-after-crash", TaskID: "task-new", Prompt: "new workflow"}
	refused := roundTripPiServiceRequest(t, socketPath, newRequest)
	if refused.Status != "refused" || refused.ErrorCode != "prior-workflow-unsettled" {
		t.Fatalf("new ID was not blocked by unresolved journal: %+v", refused)
	}
	changed := request
	changed.Prompt = "changed payload"
	conflict := roundTripPiServiceRequest(t, socketPath, changed)
	if conflict.Status != "refused" || conflict.ErrorCode != "request-id-conflict" {
		t.Fatalf("mismatched exact ID was not denied: %+v", conflict)
	}
	response := roundTripPiServiceRequest(t, socketPath, request)
	if response.Status != "completed" || response.RequestID != request.RequestID {
		t.Fatalf("exact prior ID did not recover: %+v", response)
	}
	var authority struct {
		IsReplay         bool   `json:"is_replay"`
		OriginalPeerPID  int    `json:"original_peer_pid"`
		CurrentPeerPID   int    `json:"current_peer_pid"`
		AcceptedHash     string `json:"accepted_hash"`
		AcceptedSequence uint64 `json:"accepted_sequence"`
		AcceptedPayload  string `json:"accepted_payload"`
	}
	if err := unmarshalGovernedJSON(response.Receipt, &authority); err != nil || !authority.IsReplay ||
		authority.OriginalPeerPID != peer.PID || authority.CurrentPeerPID != os.Getpid() ||
		authority.AcceptedHash != acceptedRecord.Hash || authority.AcceptedSequence != acceptedRecord.Sequence ||
		authority.AcceptedPayload != mustPiServicePayloadDigest(t, request) {
		t.Fatalf("fresh-process recovery did not retain the exact original admission authority: %+v err=%v", authority, err)
	}
	if err := <-childDone; err != nil {
		t.Fatalf("fresh-process recovery service: %v\n%s", err, childOutput.String())
	}

	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	trace, traceErr := journal.Trace()
	closeErr := journal.Close()
	if traceErr != nil || closeErr != nil {
		t.Fatalf("reopen recovered request journal: trace=%v close=%v", traceErr, closeErr)
	}
	if len(trace.Records) != 2 || trace.Records[0].Event.Kind != "pi_service_request_accepted" ||
		trace.Records[1].Event.Kind != "pi_service_request_terminal" {
		t.Fatalf("recovery changed journal admission identity or appended a new ID: %+v", trace.Records)
	}
}

func TestPiServiceJournalRecoveryProcessHelper(t *testing.T) {
	mode := os.Getenv("TB_TBOUND_PI_RECOVERY_HELPER")
	if mode == "" {
		return
	}
	journalPath := os.Getenv("TB_TBOUND_PI_RECOVERY_JOURNAL")
	uid, uidErr := strconv.Atoi(os.Getenv("TB_TBOUND_PI_RECOVERY_UID"))
	gid, gidErr := strconv.Atoi(os.Getenv("TB_TBOUND_PI_RECOVERY_GID"))
	if journalPath == "" || uidErr != nil || gidErr != nil {
		t.Fatalf("invalid recovery helper environment: journal=%q uidErr=%v gidErr=%v", journalPath, uidErr, gidErr)
	}
	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := newPiServiceRequestJournal(journal)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if mode == "begin-new" {
		request := piServiceRequest{RequestID: "request-after-restart", TaskID: "task-after-restart", Prompt: "new workflow"}
		digest := mustPiServicePayloadDigest(t, request)
		_, err := requests.begin(context.Background(), piServicePeerCredentials{PID: os.Getpid(), UID: uid, GID: gid}, request, digest)
		closeErr := requests.close()
		if !errors.Is(err, ErrPiServiceUnresolvedPrior) || closeErr != nil {
			t.Fatalf("new ID begin error=%v close=%v, want unresolved prior", err, closeErr)
		}
		return
	}
	if mode != "serve-replay" {
		_ = requests.close()
		t.Fatalf("unknown recovery helper mode %q", mode)
	}
	originalPID, originalErr := strconv.Atoi(os.Getenv("TB_TBOUND_PI_RECOVERY_ORIGINAL_PID"))
	currentPID, currentErr := strconv.Atoi(os.Getenv("TB_TBOUND_PI_RECOVERY_CURRENT_PID"))
	sequence, sequenceErr := strconv.ParseUint(os.Getenv("TB_TBOUND_PI_RECOVERY_SEQUENCE"), 10, 64)
	acceptedHash := os.Getenv("TB_TBOUND_PI_RECOVERY_HASH")
	if originalErr != nil || currentErr != nil || sequenceErr != nil || !validBareWorkflowDigest(acceptedHash) {
		_ = requests.close()
		t.Fatalf("invalid replay helper authority: original=%v current=%v sequence=%v", originalErr, currentErr, sequenceErr)
	}
	request := piServiceRequest{RequestID: "request-before-crash", TaskID: "task-before-crash", Prompt: "resume original workflow"}
	digest := mustPiServicePayloadDigest(t, request)
	originalPeer := piServicePeerCredentials{PID: originalPID, UID: uid, GID: gid}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: os.Getenv("TB_TBOUND_PI_RECOVERY_SOCKET"), Net: "unix"})
	if err != nil {
		_ = requests.close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := func(ctx context.Context, _ piServiceRequest) (json.RawMessage, error) {
		admission, ok := piServiceAdmissionFromContext(ctx)
		if !ok || !admission.IsReplay || admission.Peer != originalPeer ||
			admission.CurrentPeer.PID != currentPID || admission.CurrentPeer.UID != uid || admission.CurrentPeer.GID != gid ||
			admission.AcceptedRecord.Hash != acceptedHash || admission.AcceptedRecord.Sequence != sequence ||
			admission.Accepted.RequestID != request.RequestID || admission.Accepted.TaskID != request.TaskID ||
			admission.Accepted.PayloadDigest != digest {
			return nil, errors.New("recovered admission did not retain the original accepted authority")
		}
		return json.Marshal(struct {
			IsReplay         bool   `json:"is_replay"`
			OriginalPeerPID  int    `json:"original_peer_pid"`
			CurrentPeerPID   int    `json:"current_peer_pid"`
			AcceptedHash     string `json:"accepted_hash"`
			AcceptedSequence uint64 `json:"accepted_sequence"`
			AcceptedPayload  string `json:"accepted_payload"`
		}{admission.IsReplay, admission.Peer.PID, admission.CurrentPeer.PID, admission.AcceptedRecord.Hash,
			admission.AcceptedRecord.Sequence, admission.Accepted.PayloadDigest})
	}
	go func() {
		for {
			state, exists, lookupErr := requests.lookup(originalPeer, request, digest)
			if lookupErr != nil || exists && state.terminal != nil {
				cancel()
				return
			}
			if ctx.Err() != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	serveErr := servePiServiceRequests(ctx, listener, piServiceSocketRegistration{
		AllowedClientUIDs: []int{uid}, AllowedClientPrimaryGID: gid,
	}, requests, nil, run, nil)
	closeErr := requests.close()
	if serveErr != nil || closeErr != nil {
		t.Fatalf("recovery service err=%v journal close=%v", serveErr, closeErr)
	}
}

func seedPiServiceRecoveryJournal(t *testing.T, terminalStatus string) (string, string, piServicePeerCredentials,
	piServiceRequest, audit.Record) {
	t.Helper()
	root := privateGovernedTestRoot(t)
	path := filepath.Join(root, "requests.jsonl")
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := newPiServiceRequestJournal(journal)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	peer := piServicePeerCredentials{PID: 424242, UID: os.Geteuid(), GID: os.Getegid()}
	request := piServiceRequest{RequestID: "request-before-crash", TaskID: "task-before-crash", Prompt: "resume original workflow"}
	digest := mustPiServicePayloadDigest(t, request)
	acceptedRecord, err := requests.begin(context.Background(), peer, request, digest)
	if err != nil {
		_ = requests.close()
		t.Fatal(err)
	}
	if terminalStatus != "" {
		response := piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID, Status: terminalStatus}
		if err := requests.complete(peer.UID, request, digest, response); err != nil {
			_ = requests.close()
			t.Fatal(err)
		}
	}
	if err := requests.close(); err != nil {
		t.Fatal(err)
	}
	return root, path, peer, request, acceptedRecord
}

type piServiceTestServer struct {
	path     string
	listener *net.UnixListener
	journal  *audit.Journal
	requests *piServiceRequestJournal
	cancel   context.CancelFunc
	done     chan error
	calls    atomic.Int32
}

func newPiServiceTestServer(t *testing.T, socket piServiceSocketRegistration,
	run piServiceTaskRunner) *piServiceTestServer {
	t.Helper()
	return newPiServiceTestServerLocked(t, socket, run, nil)
}

func newPiServiceTestServerLocked(t *testing.T, socket piServiceSocketRegistration,
	run piServiceTaskRunner, maintenance func() (func(), error)) *piServiceTestServer {
	t.Helper()
	root := privateGovernedTestRoot(t)
	path := filepath.Join(root, "service.sock")
	address := &net.UnixAddr{Name: path, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(root, "requests.jsonl"))
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	requests, err := newPiServiceRequestJournal(journal)
	if err != nil {
		_ = listener.Close()
		_ = journal.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &piServiceTestServer{path: path, listener: listener, journal: journal, requests: requests,
		cancel: cancel, done: make(chan error, 1)}
	wrappedRunner := func(ctx context.Context, request piServiceRequest) (json.RawMessage, error) {
		server.calls.Add(1)
		return run(ctx, request)
	}
	go func() {
		server.done <- servePiServiceRequests(ctx, listener, socket, journalRequestsOnly(requests), nil, wrappedRunner, maintenance)
	}()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		select {
		case <-server.done:
		case <-time.After(5 * time.Second):
			t.Error("Pi service fixture did not stop within cleanup deadline")
		}
		_ = journal.Close()
	})
	return server
}

func journalRequestsOnly(requests *piServiceRequestJournal) *piServiceRequestJournal { return requests }

func dialPiServiceTest(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func roundTripPiServiceRequest(t *testing.T, path string, request piServiceRequest) piServiceResponse {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return roundTripPiServiceRaw(t, path, encoded)
}

func roundTripPiServiceRaw(t *testing.T, path string, payload []byte) piServiceResponse {
	t.Helper()
	conn := dialPiServiceTest(t, path)
	defer conn.Close()
	if err := writePiServiceRequest(conn, payload); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := readPiServiceResponse(conn)
	if err != nil {
		t.Fatalf("read local service response: %v", err)
	}
	return response
}

func mustPiServicePayloadDigest(t *testing.T, request piServiceRequest) string {
	t.Helper()
	digest, err := piServicePayloadDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
