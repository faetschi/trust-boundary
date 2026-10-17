//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/researchroute"
	"tbound/supervisor/internal/sessionrepo"
)

const (
	piServiceSocketPath          = "/run/tbound/serve-pi.sock"
	piServiceSocketUnitPath      = "/etc/systemd/system/tbound-pi.socket"
	piServiceUnitPath            = "/etc/systemd/system/tbound-pi.service"
	piServiceConfigPath          = "/etc/tbound/serve-pi-service.json"
	piServiceRequestJournalPath  = "/var/lib/tbound/audit/serve-pi-requests.jsonl"
	piServiceResponseSchema      = "tbound-pi-service-response/v1"
	piServiceConfigSchema        = "tbound-pi-service-config/v4"
	piServiceConfigSchemaV3      = "tbound-pi-service-config/v3" // single-task schema, still valid for fresh and resume
	piServiceAdmissionCorpus     = "corpus"
	piServiceCorpusManifestPath  = "/etc/tbound/corpus/manifest.json"
	piServiceSocketSchema        = "tbound-managed-pi-service-socket/v1"
	piServiceSocketMode          = "systemd-socket-activation-single-task/v1"
	piServiceSourcePolicy        = "current-managed-live-root/v1"
	piServiceWorkflowCatalogPath = "/var/lib/tbound/private-admin/workflow-recovery/catalog.jsonl"
	piServiceWorkflowRootDBPath  = "/var/lib/tbound/private-admin/workflow-recovery/root.db"
	piServiceOwnerStoreRoot      = "/var/lib/tbound/sessions/workflow-owners"
	piServiceClientMaxBytes      = 16 << 10
	piServiceResponseMaxBytes    = 64 << 10
	piServiceReadTimeout         = 5 * time.Second
	piServiceWriteTimeout        = 10 * time.Second
	piServiceLinuxOPath          = 0x200000
)

var (
	ErrPiServiceUnavailable     = errors.New("registered local Pi service is unavailable")
	ErrPiServiceRefused         = errors.New("local Pi service refused the request")
	ErrPiServiceFrameTooLarge   = errors.New("local Pi service frame exceeds its bound")
	ErrPiServiceRequestConflict = errors.New("local Pi service request ID conflicts with a prior payload")
	ErrPiServiceUnresolvedPrior = errors.New("a prior accepted Pi service workflow is not independently settled")
)

// piServiceRequest is the wire request. Attempt (1, or 2 for the one outage rerun) and
// Role (a governed role) are the corpus-mode members: the task_id is then the case_id
// and the prompt must be empty, because the service takes the prompt and budgets of the
// case from its frozen manifest. Zero Attempt and empty Role mean a single-task request.
type piServiceRequest struct {
	RequestID       string    `json:"request_id"`
	TaskID          string    `json:"task_id"`
	Prompt          string    `json:"prompt"`
	BashOutputPaths *[]string `json:"bash_output_paths,omitempty"`
	Attempt         int       `json:"attempt,omitempty"`
	Role            string    `json:"role,omitempty"`
}

