//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/researchroute"
	"tbound/supervisor/internal/vrun"
)

const vTestProfile = `{
  "schema_version": 1,
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "deepseek/deepseek-v4.1-flash",
  "routing": {"order": ["example-provider"], "allow_fallbacks": false, "data_collection": "deny",
              "quantizations": [], "require_parameters": true, "zdr": true},
  "input_micro_usd_per_1m_tokens": 150000,
  "output_micro_usd_per_1m_tokens": 600000,
  "max_price_micro_usd_per_1m_tokens_in": 300000,
  "max_price_micro_usd_per_1m_tokens_out": 1200000,
  "max_input_tokens": 20000,
  "input_framing_reserve_tokens": 512,
  "max_output_tokens": 2000,
  "max_requests_per_unit": 5,
  "temperature_milli": 0,
  "seed": 7,
  "reasoning": {"enabled": false},
  "timeout_ms": 5000,
  "frozen_at": "2026-10-17T00:00:00Z"
}`

const vTestLedgerConfig = `{"schema_version":1,"ledger_id":"v-stub-test","cap_micro_usd":5000000,"min_per_unit_worst_case_micro_usd":20000,"reserve_basis":"request"}`

const vTestRequestBody = `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`

type vTestUpstream struct {
	*httptest.Server
	requests atomic.Int32
	mu       sync.Mutex
	headers  []http.Header
}

func newVTestBundle(t *testing.T) (*researchroute.Bundle, *vTestUpstream) {
	t.Helper()
	upstream := &vTestUpstream{}
	upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.requests.Add(1)
		upstream.mu.Lock()
		upstream.headers = append(upstream.headers, r.Header.Clone())
		upstream.mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"model\":\"deepseek/deepseek-v4.1-flash\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"cost\":0.000002}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := researchroute.Paths{Profile: filepath.Join(dir, "profile.json"), LedgerConfig: filepath.Join(dir, "ledger.json"),
		Ledger: filepath.Join(dir, "stub-ledger"), Checkpoint: filepath.Join(dir, "checkpoint.json"), Key: filepath.Join(dir, "stub.key"),
		OwnerUID: uint32(os.Getuid())}
	for path, content := range map[string]string{paths.Profile: vTestProfile, paths.LedgerConfig: vTestLedgerConfig, paths.Key: "dummy-stub-key-123\n"} {
		if err := os.WriteFile(path, []byte(content), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := researchroute.Genesis(paths)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := fmt.Sprintf(`{"records":%d,"head_sha256":%q}`, snapshot.Records, snapshot.HeadSHA256)
	if err := os.WriteFile(paths.Checkpoint, []byte(checkpoint), 0o400); err != nil {
		t.Fatal(err)
	}
	bundle, err := researchroute.OpenStub(paths, upstream.URL+"/api/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	return bundle, upstream
}

type vTestHarness struct {
	*corpusServiceHarness
	service    *vForwardService
	bundle     *researchroute.Bundle
	upstream   *vTestUpstream
	caseID     string
	admissions string
}

func newVTestHarness(t *testing.T) *vTestHarness {
	t.Helper()
	h := newCorpusServiceHarness(t)
	bundle, upstream := newVTestBundle(t)
	root := privateGovernedTestRoot(t)
	corpus := h.catalog.corpus
	service := &vForwardService{bundle: bundle, corpus: corpus, unitsDir: filepath.Join(root, "v-fwd"),
		admissions: filepath.Join(root, "v-admissions"), slack: time.Minute}
	h.server.requests.setVHandler(service.serve)
	config := h.config
	config.ProviderRoute = piruntime.D09ManagedPiProviderRouteVStub
	h.server.requests.setAdmissionScreen(h.catalog.admissionScreen(config))
	return &vTestHarness{corpusServiceHarness: h, service: service, bundle: bundle, upstream: upstream,
		caseID: corpusCaseForRole(t, h.manifest, corpusRoleCompliant), admissions: service.admissions}
}

func vRequest(id, caseID string) piServiceRequest { return corpusRequest(id, caseID, corpusRoleV, 1) }

func openVUnit(t *testing.T, h *vTestHarness, id, caseID string) (*net.UnixConn, string, string) {
	t.Helper()
	conn := dialPiServiceTest(t, h.server.path)
	t.Cleanup(func() { _ = conn.Close() })
	encoded, err := json.Marshal(vRequest(id, caseID))
	if err != nil {
		t.Fatal(err)
	}
	if err := writePiServiceRequest(conn, encoded); err != nil {
		t.Fatal(err)
	}
	ready, err := readPiServiceResponse(conn)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != piServiceStatusReady {
		t.Fatalf("response %+v", ready)
	}
	var receipt struct {
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal(ready.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(filepath.Join(receipt.Dir, vrun.TokenName))
	if err != nil {
		t.Fatal(err)
	}
	return conn, receipt.Dir, string(token)
}

func vUnitClient(dir string) *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, vrun.SocketName))
		}}}
}

