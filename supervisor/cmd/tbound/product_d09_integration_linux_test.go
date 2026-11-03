//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
)

const productE4Dir = "/run/tbound-product/e4"

type productE4ChildRecord struct {
	Tool, Phase string
	Args, Env   []string
	Output      []byte
	Exit        int
}

// Root installs two copies of this test binary under these names and signs their
// digests into the TEST host template. No shell can add variables before recording.
func init() {
	tool := ""
	switch filepath.Base(os.Args[0]) {
	case "tbound-e4-podman":
		tool = "podman"
	case "tbound-e4-cosign":
		tool = "cosign"
	default:
		return
	}
	if len(os.Args) < 2 {
		os.Exit(125)
	}
	if err := productE4Child(tool, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
	os.Exit(0)
}
func productE4Child(tool string, args []string) error {
	scenarioBytes, err := os.ReadFile(productE4Dir + "/scenario")
	if err != nil {
		return err
	}
	scenario := string(scenarioBytes)
	if !slices.Contains([]string{"success", "explicit", "deferred"}, scenario) {
		return errors.New("invalid E4 scenario")
	}
	dir := productE4Dir + "/" + scenario
	rawEnv, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		return err
	}
	record := productE4ChildRecord{Tool: tool, Args: args, Env: strings.Split(strings.TrimSuffix(string(rawEnv), "\x00"), "\x00")}
	slices.Sort(record.Env)
	file, err := os.CreateTemp(dir, "child-*.json")
	if err != nil {
		return err
	}
	defer file.Close()
	writeRecord := func() error {
		if err := file.Truncate(0); err != nil {
			return err
		}
		if _, err := file.Seek(0, 0); err != nil {
			return err
		}
		if err := json.NewEncoder(file).Encode(record); err != nil {
			return err
		}
		return file.Sync()
	}
	if err := writeRecord(); err != nil {
		return err
	}
	realTool := "/usr/bin/podman"
	if tool == "cosign" {
		realTool = "/usr/local/bin/cosign"
	}
	if tool == "podman" && args[0] == "run" {
		raw, _ := json.Marshal(record)
		if err := os.WriteFile(dir+"/run.json", raw, 0600); err != nil {
			return err
		}
		// Exec preserves fd3=view, fd4=proof and fd5=ack, and the recorded environment.
		return syscall.Exec(realTool, append([]string{realTool}, args...), os.Environ())
	}
	if tool == "podman" && args[0] == "rm" && scenario != "success" {
		counter := dir + "/explicit-rm"
		_, statErr := os.Stat(counter)
		if errors.Is(statErr, os.ErrNotExist) {
			record.Phase = "explicit-cleanup"
			if err := os.WriteFile(counter, nil, 0600); err != nil {
				return err
			}
			if scenario == "deferred" {
				record.Exit = 125
				if err := writeRecord(); err != nil {
					return err
				}
				os.Exit(125) // Force the first cleanup to leave ownership for the deferred block.
			}
		} else {
			if statErr != nil {
				return statErr
			}
			record.Phase = "deferred-cleanup"
			kill, err := productE4CommandKillPath(dir)
			if err != nil {
				return err
			}
			if err := os.Chmod(kill, 0200); err != nil {
				return err
			}
		}
	}
	command := exec.Command(realTool, args...)
	command.Env = os.Environ()
	var stdout bytes.Buffer
	command.Stdout, command.Stderr = &stdout, os.Stderr
	err = command.Run()
	record.Output = stdout.Bytes()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return err
		}
		record.Exit = exit.ExitCode()
	}
	if tool == "podman" && args[0] == "inspect" {
		var states []struct{ State struct{ Status string } }
		if json.Unmarshal(record.Output, &states) == nil && len(states) == 1 {
			record.Phase = states[0].State.Status
			if record.Phase == "running" && scenario != "success" {
				if scenario == "deferred" {
					kill, err := productE4CommandKillPath(dir)
					if err != nil {
						return err
					}
					// Real cgroupfs denial makes explicit Settle fail; the deferred rm restores it.
					if err := os.Chmod(kill, 0); err != nil {
						return err
					}
				}
				record.Output = []byte("induced post-start inspect failure\n")
			}
		}
	}
	if err := writeRecord(); err != nil {
		return err
	}
	if _, err := os.Stdout.Write(record.Output); err != nil {
		return err
	}
	if record.Exit != 0 {
		os.Exit(record.Exit)
	}
	return nil
}

func productE4CommandKillPath(dir string) (string, error) {
	raw, err := os.ReadFile(dir + "/run.json")
	if err != nil {
		return "", err
	}
	var run productE4ChildRecord
	if err := json.Unmarshal(raw, &run); err != nil {
		return "", err
	}
	index := slices.Index(run.Args, "--cgroup-parent")
	if index < 0 || index+1 == len(run.Args) {
		return "", errors.New("run has no cgroup parent")
	}
	parent := run.Args[index+1]
	if !filepath.IsAbs(parent) || filepath.Clean(parent) != parent || filepath.Dir(filepath.Dir(parent)) != "/system.slice/tbound-product.service/cells" || !strings.HasPrefix(filepath.Base(filepath.Dir(parent)), "turn-") || !strings.HasPrefix(filepath.Base(parent), "tbound-cmd-") {
		return "", fmt.Errorf("refuse E4 cgroup permission fault outside exact command child: %s", parent)
	}
	return "/sys/fs/cgroup" + parent + "/cgroup.kill", nil
}

