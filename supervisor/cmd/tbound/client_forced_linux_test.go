//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"tbound/supervisor/internal/runner"
)

type cfTestEnv struct {
	clientForcedEnv
	execs [][]string // argv of every exec the fake made, path first
	envs  [][]string
}

func newCFTestEnv(uid int, original string) *cfTestEnv {
	e := &cfTestEnv{}
	e.clientForcedEnv = clientForcedEnv{
		geteuid: func() int { return uid },
		getenv: func(k string) string {
			if k == "SSH_ORIGINAL_COMMAND" {
				return original
			}
			return ""
		},
		exec: func(path string, argv, env []string) error {
			e.execs = append(e.execs, append([]string{path}, argv...))
			e.envs = append(e.envs, env)
			return nil
		},
	}
	return e
}

func runCF(env *cfTestEnv, args ...string) (int, string) {
	var stderr bytes.Buffer
	code := clientForcedMain(args, &stderr, env.clientForcedEnv)
	return code, stderr.String()
}

// cfRemote is the exact string runner.SSHTransport hands the guest for argv.
func cfRemote(t *testing.T, argv []string) string {
	t.Helper()
	remote, err := runner.SSHTransport{}.RemoteCommand(argv)
	if err != nil {
		t.Fatal(err)
	}
	return remote
}

func cfArgv(role string, attempt int) []string {
	argv, err := runner.GovernedRequest{TaskID: "PILOT-ADV-A-01", Role: role, Attempt: attempt, RequestID: "tbr-0123456789abcdef0123456789abcdef"}.Argv()
	if err != nil {
		panic(err)
	}
	return argv
}

// Parity with the runner: for every governed role and both attempts, the string the
// runner's SSH transport sends is accepted and is exec'd as exactly the launcher the
// transport documents plus the runner's argv, under a fixed absolute setpriv and a
// fixed environment.
func TestClientForcedAcceptsEveryRunnerArgv(t *testing.T) {
	roles := runner.GovernedRoles()
	if len(roles) != 11 {
		t.Fatalf("expected the 11 governed roles, got %d", len(roles))
	}
	for _, role := range roles {
		for _, attempt := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", role, attempt), func(t *testing.T) {
				argv := cfArgv(role, attempt)
				env := newCFTestEnv(0, cfRemote(t, argv))
				if code, stderr := runCF(env); code != 0 {
					t.Fatalf("exit %d: %s", code, stderr)
				}
				want := append([]string{clientForcedSetpriv}, runner.DefaultClientCommand()...)
				want = append(want, argv...)
				if len(env.execs) != 1 || !slices.Equal(env.execs[0], want) {
					t.Fatalf("exec = %q, want %q", env.execs, want)
				}
				if !slices.Equal(env.envs[0], clientForcedEnviron()) {
					t.Fatalf("environment = %q", env.envs[0])
				}
			})
		}
	}
}

func TestClientForcedAcceptsPlainWordsAndFlagOrderAndCanonicalizes(t *testing.T) {
	want := append(append([]string{clientForcedSetpriv, "setpriv"}, runner.DefaultClientCommand()[1:]...), cfArgv("compliant", 1)...)
	for name, original := range map[string]string{
		"plain words": "setpriv --reuid 994 --regid 985 --clear-groups /usr/local/bin/tbound serve --pi --task-id PILOT-ADV-A-01 --role compliant --attempt 1 --task-request-id tbr-0123456789abcdef0123456789abcdef --bash-no-outputs",
		"flags reordered": "'setpriv' '--reuid' '994' '--regid' '985' '--clear-groups' '/usr/local/bin/tbound' 'serve' '--pi' '--bash-no-outputs' " +
			"'--task-request-id' 'tbr-0123456789abcdef0123456789abcdef' '--attempt' '1' '--role' 'compliant' '--task-id' 'PILOT-ADV-A-01'",
	} {
		env := newCFTestEnv(0, original)
		if code, stderr := runCF(env); code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, stderr)
		}
		if len(env.execs) != 1 || !slices.Equal(env.execs[0], want) {
			t.Fatalf("%s: exec = %q, want %q", name, env.execs, want)
		}
	}
}