func vPost(t *testing.T, dir, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://forwarder/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := vUnitClient(dir).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func finishVUnit(t *testing.T, conn *net.UnixConn) (piServiceResponse, researchroute.ForwardTotals) {
	t.Helper()
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	final, err := readPiServiceResponse(conn)
	if err != nil {
		t.Fatal(err)
	}
	var totals researchroute.ForwardTotals
	if final.Status == "completed" {
		if err := json.Unmarshal(final.Receipt, &totals); err != nil {
			t.Fatal(err)
		}
	}
	return final, totals
}

func TestVServiceServesOneUnitAndEverythingIsDeadAfterwards(t *testing.T) {
	h := newVTestHarness(t)
	conn, dir, token := openVUnit(t, h, "tbr-v-1", h.caseID)
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 || filepath.Dir(dir) != h.service.unitsDir {
		t.Fatalf("unit directory %v %v", info, err)
	}
	for name, mode := range map[string]os.FileMode{vrun.SocketName: 0o600, vrun.TokenName: 0o600} {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", name, st, err)
		}
	}
	if len(token) < 32 || strings.Contains(token, "dummy-stub-key") {
		t.Fatalf("credential %q", token)
	}
	if status, _ := vPost(t, dir, token, vTestRequestBody); status != 200 {
		t.Fatalf("status %d", status)
	}
	if status, _ := vPost(t, dir, "wrong-"+token, vTestRequestBody); status != 401 {
		t.Fatalf("wrong credential: status %d", status)
	}
	if h.upstream.requests.Load() != 1 {
		t.Fatalf("upstream saw %d requests", h.upstream.requests.Load())
	}
	if got := h.upstream.headers[0].Get("Authorization"); got != "Bearer dummy-stub-key-123" {
		t.Fatalf("upstream credential %q", got)
	}
	final, totals := finishVUnit(t, conn)
	if final.Status != "completed" || totals.Booked != 1 || totals.Settled != 1 || totals.TokensIn != 11 || totals.TokensOut != 3 || totals.CostMicroUSD != 2 {
		t.Fatalf("final %+v totals %+v", final, totals)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the unit directory is still there: %v", err)
	}
	if c, err := net.DialTimeout("unix", filepath.Join(dir, vrun.SocketName), time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the forwarder socket still accepts")
	}
	snapshot, err := h.bundle.Snapshot()
	if err != nil || snapshot.SettledUnits != 1 || snapshot.OpenMicroUSD != 0 {
		t.Fatalf("stub ledger %+v %v", snapshot, err)
	}
	if h.journalRecords(t) != 0 {
		t.Fatal("a V unit wrote the service request journal")
	}
}

func TestVServiceAdmitsACaseOnce(t *testing.T) {
	h := newVTestHarness(t)
	conn, _, _ := openVUnit(t, h, "tbr-v-once-1", h.caseID)
	finishVUnit(t, conn)
	if _, err := os.Stat(filepath.Join(h.admissions)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"tbr-v-once-2", "tbr-v-once-1"} {
		response := roundTripPiServiceRequest(t, h.server.path, vRequest(id, h.caseID))
		if response.Status != "completed" && response.Status != "refused" || response.ErrorCode != "admission-already-used" {
			t.Fatalf("second admission under %s: %+v", id, response)
		}
	}
	restarted := &vForwardService{bundle: h.bundle, corpus: h.service.corpus, unitsDir: h.service.unitsDir, admissions: h.admissions, slack: time.Minute}
	h.server.requests.setVHandler(restarted.serve)
	if response := roundTripPiServiceRequest(t, h.server.path, vRequest("tbr-v-once-3", h.caseID)); response.ErrorCode != "admission-already-used" {
		t.Fatalf("after a restart: %+v", response)
	}
	if h.upstream.requests.Load() != 0 {
		t.Fatal("a refused admission reached the provider")
	}
}