func TestProductD09IntegrationFaultIsConfinedToACommandChild(t *testing.T) {
	dir := t.TempDir()
	for _, parent := range []string{
		"/system.slice/tbound-product.service/cells/turn-owned/tbound-cmd-owned",
		"/system.slice/tbound-product.service/cells/turn-owned",
		"/system.slice/tbound-product.service/runner/tbound-cmd-owned",
		"/system.slice/tbound-product.service/cells/turn-owned/extra/tbound-cmd-owned",
		"/system.slice/tbound-product.service/cells/turn-owned/../tbound-cmd-owned",
		"relative/tbound-cmd-owned",
	} {
		raw, _ := json.Marshal(productE4ChildRecord{Args: []string{"run", "--cgroup-parent", parent}})
		if err := os.WriteFile(dir+"/run.json", raw, 0600); err != nil {
			t.Fatal(err)
		}
		path, err := productE4CommandKillPath(dir)
		valid := parent == "/system.slice/tbound-product.service/cells/turn-owned/tbound-cmd-owned"
		if (err == nil) != valid || (valid && path != "/sys/fs/cgroup"+parent+"/cgroup.kill") {
			t.Fatalf("fault path %q: %q %v", parent, path, err)
		}
	}
}

// This is deliberately not an unprivileged fixture: the loader's installed inputs,
// signatures, account, unit, cgroup2 topology and the cell proof/ack are all real.
func TestProductD09PrivilegedRuntimeEnvironmentIntegration(t *testing.T) {
	if os.Getenv("TBOUND_PRODUCT_D09_INTEGRATION") != "1" {
		t.Skip("set TBOUND_PRODUCT_D09_INTEGRATION=1 on the prepared disposable product host; see e4-integration.md")
	}
	if _, err := os.Stat(productE4Dir); errors.Is(err, os.ErrNotExist) {
		t.Skip("E4 host not prepared: /run/tbound-product/e4 absent")
	} else if err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Fatal("run as tbound-product, never root")
	}
	for _, scenario := range []string{"success", "explicit", "deferred"} {
		t.Run(scenario, func(t *testing.T) {
			dir := productE4Dir + "/" + scenario
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatalf("E4 requires fresh records: %v", err)
			}
			if err := os.WriteFile(productE4Dir+"/scenario", []byte(scenario), 0600); err != nil {
				t.Fatal(err)
			}
			f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, []string{"task.txt"}, nil)
			if filepath.Dir(f.root) != "/var/lib/tbound-product" {
				t.Fatal("prepare HOME=/var/lib/tbound-product before starting the test")
			}
			if _, err := exec.LookPath("git"); err != nil {
				t.Fatal("prepared integration host requires Git")
			}
			git, gitProfile := pinGovernedTestGit(t)
			f.env.PinGit = func() (privategit.PinnedGit, sessionlaunch.GitProfile, error) { return git, gitProfile, nil }
			poison := dir + "/poison"
			if err := os.Mkdir(poison, 0700); err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{
				"HOME": f.root, "TMPDIR": poison, "XDG_RUNTIME_DIR": poison, "XDG_CACHE_HOME": poison,
				"XDG_CONFIG_HOME": poison, "XDG_DATA_HOME": poison, "CONTAINERS_CONF": poison,
				"CONTAINERS_STORAGE_CONF": poison, "CONTAINERS_FOO": "poison", "DBUS_SESSION_BUS_ADDRESS": "poison",
				"USER": "poison", "LOGNAME": "poison", "LANG": "poison", "LC_ALL": "poison", "PATH": poison,
			} {
				t.Setenv(key, value)
			}
			host, err := piruntime.LoadProductD09CommandHost(context.Background())
			if err != nil {
				t.Fatalf("installed product host: %v", err)
			}
			if host.Template().PodmanPath != "/usr/local/libexec/tbound-e4-podman" || host.Template().CosignPath != "/usr/local/libexec/tbound-e4-cosign" {
				t.Fatal("signed template must pin the recording wrappers")
			}
			var runner *productD09TurnRunner
			var launcher *piruntime.D09PodmanCrunLauncher
			f.env.NewCommandRunner = func(c productTurnCommandContext) (productTurnCommandRunner, error) {
				scope, err := openProductCommandCgroupScope(context.Background(), host, c)
				if err != nil {
					return nil, err
				}
				journal, err := audit.Open(dir + "/command-owner.jsonl")
				if err != nil {
					return nil, errors.Join(err, scope.Remove(context.Background()))
				}
				path, err := scope.Path()
				if err != nil {
					return nil, errors.Join(err, scope.Remove(context.Background()), journal.Close())
				}
				owner := productD09ScopeOwner{Schema: "tbound-product-d09-scope-owner/v1", State: "opened", WorkflowID: c.WorkflowID, RunID: c.RunID,
					RegistrationDigest: host.Template().ProductRegistrationDigest, ProfileDigest: host.Profile().Digest, RootDigest: scope.RootDigest(), ScopePath: path, ScopeIdentity: scope.IdentityDigest()}
				if err := persistProductCommandScopeOwner(journal, owner); err != nil {
					return nil, errors.Join(err, scope.Remove(context.Background()), journal.Close())
				}
				launcher, err = piruntime.NewProductD09PodmanCrunLauncher(context.Background(), host, scope)
				if err != nil {
					return nil, errors.Join(err, scope.Remove(context.Background()), journal.Close())
				}
				runner = newProductD09CommandRunnerWithFactory(func(lease string) (sessionrepo.CommandRunner, error) {
					inner, err := piruntime.NewD09CommandRunner(lease, launcher)
					if err != nil {
						return nil, err
					}
					return newProductionD09BashRunner(lease, inner, c.Journal, c.RunID, c.SessionID, c.WorkflowID, c.ConversationID, host.Profile(), host.Template()), nil
				}, func(ctx context.Context) error {
					if err := scope.Remove(ctx); err != nil {
						return err
					}
					if err := launcher.Close(ctx); err != nil {
						return err
					}
					owner.State = "drained"
					if err := persistProductCommandScopeOwner(journal, owner); err != nil {
						return err
					}
					return journal.Close()
				})
				return runner, nil
			}
			script := &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("e4", "printf 'e4-ok\n'"), scopeDriverFinal("done", scopeDriverAnswer)}}
			receipt, turn, runErr := f.run(t, f.submit(t, "command", "run E4"), script)
			if scenario == "success" {
				if runErr != nil || turn.Outcome != productsession.OutcomeNoDiff || receipt.CommandJournalCount != 1 || runner == nil || runner.Pending() != 0 {
					t.Fatalf("admitted real Run/Wait/receipt: %+v %v", turn, runErr)
				}
				if got := toolMessage(t, lastProviderRequestMessages(t, turn.Link.ProviderJournalPath), "call-e4"); !strings.Contains(got, "e4-ok") {
					t.Fatalf("missing real output: %s", got)
				}
			} else {
				if runErr == nil || runner == nil || runner.Pending() == 0 || script.requests() != 1 {
					t.Fatalf("failure released result/ownership: %+v %v", turn, runErr)
				}
				if err := runner.Close(); err == nil || runner.Pending() == 0 {
					t.Fatal("Close forgot retained runtime ownership")
				}
				if err := launcher.Close(context.Background()); !errors.Is(err, piruntime.ErrUnknownSettlement) {
					t.Fatalf("runtime Close forgot its owned command cell: %v", err)
				}
				// Preserve the full Store and owner journal for the orchestrator's reconciliation.
				if err := os.Rename(f.root, "/var/lib/tbound-product/e4-retained-"+scenario); err != nil {
					t.Fatal(err)
				}
				t.Log("Close refused and retained runtime ownership; /var/lib/tbound-product/e4-retained-" + scenario + " preserves the turn")
			}
			productE4CheckRecords(t, dir, scenario)
			t.Log("E4 loaded host -> revalidated constructor -> admitted Probe; child environments verified:", scenario)
		})
	}
}