type piServiceResponse struct {
	SchemaVersion string          `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	Status        string          `json:"status"`
	ErrorCode     string          `json:"error_code,omitempty"`
	Receipt       json.RawMessage `json:"receipt,omitempty"`
}

type piServiceSocketRegistration struct {
	SchemaVersion           string `json:"schema_version"`
	Mode                    string `json:"mode"`
	SocketPath              string `json:"socket_path"`
	SocketUnitPath          string `json:"socket_unit_path"`
	SocketUnitDigest        string `json:"socket_unit_digest"`
	ListenStream            string `json:"listen_stream"`
	Accept                  string `json:"accept"`
	ServiceUnit             string `json:"service_unit"`
	SocketUser              string `json:"socket_user"`
	SocketGroup             string `json:"socket_group"`
	SocketUID               uint32 `json:"socket_uid"`
	SocketGID               uint32 `json:"socket_gid"`
	SocketMode              uint32 `json:"socket_mode"`
	DirectoryMode           uint32 `json:"directory_mode"`
	ParentPath              string `json:"parent_path"`
	ParentUID               uint32 `json:"parent_uid"`
	ParentGID               uint32 `json:"parent_gid"`
	ParentMode              uint32 `json:"parent_mode"`
	ParentFilesystemMagic   uint64 `json:"parent_filesystem_magic"`
	ParentMountPolicy       string `json:"parent_mount_policy"`
	AllowedClientUIDs       []int  `json:"allowed_client_uids"`
	AllowedClientPrimaryGID int    `json:"allowed_client_primary_gid"`
}

type piServiceRegistrationContract struct {
	ServiceSocket        json.RawMessage `json:"service_socket"`
	ServiceConfigPath    string          `json:"service_config_path"`
	ServiceConfigDigest  string          `json:"service_config_digest"`
	ServiceWritablePaths []string        `json:"service_writable_paths"`
}

type piServiceConfig struct {
	SchemaVersion               string   `json:"schema_version"`
	SourcePolicy                string   `json:"source_policy"`
	ProviderRoute               string   `json:"provider_route"`
	WorkflowAdmissionMode       string   `json:"workflow_admission_mode"`
	WorkflowAdmissionID         string   `json:"workflow_admission_id"`
	WorkflowTaskID              string   `json:"workflow_task_id"`
	WorkflowPrompt              string   `json:"workflow_prompt"`
	WorkflowBashOutputPaths     []string `json:"workflow_bash_output_paths"`
	WorkflowCatalogPath         string   `json:"workflow_catalog_path"`
	WorkflowRootDBPath          string   `json:"workflow_root_db_path"`
	WorkflowOwnerStoreRoot      string   `json:"workflow_owner_store_root"`
	WorkflowMaxPreEffectRetries uint64   `json:"workflow_max_pre_effect_retries"`
	WorkflowMaxCleanRestarts    uint64   `json:"workflow_max_clean_restarts"`
	WorkflowMaxHostCalls        uint64   `json:"workflow_max_host_calls"`
	WorkflowMaxTokens           uint64   `json:"workflow_max_tokens"`
	WorkflowDurationSeconds     uint64   `json:"workflow_duration_seconds"`
	CorpusManifestPath          string   `json:"corpus_manifest_path,omitempty"`
	CorpusManifestDigest        string   `json:"corpus_manifest_digest,omitempty"`
	CorpusQuotaProfile          string   `json:"corpus_quota_profile,omitempty"`
	AllowPilotOnResearch        bool     `json:"allow_pilot_on_research,omitempty"`
	PodmanHome                  string   `json:"podman_home"`
	PodmanGraphRoot             string   `json:"podman_graphroot"`
	PodmanRunRoot               string   `json:"podman_runroot"`
	XDGRuntimeDir               string   `json:"xdg_runtime_dir"`
	XDGConfigHome               string   `json:"xdg_config_home"`
	XDGDataHome                 string   `json:"xdg_data_home"`
	XDGCacheHome                string   `json:"xdg_cache_home"`
	TMPDir                      string   `json:"tmp_dir"`
	ContainersConfigPath        string   `json:"containers_conf_path"`
	ContainersConfigDigest      string   `json:"containers_conf_digest"`
	StorageConfigPath           string   `json:"storage_conf_path"`
	StorageConfigDigest         string   `json:"storage_conf_digest"`
}

type piServiceSocketFDIdentity struct {
	Device uint64
	Inode  uint64
}

type piServiceSocketPathIdentity struct {
	Device  uint64
	Inode   uint64
	MountID uint64
}

type piServiceParentIdentity struct {
	Path       string
	Device     uint64
	Inode      uint64
	MountID    uint64
	Filesystem uint64
	UID        uint32
	GID        uint32
	Mode       uint32
}

type piServiceActivatedListener struct {
	Listener *net.UnixListener
	FD       *os.File
	Policy   piServiceSocketRegistration
	Identity piServiceSocketRuntimeIdentity
}

func (activated *piServiceActivatedListener) Close() error {
	if activated == nil {
		return nil
	}
	var listenerErr, fileErr error
	if activated.Listener != nil {
		listenerErr = activated.Listener.Close()
	}
	if activated.FD != nil {
		fileErr = activated.FD.Close()
	}
	return errors.Join(listenerErr, fileErr)
}

func (activated *piServiceActivatedListener) Revalidate() error {
	if activated == nil || activated.Listener == nil || activated.FD == nil {
		return ErrPiServiceUnavailable
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(activated.FD.Fd()), &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFSOCK ||
		uint64(stat.Dev) != activated.Identity.FD.Device || stat.Ino != activated.Identity.FD.Inode {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	typeValue, err := syscall.GetsockoptInt(int(activated.FD.Fd()), syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil || typeValue != syscall.SOCK_STREAM {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	accepting, err := syscall.GetsockoptInt(int(activated.FD.Fd()), syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
	if err != nil || accepting != 1 {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	address, err := syscall.Getsockname(int(activated.FD.Fd()))
	unixAddress, ok := address.(*syscall.SockaddrUnix)
	if err != nil || !ok || unixAddress.Name != activated.Policy.SocketPath {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	return activated.Identity.revalidate(activated.Policy)
}

type piServiceSocketRuntimeIdentity struct {
	FD     piServiceSocketFDIdentity
	Path   piServiceSocketPathIdentity
	Parent piServiceParentIdentity
}

type piServicePeerCredentials struct {
	PID int
	UID int
	GID int
}

type piServiceRequestAcceptedEvent struct {
	SchemaVersion string `json:"schema_version"`
	ClientUID     int    `json:"client_uid"`
	ClientGID     int    `json:"client_gid"`
	ClientPID     int    `json:"client_pid"`
	RequestID     string `json:"request_id"`
	TaskID        string `json:"task_id"`
	PayloadDigest string `json:"payload_digest"`
	// ConfigDigest, RegistrationID and EnvelopeDigest bind the request to the service
	// config, registration and admission envelope that were active when it was accepted.
	// A stored terminal is replayed only to a service bound to the same three, whether or
	// not the request ever reserved a workflow. Entries journaled before this binding existed
	// carry none of them and are refused on replay (replay-binding-mismatch).
	ConfigDigest   string `json:"config_digest,omitempty"`
	RegistrationID string `json:"registration_id,omitempty"`
	EnvelopeDigest string `json:"envelope_digest,omitempty"`
}

// piServiceAcceptanceBinding is what the service binds into the accepted record.
type piServiceAcceptanceBinding struct {
	ConfigDigest   string
	RegistrationID string
	EnvelopeDigest string
}

type piServiceRequestTerminalEvent struct {
	SchemaVersion string            `json:"schema_version"`
	ClientUID     int               `json:"client_uid"`
	RequestID     string            `json:"request_id"`
	PayloadDigest string            `json:"payload_digest"`
	Response      piServiceResponse `json:"response"`
}

type piServiceRequestState struct {
	accepted       piServiceRequestAcceptedEvent
	acceptedRecord audit.Record
	terminal       *piServiceResponse
	// abandoned is the operator settlement of an unknown terminal, if one was journaled.
	abandoned *piServiceRequestAbandonedEvent
}

// piServiceVerifiedSettlement identifies the journal state of one request that passed
// settlement verification. The terminal pointer makes a replaced terminal a different state.
type piServiceVerifiedSettlement struct {
	acceptedHash     string
	acceptedSequence uint64
	terminal         *piServiceResponse
	// evidence is the fingerprint of the evidence files the verifier read (see
	// evidenceFingerprint), taken before the verification ran.
	evidence string
}

type piServiceRequestJournal struct {
	mu                 sync.Mutex
	journal            *audit.Journal
	entries            map[string]piServiceRequestState
	settlementVerifier func(context.Context, piServiceRequestState) error
	// verified is the in-memory record of settlement verifications that passed in this
	// process. See begin for the exact guarantee. It starts empty at every service start.
	verified map[string]piServiceVerifiedSettlement
	// admissionScreen is the service's host-side check of a request that runs before
	// the request is journaled as accepted. It returns an error code to refuse the
	// request without consuming its request id, or "" to let it proceed. It is nil
	// until the service installs it.
	admissionScreen func(piServicePeerCredentials, piServiceRequest) string
	// terminalReplayCheck is the service's check of a stored terminal response against the
	// active manifest, config, registration and envelope before it is returned. It returns
	// an error code to refuse the replay, or "". It is nil until the service installs it.
	terminalReplayCheck func(piServicePeerCredentials, piServiceRequest, piServiceRequestState) string
	// acceptanceBinding returns the config, registration and envelope binding that begin
	// persists in the accepted record. It is nil until the service installs it.
	acceptanceBinding func(piServicePeerCredentials, piServiceRequest) piServiceAcceptanceBinding
	// evidenceFiles names the host files a settlement verification of one request reads
	// (owner database, store journal). Their fingerprints are part of the verified mark. It is
	// nil until the service installs it, and then no file is fingerprinted.
	evidenceFiles func(piServiceRequestState) []string
	// evidenceState returns a cheap value of the per-workflow state a settlement verification
	// reads from a store that is not a per-request file (the workflow's checkpoint and fence head
	// in the protected-root database). It is part of the verified mark. It is nil until the
	// service installs it.
	evidenceState func(piServiceRequestState) string
	// reconciler proves, before an unknown request is abandoned and again on every begin that
	// skips it, that the request's workflow is quiescent. It is nil until the service installs
	// it, and then no unknown request can be abandoned or skipped.
	reconciler piServiceAbandonReconciler
	// afterScreen is a test seam called after the first admission screen passed and before
	// the request competes for the one active-task slot. It is nil in production.
	afterScreen func(request piServiceRequest, active *atomic.Bool)
}

func (j *piServiceRequestJournal) setAdmissionScreen(screen func(piServicePeerCredentials, piServiceRequest) string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.admissionScreen = screen
}

func (j *piServiceRequestJournal) setTerminalReplayCheck(check func(piServicePeerCredentials, piServiceRequest, piServiceRequestState) string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.terminalReplayCheck = check
}

func (j *piServiceRequestJournal) setAcceptanceBinding(binding func(piServicePeerCredentials, piServiceRequest) piServiceAcceptanceBinding) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.acceptanceBinding = binding
}

func (j *piServiceRequestJournal) setEvidenceFiles(files func(piServiceRequestState) []string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.evidenceFiles = files
}

func (j *piServiceRequestJournal) setEvidenceState(state func(piServiceRequestState) string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.evidenceState = state
}

func (j *piServiceRequestJournal) setReconciler(reconciler piServiceAbandonReconciler) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.reconciler = reconciler
}

func (j *piServiceRequestJournal) setAfterScreen(hook func(piServiceRequest, *atomic.Bool)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.afterScreen = hook
}

func (j *piServiceRequestJournal) runAfterScreen(request piServiceRequest, active *atomic.Bool) {
	j.mu.Lock()
	hook := j.afterScreen
	j.mu.Unlock()
	if hook != nil {
		hook(request, active)
	}
}

// checkTerminalReplay runs the installed replay check. With none installed (a test
// fixture without a catalog) a stored terminal is returned as is.
func (j *piServiceRequestJournal) checkTerminalReplay(peer piServicePeerCredentials, request piServiceRequest,
	state piServiceRequestState) string {
	j.mu.Lock()
	check := j.terminalReplayCheck
	j.mu.Unlock()
	if check == nil {
		return ""
	}
	return check(peer, request, state)
}

// screen runs the installed admission screen. A refused request is not journaled.
func (j *piServiceRequestJournal) screen(peer piServicePeerCredentials, request piServiceRequest) string {
	j.mu.Lock()
	screen := j.admissionScreen
	j.mu.Unlock()
	if screen == nil {
		return ""
	}
	return screen(peer, request)
}

type piServiceTaskRunner func(context.Context, piServiceRequest) (json.RawMessage, error)

type piServiceAdmissionContext struct {
	Peer           piServicePeerCredentials
	CurrentPeer    piServicePeerCredentials
	Request        piServiceRequest
	PayloadDigest  string
	Accepted       piServiceRequestAcceptedEvent
	AcceptedRecord audit.Record
	IsReplay       bool
	ConfigDigest   string
	RegistrationID string
}

type piServiceAdmissionContextKey struct{}

func withPiServiceAdmission(ctx context.Context, admission piServiceAdmissionContext) context.Context {
	return context.WithValue(ctx, piServiceAdmissionContextKey{}, admission)
}

func piServiceAdmissionFromContext(ctx context.Context) (piServiceAdmissionContext, bool) {
	if ctx == nil {
		return piServiceAdmissionContext{}, false
	}
	admission, ok := ctx.Value(piServiceAdmissionContextKey{}).(piServiceAdmissionContext)
	return admission, ok
}

func validatePiServiceSocketRegistration(reg piServiceSocketRegistration, serviceUID, serviceGID int) error {
	if reg.SchemaVersion != piServiceSocketSchema || reg.Mode != piServiceSocketMode ||
		reg.SocketPath != piServiceSocketPath || reg.ListenStream != piServiceSocketPath ||
		reg.SocketUnitPath != piServiceSocketUnitPath || !validDigestText(reg.SocketUnitDigest) ||
		reg.Accept != "no" || reg.ServiceUnit != filepath.Base(piServiceUnitPath) ||
		reg.SocketUser != "root" || reg.SocketGroup != "tbound-pi-clients" || reg.SocketUID != 0 ||
		reg.SocketGID == 0 || int(reg.SocketGID) == serviceGID || serviceUID <= 0 || serviceGID <= 0 ||
		reg.SocketMode != 0o660 || reg.DirectoryMode != 0o755 ||
		reg.ParentPath != filepath.Dir(piServiceSocketPath) || reg.ParentUID != 0 || reg.ParentGID != 0 ||
		reg.ParentMode != 0o755 || reg.ParentFilesystemMagic != 0x01021994 ||
		reg.ParentMountPolicy != "tmpfs-rw-nodev-nosuid/v1" ||
		len(reg.AllowedClientUIDs) == 0 || reg.AllowedClientPrimaryGID <= 0 ||
		reg.AllowedClientPrimaryGID == serviceGID || reg.AllowedClientPrimaryGID != int(reg.SocketGID) {
		return ErrPiServiceUnavailable
	}
	previous := -1
	for _, uid := range reg.AllowedClientUIDs {
		if uid < 0 || uid == serviceUID || uid <= previous {
			return ErrPiServiceUnavailable
		}
		previous = uid
	}
	return nil
}

func validatePiServiceRegistrationContract(contract piServiceRegistrationContract, reg piruntime.D09ManagedLiveRootRegistration,
	socket piServiceSocketRegistration) error {
	if contract.ServiceConfigPath != piServiceConfigPath || !validDigestText(contract.ServiceConfigDigest) ||
		reg.ServiceUnitPath != piServiceUnitPath ||
		!validDigestText(reg.ServiceUnitDigest) || reg.RunnerPath == "" || !filepath.IsAbs(reg.RunnerPath) {
		return ErrPiServiceUnavailable
	}
	wantPaths := []string{
		"/run/tbound-pi",
		"/var/lib/tbound/audit",
		"/var/lib/tbound/live",
		"/var/lib/tbound/podman-home",
		"/var/lib/tbound/podman-storage/graphroot",
		"/var/lib/tbound/private-admin",
		"/var/lib/tbound/publication-state",
		"/var/lib/tbound/sessions",
	}
	if len(contract.ServiceWritablePaths) != len(wantPaths) || !sort.StringsAreSorted(contract.ServiceWritablePaths) {
		return ErrPiServiceUnavailable
	}
	for index := range wantPaths {
		if contract.ServiceWritablePaths[index] != wantPaths[index] {
			return ErrPiServiceUnavailable
		}
	}
	serviceConfig, err := readProductionPiServiceConfig(contract.ServiceConfigPath, contract.ServiceConfigDigest)
	if err != nil {
		return err
	}
	serviceUnit, err := readProtectedPiServiceFile(reg.ServiceUnitPath, 1<<20)
	if err != nil || digestBytes(serviceUnit) != reg.ServiceUnitDigest {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	if err := validatePiServiceUnit(serviceUnit, reg, serviceConfig, contract.ServiceWritablePaths); err != nil {
		return err
	}
	socketUnit, err := readProtectedPiServiceFile(piServiceSocketUnitPath, 1<<20)
	if err != nil || digestBytes(socketUnit) != socket.SocketUnitDigest {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	if err := validatePiServiceSocketUnit(socketUnit, socket); err != nil {
		return err
	}
	return nil
}

func parsePiSystemdUnit(data []byte) (map[string]map[string][]string, error) {
	sections := make(map[string]map[string][]string)
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			return nil, ErrPiServiceUnavailable // no continuations in the pinned narrow unit grammar
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if sections[section] == nil {
				sections[section] = make(map[string][]string)
			}
			continue
		}
		if section == "" {
			return nil, ErrPiServiceUnavailable
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, ErrPiServiceUnavailable
		}
		sections[section][key] = append(sections[section][key], strings.TrimSpace(value))
	}
	return sections, nil
}

func onePiSystemdValue(sections map[string]map[string][]string, section, key string) (string, bool) {
	values := sections[section][key]
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func piServiceSystemdUnitHasOnlyAllowedDirectives(sections map[string]map[string][]string,
	allowed map[string][]string) bool {
	for section, directives := range sections {
		allowedDirectives, sectionAllowed := allowed[section]
		if !sectionAllowed {
			return false
		}
		for directive := range directives {
			found := false
			for _, allowedDirective := range allowedDirectives {
				if directive == allowedDirective {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func piServiceSystemdInstallTargetAllowed(sections map[string]map[string][]string, target string) bool {
	install, present := sections["Install"]
	if !present {
		return true
	}
	wantedBy := install["WantedBy"]
	return len(install) == 1 && len(wantedBy) == 1 && wantedBy[0] == target
}

func validatePiServiceUnit(data []byte, registration piruntime.D09ManagedLiveRootRegistration,
	serviceConfig piServiceConfig, writablePaths []string) error {
	sections, err := parsePiSystemdUnit(data)
	if err != nil {
		return err
	}
	if !piServiceSystemdUnitHasOnlyAllowedDirectives(sections, map[string][]string{
		"Unit": {"Description"},
		"Service": {"User", "Group", "ExecStart", "KillMode", "UMask", "NoNewPrivileges", "ProtectSystem",
			"ProtectHome", "PrivateTmp", "Delegate", "DelegateSubgroup", "RuntimeDirectory", "RuntimeDirectoryMode", "StateDirectory",
			"StateDirectoryMode", "ReadWritePaths", "Environment", "UnsetEnvironment"},
	}) {
		return fmt.Errorf("%w: signed Pi service unit contains an unsupported section or directive", ErrPiServiceUnavailable)
	}
	service := sections["Service"]
	get := func(key string) (string, bool) { return onePiSystemdValue(sections, "Service", key) }
	user, userOK := get("User")
	group, groupOK := get("Group")
	execStart, execOK := get("ExecStart")
	killMode, killOK := get("KillMode")
	umask, umaskOK := get("UMask")
	noNewPrivs, nnpOK := get("NoNewPrivileges")
	protectSystem, protectSystemOK := get("ProtectSystem")
	protectHome, protectHomeOK := get("ProtectHome")
	privateTmp, privateTmpOK := get("PrivateTmp")
	delegate, delegateOK := get("Delegate")
	delegateSubgroup, delegateSubgroupOK := get("DelegateSubgroup")
	runtimeDirectory, runtimeDirectoryOK := get("RuntimeDirectory")
	runtimeDirectoryMode, runtimeDirectoryModeOK := get("RuntimeDirectoryMode")
	stateDirectories, stateDirectoriesOK := get("StateDirectory")
	stateDirectoryMode, stateDirectoryModeOK := get("StateDirectoryMode")
	writePaths, writePathsOK := get("ReadWritePaths")
	wantStateDirectories := []string{"tbound/audit", "tbound/live", "tbound/podman-home",
		"tbound/podman-storage/graphroot", "tbound/private-admin", "tbound/publication-state", "tbound/sessions"}
	if !userOK || user != registration.ServiceName || !groupOK || group != registration.ServiceGroup ||
		!execOK || execStart != registration.RunnerPath+" serve --pi --service" ||
		!killOK || killMode != "control-group" || !umaskOK || umask != "0077" ||
		!nnpOK || !strings.EqualFold(noNewPrivs, "yes") || !protectSystemOK || protectSystem != "strict" ||
		!protectHomeOK || !strings.EqualFold(protectHome, "yes") || !privateTmpOK || !strings.EqualFold(privateTmp, "yes") ||
		!delegateOK || !strings.EqualFold(delegate, "yes") ||
		!delegateSubgroupOK || delegateSubgroup != piruntime.D09ManagedPiRunnerCgroupLeaf || !runtimeDirectoryOK || runtimeDirectory != "tbound-pi" ||
		!runtimeDirectoryModeOK || runtimeDirectoryMode != "0700" || !stateDirectoriesOK ||
		!piServiceStringListEqual(strings.Fields(stateDirectories), wantStateDirectories) ||
		!stateDirectoryModeOK || stateDirectoryMode != "0700" || !writePathsOK ||
		!piServicePathListEqual(strings.Fields(writePaths), writablePaths) {
		return fmt.Errorf("%w: signed Pi service unit directives differ from the fixed service contract", ErrPiServiceUnavailable)
	}
	wantEnvironment := map[string]string{
		"HOME": serviceConfig.PodmanHome, "USER": registration.ServiceName, "LOGNAME": registration.ServiceName,
		"XDG_RUNTIME_DIR": serviceConfig.XDGRuntimeDir, "XDG_CONFIG_HOME": serviceConfig.XDGConfigHome,
		"XDG_DATA_HOME": serviceConfig.XDGDataHome, "XDG_CACHE_HOME": serviceConfig.XDGCacheHome, "TMPDIR": serviceConfig.TMPDir,
		"CONTAINERS_CONF": serviceConfig.ContainersConfigPath, "CONTAINERS_STORAGE_CONF": serviceConfig.StorageConfigPath,
		"LANG": "C", "LC_ALL": "C",
	}
	if !piServiceEnvironmentDirectivesEqual(service["Environment"], wantEnvironment) ||
		!piServiceUnsetEnvironmentAllowsOnly(service["UnsetEnvironment"], "DBUS_SESSION_BUS_ADDRESS") {
		return fmt.Errorf("%w: service unit environment is not the fixed registered Podman profile", ErrPiServiceUnavailable)
	}
	return nil
}

func piServiceStringListEqual(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	actual = append([]string(nil), actual...)
	expected = append([]string(nil), expected...)
	sort.Strings(actual)
	sort.Strings(expected)
	for index := range actual {
		if actual[index] == "" || actual[index] != expected[index] || strings.Contains(actual[index], "..") ||
			strings.HasPrefix(actual[index], "/") || strings.ContainsAny(actual[index], "\\\x00") {
			return false
		}
	}
	return true
}

func piServiceEnvironmentDirectivesEqual(directives []string, expected map[string]string) bool {
	actual := make(map[string]string)
	for _, directive := range directives {
		for _, assignment := range strings.Fields(directive) {
			key, value, ok := strings.Cut(assignment, "=")
			if !ok || key == "" || value == "" {
				return false
			}
			if _, duplicate := actual[key]; duplicate {
				return false
			}
			actual[key] = strings.Trim(value, `"'`)
		}
	}
	if len(actual) != len(expected) {
		return false
	}
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func piServiceUnsetEnvironmentAllowsOnly(directives []string, want string) bool {
	if len(directives) != 1 {
		return false
	}
	fields := strings.Fields(strings.Trim(directives[0], `"'`))
	return len(fields) == 1 && fields[0] == want
}

