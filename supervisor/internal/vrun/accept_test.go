package vrun

import (
	"slices"
	"strings"
	"testing"
)

func acceptSpec() AcceptSpec {
	return AcceptSpec{Name: "tbound-accept-abc", ImageRef: "localhost/pi-cell@sha256:" + strings.Repeat("a", 64),
		CrunPath: "/usr/bin/crun", CheckDir: "/var/lib/tbound/v-work/accept-abc", UID: 1000, GID: 1000,
		PidsMax: 128, MemoryMB: 512, CPUs: "1"}
}

// The acceptance cell runs only the program file, read-only, with no network, no workspace and no
// other host path, as the unprivileged cell identity, under a fixed timeout.
func TestAcceptPodmanArgsIsolateTheCheck(t *testing.T) {
	args, err := AcceptPodmanArgs(acceptSpec())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt no-new-privileges",
		"--rm", "--pull=never", "--user 1000:1000", "--uidmap 1000:0:1", "--gidmap 1000:0:1", "--timeout 30",
		"--mount type=bind,src=/var/lib/tbound/v-work/accept-abc,dst=/check,ro,bind-propagation=rprivate",
		"--entrypoint /usr/local/bin/node", "localhost/pi-cell@sha256:" + strings.Repeat("a", 64) + " /check/program.js"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q:\n%s", want, joined)
		}
	}
	mounts := 0
	for i, a := range args {
		if a == "--mount" || a == "-v" || a == "--volume" {
			mounts++
			if a != "--mount" || !strings.HasSuffix(args[i+1], ",ro,bind-propagation=rprivate") {
				t.Errorf("mount %q is not the single read-only check mount", args[i+1])
			}
		}
	}
	for _, bad := range []string{WorkDir, RuntimeDir, ForwarderDir, "--privileged", "--network=host", "--net=", ",rw"} {
		if strings.Contains(joined, bad) {
			t.Errorf("argv contains %q:\n%s", bad, joined)
		}
	}
	if mounts != 1 || !slices.Contains(args, "--tmpfs") {
		t.Errorf("want exactly one mount and a /tmp tmpfs: %s", joined)
	}
}

func TestAcceptPodmanArgsRefusesUnsafeSpecs(t *testing.T) {
	for name, mutate := range map[string]func(*AcceptSpec){
		"root":           func(s *AcceptSpec) { s.UID = 0 },
		"unpinned image": func(s *AcceptSpec) { s.ImageRef = "localhost/pi-cell:latest" },
		"relative dir":   func(s *AcceptSpec) { s.CheckDir = "accept" },
		"comma in dir":   func(s *AcceptSpec) { s.CheckDir = "/x,dst=/" },
		"no limits":      func(s *AcceptSpec) { s.PidsMax = 0 },
		"bad name":       func(s *AcceptSpec) { s.Name = "-x" },
	} {
		s := acceptSpec()
		mutate(&s)
		if _, err := AcceptPodmanArgs(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