func TestClientForcedRefusalMatrix(t *testing.T) {
	const (
		launcher = "'setpriv' '--reuid' '994' '--regid' '985' '--clear-groups' '/usr/local/bin/tbound' "
		tail     = "'--task-request-id' 'tbr-0123456789abcdef0123456789abcdef' '--bash-no-outputs'"
		head     = launcher + "'serve' '--pi' '--task-id' 'PILOT-ADV-A-01' '--role' 'trajectory' '--attempt' '1' "
	)
	good := head + tail
	long := strings.Repeat("a", 257)
	cases := map[string]string{
		"empty":                     "",
		"shell metacharacter":       good + " ; id",
		"command substitution id":   strings.Replace(good, "PILOT-ADV-A-01", "$(id)", 1),
		"backtick id":               strings.Replace(good, "PILOT-ADV-A-01", "`id`", 1),
		"space in id":               strings.Replace(good, "'PILOT-ADV-A-01'", "'PILOT ADV'", 1),
		"newline":                   good + "\n",
		"id leading dash":           strings.Replace(good, "PILOT-ADV-A-01", "-x", 1),
		"id is a flag":              strings.Replace(good, "'PILOT-ADV-A-01'", "'--role'", 1),
		"id too long":               strings.Replace(good, "PILOT-ADV-A-01", long, 1),
		"id empty":                  strings.Replace(good, "'PILOT-ADV-A-01'", "''", 1),
		"request id too long":       strings.Replace(good, "tbr-0123456789abcdef0123456789abcdef", long, 1),
		"request id leading dash":   strings.Replace(good, "tbr-0123456789abcdef0123456789abcdef", "-tbr", 1),
		"request id empty":          strings.Replace(good, "'tbr-0123456789abcdef0123456789abcdef'", "''", 1),
		"unknown role":              strings.Replace(good, "'trajectory'", "'root'", 1),
		"role of other case":        strings.Replace(good, "'trajectory'", "'Trajectory'", 1),
		"empty role":                strings.Replace(good, "'trajectory'", "''", 1),
		"attempt 0":                 strings.Replace(good, "'--attempt' '1'", "'--attempt' '0'", 1),
		"attempt 3":                 strings.Replace(good, "'--attempt' '1'", "'--attempt' '3'", 1),
		"attempt 01":                strings.Replace(good, "'--attempt' '1'", "'--attempt' '01'", 1),
		"attempt +1":                strings.Replace(good, "'--attempt' '1'", "'--attempt' '+1'", 1),
		"attempt word":              strings.Replace(good, "'--attempt' '1'", "'--attempt' 'one'", 1),
		"no launcher":               strings.TrimPrefix(good, launcher),
		"wrong uid":                 strings.Replace(good, "'994'", "'0'", 1),
		"wrong gid":                 strings.Replace(good, "'985'", "'0'", 1),
		"keep groups":               strings.Replace(good, "'--clear-groups'", "'--keep-groups'", 1),
		"extra setpriv flag":        strings.Replace(good, "'--clear-groups' ", "'--clear-groups' '--no-new-privs' ", 1),
		"other binary":              strings.Replace(good, "/usr/local/bin/tbound", "/bin/sh", 1),
		"other verb":                strings.Replace(good, "'serve' '--pi'", "'evidence' 'proc-cell'", 1),
		"serve without pi":          strings.Replace(good, "'serve' '--pi'", "'serve'", 1),
		"serve service mode":        strings.Replace(good, "'--pi'", "'--pi' '--service'", 1),
		"dev fixture":               strings.Replace(good, "'--pi'", "'--dev-fixture'", 1),
		"extra --task":              good + " '--task' 'do something'",
		"extra bash-output":         good + " '--bash-output' 'a.txt'",
		"extra host profile":        good + " '--native-host-profile' '/tmp/p.json'",
		"extra workspace":           good + " '--workspace' '/tmp'",
		"extra source":              good + " '--source' '/tmp'",
		"extra positional":          good + " 'x'",
		"duplicate task id":         good + " '--task-id' 'PILOT-ADV-A-02'",
		"duplicate role":            good + " '--role' 'compliant'",
		"duplicate attempt":         good + " '--attempt' '2'",
		"duplicate request id":      good + " '--task-request-id' 'tbr-1'",
		"duplicate bash-no-outputs": good + " '--bash-no-outputs'",
		"missing task id":           launcher + "'serve' '--pi' '--role' 'trajectory' '--attempt' '1' " + tail,
		"missing role":              launcher + "'serve' '--pi' '--task-id' 'PILOT-ADV-A-01' '--attempt' '1' " + tail,
		"missing attempt":           launcher + "'serve' '--pi' '--task-id' 'PILOT-ADV-A-01' '--role' 'trajectory' " + tail,
		"missing request id":        head + "'--bash-no-outputs'",
		"missing bash-no-outputs":   head + "'--task-request-id' 'tbr-0123456789abcdef0123456789abcdef'",
		"bash-no-outputs value":     head + "'--task-request-id' 'tbr-0123456789abcdef0123456789abcdef' '--bash-no-outputs' 'true'",
		"equals form":               launcher + "'serve' '--pi' '--task-id=PILOT-ADV-A-01' '--role' 'trajectory' '--attempt' '1' " + tail,
		"single dash form":          strings.Replace(good, "'--role'", "'-role'", 1),
		"double space":              strings.Replace(good, "'serve' '--pi'", "'serve'  '--pi'", 1),
		"tab":                       strings.Replace(good, "'serve' '--pi'", "'serve'\t'--pi'", 1),
		"half quoted":               strings.Replace(good, "'--pi'", "'--pi", 1),
		"embedded quote":            strings.Replace(good, "'PILOT-ADV-A-01'", "'PILOT'\\''X'", 1),
		"nested":                    "/usr/local/bin/tbound client-forced",
		"too long":                  good + strings.Repeat(" 'a'", 600),
	}
	for name, original := range cases {
		t.Run(name, func(t *testing.T) {
			env := newCFTestEnv(0, original)
			code, stderr := runCF(env)
			if code != 2 || len(env.execs) != 0 {
				t.Fatalf("exit %d, execs %q, want a refusal with exit 2 and no exec (stderr %q)", code, env.execs, stderr)
			}
		})
	}
	// The unmodified base of the matrix is accepted, so each refusal above is caused by its edit.
	env := newCFTestEnv(0, good)
	if code, stderr := runCF(env); code != 0 || len(env.execs) != 1 {
		t.Fatalf("base command refused: exit %d: %s", code, stderr)
	}
}

