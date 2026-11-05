//go:build linux

package piruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/sessionrepo"
)

// The goldens under testdata/thesis-d09-supplementary are the bytes the baseline 21881490 produces,
// written by the baseline generator (TestGenerateThesisD09SupplementaryGoldens), never by this tree.
// A change that moves a thesis command outcome, proof, receipt, or managed-root signing byte fails here.
const thesisD09SupplementaryGoldenDir = "testdata/thesis-d09-supplementary"

// thesisD09SupplementaryOutcome is the command outcome the runner returns to the Store. A denied
// pre-start outcome carries the error text and zero result and settlement.
type thesisD09SupplementaryOutcome struct {
	Error      string                        `json:"error,omitempty"`
	Result     sessionrepo.CommandResult     `json:"result"`
	Settlement sessionrepo.CommandSettlement `json:"settlement"`
}

// thesisD09SupplementaryArtifacts returns the exact bytes each golden file must hold. Every input is a
// literal, so the bytes depend only on the production serialization under test.
func thesisD09SupplementaryArtifacts(t *testing.T) map[string][]byte {
	t.Helper()
	imageDigest := DigestBytes([]byte("image"))
	templateDigest := DigestBytes([]byte("template"))
	// Kernel /proc/self/uid_map layout: the cell's uid and gid 1000 map to host ID 0, a single own-ID
	// range with no subordinate IDs. The same mapping is in the command cell argv.
	idMap := fmt.Sprintf("%10d %10d %10d", 1000, 0, 1)
	proof := D09CommandProof{
		Schema: "tbound-pi-entrypoint/v1", UID: 1000, GID: 1000, CapEff: "0000000000000000",
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: DigestBytes([]byte("landlock")),
		LandlockTSYNC: true, CgroupV2: true, UIDMap: idMap, GIDMap: idMap,
		UserNamespaceMapDigest: DigestBytes([]byte("userns-map")), LeaderPID: 321, ContainerLeaderPID: 1,
		LeaseID: "lease-1", ViewID: "view-1", CommandStarted: true, CommandReady: true,
		CommandExitObserved: true, CommandExitCode: 0, CommandPath: "/bin/bash",
		CommandDigest: DigestBytes([]byte("bash-executable")), CommandWorkdir: "/work",
		ContainerName: "cell-1", ImageRef: "localhost/tbound/pi-cell@" + imageDigest,
		ImageDigest: imageDigest, TemplateDigest: templateDigest,
		ConsumerMountsVerified: true, RawSourceFDsClosed: true,
		WorkerMountID: 35, RuntimeMountID: 36, WorkerDevice: 43, WorkerInode: 1048577,
		RuntimeDevice: 43, RuntimeInode: 1048578, UserNSInode: 4026531837, MountNSInode: 4026532244,
		PIDNSInode: 4026532246, IPCNSInode: 4026532245, UTSNSInode: 4026532243, NetworkNSInode: 4026532248,
		NoExternalRoutes: true,
	}
	result := sessionrepo.CommandResult{ExitCode: 0, ExitObserved: true, Stdout: []byte("ok\n")}
	settlement := sessionrepo.CommandSettlement{
		LeaseID: "lease-1", ViewID: "view-1", ProcessScopeEmpty: true, WritersStopped: true,
		MountDetached: true, ExitObserved: true, ExitCode: 0,
		EvidenceClass: D09CommandRunnerProfile + ":inspect-exit+podman-wait+proof-pipe-eof+output-drained+container-removed+leader-pidfd+cgroup-populated0+mountns-released+cgroup-removed",
	}
	evidence := D09CommandSettlementEvidence{
		ContainerExitInspected: true, FinalCommandProofRead: true, PodmanWaitCompleted: true,
		OutputWritersDrained: true, ContainerRemoved: true, LeaderPidfdExited: true,
		CgroupPopulatedZero: true, MountNamespaceReleased: true, CommandOutputWithinBound: true,
		CommandCgroupRemoved: true,
	}
	spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}}
	receipt, err := newD09CommandReceipt("lease-1", "view-1", "cell-1", strings.Repeat("a", 64), spec, result, settlement, proof, evidence)
	if err != nil {
		t.Fatalf("issue the golden receipt: %v", err)
	}
	if err := receipt.ValidateSnapshot(); err != nil {
		t.Fatalf("the golden receipt is not a valid snapshot: %v", err)
	}
	proofJSON, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	successJSON, err := json.Marshal(thesisD09SupplementaryOutcome{Result: result, Settlement: settlement})
	if err != nil {
		t.Fatal(err)
	}
	denied := d09PreStartFailure(errors.New("fixed pre-start cause"), false, true)
	if !errors.Is(denied, protocol.ErrCommandNotStarted) {
		t.Fatalf("the denied pre-start outcome lost ErrCommandNotStarted: %v", denied)
	}
	deniedJSON, err := json.Marshal(thesisD09SupplementaryOutcome{Error: denied.Error()})
	if err != nil {
		t.Fatal(err)
	}
	registration := D09ManagedLiveRootRegistration{
		SchemaVersion: D09ManagedLiveRootSchema, WriterPolicy: "managed-root-writer-policy-golden",
		RootPath: "/var/lib/tbound/managed-root", RootDevice: 2049, RootInode: 1001, RootFilesystemMagic: 61267,
		RootMountID: 42, RootMountOptionsDigest: DigestBytes([]byte("root-mount")), RootUID: 0, RootGID: 0, RootMode: 0o750,
		ParentPath: "/var/lib/tbound", ParentDevice: 2049, ParentInode: 1000, ParentFilesystemMagic: 61267,
		ParentMountID: 42, ParentMountOptionsDigest: DigestBytes([]byte("parent-mount")), ParentUID: 0, ParentGID: 0, ParentMode: 0o755,
		ServiceName: "tbound-pi", ServiceGroup: "tbound-pi", ServiceUID: 990, ServiceGID: 990,
		ServiceLoginShell: "/usr/sbin/nologin", ServiceShellDigest: DigestBytes([]byte("nologin")),
		PasswdDigest: DigestBytes([]byte("passwd")), GroupDigest: DigestBytes([]byte("group")),
		NSSwitchDigest:  DigestBytes([]byte("nsswitch")),
		ServiceUnitPath: "/etc/systemd/system/tbound-pi.service", ServiceUnitDigest: DigestBytes([]byte("unit")),
		MountUnitPath: "/etc/systemd/system/var-lib-tbound-managed.mount", MountUnitDigest: DigestBytes([]byte("mount-unit")),
		ServiceConfigPath: "/etc/tbound/pi-service.json", ServiceConfigDigest: DigestBytes([]byte("service-config")),
		ServiceWritablePaths:              []string{"/var/lib/tbound/managed-root/state"},
		ManagedRootXattrCalibrationDigest: DigestBytes([]byte("xattr")),
		RunnerPath:                        "/usr/local/bin/tbound", RunnerDigest: DigestBytes([]byte("runner")),
		ServiceCgroupPath: "/system.slice/tbound-pi.service",
	}
	signingBytes, err := D09ManagedLiveRootSigningBytes(registration)
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"proof-snapshot.json":            proofJSON,
		"receipt-snapshot.json":          receiptJSON,
		"command-success.json":           successJSON,
		"command-denied.json":            deniedJSON,
		"managed-root-signing-bytes.bin": signingBytes,
	}
}

func TestThesisD09SupplementaryRecordGoldensAreUnchanged(t *testing.T) {
	artifacts := thesisD09SupplementaryArtifacts(t)
	names := make([]string, 0, len(artifacts))
	for name := range artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		got := artifacts[name]
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(thesisD09SupplementaryGoldenDir, name))
			if err != nil {
				t.Fatalf("read golden %s (generate it from baseline 21881490): %v", name, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s drifted from the baseline golden:\n got %q\nwant %q", name, got, want)
			}
		})
	}
}