func TestVServiceHoldsTheTaskSlotForTheUnit(t *testing.T) {
	h := newVTestHarness(t)
	conn, _, _ := openVUnit(t, h, "tbr-v-slot-1", h.caseID)
	research := h.config
	research.ProviderRoute = piruntime.D09ManagedPiProviderRouteResearch
	h.server.requests.setAdmissionScreen(h.catalog.admissionScreen(research))
	other := corpusRequest("tbr-slot-governed", h.caseID, corpusRoleCompliant, 1)
	if response := roundTripPiServiceRequest(t, h.server.path, other); response.Status != "busy" || response.ErrorCode != "one-task-at-a-time" {
		t.Fatalf("a governed request during a V unit: %+v", response)
	}
	final, _ := finishVUnit(t, conn)
	if final.Status != "completed" {
		t.Fatalf("final %+v", final)
	}
	if response := roundTripPiServiceRequest(t, h.server.path, vRequest("tbr-v-slot-2", "cmp-read-02")); response.Status == "busy" {
		t.Fatalf("the slot was not freed: %+v", response)
	}
}

func TestVServiceEndsTheUnitWhenTheClientDisappears(t *testing.T) {
	h := newVTestHarness(t)
	conn, dir, _ := openVUnit(t, h, "tbr-v-gone-1", h.caseID)
	_ = conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the unit directory outlived its client")
}

