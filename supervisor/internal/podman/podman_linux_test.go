//go:build linux

package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	testViewID     = "view-000000000000000000000000000000000000000000000000"
	testLeaseID    = "lease-podman-conformance"
	policyDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	metadataDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	xattrDigest    = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// requireProfile returns a usable measured profile or skips when rootless
// podman is genuinely absent. A present-but-misconfigured host fails loudly.
func requireProfile(t *testing.T) Report {
	t.Helper()
	report, err := Probe()
	if err != nil {
		if errors.Is(err, ErrUnsupported) && !report.PodmanPresent {
			t.Skipf("rootless podman is not installed: %v", err)
		}
		t.Fatalf("rootless podman profile is not usable: %v (unsupported=%v)", err, report.Unsupported)
	}
	return report
}

func TestProbe(t *testing.T) {
	report := requireProfile(t)
	if !report.PodmanPresent || report.PodmanPath == "" {
		t.Fatalf("probe did not locate podman: %+v", report)
	}
	if report.PodmanVersion == "" {
		t.Fatalf("probe did not record a podman version: %+v", report)
	}
	if !report.Rootless {
		t.Fatalf("podman is not rootless: %+v", report)
	}
	if !strings.EqualFold(report.OCIRuntime, "crun") {
		t.Fatalf("OCI runtime is not crun: %q", report.OCIRuntime)
	}
	if report.CgroupVersion != "v2" {
		t.Fatalf("cgroup version is not v2: %q", report.CgroupVersion)
	}
	if !report.UserNamespace {
		t.Fatalf("user namespace was not proven usable: %+v", report)
	}
	if report.ImageRef == "" {
		t.Fatalf("probe did not select a local candidate image: %+v", report)
	}
	if !strings.Contains(report.EvidenceClass(), "non-claim-bearing") {
		t.Fatalf("evidence class is not explicitly non-claim-bearing: %q", report.EvidenceClass())
	}
	t.Logf("probe: podman=%s path=%s rootless=%t runtime=%s runtime_version=%q cgroup=%s userns=%t image=%s pinned=%t digest=%s evidence=%s unsupported=%v",
		report.PodmanVersion, report.PodmanPath, report.Rootless, report.OCIRuntime, report.OCIRuntimeVersion,
		report.CgroupVersion, report.UserNamespace, report.ImageRef, report.ImagePinned, report.ImageDigest,
		report.EvidenceClass(), report.Unsupported)
}

