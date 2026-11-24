//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/researchroute"
	"tbound/supervisor/internal/vrun"
)

const (
	piServiceStatusReady = "ready"
	vServiceSlack        = 2 * time.Minute
	vForwarderDirName    = "v-fwd"
)

type piServiceVHandler func(ctx context.Context, request piServiceRequest, conn *net.UnixConn) *piServiceResponse

func (j *piServiceRequestJournal) setVHandler(handler piServiceVHandler) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.vHandler = handler
}

func (j *piServiceRequestJournal) vService() piServiceVHandler {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.vHandler
}

func serveVRequest(ctx context.Context, handler piServiceVHandler, conn *net.UnixConn, request piServiceRequest,
	active *atomic.Bool, maintenance func() (func(), error), tasks *sync.WaitGroup) {
	refuse := func(status, code string) {
		_ = writePiServiceFrame(conn, piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID, Status: status, ErrorCode: code})
		_ = conn.Close()
	}
	if handler == nil {
		refuse("refused", "role-unavailable")
		return
	}
	if !active.CompareAndSwap(false, true) {
		refuse("busy", "one-task-at-a-time")
		return
	}
	release := func() {}
	if maintenance != nil {
		var err error
		if release, err = maintenance(); err != nil {
			active.Store(false)
			if errors.Is(err, errMaintenanceBusy) {
				refuse("busy", "maintenance-in-progress")
			} else {
				refuse("refused", "maintenance-lock-unavailable")
			}
			return
		}
	}
	tasks.Add(1)
	go func() {
		defer tasks.Done()
		defer conn.Close()
		final := handler(ctx, request, conn)
		release()
		active.Store(false)
		if final != nil {
			_ = writePiServiceFrame(conn, *final)
		}
	}()
}

type vForwardService struct {
	bundle     *researchroute.Bundle
	corpus     *productionCorpus
	unitsDir   string
	admissions string
	slack      time.Duration
}

func newVForwardService(route productionProviderRoute, corpus *productionCorpus, config piServiceConfig) piServiceVHandler {
	if route.research == nil || corpus == nil {
		return nil
	}
	admissions := "/var/lib/tbound/sessions/v-admissions"
	if config.ProviderRoute == piruntime.D09ManagedPiProviderRouteVStub {
		admissions = "/var/lib/tbound/sessions/v-stub-admissions"
	}
	service := &vForwardService{bundle: route.research, corpus: corpus,
		unitsDir: filepath.Join(config.XDGRuntimeDir, vForwarderDirName), admissions: admissions, slack: vServiceSlack}
	return service.serve
}

func (s *vForwardService) serve(ctx context.Context, request piServiceRequest, conn *net.UnixConn) *piServiceResponse {
	answer := func(status, code string, receipt any) *piServiceResponse {
		response := &piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID, Status: status, ErrorCode: code}
		if receipt != nil {
			response.Receipt, _ = json.Marshal(receipt)
		}
		return response
	}
	trial, known := s.corpus.trial(request.TaskID)
	if !known || request.Role != corpusRoleV || request.Attempt != 1 {
		return answer("refused", "invalid-request", nil)
	}
	admissionID := corpusAdmissionID(request.TaskID, corpusRoleV, request.Attempt)
	if err := s.claim(admissionID); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return answer("refused", "admission-already-used", nil)
		}
		return answer("refused", "v-forwarder-unavailable", nil)
	}
	unit, err := s.open(admissionID, trial.MaxHostCalls)
	if err != nil {
		return answer("unknown", "v-forwarder-unavailable", nil)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil ||
		writePiServiceFrame(conn, *answer(piServiceStatusReady, "", map[string]string{"dir": unit.dir})) != nil {
		unit.close()
		return nil
	}
	gone := make(chan struct{})
	go func() {
		var one [1]byte
		_, _ = conn.Read(one[:])
		close(gone)
	}()
	timer := time.NewTimer(time.Duration(trial.DurationSeconds)*time.Second + s.slack)
	defer timer.Stop()
	select {
	case <-gone:
	case <-ctx.Done():
	case <-timer.C:
	}
	return answer("completed", "", unit.close())
}

func (s *vForwardService) claim(admissionID string) error {
	if err := os.MkdirAll(s.admissions, 0o700); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(admissionID))
	marker, err := os.OpenFile(filepath.Join(s.admissions, hex.EncodeToString(sum[:])), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := marker.WriteString(admissionID + "\n")
	if err := errors.Join(writeErr, marker.Sync(), marker.Close()); err != nil {
		return err
	}
	return syncVAdmissions(s.admissions)
}

var syncVAdmissions = func(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(handle.Sync(), handle.Close())
}

const (
	vMaxConnections       = 8
	vServerReadTimeout    = 2 * time.Minute
	vServerIdleTimeout    = 30 * time.Second
	vServerMaxHeaderBytes = 16 << 10
)

func newVServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 15 * time.Second, ReadTimeout: vServerReadTimeout,
		IdleTimeout: vServerIdleTimeout, MaxHeaderBytes: vServerMaxHeaderBytes}
}

type vConnectionLimiter struct {
	net.Listener
	open atomic.Int32
	max  int32
}

type vLimitedConn struct {
	net.Conn
	release func()
}

func (c *vLimitedConn) Close() error {
	c.release()
	return c.Conn.Close()
}

func limitVConnections(listener net.Listener, limit int32) net.Listener {
	return &vConnectionLimiter{Listener: listener, max: limit}
}

func (l *vConnectionLimiter) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.open.Add(1) > l.max {
			l.open.Add(-1)
			_ = conn.Close()
			continue
		}
		return &vLimitedConn{Conn: conn, release: sync.OnceFunc(func() { l.open.Add(-1) })}, nil
	}
}

type vUnit struct {
	dir    string
	server *http.Server
	fwd    *researchroute.Forwarder
}

func (s *vForwardService) open(admissionID string, maxHostCalls uint64) (*vUnit, error) {
	if err := os.MkdirAll(s.unitsDir, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(s.unitsDir, "v-")
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*vUnit, error) { return nil, errors.Join(cause, os.RemoveAll(dir)) }
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return fail(err)
	}
	token := hex.EncodeToString(secret[:])
	if err := os.WriteFile(filepath.Join(dir, vrun.TokenName), []byte(token), 0o600); err != nil {
		return fail(err)
	}
	fwd, err := s.bundle.NewForwarder(admissionID, token, maxHostCalls)
	if err != nil {
		return fail(err)
	}
	socket := filepath.Join(dir, vrun.SocketName)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fail(err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		return fail(err)
	}
	server := newVServer(fwd)
	go func() { _ = server.Serve(limitVConnections(listener, vMaxConnections)) }()
	return &vUnit{dir: dir, server: server, fwd: fwd}, nil
}

func (u *vUnit) close() researchroute.ForwardTotals {
	_ = u.server.Close()
	totals := u.fwd.Finish()
	_ = os.RemoveAll(u.dir)
	return totals
}