func validatePiServiceSocketUnit(data []byte, socket piServiceSocketRegistration) error {
	sections, err := parsePiSystemdUnit(data)
	if err != nil {
		return err
	}
	if !piServiceSystemdUnitHasOnlyAllowedDirectives(sections, map[string][]string{
		"Unit": {"Description"},
		"Socket": {"ListenStream", "Accept", "Service", "SocketUser", "SocketGroup", "SocketMode", "DirectoryMode",
			"FileDescriptorName"},
		"Install": {"WantedBy"},
	}) || !piServiceSystemdInstallTargetAllowed(sections, "sockets.target") {
		return fmt.Errorf("%w: signed Pi socket unit contains an unsupported section or directive", ErrPiServiceUnavailable)
	}
	get := func(key string) (string, bool) { return onePiSystemdValue(sections, "Socket", key) }
	listen, listenOK := get("ListenStream")
	accept, acceptOK := get("Accept")
	service, serviceOK := get("Service")
	user, userOK := get("SocketUser")
	group, groupOK := get("SocketGroup")
	mode, modeOK := get("SocketMode")
	directoryMode, directoryModeOK := get("DirectoryMode")
	fdName, fdNameOK := get("FileDescriptorName")
	if !listenOK || listen != socket.SocketPath || !acceptOK || !strings.EqualFold(accept, "no") ||
		!serviceOK || service != socket.ServiceUnit || !userOK || user != socket.SocketUser ||
		!groupOK || group != socket.SocketGroup || !modeOK || mode != "0660" ||
		!directoryModeOK || directoryMode != "0755" || !fdNameOK || fdName != "tbound-pi" ||
		len(sections["Socket"]["ListenStream"]) != 1 {
		return fmt.Errorf("%w: signed Pi socket unit directives differ from the fixed activation contract", ErrPiServiceUnavailable)
	}
	return nil
}

func piServicePathListEqual(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	actual = append([]string(nil), actual...)
	expected = append([]string(nil), expected...)
	sort.Strings(actual)
	sort.Strings(expected)
	for index := range actual {
		if actual[index] != expected[index] || !filepath.IsAbs(actual[index]) || filepath.Clean(actual[index]) != actual[index] ||
			strings.Contains(actual[index], "/../") {
			return false
		}
	}
	return true
}

func serviceSocketRegistrationFromSignedRegistration(reg piruntime.D09ManagedLiveRootRegistration) (piServiceSocketRegistration,
	piServiceRegistrationContract, error) {
	encoded, err := json.Marshal(reg)
	if err != nil {
		return piServiceSocketRegistration{}, piServiceRegistrationContract{}, errors.Join(ErrPiServiceUnavailable, err)
	}
	var contract piServiceRegistrationContract
	var envelope map[string]json.RawMessage
	if err := protocol.ValidateStrictJSON(encoded); err != nil {
		return piServiceSocketRegistration{}, contract, errors.Join(ErrPiServiceUnavailable, err)
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return piServiceSocketRegistration{}, contract, errors.Join(ErrPiServiceUnavailable, err)
	}
	for field, target := range map[string]any{
		"service_config_path": &contract.ServiceConfigPath, "service_config_digest": &contract.ServiceConfigDigest,
		"service_writable_paths": &contract.ServiceWritablePaths,
	} {
		data := envelope[field]
		if len(data) == 0 {
			return piServiceSocketRegistration{}, contract, fmt.Errorf("%w: signed registration lacks %s", ErrPiServiceUnavailable, field)
		}
		if err := json.Unmarshal(data, target); err != nil {
			return piServiceSocketRegistration{}, contract, errors.Join(ErrPiServiceUnavailable, err)
		}
	}
	data := envelope["service_socket"]
	if len(data) == 0 || protocol.ValidateStrictJSON(data) != nil {
		return piServiceSocketRegistration{}, contract, ErrPiServiceUnavailable
	}
	var socket piServiceSocketRegistration
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&socket); err != nil {
		return piServiceSocketRegistration{}, contract, errors.Join(ErrPiServiceUnavailable, err)
	}
	if err := validatePiServiceSocketRegistration(socket, reg.ServiceUID, reg.ServiceGID); err != nil {
		return piServiceSocketRegistration{}, contract, err
	}
	if err := validatePiServiceRegistrationContract(contract, reg, socket); err != nil {
		return piServiceSocketRegistration{}, contract, err
	}
	return socket, contract, nil
}