// TestConformance runs trivial commands in the cell and asserts the observable
// postconditions. It never asserts containment.
func TestConformance(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("conformance requires an unprivileged invoking user for a non-root cell")
	}
	report := requireProfile(t)
	runner := New(testLeaseID)
	if report.ImageRef != "" {
		runner.Image = report.ImageRef
	}
	viewPath := t.TempDir()
	if err := os.Chmod(viewPath, 0o700); err != nil {
		t.Fatal(err)
	}

	const script = `echo "UID=$(id -u)"
echo "GID=$(id -g)"
echo "NNP=$(awk '/^NoNewPrivs:/{print $2}' /proc/self/status)"
echo "CAPEFF=$(awk '/^CapEff:/{print $2}' /proc/self/status)"
echo "SECCOMP=$(awk '/^Seccomp:/{print $2}' /proc/self/status)"
echo "CGROUP=$(cat /proc/self/cgroup)"
echo "ROUTES=$(awk 'NR>1 && NF>0 {print $2}' /proc/net/route | tr '\n' ',')"
if printf probe > /etc/tbound-write 2>/dev/null; then echo "ROOTFS=writable"; else echo "ROOTFS=readonly"; fi
if printf probe > /work/tbound-write 2>/dev/null; then echo "WORK=writable"; else echo "WORK=readonly"; fi
echo DONE`

	spec := sessionrepo.CommandSpec{Executable: "/bin/sh", Args: []string{"-c", script}}
	t.Logf("cell invocation: podman %s",
		strings.ReplaceAll(strings.Join(cellArguments(runner.normalized(report), "tbound-cell-<token>", viewPath, spec), " "), "\n", "\\n"))

	result, settlement, err := runner.runCell(context.Background(), report, viewPath, testViewID, testLeaseID, spec)
	if err != nil {
		t.Fatalf("run cell: %v", err)
	}
	output := string(result.Stdout)
	if result.ExitCode != 0 || !result.ExitObserved {
		t.Fatalf("cell did not exit cleanly: %+v\nstdout:\n%s\nstderr:\n%s", result, output, result.Stderr)
	}
	if !strings.Contains(output, "DONE") {
		t.Fatalf("cell did not run the target to completion:\nstdout:\n%s\nstderr:\n%s", output, result.Stderr)
	}

	if got := fieldValue(t, output, "UID"); got != strconv.Itoa(os.Geteuid()) || got == "0" {
		t.Fatalf("cell uid is not the invoking non-root user: got %q want %d", got, os.Geteuid())
	}
	if got := fieldValue(t, output, "GID"); got != strconv.Itoa(os.Getegid()) {
		t.Fatalf("cell gid is not the invoking user: got %q want %d", got, os.Getegid())
	}
	if got := fieldValue(t, output, "NNP"); got != "1" {
		t.Fatalf("NoNewPrivs is not set: %q", got)
	}
	if got := fieldValue(t, output, "CAPEFF"); got != "0000000000000000" {
		t.Fatalf("capability sets are not empty: %q", got)
	}
	if got := fieldValue(t, output, "SECCOMP"); got != "2" {
		t.Fatalf("seccomp is not in filter mode: %q", got)
	}
	if got := fieldValue(t, output, "CGROUP"); !strings.HasPrefix(got, "0::") {
		t.Fatalf("cell has no cgroup-v2 membership: %q", got)
	}
	if got := fieldValue(t, output, "ROUTES"); got != "" {
		t.Fatalf("cell has an external route: %q", got)
	}
	if got := fieldValue(t, output, "ROOTFS"); got != "readonly" {
		t.Fatalf("cell root filesystem is not read-only: %q", got)
	}
	if got := fieldValue(t, output, "WORK"); got != "writable" {
		t.Fatalf("cell /work is not writable: %q", got)
	}
	written, err := os.ReadFile(filepath.Join(viewPath, "tbound-write"))
	if err != nil || string(written) != "probe" {
		t.Fatalf("cell /work write did not reach the command view: data=%q err=%v", written, err)
	}

	assertSettlement(t, settlement, result.ExitCode, testViewID, testLeaseID)

	// Exit-code propagation: a nonzero target exit must survive the entrypoint.
	exitResult, exitSettlement, err := runner.runCell(context.Background(), report, viewPath, testViewID, testLeaseID,
		sessionrepo.CommandSpec{Executable: "/bin/sh", Args: []string{"-c", "exit 42"}})
	if err != nil {
		t.Fatalf("run exit-code cell: %v", err)
	}
	if exitResult.ExitCode != 42 || !exitResult.ExitObserved {
		t.Fatalf("target exit code was not propagated: %+v", exitResult)
	}
	assertSettlement(t, exitSettlement, 42, testViewID, testLeaseID)
}

func assertSettlement(t *testing.T, settlement sessionrepo.CommandSettlement, wantExit int, viewID, leaseID string) {
	t.Helper()
	if !settlement.ExitObserved || settlement.ExitCode != wantExit {
		t.Fatalf("settlement did not observe the real exit: %+v want=%d", settlement, wantExit)
	}
	if settlement.ViewID != viewID || settlement.LeaseID != leaseID {
		t.Fatalf("settlement did not echo view/lease: %+v", settlement)
	}
	if !settlement.ProcessScopeEmpty || !settlement.WritersStopped || !settlement.MountDetached {
		t.Fatalf("settlement postconditions are not observed: %+v", settlement)
	}
	if !strings.Contains(settlement.EvidenceClass, "non-claim-bearing") {
		t.Fatalf("settlement is not an honest non-claim-bearing receipt: %q", settlement.EvidenceClass)
	}
}

func fieldValue(t *testing.T, output, key string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	t.Fatalf("missing %s= in cell output:\n%s", key, output)
	return ""
}

