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
	// ForwarderDir is where the unit's forwarder directory (SocketName and TokenName in it) is mounted read-only.
	ForwarderDir = "/run/tbound/fwd"
	// Home is Pi's HOME, on the container's tmpfs.
	Home = "/tmp/home"
)

// The forwarder directory holds the unix socket of the unit's forwarder and its credential.
const (
	SocketName = "fwd.sock"
	TokenName  = "token"
	// RelayPort is the loopback port in the container where a relay joins Pi's HTTP client to the socket.
	RelayPort = "18081"
	// PiProvider is the name of the custom OpenAI-compatible provider that the launch script writes to Pi's models.json.
	PiProvider = "research"
	// NodePath is the Node binary of the cell image (the governed cell execs the same one).
	NodePath = "/usr/local/bin/node"
	// PiEntry is the official Pi CLI inside the runtime bundle.
	PiEntry = RuntimeDir + "/cli/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js"
)

const relayScript = `const n=require("net");n.createServer(c=>{const u=n.connect("` + ForwarderDir + `/` + SocketName + `");c.pipe(u);u.pipe(c);c.on("error",()=>u.destroy());u.on("error",()=>c.destroy())}).listen(` + RelayPort + `,"127.0.0.1")`

// launchScript is the container's command. It writes Pi's models.json (a custom provider whose base URL is the
// relay on loopback), starts the relay, reads the unit credential into the variable models.json names and execs
// Node on the Pi CLI. The credential is the forwarder's, not a provider key: the container has none. The
// positional parameters are Node, the CLI entry, the model id and the prompt, all passed as argv words and never
// interpreted by the shell. Pi runs natively: no TBound extension, no tool replacement, the default tools, one
// prompt, JSONL events on stdout, no session file. The other flags match the governed cell's Pi.
const launchScript = `set -eu
umask 077
mkdir -p "$HOME/.pi/agent" "$TMPDIR"
printf '%s' '{"providers":{"` + PiProvider + `":{"baseUrl":"http://127.0.0.1:` + RelayPort + `/v1","api":"openai-completions","apiKey":"$TBOUND_V_KEY","models":[{"id":"'"$3"'"}]}}}' > "$HOME/.pi/agent/models.json"
"$1" -e '` + relayScript + `' &
TBOUND_V_KEY="$(cat ` + ForwarderDir + `/` + TokenName + `)"
export TBOUND_V_KEY
i=0
while [ "$i" -lt 100 ] && ! (exec 3<>/dev/tcp/127.0.0.1/` + RelayPort + `) 2>/dev/null; do i=$((i+1)); sleep 0.1; done
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
	// Forwarder is the host directory of the unit's forwarder, mounted read-only at ForwarderDir.
	Forwarder string
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
	modelRE    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,63}$`)
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
//     runtime bundle and the forwarder directory are read-only mounts;
//   - podman's own --timeout kills the container at the case's duration;
//   - the network is podman's default (no egress filter, as before); the provider is reached only through the
//     forwarder's unix socket, and no provider key is in the argument vector, the environment or any mount.
func PodmanArgs(s Spec) ([]string, error) {
	switch {
	case !nameRE.MatchString(s.Name):
		return nil, errors.New("v-run: container name is not plain")
	case !imageRefRE.MatchString(s.ImageRef):
		return nil, fmt.Errorf("v-run: image reference %q is not pinned by digest", s.ImageRef)
	case !path.IsAbs(s.CrunPath) || !path.IsAbs(s.Workspace) || !path.IsAbs(s.RuntimeBundle) || !path.IsAbs(s.Forwarder):
		return nil, errors.New("v-run: runtime, workspace, runtime bundle and forwarder directory must be absolute paths")
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
	for _, p := range []string{s.Workspace, s.RuntimeBundle, s.Forwarder} {
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
		"--mount", "type=bind,src=" + s.Forwarder + ",dst=" + ForwarderDir + ",ro,bind-propagation=rprivate",
		"-e", "HOME=" + Home, "-e", "TMPDIR=/tmp/tbound-tmp", "-e", "LANG=C.UTF-8",
		"-e", "PI_OFFLINE=1", "-e", "PI_SKIP_VERSION_CHECK=1", "-e", "PI_TELEMETRY=0",
		"--entrypoint", "/bin/bash", s.ImageRef,
		"-c", launchScript, "pi-v", NodePath, PiEntry, s.Model, s.Prompt,
	}, nil
}