func readProductionPiServiceConfig(path, expectedDigest string) (piServiceConfig, error) {
	if path != piServiceConfigPath || !validDigestText(expectedDigest) {
		return piServiceConfig{}, ErrPiServiceUnavailable
	}
	data, err := readProtectedPiServiceFile(path, 16<<10)
	if err != nil || digestBytes(data) != expectedDigest {
		return piServiceConfig{}, errors.Join(ErrPiServiceUnavailable, err)
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return piServiceConfig{}, errors.Join(ErrPiServiceUnavailable, err)
	}
	var config piServiceConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return piServiceConfig{}, errors.Join(ErrPiServiceUnavailable, err)
	}
	if validateProductionPiServiceConfig(config) != nil {
		return piServiceConfig{}, ErrPiServiceUnavailable
	}
	containers, err := readProtectedPiServiceFile(config.ContainersConfigPath, 1<<20)
	if err != nil || digestBytes(containers) != config.ContainersConfigDigest {
		return piServiceConfig{}, errors.Join(ErrPiServiceUnavailable, err)
	}
	storage, err := readProtectedPiServiceFile(config.StorageConfigPath, 1<<20)
	if err != nil || digestBytes(storage) != config.StorageConfigDigest {
		return piServiceConfig{}, errors.Join(ErrPiServiceUnavailable, err)
	}
	return config, nil
}

// validateProductionPiServiceConfig is the structural check of a decoded service
// config. Single-task modes (fresh, resume) pin one inline task, prompt and admission
// id and accept schema v3 or v4. Corpus mode (schema v4 only) pins a frozen manifest by
// fixed path and digest and carries no inline task, prompt or admission id. In both
// shapes the workflow_max_* members are the host ceilings of a workflow.
func validateProductionPiServiceConfig(config piServiceConfig) error {
	if (config.SchemaVersion != piServiceConfigSchema && config.SchemaVersion != piServiceConfigSchemaV3) ||
		config.SourcePolicy != piServiceSourcePolicy ||
		(config.ProviderRoute != piruntime.D09ManagedPiProviderRouteEngineering && config.ProviderRoute != piruntime.D09ManagedPiProviderRouteResearch) ||
		!validPiServiceAdmissionEnvelope(config) ||
		sessionrepo.ValidateOutputScopePaths(config.WorkflowBashOutputPaths) != nil ||
		config.WorkflowCatalogPath != piServiceWorkflowCatalogPath || config.WorkflowRootDBPath != piServiceWorkflowRootDBPath ||
		config.WorkflowOwnerStoreRoot != piServiceOwnerStoreRoot || config.WorkflowMaxPreEffectRetries > 2 ||
		config.WorkflowMaxCleanRestarts > 2 || config.WorkflowMaxHostCalls == 0 || config.WorkflowMaxTokens == 0 ||
		config.WorkflowDurationSeconds == 0 || config.WorkflowDurationSeconds > uint64(productionSessionMaxRuntime/time.Second) ||
		config.PodmanHome != "/var/lib/tbound/podman-home" || config.PodmanGraphRoot != "/var/lib/tbound/podman-storage/graphroot" ||
		config.PodmanRunRoot != "/run/tbound-pi/podman-runroot" || config.XDGRuntimeDir != "/run/tbound-pi" ||
		config.XDGConfigHome != "/etc/tbound/podman-config" || config.XDGDataHome != "/var/lib/tbound/podman-home/data" ||
		config.XDGCacheHome != "/run/tbound-pi/cache" || config.TMPDir != "/run/tbound-pi/tmp" ||
		config.ContainersConfigPath != "/etc/tbound/podman-config/containers.conf" ||
		config.StorageConfigPath != "/etc/tbound/podman-config/storage.conf" ||
		!validDigestText(config.ContainersConfigDigest) || !validDigestText(config.StorageConfigDigest) {
		return ErrPiServiceUnavailable
	}
	return nil
}

// validPiServiceCorpusQuotaProfile mirrors piruntime.D09ManagedPiServiceConfig: the thesis
// profile (absent or explicit) on any route, the pilot profile on the engineering route, or
// on the research route only with the explicit allowance, which is refused wherever it has
// nothing to allow.
func validPiServiceCorpusQuotaProfile(config piServiceConfig) bool {
	switch config.CorpusQuotaProfile {
	case "", piruntime.D09ManagedPiCorpusQuotaProfileThesis:
		return !config.AllowPilotOnResearch
	case piruntime.D09ManagedPiCorpusQuotaProfilePilot:
		return config.AllowPilotOnResearch == (config.ProviderRoute == piruntime.D09ManagedPiProviderRouteResearch)
	}
	return false
}

// effectiveCorpusQuotaProfile is "thesis" or "pilot" for a corpus-mode config (an absent
// member is thesis) and "" for a single-task config.
func (config piServiceConfig) effectiveCorpusQuotaProfile() string {
	if config.WorkflowAdmissionMode != piServiceAdmissionCorpus {
		return ""
	}
	if config.CorpusQuotaProfile == "" {
		return piruntime.D09ManagedPiCorpusQuotaProfileThesis
	}
	return config.CorpusQuotaProfile
}

func validPiServiceAdmissionEnvelope(config piServiceConfig) bool {
	switch config.WorkflowAdmissionMode {
	case "fresh", "resume":
		return config.CorpusManifestPath == "" && config.CorpusManifestDigest == "" &&
			config.CorpusQuotaProfile == "" && !config.AllowPilotOnResearch &&
			boundedProductionIdentity(config.WorkflowAdmissionID, productionRequestMaxBytes) &&
			boundedProductionIdentity(config.WorkflowTaskID, productionTaskIDMaxBytes) &&
			boundedProductionPrompt(config.WorkflowPrompt)
	case piServiceAdmissionCorpus:
		return config.SchemaVersion == piServiceConfigSchema &&
			config.WorkflowAdmissionID == "" && config.WorkflowTaskID == "" && config.WorkflowPrompt == "" &&
			config.CorpusManifestPath == piServiceCorpusManifestPath && validDigestText(config.CorpusManifestDigest) &&
			validPiServiceCorpusQuotaProfile(config)
	}
	return false
}

func readProtectedPiServiceFile(path string, maximum int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || maximum <= 0 {
		return nil, ErrPiServiceUnavailable
	}
	parent := filepath.Dir(path)
	parentFile, err := openServiceDirectoryNoSymlinks(parent)
	if err != nil {
		return nil, err
	}
	defer parentFile.Close()
	parentInfo, err := parentFile.Stat()
	if err != nil {
		return nil, err
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, ErrPiServiceUnavailable
	}
	fd, err := syscall.Openat(int(parentFile.Fd()), filepath.Base(path), syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.Join(ErrPiServiceUnavailable, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, ErrPiServiceUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) != info.Size() || int64(len(data)) > maximum {
		return nil, errors.Join(ErrPiServiceUnavailable, err)
	}
	return data, nil
}

func openServiceDirectoryNoSymlinks(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrPiServiceUnavailable
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "pi-service-root")
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" {
			continue
		}
		nextFD, err := syscall.Openat(int(current.Fd()), component,
			syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		next := os.NewFile(uintptr(nextFD), "pi-service-dir")
		_ = current.Close()
		current = next
	}
	return current, nil
}

func verifyPiServiceEnvironment(config piServiceConfig, registration piruntime.D09ManagedLiveRootRegistration) error {
	want := map[string]string{"HOME": config.PodmanHome, "USER": registration.ServiceName,
		"LOGNAME": registration.ServiceName, "XDG_RUNTIME_DIR": config.XDGRuntimeDir,
		"XDG_CONFIG_HOME": config.XDGConfigHome, "XDG_DATA_HOME": config.XDGDataHome,
		"XDG_CACHE_HOME": config.XDGCacheHome, "TMPDIR": config.TMPDir,
		"CONTAINERS_CONF": config.ContainersConfigPath, "CONTAINERS_STORAGE_CONF": config.StorageConfigPath}
	for key, value := range want {
		if os.Getenv(key) != value {
			return fmt.Errorf("%w: service environment %s differs from the protected configuration", ErrPiServiceUnavailable, key)
		}
	}
	return nil
}