// TestStoreRunBashIntegration drives the real store.RunBash seam with the real
// rootless-Podman runner. It proves the sealed source generation is unchanged,
// the command view is imported as a new generation, and the recorded
// containment status remains "not-established".
func TestStoreRunBashIntegration(t *testing.T) {
	report := requireProfile(t)
	_ = report
	base := privateBase(t)
	storePath := filepath.Join(base, "repository")
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(base, "audit")
	if err := os.Mkdir(auditPath, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(auditPath, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	root, err := os.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	options := sessionrepo.Options{
		PolicyDigest:         policyDigest,
		MetadataPolicyDigest: metadataDigest,
		Limits:               workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{
			ProfileDigest: xattrDigest,
			Complete:      true,
		},
		Journal: journal,
		AuthorizeOperation: func(sessionrepo.OperationRequest) error {
			return nil
		},
		VerifyDecision: func(delta.PolicyDecision, delta.TransitionBinding) error {
			return nil
		},
		VerifySettlement: func(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
			if settlement.ViewID != viewID || settlement.LeaseID != leaseID ||
				!settlement.ProcessScopeEmpty || !settlement.WritersStopped ||
				!settlement.MountDetached || !settlement.ExitObserved {
				return fmt.Errorf("incomplete settlement: %+v", settlement)
			}
			if !strings.Contains(settlement.EvidenceClass, "non-claim-bearing") {
				return fmt.Errorf("settlement is not an honest non-claim-bearing receipt: %q", settlement.EvidenceClass)
			}
			return nil
		},
	}
	store, err := sessionrepo.Create(root, options)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sourcePath := filepath.Join(base, "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sourcePath, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(sourcePath, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
	writeFile(t, filepath.Join(sourcePath, "README.md"), "baseline text\n", 0o644)
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}

	lease := delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: "lease-podman-integration"}
	decision := delta.PolicyDecision{
		ID: "decision-podman-integration", Outcome: delta.PolicyAllow,
		PolicyDigest: policyDigest, MetadataPolicyDigest: metadataDigest,
	}
	spec := sessionrepo.CommandSpec{
		Executable: "/bin/sh",
		Args:       []string{"-c", "printf 'command output\\n' > build/result.txt && printf 'command completed\\n'"},
	}
	mutation, err := store.RunBash(context.Background(), g0, lease, decision, spec, New(lease.LeaseID))
	if err != nil {
		t.Fatalf("RunBash with podman runner: %v", err)
	}
	if mutation.Generation == nil || mutation.Generation.ID() != "g1" {
		t.Fatalf("command did not promote g1: %+v", mutation)
	}
	if mutation.Command == nil || mutation.Command.ExitCode != 0 || !mutation.Command.ExitObserved {
		t.Fatalf("command result not observed: %+v", mutation.Command)
	}
	if !strings.Contains(string(mutation.Command.Stdout), "command completed") {
		t.Fatalf("command stdout not captured: %q", mutation.Command.Stdout)
	}
	if g0.ID() != "g0" || g0.TreeDigest() == "" {
		t.Fatalf("baseline generation lost identity: %+v", g0)
	}
	if chain, err := store.Verify(); err != nil || chain.SealedGeneration != "g1" || chain.TransitionCount != 1 {
		t.Fatalf("verify chain after podman command: result=%+v err=%v", chain, err)
	}
	evidence, err := store.Evidence()
	if err != nil {
		t.Fatalf("collect evidence: %v", err)
	}
	var bashOperation *sessionrepo.OperationEvidence
	for index := range evidence.Operations {
		if evidence.Operations[index].Tool == "bash" {
			bashOperation = &evidence.Operations[index]
		}
	}
	if bashOperation == nil {
		t.Fatalf("no bash operation recorded: %+v", evidence.Operations)
	}
	if bashOperation.CommandContainmentStatus != "not-established" {
		t.Fatalf("podman runner overstated containment: %+v", bashOperation)
	}
	if bashOperation.CommandRunnerProfile != Profile {
		t.Fatalf("runner profile not recorded: got %q want %q", bashOperation.CommandRunnerProfile, Profile)
	}
	t.Logf("podman-backed RunBash promoted %s with runner %q and containment %q",
		mutation.Generation.ID(), bashOperation.CommandRunnerProfile, bashOperation.CommandContainmentStatus)
}

func privateBase(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, home)
	}
	if tmp := os.TempDir(); tmp != "" {
		candidates = append(candidates, tmp)
	}
	for _, base := range candidates {
		if !ancestorsArePrivate(base) {
			continue
		}
		directory, err := os.MkdirTemp(base, ".podman-test-")
		if err != nil {
			continue
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			_ = os.RemoveAll(directory)
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(directory) })
		return directory
	}
	t.Skip("no private base directory with a trusted ancestor chain is available")
	return ""
}

func ancestorsArePrivate(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for {
		info, err := os.Lstat(absolute)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
			return false
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return true
		}
		absolute = parent
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
