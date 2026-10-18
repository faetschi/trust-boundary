package vrun

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Paths inside the V container.
const (
	// WorkDir is where the workspace (the fresh fixture copy) is mounted and Pi's working directory.
	WorkDir = "/work"
	// RuntimeDir is where the read-only Pi runtime bundle is mounted, as in the governed cell.
	RuntimeDir = "/runtime"
	// KeyFile is where the provider key file is mounted read-only.
	KeyFile = "/run/tbound/opencode-go.key"
	// Home is Pi's HOME, on the container's tmpfs.
	Home = "/tmp/home"
)

// Provider and CLI constants of the engineering route. Pi 0.87.1 ships an "opencode-go"
// provider whose API key it reads from OPENCODE_API_KEY.
const (
	PiProvider = "opencode-go"
	// NodePath is the Node binary of the cell image (the governed cell execs the same one).
	NodePath = "/usr/local/bin/node"
	// PiEntry is the official Pi CLI inside the runtime bundle.
	PiEntry = RuntimeDir + "/cli/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js"
)

// launchScript is the container's command: bash reads the key file into OPENCODE_API_KEY (so the
// key never appears in an argument list or in the image) and execs Node on the Pi CLI. The
// positional parameters are Node, the CLI entry, the model id and the prompt, all passed as
// argv words and never interpreted by the shell. Pi runs natively: no TBound extension, no tool
// replacement, the default tools (read, bash, edit, write), one prompt, JSONL events on stdout,
// no session file. The other --no-* flags and --offline match the governed cell's Pi, so that
// only the TBound layer differs.
const launchScript = `set -eu
umask 077
mkdir -p "$HOME" "$TMPDIR"
OPENCODE_API_KEY="$(cat ` + KeyFile + `)"
export OPENCODE_API_KEY
exec "$1" "$2" --mode json --no-session --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files --no-approve --offline --provider ` + PiProvider + ` --model "` + PiProvider + `/$3" -- "$4"`

// Spec is everything the argument vector depends on. All of it is chosen by the guest from the
// signed template, the pinned manifest and its own work directory; none comes from the caller.
type Spec struct {
	// Name is the container name (so that a timed-out run can be removed by name).
	Name string
	// ImageRef is the image reference pinned by digest: <repository>@sha256:<64 hex>.
	ImageRef string
	// CrunPath is the registered OCI runtime.
	CrunPath string
	// UID and GID are the identity the container runs as: the cell uid and gid. The user
	// namespace maps exactly that one pair, as in the governed cell.
	UID, GID int
	// Workspace is the host directory mounted read-write at WorkDir.
	Workspace string
	// RuntimeBundle is the host directory of the Pi runtime bundle, mounted read-only.
	RuntimeBundle string
	// KeyFile is the host path of the provider key file, mounted read-only.
	KeyFile string
	// Model is the provider model id under PiProvider.
	Model string
	// Prompt is the case's trial prompt from the pinned manifest.
	Prompt string
	// DurationSeconds is the hard deadline of the container (podman --timeout).
	DurationSeconds uint64
	// PidsMax, MemoryMB and CPUs are the resource limits of the registered cell template.
	PidsMax  int
	MemoryMB int
	CPUs     string
}

var (
	imageRefRE = regexp.MustCompile(`^[a-z0-9][a-z0-9./_-]*@sha256:[0-9a-f]{64}$`)
	nameRE     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
	modelRE    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	cpusRE     = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

// PodmanArgs returns the arguments of `podman run` (after the podman binary) that run native Pi
// once on the workspace:
//
//   - the image is the digest-pinned reference and is never pulled (--pull=never);
//   - the container is removed on exit (--rm), has a read-only root file system, drops every
//     capability, cannot gain privilege, and runs as the cell uid and gid in a user namespace
//     that maps only that pair (the governed cell's --uidmap/--gidmap form);
//   - the workspace is the only writable mount besides a size-bounded tmpfs at /tmp; the Pi
//     runtime bundle and the provider key file are read-only mounts;
//   - podman's own --timeout kills the container at the case's duration;
//   - the network is podman's default (the provider is reached over it). There is no egress
//     filter: that is a stated limit of V, not a property.
func PodmanArgs(s Spec) ([]string, error) {
	switch {
	case !nameRE.MatchString(s.Name):
		return nil, errors.New("v-run: container name is not plain")
	case !imageRefRE.MatchString(s.ImageRef):
		return nil, fmt.Errorf("v-run: image reference %q is not pinned by digest", s.ImageRef)
	case !path.IsAbs(s.CrunPath) || !path.IsAbs(s.Workspace) || !path.IsAbs(s.RuntimeBundle) || !path.IsAbs(s.KeyFile):
		return nil, errors.New("v-run: runtime, workspace, runtime bundle and key file must be absolute paths")
	case s.UID <= 0 || s.GID <= 0:
		return nil, errors.New("v-run: the container must not run as root")
	case !modelRE.MatchString(s.Model):
		return nil, errors.New("v-run: model id is not plain")
	case s.Prompt == "" || strings.ContainsRune(s.Prompt, 0):
		return nil, errors.New("v-run: the prompt is empty or contains NUL")
	case s.DurationSeconds == 0:
		return nil, errors.New("v-run: no duration")
	case s.PidsMax <= 0 || s.MemoryMB <= 0 || !cpusRE.MatchString(s.CPUs):
		return nil, errors.New("v-run: resource limits are not set")
	}
	for _, p := range []string{s.Workspace, s.RuntimeBundle, s.KeyFile} {
		if strings.ContainsAny(p, ",=\x00") {
			return nil, fmt.Errorf("v-run: path %q cannot be used in a mount option", p)
		}
	}
	uid, gid := strconv.Itoa(s.UID), strconv.Itoa(s.GID)
	return []string{
		"run", "--name", s.Name, "--rm", "--pull=never", "--runtime", s.CrunPath,
		"--read-only", "--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(s.PidsMax), "--memory", strconv.Itoa(s.MemoryMB) + "m", "--cpus", s.CPUs,
		"--uidmap", uid + ":0:1", "--gidmap", gid + ":0:1", "--user", uid + ":" + gid,
		"--workdir", WorkDir, "--timeout", strconv.FormatUint(s.DurationSeconds, 10),
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777",
		"--mount", "type=bind,src=" + s.Workspace + ",dst=" + WorkDir + ",rw,bind-propagation=rprivate",
		"--mount", "type=bind,src=" + s.RuntimeBundle + ",dst=" + RuntimeDir + ",ro,bind-propagation=rprivate",
		"--mount", "type=bind,src=" + s.KeyFile + ",dst=" + KeyFile + ",ro,bind-propagation=rprivate",
		"-e", "HOME=" + Home, "-e", "TMPDIR=/tmp/tbound-tmp", "-e", "LANG=C.UTF-8",
		"-e", "PI_OFFLINE=1", "-e", "PI_SKIP_VERSION_CHECK=1", "-e", "PI_TELEMETRY=0",
		"--entrypoint", "/bin/bash", s.ImageRef,
		"-c", launchScript, "pi-v", NodePath, PiEntry, s.Model, s.Prompt,
	}, nil
}