func productE4CheckRecords(t *testing.T, dir, scenario string) {
	t.Helper()
	want := []string{"CONTAINERS_CONF=/etc/tbound-product/podman/containers.conf", "CONTAINERS_STORAGE_CONF=/etc/tbound-product/podman/storage.conf", "HOME=/var/lib/tbound-product/podman-home", "LANG=C", "LC_ALL=C", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TMPDIR=/run/tbound-product/tmp", "XDG_CACHE_HOME=/run/tbound-product/cache", "XDG_RUNTIME_DIR=/run/tbound-product"}
	files, err := filepath.Glob(dir + "/child-*.json")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var r productE4ChildRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.Env, want) {
			t.Errorf("child %s %v phase=%s environment=%q want=%q", r.Tool, r.Args, r.Phase, r.Env, want)
		}
		seen[r.Tool+":"+r.Args[0]] = true
		seen[r.Phase] = true
	}
	required := []string{"podman:--version", "podman:info", "podman:image", "cosign:verify", "podman:run", "running", "podman:rm"}
	if scenario == "success" {
		required = append(required, "exited", "podman:container")
	} else {
		required = append(required, "explicit-cleanup")
	}
	if scenario == "deferred" {
		required = append(required, "deferred-cleanup")
	}
	for _, phase := range required {
		if !seen[phase] {
			t.Errorf("runtime phase %s not exercised; records=%v", phase, seen)
		}
	}
	t.Logf("E4 %s: %d actual child environments; phases=%v (Wait is the recorded run process's completion)", scenario, len(files), seen)
}
