//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"tbound/supervisor/internal/runner"
)

// `tbound client-forced` is the forced-command entry of the client SSH key, the third
// least-privilege key next to `tbound evidence ssh-original-command` and `tbound trial
// ssh-original-command`. It takes no argument: it reads $SSH_ORIGINAL_COMMAND, which must be
// exactly what runner.SSHTransport sends for a governed client run,
//
//	setpriv --reuid 994 --regid 985 --clear-groups --bounding-set=-all --inh-caps=-all
//	  --ambient-caps=-all --no-new-privs -- /usr/local/bin/tbound serve --pi --task-id <case_id> --role <role> --attempt <1|2> --task-request-id <id> --bash-no-outputs
//
// (runner.DefaultClientCommand and runner.GovernedRequest.Argv, every word shell-quoted by
// SSHTransport.RemoteCommand), and runs that and nothing else. The key logs in as root, because
// only root can drop to the client uid; the account's own shell is therefore the root shell and
// the nologin shell of tbound-client plays no part (authorized_keys
// restrict,command="/usr/local/bin/tbound client-forced").
//
// The grammar is strict. The launcher words must equal runner.DefaultClientCommand word for
// word; then `serve --pi`, then the four value flags --task-id, --role, --attempt and
// --task-request-id and the bare flag --bash-no-outputs, each exactly once, in any order. No
// other flag, positional word or repeated flag is accepted, so a client cannot select a
// profile, workspace, source, task text, Bash output scope or the service mode. The values
// are checked with the service's own validators: the ids by boundedProductionIdentity with the
// bounds of the service (productionTaskIDMaxBytes, productionRequestMaxBytes) and the safe
// alphabet of the other forced-command entries (no leading dash), the role by validCorpusRole,
// the attempt as exactly "1" or "2". The program that is run is rebuilt from the validated
// values: an absolute setpriv, the launcher and the flags in the runner's order, under a fixed
// environment. No shell ever sees any of it.
const (
	clientForcedVerb = "client-forced"
	// clientForcedSetpriv is the util-linux setpriv the entry runs; the runner's word "setpriv"
	// is matched, not looked up on a PATH.
	clientForcedSetpriv = "/usr/bin/setpriv"
)

const clientForcedUsage = `usage: tbound client-forced   (root only; forced-command entry; reads $SSH_ORIGINAL_COMMAND)
  accepted: setpriv --reuid 994 --regid 985 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- /usr/local/bin/tbound serve --pi --task-id <id> --role <role> --attempt <1|2> --task-request-id <id> --bash-no-outputs
`

// clientForcedFlags are the flags that carry a value; --bash-no-outputs is the one bare flag.
var clientForcedFlags = []string{"--task-id", "--role", "--attempt", "--task-request-id"}

const clientForcedBareFlag = "--bash-no-outputs"

// clientForcedEnv is everything the entry touches outside its arguments; tests replace it.
type clientForcedEnv struct {
	geteuid func() int
	getenv  func(string) string
	// exec replaces the process (syscall.Exec); it returns only on failure.
	exec func(path string, argv, env []string) error
}

func productionClientForcedEnv() clientForcedEnv {
	return clientForcedEnv{geteuid: os.Geteuid, getenv: os.Getenv, exec: syscall.Exec}
}

// clientForcedEnviron is the fixed environment of the client the entry starts.
func clientForcedEnviron() []string {
	return []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
}

// init dispatches `tbound client-forced` before main runs, as evidence and trial do.
func init() {
	if len(os.Args) > 1 && os.Args[1] == clientForcedVerb {
		os.Exit(clientForcedMain(os.Args[2:], os.Stderr, productionClientForcedEnv()))
	}
}

func clientForcedMain(args []string, stderr io.Writer, env clientForcedEnv) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		_, _ = io.WriteString(stderr, clientForcedUsage)
		return 0
	}
	if err := runClientForced(args, env); err != nil {
		fmt.Fprintf(stderr, "tbound client-forced: %v\n%s", err, clientForcedUsage)
		return 2
	}
	return 0
}

// runClientForced returns only on refusal or when the exec failed; with a real exec it does not
// return on success.
func runClientForced(args []string, env clientForcedEnv) error {
	if env.geteuid == nil || env.getenv == nil || env.exec == nil || env.geteuid() != 0 {
		return errors.New("client-forced may run only as uid 0")
	}
	if len(args) != 0 {
		return errors.New("client-forced takes no argument")
	}
	argv, err := parseClientForced(env.getenv("SSH_ORIGINAL_COMMAND"))
	if err != nil {
		return err
	}
	return env.exec(clientForcedSetpriv, argv, clientForcedEnviron())
}

// parseClientForced validates $SSH_ORIGINAL_COMMAND and returns the argv (argv[0] first) of the
// setpriv invocation to run.
func parseClientForced(original string) ([]string, error) {
	words, err := splitOriginalCommand(original)
	if err != nil {
		return nil, err
	}
	launcher := runner.DefaultClientCommand()
	if len(words) < len(launcher) || !slices.Equal(words[:len(launcher)], launcher) {
		return nil, errors.New("SSH_ORIGINAL_COMMAND is not a client command")
	}
	words = words[len(launcher):]
	if len(words) < 2 || words[0] != "serve" || words[1] != "--pi" {
		return nil, errors.New("SSH_ORIGINAL_COMMAND is not a client `serve --pi` command")
	}
	words = words[2:]
	if len(words) != 2*len(clientForcedFlags)+1 {
		return nil, fmt.Errorf("the client command takes exactly %s and %s and no other argument", strings.Join(clientForcedFlags, ", "), clientForcedBareFlag)
	}
	values := map[string]string{}
	bare := false
	for i := 0; i < len(words); {
		name := words[i]
		if name == clientForcedBareFlag {
			if bare {
				return nil, fmt.Errorf("%s given twice", name)
			}
			bare = true
			i++
			continue
		}
		if !slices.Contains(clientForcedFlags, name) {
			return nil, fmt.Errorf("unknown argument %q", name)
		}
		if _, dup := values[name]; dup {
			return nil, fmt.Errorf("%s given twice", name)
		}
		if i+1 >= len(words) {
			return nil, fmt.Errorf("%s has no value", name)
		}
		values[name] = words[i+1]
		i += 2
	}
	if !bare || len(values) != len(clientForcedFlags) {
		return nil, errors.New("the client command lacks a flag")
	}
	taskID, role, attempt, requestID := values["--task-id"], values["--role"], values["--attempt"], values["--task-request-id"]
	if !clientForcedIdentity(taskID, productionTaskIDMaxBytes) || !clientForcedIdentity(requestID, productionRequestMaxBytes) {
		return nil, errors.New("the task id or request id is not a bounded identity in the safe alphabet")
	}
	if !validCorpusRole(role) {
		return nil, fmt.Errorf("role %q is not a governed role", role)
	}
	if attempt != strconv.Itoa(1) && attempt != strconv.Itoa(2) {
		return nil, errors.New("the attempt is not 1 or 2")
	}
	argv := append([]string{"setpriv"}, launcher[1:]...)
	return append(argv, "serve", "--pi", "--task-id", taskID, "--role", role, "--attempt", attempt,
		"--task-request-id", requestID, clientForcedBareFlag), nil
}

func clientForcedIdentity(v string, maximum int) bool {
	return boundedProductionIdentity(v, maximum) && !strings.HasPrefix(v, "-") && evidenceWordRE.MatchString(v)
}