func systemdActivatedPiListener(socket piServiceSocketRegistration) (*piServiceActivatedListener, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) || os.Getenv("LISTEN_FDS") != "1" ||
		os.Getenv("LISTEN_FDNAMES") != "tbound-pi" {
		return nil, ErrPiServiceUnavailable
	}
	file := os.NewFile(3, "tbound-pi-systemd-listener")
	if file == nil {
		return nil, ErrPiServiceUnavailable
	}
	syscall.CloseOnExec(3)
	var descriptor syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &descriptor); err != nil || descriptor.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
		_ = file.Close()
		return nil, errors.Join(ErrPiServiceUnavailable, err)
	}
	socketType, err := syscall.GetsockoptInt(int(file.Fd()), syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil || socketType != syscall.SOCK_STREAM {
		_ = file.Close()
		return nil, errors.Join(ErrPiServiceUnavailable, err)
	}
	accepting, err := syscall.GetsockoptInt(int(file.Fd()), syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
	if err != nil || accepting != 1 {
		_ = file.Close()
		return nil, errors.Join(ErrPiServiceUnavailable, err)
	}
	address, err := syscall.Getsockname(int(file.Fd()))
	unixAddress, ok := address.(*syscall.SockaddrUnix)
	if err != nil || !ok || unixAddress.Name != socket.SocketPath || strings.HasPrefix(unixAddress.Name, "@") {
		_ = file.Close()
		return nil, errors.Join(ErrPiServiceUnavailable, err)
	}
	identity, err := measurePiServiceSocketPath(socket)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	fdIdentity := piServiceSocketFDIdentity{Device: uint64(descriptor.Dev), Inode: descriptor.Ino}
	runtimeIdentity := piServiceSocketRuntimeIdentity{FD: fdIdentity, Path: identity.Path, Parent: identity.Parent}
	if err := verifyActivatedPiListenerFile(file, socket, runtimeIdentity); err != nil {
		_ = file.Close()
		return nil, err
	}
	listener, err := net.FileListener(file)
	if err != nil {
		_ = file.Close()
		return nil, errors.Join(ErrPiServiceUnavailable, err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		_ = file.Close()
		return nil, ErrPiServiceUnavailable
	}
	unixListener.SetUnlinkOnClose(false)
	return &piServiceActivatedListener{Listener: unixListener, FD: file, Policy: socket,
		Identity: runtimeIdentity}, nil
}

func verifyActivatedPiListenerFile(fd *os.File, socket piServiceSocketRegistration,
	identity piServiceSocketRuntimeIdentity) error {
	if fd == nil {
		return ErrPiServiceUnavailable
	}
	var descriptor syscall.Stat_t
	if err := syscall.Fstat(int(fd.Fd()), &descriptor); err != nil || descriptor.Mode&syscall.S_IFMT != syscall.S_IFSOCK ||
		uint64(descriptor.Dev) != identity.FD.Device || descriptor.Ino != identity.FD.Inode {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	socketType, err := syscall.GetsockoptInt(int(fd.Fd()), syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil || socketType != syscall.SOCK_STREAM {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	accepting, err := syscall.GetsockoptInt(int(fd.Fd()), syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
	if err != nil || accepting != 1 {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	address, err := syscall.Getsockname(int(fd.Fd()))
	unixAddress, ok := address.(*syscall.SockaddrUnix)
	if err != nil || !ok || unixAddress.Name != socket.SocketPath || strings.HasPrefix(unixAddress.Name, "@") {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	// FD/socketfs identity and filesystem pathname identity are intentionally
	// checked separately. They are not required to be os.SameFile/inode-equal.
	return identity.revalidate(socket)
}

func measurePiServiceSocketPath(socket piServiceSocketRegistration) (piServiceSocketRuntimeIdentity, error) {
	var identity piServiceSocketRuntimeIdentity
	if !filepath.IsAbs(socket.SocketPath) || filepath.Clean(socket.SocketPath) != socket.SocketPath ||
		socket.ParentPath != filepath.Dir(socket.SocketPath) {
		return identity, ErrPiServiceUnavailable
	}
	parent, err := openServiceDirectoryNoSymlinks(socket.ParentPath)
	if err != nil {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	defer parent.Close()
	parentInfo, err := parent.Stat()
	if err != nil || !parentInfo.IsDir() {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != socket.ParentUID || parentStat.Gid != socket.ParentGID ||
		uint32(parentStat.Mode&0o7777) != socket.DirectoryMode {
		return identity, ErrPiServiceUnavailable
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Fstatfs(int(parent.Fd()), &filesystem); err != nil || uint64(filesystem.Type) != socket.ParentFilesystemMagic {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	parentMountID, err := productionDescriptorMountID(parent.Fd())
	if err != nil {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	mountOptions, err := piServiceMountOptions(parentMountID)
	if err != nil || !containsMountOption(mountOptions, "rw") || !containsMountOption(mountOptions, "nodev") ||
		!containsMountOption(mountOptions, "nosuid") || containsMountOption(mountOptions, "ro") {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	if socket.ParentMountPolicy != "tmpfs-rw-nodev-nosuid/v1" {
		return identity, ErrPiServiceUnavailable
	}
	pathInfo, err := os.Lstat(socket.SocketPath)
	if err != nil || pathInfo.Mode()&os.ModeSocket == 0 {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	pathStat, ok := pathInfo.Sys().(*syscall.Stat_t)
	if !ok || pathStat.Uid != socket.SocketUID || pathStat.Gid != socket.SocketGID ||
		uint32(pathStat.Mode&0o7777) != socket.SocketMode {
		return identity, ErrPiServiceUnavailable
	}
	pathFD, err := syscall.Openat(int(parent.Fd()), filepath.Base(socket.SocketPath), piServiceLinuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	pathFile := os.NewFile(uintptr(pathFD), "tbound-pi-socket-dentry")
	var opened syscall.Stat_t
	statErr := syscall.Fstat(pathFD, &opened)
	pathMountID, mountErr := productionDescriptorMountID(pathFile.Fd())
	closeErr := pathFile.Close()
	if statErr != nil || mountErr != nil || opened.Dev != pathStat.Dev || opened.Ino != pathStat.Ino {
		return identity, errors.Join(ErrPiServiceUnavailable, statErr, mountErr, closeErr)
	}
	info, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", parent.Fd()))
	if err != nil || filepath.Clean(info) != socket.ParentPath {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	identity.Path = piServiceSocketPathIdentity{Device: uint64(opened.Dev), Inode: opened.Ino, MountID: pathMountID}
	identity.Parent = piServiceParentIdentity{Path: socket.ParentPath, Device: uint64(parentStat.Dev), Inode: parentStat.Ino,
		MountID: parentMountID, Filesystem: uint64(filesystem.Type), UID: parentStat.Uid, GID: parentStat.Gid,
		Mode: uint32(parentStat.Mode & 0o7777)}
	return identity, nil
}

func piServiceMountOptions(mountID uint64) (string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 3 {
			continue
		}
		id, err := strconv.ParseUint(left[0], 10, 64)
		if err != nil || id != mountID {
			continue
		}
		return strings.Join([]string{left[3], left[4], left[5], right[0], right[1], right[2]}, "\x00"), nil
	}
	return "", errors.New("socket parent mount ID is absent from mountinfo")
}

func containsMountOption(joined, expected string) bool {
	for _, field := range strings.Split(joined, "\x00") {
		for _, option := range strings.Split(field, ",") {
			if option == expected {
				return true
			}
		}
	}
	return false
}

func (identity piServiceSocketRuntimeIdentity) revalidate(socket piServiceSocketRegistration) error {
	current, err := measurePiServiceSocketPath(socket)
	if err != nil {
		return err
	}
	if current.Path != identity.Path || current.Parent != identity.Parent {
		return ErrPiServiceUnavailable
	}
	return nil
}

func readPiServicePeer(conn *net.UnixConn) (piServicePeerCredentials, error) {
	if conn == nil {
		return piServicePeerCredentials{}, ErrPiServiceRefused
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return piServicePeerCredentials{}, err
	}
	var credentials *syscall.Ucred
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, controlErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return piServicePeerCredentials{}, err
	}
	if controlErr != nil || credentials == nil || credentials.Pid <= 0 {
		return piServicePeerCredentials{}, errors.Join(ErrPiServiceRefused, controlErr)
	}
	return piServicePeerCredentials{PID: int(credentials.Pid), UID: int(credentials.Uid), GID: int(credentials.Gid)}, nil
}

func piServicePeerAllowed(socket piServiceSocketRegistration, peer piServicePeerCredentials) bool {
	if peer.PID <= 0 || peer.GID != socket.AllowedClientPrimaryGID {
		return false
	}
	index := sort.SearchInts(socket.AllowedClientUIDs, peer.UID)
	return index < len(socket.AllowedClientUIDs) && socket.AllowedClientUIDs[index] == peer.UID
}

func piServiceServerAllowed(peer piServicePeerCredentials, serviceUID, serviceGID int) bool {
	return peer.PID > 0 && serviceUID > 0 && serviceGID > 0 && peer.UID == serviceUID && peer.GID == serviceGID
}

func newPiServiceRequestJournal(journal *audit.Journal) (*piServiceRequestJournal, error) {
	if journal == nil {
		return nil, ErrPiServiceUnavailable
	}
	trace, err := journal.Trace()
	if err != nil {
		return nil, err
	}
	requestJournal := &piServiceRequestJournal{journal: journal, entries: make(map[string]piServiceRequestState)}
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "pi_service_request_accepted":
			var accepted piServiceRequestAcceptedEvent
			if err := unmarshalGovernedJSON(record.Event.Data, &accepted); err != nil ||
				accepted.SchemaVersion != "tbound-pi-service-request-accepted/v1" || accepted.ClientUID <= 0 ||
				accepted.ClientPID <= 0 || !boundedProductionIdentity(accepted.RequestID, productionRequestMaxBytes) ||
				!boundedProductionIdentity(accepted.TaskID, productionTaskIDMaxBytes) || !validDigestText(accepted.PayloadDigest) ||
				!optionalDigestText(accepted.ConfigDigest) || !optionalDigestText(accepted.RegistrationID) ||
				!optionalDigestText(accepted.EnvelopeDigest) {
				return nil, errors.Join(ErrPiServiceUnavailable, err)
			}
			key := piServiceRequestKey(accepted.ClientUID, accepted.RequestID)
			if record.Event.ID != key || requestJournal.entries[key].accepted.RequestID != "" {
				return nil, ErrPiServiceUnavailable
			}
			requestJournal.entries[key] = piServiceRequestState{accepted: accepted, acceptedRecord: record}
		case "pi_service_request_terminal":
			var terminal piServiceRequestTerminalEvent
			if err := unmarshalGovernedJSON(record.Event.Data, &terminal); err != nil ||
				terminal.SchemaVersion != "tbound-pi-service-request-terminal/v1" || terminal.ClientUID <= 0 ||
				!boundedProductionIdentity(terminal.RequestID, productionRequestMaxBytes) || !validDigestText(terminal.PayloadDigest) ||
				terminal.Response.SchemaVersion != piServiceResponseSchema || terminal.Response.RequestID != terminal.RequestID ||
				terminal.Response.Status != "completed" && terminal.Response.Status != "unknown" {
				return nil, errors.Join(ErrPiServiceUnavailable, err)
			}
			key := piServiceRequestKey(terminal.ClientUID, terminal.RequestID)
			state, ok := requestJournal.entries[key]
			if record.Event.ID != key+"/terminal" || !ok || state.terminal != nil ||
				state.accepted.PayloadDigest != terminal.PayloadDigest {
				return nil, ErrPiServiceUnavailable
			}
			response := terminal.Response
			state.terminal = &response
			requestJournal.entries[key] = state
		case "pi_service_request_abandoned":
			// The operator settlement of an unknown terminal. It must name that exact
			// terminal, so a record that does not match it refuses the service start.
			var abandoned piServiceRequestAbandonedEvent
			if err := unmarshalGovernedJSON(record.Event.Data, &abandoned); err != nil || abandoned.ClientUID <= 0 ||
				!boundedProductionIdentity(abandoned.RequestID, productionRequestMaxBytes) {
				return nil, errors.Join(ErrPiServiceUnavailable, err)
			}
			key := piServiceRequestKey(abandoned.ClientUID, abandoned.RequestID)
			state, ok := requestJournal.entries[key]
			if record.Event.ID != key+"/abandoned" || !ok || !abandoned.matches(state) {
				return nil, ErrPiServiceUnavailable
			}
			state.abandoned = &abandoned
			requestJournal.entries[key] = state
		}
	}
	return requestJournal, nil
}

func (j *piServiceRequestJournal) lookup(peer piServicePeerCredentials, request piServiceRequest, payloadDigest string) (piServiceRequestState,
	bool, error) {
	if j == nil {
		return piServiceRequestState{}, false, ErrPiServiceUnavailable
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	state, exists := j.entries[piServiceRequestKey(peer.UID, request.RequestID)]
	if !exists {
		return piServiceRequestState{}, false, nil
	}
	if state.accepted.PayloadDigest != payloadDigest || state.accepted.TaskID != request.TaskID ||
		state.accepted.ClientUID != peer.UID || state.accepted.ClientGID != peer.GID {
		return state, true, ErrPiServiceRequestConflict
	}
	return state, true, nil
}

func (j *piServiceRequestJournal) begin(ctx context.Context, peer piServicePeerCredentials,
	request piServiceRequest, payloadDigest string) (audit.Record, error) {
	if j == nil {
		return audit.Record{}, ErrPiServiceUnavailable
	}
	if ctx == nil {
		return audit.Record{}, ErrPiServiceUnavailable
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	key := piServiceRequestKey(peer.UID, request.RequestID)
	if _, exists := j.entries[key]; exists {
		return audit.Record{}, ErrPiServiceRequestConflict
	}
	// Guarantee. Every prior must be a completed request whose settlement verifies, or
	// the new request is refused. A settlement verification that passed is remembered
	// in memory for the life of this process, keyed to the exact journal state it
	// covered (accepted record hash and sequence, and the terminal response object)
	// and to a fingerprint of the evidence files that verification read (see
	// evidenceFingerprint: dev, ino, size, mtime_ns and ctime_ns of each file, one
	// lstat per file per begin). The memory is never persisted: a service start begins
	// empty and its first begin verifies every prior in full.
	//
	// What a cached prior is protected against within one running service: any change
	// to a file the verification read that moves its fingerprint (rewrite, growth,
	// truncation, replacement, deletion, chmod or chown, even with size and mtime
	// restored, because ctime cannot be set by a caller). Such a prior is verified
	// again at the next begin, and the admission is refused if it no longer verifies.
	// The mark also carries the workflow's own checkpoint and fence head in the
	// protected-root database (evidenceState: one read-only query per prior), so a change of
	// that head is caught even when the three files are unchanged.
	// What is not covered: shared append-only journals that every request grows
	// (catalog, publication journal), which cannot be fingerprinted per prior without
	// invalidating every prior on every request; a change confined to them is detected
	// at the next service start. A failed verification is never remembered. A prior whose
	// files change while it is being verified keeps its pre-verification fingerprint,
	// so it is verified again at the next begin.
	//
	// Abandoned priors are never cached: each is re-reconciled on every begin. All of them
	// share one fresh publication-journal snapshot that lives for this admission only
	// (admissionReconciler), so the journal is read and verified once per begin, not once
	// per abandoned prior.
	reconciler := j.admissionReconciler()
	for key, prior := range j.entries {
		if err := ctx.Err(); err != nil {
			return audit.Record{}, err
		}
		if prior.abandoned != nil && prior.terminal != nil && prior.terminal.Status == "unknown" {
			// An operator-settled unknown terminal (tbound abandon-unknown). Its entries
			// stay in the journal. The block on later requests is lifted only while the
			// reconciliation evidence bound in the abandon record still holds: it is
			// re-derived on every begin, never trusted from the record alone.
			if err := j.recheckAbandoned(ctx, reconciler, prior); err != nil {
				return audit.Record{}, fmt.Errorf("%w: %v", ErrPiServiceUnresolvedPrior, err)
			}
			continue
		}
		if prior.terminal == nil || prior.terminal.Status != "completed" || j.settlementVerifier == nil {
			return audit.Record{}, ErrPiServiceUnresolvedPrior
		}
		mark := piServiceVerifiedSettlement{acceptedHash: prior.acceptedRecord.Hash,
			acceptedSequence: prior.acceptedRecord.Sequence, terminal: prior.terminal}
		if j.evidenceFiles != nil {
			mark.evidence = evidenceFingerprint(j.evidenceFiles(prior))
		}
		if j.evidenceState != nil {
			mark.evidence += "|" + j.evidenceState(prior)
		}
		if j.verified[key] == mark {
			continue
		}
		if err := j.settlementVerifier(ctx, prior); err != nil {
			delete(j.verified, key)
			return audit.Record{}, fmt.Errorf("%w: %v", ErrPiServiceUnresolvedPrior, err)
		}
		if j.verified == nil {
			j.verified = make(map[string]piServiceVerifiedSettlement)
		}
		j.verified[key] = mark
	}
	accepted := piServiceRequestAcceptedEvent{SchemaVersion: "tbound-pi-service-request-accepted/v1",
		ClientUID: peer.UID, ClientGID: peer.GID, ClientPID: peer.PID, RequestID: request.RequestID,
		TaskID: request.TaskID, PayloadDigest: payloadDigest}
	if j.acceptanceBinding != nil {
		binding := j.acceptanceBinding(peer, request)
		accepted.ConfigDigest, accepted.RegistrationID, accepted.EnvelopeDigest =
			binding.ConfigDigest, binding.RegistrationID, binding.EnvelopeDigest
	}
	data, err := json.Marshal(accepted)
	if err != nil {
		return audit.Record{}, err
	}
	record, err := j.journal.Append(audit.Event{Kind: "pi_service_request_accepted", ID: key, Data: data})
	if err != nil {
		return audit.Record{}, err
	}
	j.entries[key] = piServiceRequestState{accepted: accepted, acceptedRecord: record}
	return record, nil
}

func (j *piServiceRequestJournal) complete(clientUID int, request piServiceRequest, payloadDigest string,
	response piServiceResponse) error {
	if j == nil {
		return ErrPiServiceUnavailable
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	key := piServiceRequestKey(clientUID, request.RequestID)
	state, exists := j.entries[key]
	if !exists || state.terminal != nil || state.accepted.PayloadDigest != payloadDigest ||
		response.SchemaVersion != piServiceResponseSchema || response.RequestID != request.RequestID ||
		response.Status != "completed" && response.Status != "unknown" {
		return ErrPiServiceRequestConflict
	}
	terminal := piServiceRequestTerminalEvent{SchemaVersion: "tbound-pi-service-request-terminal/v1",
		ClientUID: clientUID, RequestID: request.RequestID, PayloadDigest: payloadDigest, Response: response}
	data, err := json.Marshal(terminal)
	if err != nil {
		return err
	}
	if _, err := j.journal.Append(audit.Event{Kind: "pi_service_request_terminal", ID: key + "/terminal", Data: data}); err != nil {
		return err
	}
	state.terminal = &response
	j.entries[key] = state
	return nil
}

func piServiceRequestKey(clientUID int, requestID string) string {
	encoded, _ := json.Marshal(struct {
		ClientUID int    `json:"client_uid"`
		RequestID string `json:"request_id"`
	}{clientUID, requestID})
	sum := sha256.Sum256(encoded)
	return "pi-service-request-" + hex.EncodeToString(sum[:])
}

func piServicePayloadDigest(request piServiceRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}

func readPiServiceRequest(conn *net.UnixConn) (piServiceRequest, error) {
	var request piServiceRequest
	if conn == nil {
		return request, ErrPiServiceRefused
	}
	if err := conn.SetReadDeadline(time.Now().Add(piServiceReadTimeout)); err != nil {
		return request, err
	}
	var prefix [4]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return request, errors.Join(ErrPiServiceRefused, err)
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > piServiceClientMaxBytes {
		return request, ErrPiServiceFrameTooLarge
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(conn, data); err != nil {
		return request, errors.Join(ErrPiServiceRefused, err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(piServiceReadTimeout)); err != nil {
		return request, err
	}
	var trailing [1]byte
	if count, err := conn.Read(trailing[:]); count != 0 || !errors.Is(err, io.EOF) {
		return request, errors.Join(ErrPiServiceRefused, errors.New("service protocol accepts exactly one request frame per connection"), err)
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return request, errors.Join(ErrPiServiceRefused, err)
	}
	if err := unmarshalGovernedJSON(data, &request); err != nil {
		return piServiceRequest{}, errors.Join(ErrPiServiceRefused, err)
	}
	if err := validatePiServiceRequest(request); err != nil {
		return piServiceRequest{}, err
	}
	if request.BashOutputPaths != nil {
		cloned := cloneDurableOutputScopePaths(*request.BashOutputPaths)
		request.BashOutputPaths = &cloned
	}
	return request, nil
}

func writePiServiceFrame(conn net.Conn, response piServiceResponse) error {
	if conn == nil || response.SchemaVersion != piServiceResponseSchema ||
		response.Status != "completed" && response.Status != "busy" && response.Status != "unknown" && response.Status != "refused" {
		return ErrPiServiceRefused
	}
	data, err := json.Marshal(response)
	if err != nil || len(data) == 0 || len(data) > piServiceResponseMaxBytes {
		return errors.Join(ErrPiServiceRefused, err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(piServiceWriteTimeout)); err != nil {
		return err
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if err := writePiServiceAll(conn, prefix[:]); err != nil {
		return err
	}
	return writePiServiceAll(conn, data)
}

func writePiServiceAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		count, err := writer.Write(data)
		if err != nil {
			return err
		}
		if count <= 0 || count > len(data) {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

func readPiServiceResponse(conn net.Conn) (piServiceResponse, error) {
	var response piServiceResponse
	if conn == nil {
		return response, ErrPiServiceRefused
	}
	if err := conn.SetReadDeadline(time.Now().Add(productionSessionMaxRuntime + 30*time.Second)); err != nil {
		return response, err
	}
	var prefix [4]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return response, errors.Join(ErrPiServiceRefused, err)
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > piServiceResponseMaxBytes {
		return response, ErrPiServiceFrameTooLarge
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(conn, data); err != nil {
		return response, errors.Join(ErrPiServiceRefused, err)
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return response, errors.Join(ErrPiServiceRefused, err)
	}
	if err := unmarshalGovernedJSON(data, &response); err != nil || response.SchemaVersion != piServiceResponseSchema ||
		(response.RequestID != "" && !boundedProductionIdentity(response.RequestID, productionRequestMaxBytes)) ||
		response.Status != "completed" && response.Status != "busy" && response.Status != "unknown" && response.Status != "refused" ||
		len(response.Receipt) > piServiceResponseMaxBytes {
		return response, errors.Join(ErrPiServiceRefused, err)
	}
	if response.RequestID == "" && response.Status != "refused" {
		return response, ErrPiServiceRefused
	}
	if len(response.Receipt) > 0 {
		if err := protocol.ValidateStrictJSON(response.Receipt); err != nil {
			return response, errors.Join(ErrPiServiceRefused, err)
		}
	}
	return response, nil
}

func servePiServiceRequests(ctx context.Context, listener net.Listener, socket piServiceSocketRegistration,
	journal *piServiceRequestJournal, validateListener func() error, run piServiceTaskRunner) error {
	if ctx == nil || listener == nil || journal == nil || run == nil {
		return ErrPiServiceUnavailable
	}
	var active atomic.Bool
	var tasks sync.WaitGroup
	listenerClosed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-listenerClosed:
		}
	}()
	defer close(listenerClosed)
	for {
		if validateListener != nil {
			if err := validateListener(); err != nil {
				tasks.Wait()
				return errors.Join(ErrPiServiceUnavailable, err)
			}
		}
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				tasks.Wait()
				return nil
			}
			return errors.Join(ErrPiServiceUnavailable, err)
		}
		unixConn, ok := conn.(*net.UnixConn)
		if !ok {
			_ = conn.Close()
			continue
		}
		peer, err := readPiServicePeer(unixConn)
		if err != nil || !piServicePeerAllowed(socket, peer) {
			_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema, Status: "refused", ErrorCode: "peer-not-authorized"})
			_ = unixConn.Close()
			continue
		}
		request, err := readPiServiceRequest(unixConn)
		if err != nil {
			_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema, Status: "refused", ErrorCode: "invalid-request"})
			_ = unixConn.Close()
			continue
		}
		payloadDigest, err := piServicePayloadDigest(request)
		if err != nil {
			_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema,
				RequestID: request.RequestID, Status: "refused", ErrorCode: "invalid-request"})
			_ = unixConn.Close()
			continue
		}
		if code := journal.screen(peer, request); code != "" {
			_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema,
				RequestID: request.RequestID, Status: "refused", ErrorCode: code})
			_ = unixConn.Close()
			continue
		}
		prior, exists, lookupErr := journal.lookup(peer, request, payloadDigest)
		if lookupErr != nil {
			_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema,
				RequestID: request.RequestID, Status: "refused", ErrorCode: "request-id-conflict"})
			_ = unixConn.Close()
			continue
		}
		if exists && prior.terminal != nil {
			// A stored terminal is returned only if it still belongs to the active
			// manifest, config, registration and envelope. A refusal consumes nothing.
			if code := journal.checkTerminalReplay(peer, request, prior); code != "" {
				_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema,
					RequestID: request.RequestID, Status: "refused", ErrorCode: code})
				_ = unixConn.Close()
				continue
			}
			_ = writePiServiceFrame(unixConn, *prior.terminal)
			_ = unixConn.Close()
			continue
		}
		journal.runAfterScreen(request, &active)
		if !active.CompareAndSwap(false, true) {
			_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema,
				RequestID: request.RequestID, Status: "busy", ErrorCode: "one-task-at-a-time"})
			_ = unixConn.Close()
			continue
		}
		acceptedRecord := prior.acceptedRecord
		acceptedEvent := prior.accepted
		if !exists {
			// The screen ran before this request held the active slot. The task that held
			// it may have reserved this admission, or changed what the catalog admits, in
			// the meantime, so screen again now that nothing else can run. A refusal here
			// journals nothing and frees the slot, instead of becoming an accepted request
			// whose resolution fails and whose terminal unknown blocks later work.
			if code := journal.screen(peer, request); code != "" {
				active.Store(false)
				_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema,
					RequestID: request.RequestID, Status: "refused", ErrorCode: code})
				_ = unixConn.Close()
				continue
			}
			acceptedRecord, err = journal.begin(ctx, peer, request, payloadDigest)
			if err != nil {
				active.Store(false)
				errorCode := "request-journal-unavailable"
				if errors.Is(err, ErrPiServiceUnresolvedPrior) {
					errorCode = "prior-workflow-unsettled"
				}
				_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema,
					RequestID: request.RequestID, Status: "refused", ErrorCode: errorCode})
				_ = unixConn.Close()
				continue
			}
			// The accepted record is journaled, so a failure from here on is an unknown outcome.
			stored, _, lookupErr := journal.lookup(peer, request, payloadDigest)
			if lookupErr != nil {
				active.Store(false)
				_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema,
					RequestID: request.RequestID, Status: "unknown", ErrorCode: "request-journal-unavailable"})
				_ = unixConn.Close()
				continue
			}
			acceptedEvent = stored.accepted
		}
		if validateListener != nil {
			if err := validateListener(); err != nil {
				active.Store(false)
				_ = writePiServiceFrame(unixConn, piServiceResponse{SchemaVersion: piServiceResponseSchema,
					RequestID: request.RequestID, Status: "unknown", ErrorCode: "socket-authority-changed"})
				_ = unixConn.Close()
				continue
			}
		}
		tasks.Add(1)
		go func(conn *net.UnixConn, peer piServicePeerCredentials, request piServiceRequest, payloadDigest string,
			acceptedEvent piServiceRequestAcceptedEvent, acceptedRecord audit.Record, isReplay bool) {
			defer tasks.Done()
			defer active.Store(false)
			defer conn.Close()
			taskCtx, cancel := context.WithTimeout(ctx, productionSessionMaxRuntime)
			defer cancel()
			originalPeer := piServicePeerCredentials{PID: acceptedEvent.ClientPID, UID: acceptedEvent.ClientUID, GID: acceptedEvent.ClientGID}
			admission := piServiceAdmissionContext{Peer: originalPeer, CurrentPeer: peer, Request: request, PayloadDigest: payloadDigest,
				Accepted: acceptedEvent, AcceptedRecord: acceptedRecord, IsReplay: isReplay}
			receipt, runErr := run(withPiServiceAdmission(taskCtx, admission), request)
			status, errorCode := "completed", ""
			if len(receipt) == 0 || runErr != nil {
				status, errorCode = "unknown", "task-outcome-unknown"
			}
			response := piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID,
				Status: status, ErrorCode: errorCode, Receipt: append(json.RawMessage(nil), receipt...)}
			if isReplay && runErr != nil {
				// A failed host recovery attempt remains unresolved. It cannot be
				// converted to a fresh workflow or a terminal success by retrying.
				response = piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID,
					Status: "unknown", ErrorCode: "retained-workflow-recovery-unresolved"}
			} else if err := journal.complete(peer.UID, request, payloadDigest, response); err != nil {
				response = piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID,
					Status: "unknown", ErrorCode: "terminal-journal-unavailable"}
			}
			_ = writePiServiceFrame(conn, response) // disconnect never cancels accepted work
		}(unixConn, peer, request, payloadDigest, acceptedEvent, acceptedRecord, exists)
	}
}

