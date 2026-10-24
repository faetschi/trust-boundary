package vrun

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

const (
	// AcceptDir is where the acceptance cell sees its one read-only directory.
	AcceptDir = "/check"
	// AcceptProgram is the program file in AcceptDir: the agent's output file followed by the
	// pinned dataset tests (taskaccept node_test). Nothing else is mounted.
	AcceptProgram = "program.js"
	// AcceptTimeoutSeconds is podman's --timeout of the acceptance cell; it equals
	// taskaccept.NodeTestTimeout.
	AcceptTimeoutSeconds = 30
)

// AcceptSpec is one isolated acceptance cell: the signed cell image, the cell identity and limits,
// and the host directory holding only AcceptProgram.
type AcceptSpec struct {
	Name, ImageRef, CrunPath, CheckDir string
	UID, GID, PidsMax, MemoryMB        int
	CPUs                               string
}

// AcceptPodmanArgs is the podman argv of an acceptance cell. Unlike v-run it has no network, no
// workspace, runtime bundle or key mount: the only mount is CheckDir, read-only. The root file
// system is read-only, all capabilities are dropped, no_new_privs is set, the process runs as the
// unprivileged cell identity mapped to the host's own uid, and podman stops it after
// AcceptTimeoutSeconds.
func AcceptPodmanArgs(s AcceptSpec) ([]string, error) {
	switch {
	case !nameRE.MatchString(s.Name):
		return nil, errors.New("accept cell: container name is not plain")
	case !imageRefRE.MatchString(s.ImageRef):
		return nil, fmt.Errorf("accept cell: image reference %q is not pinned by digest", s.ImageRef)
	case !path.IsAbs(s.CrunPath) || !path.IsAbs(s.CheckDir):
		return nil, errors.New("accept cell: runtime and check directory must be absolute paths")
	case strings.ContainsAny(s.CheckDir, ",=\x00"):
		return nil, fmt.Errorf("accept cell: path %q cannot be used in a mount option", s.CheckDir)
	case s.UID <= 0 || s.GID <= 0:
		return nil, errors.New("accept cell: the container must not run as root")
	case s.PidsMax <= 0 || s.MemoryMB <= 0 || !cpusRE.MatchString(s.CPUs):
		return nil, errors.New("accept cell: resource limits are not set")
	}
	uid, gid := strconv.Itoa(s.UID), strconv.Itoa(s.GID)
	return []string{
		"run", "--name", s.Name, "--rm", "--pull=never", "--runtime", s.CrunPath,
		"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(s.PidsMax), "--memory", strconv.Itoa(s.MemoryMB) + "m", "--cpus", s.CPUs,
		"--uidmap", uid + ":0:1", "--gidmap", gid + ":0:1", "--user", uid + ":" + gid,
		"--workdir", AcceptDir, "--timeout", strconv.Itoa(AcceptTimeoutSeconds),
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=16m,mode=1777",
		"--mount", "type=bind,src=" + s.CheckDir + ",dst=" + AcceptDir + ",ro,bind-propagation=rprivate",
		"-e", "HOME=/tmp", "-e", "LANG=C.UTF-8",
		"--entrypoint", NodePath, s.ImageRef, AcceptDir + "/" + AcceptProgram,
	}, nil
}
