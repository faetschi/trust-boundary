//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductRegistrationMeasurePrintsTheDigestsTheServiceChecks(t *testing.T) {
	project := filepath.Join(t.TempDir(), "demo")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, raw := adminGatePolicy(false)
	policyFile := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policyFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	out := mustAdmin(t, "product", "registration", "measure", "--project-dir", project, "--gate-policy", policyFile)

	root, err := os.Open(project)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	identity, err := recoveryDirectoryIdentityDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"root=" + sha256DigestFromBarePin(identity), "policy=" + productRegisteredPolicyDigest(policy)} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output %q lacks %q", out, want)
		}
	}
}

func TestProductRegistrationMeasureRefusesWhatTheServiceWouldRefuse(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	shared := filepath.Join(dir, "shared")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o755); err != nil { // the umask must not decide the mode
		t.Fatal(err)
	}
	_, raw := adminGatePolicy(false)
	good := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(good, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"a project directory others can read": {"--project-dir", shared, "--gate-policy", good},
		"a missing project directory":         {"--project-dir", filepath.Join(dir, "none"), "--gate-policy", good},
		"an invalid gate policy":              {"--project-dir", private, "--gate-policy", bad},
		"no gate policy":                      {"--project-dir", private},
		"no project directory":                {"--gate-policy", good},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := adminRun(append([]string{"product", "registration", "measure"}, args...)...); adminExit(err) != 2 {
				t.Fatalf("exit %d, want 2: %v", adminExit(err), err)
			}
		})
	}
}

func TestCgroupKillIsFoundBelowTheRootWhereTheKernelCreatesIt(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "system.slice")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if cgroupKillAvailable(root) {
		t.Fatal("reported cgroup.kill on a host without one")
	}
	if err := os.WriteFile(filepath.Join(child, "cgroup.kill"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !cgroupKillAvailable(root) {
		t.Fatal("missed cgroup.kill in a child cgroup, the only place the kernel creates it")
	}
}