func callProductionPiService(ctx context.Context, socket piServiceSocketRegistration, serviceUID, serviceGID int,
	request piServiceRequest, output io.Writer) error {
	if ctx == nil || output == nil || serviceUID <= 0 || serviceGID <= 0 ||
		validatePiServiceSocketRegistration(socket, serviceUID, serviceGID) != nil {
		return ErrPiServiceUnavailable
	}
	clientUID, clientGID := os.Geteuid(), os.Getegid()
	allowed := piServicePeerCredentials{PID: os.Getpid(), UID: clientUID, GID: clientGID}
	if !piServicePeerAllowed(socket, allowed) {
		return ErrPiServiceRefused
	}
	socketIdentity, err := measurePiServiceSocketPath(socket)
	if err != nil {
		return err
	}
	if err := validatePiServiceRequest(request); err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: piServiceReadTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socket.SocketPath)
	if err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return ErrPiServiceRefused
	}
	serverPeer, err := readPiServicePeer(unixConn)
	if err != nil || !piServiceServerAllowed(serverPeer, serviceUID, serviceGID) {
		return errors.Join(ErrPiServiceRefused, err)
	}
	if err := socketIdentity.revalidate(socket); err != nil {
		return errors.Join(ErrPiServiceRefused, err)
	}
	response, err := callPiServiceConn(unixConn, request)
	if err != nil {
		return err
	}
	if response.RequestID != request.RequestID || response.Status == "" {
		return ErrPiServiceRefused
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if _, err := output.Write(append(responseBytes, '\n')); err != nil {
		return err
	}
	if response.Status != "completed" {
		return fmt.Errorf("local Pi service %s: %s", response.Status, response.ErrorCode)
	}
	if len(response.Receipt) == 0 {
		return ErrPiServiceRefused
	}
	var receipt productionPiReceipt
	if err := unmarshalGovernedJSON(response.Receipt, &receipt); err != nil {
		return errors.Join(ErrPiServiceRefused, err)
	}
	if !receipt.Success {
		return errors.New("local Pi service completed with a non-success production disposition")
	}
	return nil
}