func TestVServiceEndsTheUnitAtTheCaseDeadlinePlusSlack(t *testing.T) {
	h := newVTestHarness(t)
	h.service.slack = 50*time.Millisecond - time.Duration(h.service.corpus.trials[h.caseID].DurationSeconds)*time.Second
	conn, dir, _ := openVUnit(t, h, "tbr-v-deadline-1", h.caseID)
	final, err := readPiServiceResponse(conn)
	if err != nil || final.Status != "completed" {
		t.Fatalf("final %+v %v", final, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the unit directory is still there: %v", err)
	}
}

func TestVServiceEndsTheUnitWhenTheServiceStops(t *testing.T) {
	h := newVTestHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	client, server := unixPairForTest(t)
	done := make(chan *piServiceResponse, 1)
	go func() { done <- h.service.serve(ctx, vRequest("tbr-v-stop-1", h.caseID), server) }()
	ready, err := readPiServiceResponse(client)
	if err != nil || ready.Status != piServiceStatusReady {
		t.Fatalf("%+v %v", ready, err)
	}
	cancel()
	select {
	case final := <-done:
		if final == nil || final.Status != "completed" {
			t.Fatalf("final %+v", final)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the unit outlived the service context")
	}
}

func unixPairForTest(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	root := privateGovernedTestRoot(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(root, "p.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		conn, _ := listener.AcceptUnix()
		accepted <- conn
	}()
	client := dialPiServiceTest(t, filepath.Join(root, "p.sock"))
	server := <-accepted
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client, server
}

func TestServeVRequestHoldsTheSlotAndTheMaintenanceLockOnlyForTheUnit(t *testing.T) {
	var active atomic.Bool
	var tasks sync.WaitGroup
	var locks, releases atomic.Int32
	maintenance := func() (func(), error) {
		locks.Add(1)
		return func() { releases.Add(1) }, nil
	}
	release := make(chan struct{})
	handler := func(context.Context, piServiceRequest, *net.UnixConn) *piServiceResponse {
		<-release
		return &piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: "r", Status: "completed"}
	}
	client, server := unixPairForTest(t)
	serveVRequest(context.Background(), handler, server, piServiceRequest{RequestID: "r"}, &active, maintenance, &tasks)
	if !active.Load() || locks.Load() != 1 || releases.Load() != 0 {
		t.Fatalf("active %v locks %d releases %d", active.Load(), locks.Load(), releases.Load())
	}
	client2, server2 := unixPairForTest(t)
	serveVRequest(context.Background(), handler, server2, piServiceRequest{RequestID: "r2"}, &active, maintenance, &tasks)
	if busy, err := readPiServiceResponse(client2); err != nil || busy.Status != "busy" || busy.ErrorCode != "one-task-at-a-time" || locks.Load() != 1 {
		t.Fatalf("second unit: %+v %v locks %d", busy, err, locks.Load())
	}
	close(release)
	if final, err := readPiServiceResponse(client); err != nil || final.Status != "completed" {
		t.Fatalf("final %+v %v", final, err)
	}
	tasks.Wait()
	if active.Load() || releases.Load() != 1 {
		t.Fatalf("active %v releases %d", active.Load(), releases.Load())
	}
	client3, server3 := unixPairForTest(t)
	busyLock := func() (func(), error) { return nil, errMaintenanceBusy }
	serveVRequest(context.Background(), handler, server3, piServiceRequest{RequestID: "r3"}, &active, busyLock, &tasks)
	if refused, err := readPiServiceResponse(client3); err != nil || refused.Status != "busy" || refused.ErrorCode != "maintenance-in-progress" || active.Load() {
		t.Fatalf("maintenance: %+v %v active %v", refused, err, active.Load())
	}
	client4, server4 := unixPairForTest(t)
	serveVRequest(context.Background(), nil, server4, piServiceRequest{RequestID: "r4"}, &active, maintenance, &tasks)
	if refused, err := readPiServiceResponse(client4); err != nil || refused.Status != "refused" || refused.ErrorCode != "role-unavailable" {
		t.Fatalf("no handler: %+v %v", refused, err)
	}
}

func TestVRoleAdmission(t *testing.T) {
	h := newCorpusServiceHarness(t)
	compliant := corpusCaseForRole(t, h.manifest, corpusRoleCompliant)
	ambiguous := corpusCaseForRole(t, h.manifest, corpusRoleAmbiguous)
	adversarial := corpusCaseForRole(t, h.manifest, corpusRoleTrajectory)
	screen := func(route string, request piServiceRequest) string {
		config := h.config
		config.ProviderRoute = route
		return h.catalog.screenRequest(config, piServicePeerCredentials{UID: os.Getuid()}, request)
	}
	if !validCorpusRole(corpusRoleV) {
		t.Fatal("v is not a corpus role")
	}
	for route, want := range map[string]string{
		piruntime.D09ManagedPiProviderRouteResearch:    "",
		piruntime.D09ManagedPiProviderRouteVStub:       "",
		piruntime.D09ManagedPiProviderRouteEngineering: "role-unavailable",
	} {
		if got := screen(route, vRequest("r", compliant)); got != want {
			t.Errorf("v on the %s route: %q, want %q", route, got, want)
		}
	}
	for caseID, kind := range map[string]string{ambiguous: "ambiguous", adversarial: "adversarial"} {
		if got := screen(piruntime.D09ManagedPiProviderRouteResearch, vRequest("r", caseID)); got != "role-kind-mismatch" {
			t.Errorf("v on a %s case: %q", kind, got)
		}
	}
	second := vRequest("r", compliant)
	second.Attempt = 2
	if got := screen(piruntime.D09ManagedPiProviderRouteResearch, second); got != "invalid-attempt" {
		t.Errorf("v attempt 2: %q", got)
	}
	if got := screen(piruntime.D09ManagedPiProviderRouteVStub, corpusRequest("r", compliant, corpusRoleCompliant, 1)); got != "role-unavailable" {
		t.Errorf("a governed role on the v-stub route: %q", got)
	}
	if got := screen(piruntime.D09ManagedPiProviderRouteResearch, corpusRequest("r", compliant, corpusRoleCompliant, 1)); got != "" {
		t.Errorf("a governed role on the research route: %q", got)
	}
}

func TestVStubRouteIsAValidServiceConfigRoute(t *testing.T) {
	config := corpusTestServiceConfig(corpusTestDigest("manifest"))
	for _, route := range []string{piruntime.D09ManagedPiProviderRouteEngineering, piruntime.D09ManagedPiProviderRouteResearch, piruntime.D09ManagedPiProviderRouteVStub} {
		config.ProviderRoute = route
		if err := validateProductionPiServiceConfig(config); err != nil {
			t.Errorf("%s: %v", route, err)
		}
	}
	config.ProviderRoute = "v-stubs"
	if validateProductionPiServiceConfig(config) == nil {
		t.Error("an unknown route was accepted")
	}
}

func TestVClientRefusesAForwarderDirectoryOutsideTheServiceRuntime(t *testing.T) {
	good := filepath.Join(piruntime.D09ManagedPiRuntimeDir, vForwarderDirName, "v-123")
	if !vForwarderDirAcceptable(good) {
		t.Fatalf("%s refused", good)
	}
	for _, bad := range []string{"", "v-123", "/etc/tbound", good + "/sub", good + ",dst=/", "/var/lib/tbound/v-work/x",
		filepath.Join(piruntime.D09ManagedPiRuntimeDir, "v-123"), "/run/tbound-pi/v-fwd/../tmp/x"} {
		if vForwarderDirAcceptable(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