func TestClientForcedRefusesWhenNotRootOrWithArguments(t *testing.T) {
	good := cfRemote(t, cfArgv("trajectory", 1))
	for _, uid := range []int{994, 995, 1000} {
		env := newCFTestEnv(uid, good)
		if code, _ := runCF(env); code != 2 || len(env.execs) != 0 {
			t.Fatalf("uid %d: exit %d, execs %q", uid, code, env.execs)
		}
	}
	env := newCFTestEnv(0, good)
	if code, _ := runCF(env, "serve"); code != 2 || len(env.execs) != 0 {
		t.Fatalf("an argument after client-forced was accepted: execs %q", env.execs)
	}
	env = newCFTestEnv(0, good)
	env.clientForcedEnv.getenv = nil
	if code, _ := runCF(env); code != 2 || len(env.execs) != 0 {
		t.Fatal("no getenv accepted")
	}
}

func TestClientForcedExecFailureIsAnError(t *testing.T) {
	env := newCFTestEnv(0, cfRemote(t, cfArgv("trajectory", 1)))
	env.clientForcedEnv.exec = func(string, []string, []string) error { return errors.New("exec boom") }
	code, stderr := runCF(env)
	if code != 2 || !strings.Contains(stderr, "exec boom") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

// The grammar is held to the service's own validators: every role the service admits is
// accepted and nothing else is, and the id bounds are the service's.
func TestClientForcedUsesServiceValidators(t *testing.T) {
	for _, role := range []string{corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleAdaptiveReplay, corpusRoleVariance1, corpusRoleVariance2,
		corpusRoleCompliant, corpusRoleAmbiguous, corpusRolePStaticReplay, corpusRolePAdaptiveReplay, corpusRoleBCompliant, corpusRoleBAmbiguous} {
		if !validCorpusRole(role) || !slices.Contains(runner.GovernedRoles(), role) {
			t.Fatalf("role %q: service and runner disagree", role)
		}
	}
	id := strings.Repeat("a", productionTaskIDMaxBytes)
	argv := []string{"serve", "--pi", "--task-id", id, "--role", "trajectory", "--attempt", strconv.Itoa(2), "--task-request-id", strings.Repeat("b", productionRequestMaxBytes), "--bash-no-outputs"}
	env := newCFTestEnv(0, cfRemote(t, argv))
	if code, stderr := runCF(env); code != 0 {
		t.Fatalf("ids at the service bound refused: %s", stderr)
	}
}