func runProductionPiServiceClient(ctx context.Context, output io.Writer, request piServiceRequest) error {
	if ctx == nil || output == nil {
		return ErrPiServiceUnavailable
	}
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	socket, contract, err := serviceSocketRegistrationFromSignedRegistration(registration)
	if err != nil {
		return err
	}
	serviceConfig, err := readProductionPiServiceConfig(contract.ServiceConfigPath, contract.ServiceConfigDigest)
	if err != nil {
		return err
	}
	if err := checkPiServiceClientRequest(serviceConfig, request); err != nil {
		return err
	}
	return callProductionPiService(ctx, socket, registration.ServiceUID, registration.ServiceGID, request, output)
}

// checkPiServiceClientRequest refuses, before anything is sent, a request whose shape
// does not match the installed service's admission mode. A corpus-mode service owns
// every prompt, so a client that supplies task text is refused here and again by the
// service; a single-task service takes no attempt.
func checkPiServiceClientRequest(config piServiceConfig, request piServiceRequest) error {
	if config.WorkflowAdmissionMode == piServiceAdmissionCorpus {
		if request.Prompt != "" {
			return fmt.Errorf("%w: the corpus-mode service takes its prompt from the frozen manifest; --task is not accepted", ErrPiServiceRefused)
		}
		if request.Attempt != 1 && request.Attempt != 2 {
			return fmt.Errorf("%w: the corpus-mode service needs --attempt 1 or 2", ErrPiServiceRefused)
		}
		if !validCorpusRole(request.Role) {
			return fmt.Errorf("%w: the corpus-mode service needs --role, one of trajectory, static-replay, adaptive-replay, variance-1, variance-2, compliant, ambiguous", ErrPiServiceRefused)
		}
		return nil
	}
	if request.Attempt != 0 || request.Role != "" {
		return fmt.Errorf("%w: --attempt and --role are accepted only by a corpus-mode service", ErrPiServiceRefused)
	}
	return nil
}

func writePiServiceRequest(conn net.Conn, data []byte) error {
	if conn == nil || len(data) == 0 || len(data) > piServiceClientMaxBytes {
		return ErrPiServiceFrameTooLarge
	}
	if err := conn.SetWriteDeadline(time.Now().Add(piServiceWriteTimeout)); err != nil {
		return err
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if err := writePiServiceAll(conn, prefix[:]); err != nil {
		return err
	}
	return writePiServiceAll(conn, data)
}

func callPiServiceConn(conn *net.UnixConn, request piServiceRequest) (piServiceResponse, error) {
	if conn == nil {
		return piServiceResponse{}, ErrPiServiceRefused
	}
	if err := validatePiServiceRequest(request); err != nil {
		return piServiceResponse{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > piServiceClientMaxBytes {
		return piServiceResponse{}, errors.Join(ErrPiServiceFrameTooLarge, err)
	}
	if err := writePiServiceRequest(conn, encoded); err != nil {
		return piServiceResponse{}, err
	}
	if err := conn.CloseWrite(); err != nil {
		return piServiceResponse{}, err
	}
	response, err := readPiServiceResponse(conn)
	if err != nil || response.RequestID != request.RequestID {
		return piServiceResponse{}, errors.Join(ErrPiServiceRefused, err)
	}
	return response, nil
}

// validatePiServiceRequest checks the wire form of a request. A single-task request
// carries a bounded prompt and no attempt. A corpus request (attempt present) names a
// case_id as task_id and an attempt of 1 or 2, and carries no prompt: the service owns
// the prompt. Whether the case exists is the service's decision, made against its
// frozen manifest before the request is journaled.
func validatePiServiceRequest(request piServiceRequest) error {
	if !boundedProductionIdentity(request.RequestID, productionRequestMaxBytes) ||
		!boundedProductionIdentity(request.TaskID, productionTaskIDMaxBytes) {
		return ErrPiServiceRefused
	}
	if request.Attempt != 0 || request.Role != "" {
		if (request.Attempt != 1 && request.Attempt != 2) || !validCorpusRole(request.Role) || request.Prompt != "" {
			return ErrPiServiceRefused
		}
	} else if !boundedProductionPrompt(request.Prompt) {
		return ErrPiServiceRefused
	}
	if request.BashOutputPaths != nil {
		if err := sessionrepo.ValidateOutputScopePaths(*request.BashOutputPaths); err != nil {
			return errors.Join(ErrPiServiceRefused, err)
		}
	}
	return nil
}

func (j *piServiceRequestJournal) validate() error {
	if j == nil || j.journal == nil {
		return ErrPiServiceUnavailable
	}
	return nil
}

func (j *piServiceRequestJournal) close() error {
	if j == nil || j.journal == nil {
		return nil
	}
	return j.journal.Close()
}

func piServiceRequestEventID(clientUID int, requestID string) string {
	return piServiceRequestKey(clientUID, requestID)
}

func piServiceParentPathIdentity(path string) (piServiceParentIdentity, error) {
	var identity piServiceParentIdentity
	file, err := openServiceDirectoryNoSymlinks(path)
	if err != nil {
		return identity, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return identity, ErrPiServiceUnavailable
	}
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(int(file.Fd()), &fs); err != nil {
		return identity, err
	}
	mountID, err := productionDescriptorMountID(file.Fd())
	if err != nil {
		return identity, err
	}
	fdPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil || filepath.Clean(fdPath) != filepath.Clean(path) {
		return identity, errors.Join(ErrPiServiceUnavailable, err)
	}
	identity = piServiceParentIdentity{Path: filepath.Clean(path), Device: uint64(stat.Dev), Inode: stat.Ino,
		MountID: mountID, Filesystem: uint64(fs.Type), UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode & 0o7777)}
	return identity, nil
}

func piServiceSocketRegistrationDigest(socket piServiceSocketRegistration) (string, error) {
	encoded, err := json.Marshal(socket)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}

func runProductionPiService(ctx context.Context, output io.Writer) error {
	if ctx == nil || output == nil {
		return ErrPiServiceUnavailable
	}
	registration, err := piruntime.LoadD09ManagedLiveRootRegistration(ctx)
	if err != nil {
		return fmt.Errorf("load fixed-anchor managed Pi service registration: %w", err)
	}
	candidateRoot, err := openServiceDirectoryNoSymlinks(registration.RootPath)
	if err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	verified, err := piruntime.VerifyD09ManagedLiveRoot(ctx, candidateRoot)
	if err == nil {
		err = verified.Revalidate(ctx, candidateRoot)
	}
	closeCandidateErr := candidateRoot.Close()
	if err != nil || closeCandidateErr != nil {
		if verified != nil {
			_ = verified.Close()
		}
		return errors.Join(fmt.Errorf("verify actual registered service/root identity: %w", err), closeCandidateErr)
	}
	defer verified.Close()
	registration = verified.Registration()
	verificationRoot, err := verified.DuplicateRoot()
	if err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	defer verificationRoot.Close()
	if err := verified.Revalidate(ctx, verificationRoot); err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	// The closed `cells` sibling of the runner leaf can only be created here,
	// inside the running unit's delegated cgroup, and before any listener,
	// binder or launcher exists. Failure refuses to start the service.
	if err := verified.EnsureCellsCgroup(ctx); err != nil {
		return errors.Join(ErrPiServiceUnavailable, fmt.Errorf("provision delegated cells cgroup: %w", err))
	}
	socket, contract, err := serviceSocketRegistrationFromSignedRegistration(registration)
	if err != nil {
		return err
	}
	serviceConfig, err := readProductionPiServiceConfig(contract.ServiceConfigPath, contract.ServiceConfigDigest)
	if err != nil {
		return err
	}
	if err := verifyPiServiceEnvironment(serviceConfig, registration); err != nil {
		return err
	}
	// A corpus-mode service admits only trials of its frozen manifest. The manifest is
	// read through the strict root-owned walk, must match the signed digest and pass
	// evalcorpus.Validate, or the service refuses to start before it listens.
	var corpus *productionCorpus
	if serviceConfig.WorkflowAdmissionMode == piServiceAdmissionCorpus {
		if corpus, err = loadProductionCorpus(serviceConfig); err != nil {
			return err
		}
	}
	// The provider route is fixed by the digest-pinned service config. A research
	// route opens its frozen profile, key and genesis ledger here, so a missing or
	// unfrozen input refuses the service before any listener or task exists.
	providerRoute, err := openProductionProviderRoute(serviceConfig, researchroute.DefaultPaths())
	if err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	defer providerRoute.Close()
	activated, err := systemdActivatedPiListener(socket)
	if err != nil {
		return err
	}
	defer activated.Close()
	requestJournal, err := audit.Open(piServiceRequestJournalPath)
	if err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	defer requestJournal.Close()
	requests, err := newPiServiceRequestJournal(requestJournal)
	if err != nil {
		return err
	}
	workflowCatalog, err := openPiServiceWorkflowCatalog(serviceConfig.WorkflowCatalogPath)
	if err != nil {
		return fmt.Errorf("open host-provisioned workflow admission catalog: %w", err)
	}
	defer workflowCatalog.Close()
	workflowCatalog.corpus = corpus
	// Requests the catalog could never admit (unknown case, bad attempt, client task
	// text, a used case/attempt) are refused before they are journaled as accepted.
	requests.setAdmissionScreen(workflowCatalog.admissionScreen(serviceConfig))
	registrationID := piServiceRegistrationIdentity(registration, socket, contract)
	if !validDigestText(registrationID) {
		return ErrPiServiceUnavailable
	}
	requests.setTerminalReplayCheck(workflowCatalog.terminalReplayCheck(serviceConfig, contract.ServiceConfigDigest, registrationID))
	requests.setAcceptanceBinding(workflowCatalog.acceptanceBinding(serviceConfig, contract.ServiceConfigDigest, registrationID))
	requests.settlementVerifier = func(settlementCtx context.Context, prior piServiceRequestState) error {
		template, _, err := piruntime.LoadD09CellTemplate(settlementCtx)
		if err != nil {
			return errors.Join(ErrPiServiceUnresolvedPrior, err)
		}
		profile, err := piruntime.LoadLinuxSignedHostProfile("")
		if err != nil {
			return errors.Join(ErrPiServiceUnresolvedPrior, err)
		}
		return workflowCatalog.verifyCompletedServiceRequest(settlementCtx, prior, contract.ServiceConfigDigest,
			registrationID, verified, template, profile)
	}
	// A cached settlement is tied to the per-request evidence files it read, and an abandoned
	// unknown request is skipped only while its workflow re-reconciles to the bound evidence.
	requests.setEvidenceFiles(workflowCatalog.settlementEvidenceFiles)
	requests.setEvidenceState(workflowCatalog.settlementEvidenceState)
	requests.setReconciler(newProductionUnknownReconciler(workflowCatalog, serviceConfig, providerRoute))
	runTask := func(taskCtx context.Context, request piServiceRequest) (json.RawMessage, error) {
		requestAdmission, ok := piServiceAdmissionFromContext(taskCtx)
		if !ok {
			return nil, errors.New("authenticated Pi service admission is missing from task context")
		}
		requestAdmission.ConfigDigest = contract.ServiceConfigDigest
		requestAdmission.RegistrationID = registrationID
		workflowAdmission, err := workflowCatalog.ResolveAdmission(requestAdmission, serviceConfig,
			contract.ServiceConfigDigest, registrationID)
		if err != nil {
			return nil, fmt.Errorf("resolve host-provisioned workflow admission: %w", err)
		}
		if err := requireWorkflowCatalogPath(workflowAdmission.Entry, serviceConfig); err != nil {
			return nil, err
		}
		var bashOutputPaths []string
		if request.BashOutputPaths != nil {
			bashOutputPaths = cloneDurableOutputScopePaths(*request.BashOutputPaths)
		}
		// The prompt that runs is the one the catalog admitted: the request's own in a
		// single-task mode (identical by the envelope check) and the manifest's in
		// corpus mode, where the request carries none.
		config := productionServeConfig{ServiceMode: true, ManagedLiveRootPath: registration.RootPath,
			TaskID: request.TaskID, TaskRequestID: request.RequestID, TaskPrompt: workflowAdmission.Entry.TaskPrompt,
			BashOutputPaths: bashOutputPaths, workflowAdmission: workflowAdmission, providerRoute: providerRoute}
		var receipt bytes.Buffer
		runErr := runProductionPiServe(taskCtx, &receipt, config, newProductionBinder())
		encoded := bytes.TrimSpace(receipt.Bytes())
		if len(encoded) == 0 {
			return nil, runErr
		}
		if err := protocol.ValidateStrictJSON(encoded); err != nil {
			return nil, errors.Join(errors.New("production Pi emitted a malformed terminal receipt"), err, runErr)
		}
		return append(json.RawMessage(nil), encoded...), runErr
	}
	return servePiServiceRequests(ctx, activated.Listener, socket, requests, activated.Revalidate, runTask)
}

// optionalDigestText accepts an absent digest or a valid one.
func optionalDigestText(value string) bool { return value == "" || validDigestText(value) }

// evidenceFingerprint is a cheap fingerprint of the evidence files a verification read: one
// lstat per file, committing to the path, device, inode, size, mtime_ns and ctime_ns (a file
// that is absent or cannot be examined is its own fingerprint). Any change that goes through
// the file system after the fingerprint was taken (write, truncate, replace, delete, chmod,
// chown) moves it; ctime moves even when size and mtime are put back, and cannot be set by an
// unprivileged caller. It is a change detector for a cache, not a content proof: a cache hit
// means "these files were not touched since a full verification passed".
func evidenceFingerprint(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	hash := sha256.New()
	for _, path := range paths {
		var stat syscall.Stat_t
		err := syscall.Lstat(path, &stat)
		switch {
		case err == nil:
			fmt.Fprintf(hash, "%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00", path, uint64(stat.Dev), uint64(stat.Ino),
				stat.Size, int64(stat.Mtim.Sec), int64(stat.Mtim.Nsec), int64(stat.Ctim.Sec), int64(stat.Ctim.Nsec))
		case errors.Is(err, syscall.ENOENT):
			fmt.Fprintf(hash, "%s\x00absent\x00", path)
		default:
			fmt.Fprintf(hash, "%s\x00unreadable:%v\x00", path, err)
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
