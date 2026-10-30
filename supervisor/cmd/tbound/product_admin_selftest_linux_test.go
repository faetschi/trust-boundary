//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

func TestProductAdminSelfTestCredentialIsTheClientAccountsIdentity(t *testing.T) {
	oldLookup, oldGroups := productAdminLookupUserID, productAdminUserGroups
	t.Cleanup(func() { productAdminLookupUserID, productAdminUserGroups = oldLookup, oldGroups })
	productAdminLookupUserID = func(id string) (*user.User, error) {
		if id != "1000" {
			return nil, user.UnknownUserIdError(0)
		}
		return &user.User{Uid: id, Gid: "1000"}, nil
	}
	productAdminUserGroups = func(*user.User) ([]string, error) { return []string{"1000", "950"}, nil }
	credential, err := productAdminSelfTestCredential(1000)
	if err != nil || credential.Uid != 1000 || credential.Gid != 1000 || !slices.Equal(credential.Groups, []uint32{1000, 950}) {
		t.Fatalf("credential %+v: %v", credential, err)
	}
	if _, err := productAdminSelfTestCredential(4242); err == nil || !strings.Contains(err.Error(), "no account") {
		t.Fatalf("a UID without an account must be refused: %v", err)
	}
	productAdminUserGroups = func(*user.User) ([]string, error) { return nil, errors.New("no group database") }
	if _, err := productAdminSelfTestCredential(1000); err == nil {
		t.Fatal("a failed group lookup must refuse, not run with fewer groups")
	}
}

func TestProductAdminSelfTestRunsInAChildProcessAsTheRegisteredClient(t *testing.T) {
	root := privateGovernedTestRoot(t)
	h := newAdminHost(t, root)
	if out, err := h.setupAt(h.config(true), root); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	var outcome atomic.Value
	outcome.Store("no_diff")
	fakeProductService(t, h.path(productsetup.SocketPath), &outcome)

	oldLookup, oldGroups, oldCommand := productAdminLookupUserID, productAdminUserGroups, productAdminSelfTestCommand
	t.Cleanup(func() {
		productAdminLookupUserID, productAdminUserGroups, productAdminSelfTestCommand = oldLookup, oldGroups, oldCommand
	})
	productAdminLookupUserID = func(id string) (*user.User, error) { return &user.User{Uid: id, Gid: "1000"}, nil }
	productAdminUserGroups = func(*user.User) ([]string, error) { return []string{"950"}, nil }
	var seen *syscall.Credential
	productAdminSelfTestCommand = func(ctx context.Context, credential *syscall.Credential) *exec.Cmd {
		seen = credential
		cmd := oldCommand(ctx, credential)
		if cmd.Path != productsetup.BinaryPath || cmd.SysProcAttr.Credential != credential || cmd.Dir != "/" {
			t.Errorf("the child must be the installed binary with the client credential from /: %v", cmd)
		}
		cmd.SysProcAttr = nil // this test process is not root and owns the test root
		cmd.Path = os.Args[0]
		cmd.Args = []string{os.Args[0], "-test.run=^TestProductAdminHelperProcess$", "--", "product", "self-test", "--root", root}
		cmd.Env = append(os.Environ(), "TBOUND_ADMIN_HELPER=1")
		return cmd
	}

	st := productsetup.SelfTest{ProjectID: "alpha", ProviderProfile: "opencode-go-engineering", ClientUID: 1000, TimeoutSeconds: 10}
	detail, err := productAdminSelfTestAsClient(st)
	if err != nil || !strings.Contains(detail, "on project alpha") || !strings.HasSuffix(detail, "as UID 1000") {
		t.Fatalf("self-test as the client: %q %v", detail, err)
	}
	if seen == nil || seen.Uid != 1000 || seen.Gid != 1000 || !slices.Equal(seen.Groups, []uint32{1000, 950}) {
		t.Fatalf("the child did not get the client's identity: %+v", seen)
	}
	outcome.Store("failed")
	if _, err := productAdminSelfTestAsClient(st); err == nil || !strings.Contains(err.Error(), "as UID 1000") {
		t.Fatalf("a failing turn must fail the self-test: %v", err)
	}
}

func TestProductAdminSelfTestChildRefusesAnInvalidRequest(t *testing.T) {
	for name, request := range map[string]string{"not json": "garbage", "unknown field": `{"project_id":"alpha","extra":1}`, "empty": ""} {
		var out strings.Builder
		if err := productAdminSelfTestChild(t.TempDir(), strings.NewReader(request), &out); adminExit(err) != 2 {
			t.Errorf("%s: exit %d (%v), want 2", name, adminExit(err), err)
		}
	}
}

func TestProductAdminSetupRefusesASelfTestWithoutARegisteredClientUID(t *testing.T) {
	root := privateGovernedTestRoot(t)
	h := newAdminHost(t, root)
	before := adminTree(t, root)
	for name, uid := range map[string]int{"none configured": 0, "not registered": 4242} {
		cfg := h.setupConfig()
		cfg.SelfTest = &productsetup.SelfTest{ProjectID: "alpha", ProviderProfile: "opencode-go-engineering", ClientUID: uid}
		data, _ := json.Marshal(cfg)
		file := filepath.Join(h.dir, "setup-uid.json")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, extra := range [][]string{nil, {"--dry-run"}} {
			out, err := h.setupAt(file, root, extra...)
			if adminExit(err) != 2 || !strings.Contains(err.Error(), "client_uid") {
				t.Fatalf("%s %v: %v\n%s", name, extra, err, out)
			}
		}
	}
	if after := adminTree(t, root); after != before {
		t.Fatalf("a refused setup changed the root:\n%s\n%s", before, after)
	}
}

// TestProductAdminSelfTestChildIsBuiltWithExactlyTheClientsIdentityAndAMinimalEnvironment checks the command as
// the installed code builds it, which the test above replaces before it runs, so it needs no root.
func TestProductAdminSelfTestChildIsBuiltWithExactlyTheClientsIdentityAndAMinimalEnvironment(t *testing.T) {
	oldLookup, oldGroups, oldCommand := productAdminLookupUserID, productAdminUserGroups, productAdminSelfTestCommand
	t.Cleanup(func() {
		productAdminLookupUserID, productAdminUserGroups, productAdminSelfTestCommand = oldLookup, oldGroups, oldCommand
	})
	productAdminLookupUserID = func(id string) (*user.User, error) { return &user.User{Uid: id, Gid: "2000"}, nil }
	productAdminUserGroups = func(*user.User) ([]string, error) { return []string{"2000", "3000", "3001"}, nil }
	var built, run *exec.Cmd
	productAdminSelfTestCommand = func(ctx context.Context, credential *syscall.Credential) *exec.Cmd {
		built = oldCommand(ctx, credential)
		run = exec.CommandContext(ctx, "/bin/sh", "-c", "cat >/dev/null; echo done")
		return run
	}
	detail, err := productAdminSelfTestAsClient(productsetup.SelfTest{ProjectID: "alpha", ProviderProfile: "p", ClientUID: 1234, TimeoutSeconds: 5})
	if err != nil || !strings.HasPrefix(detail, "done") {
		t.Fatalf("self-test as the client: %q %v", detail, err)
	}
	wantCredential := &syscall.Credential{Uid: 1234, Gid: 2000, Groups: []uint32{2000, 3000, 3001}}
	if built == nil || !reflect.DeepEqual(built.SysProcAttr, &syscall.SysProcAttr{Credential: wantCredential}) {
		t.Fatalf("the child's process attributes are not exactly the client's UID, GID and groups: %+v", built.SysProcAttr)
	}
	if built.Path != productsetup.BinaryPath || !slices.Equal(built.Args, []string{productsetup.BinaryPath, "product", "self-test"}) || built.Dir != "/" {
		t.Fatalf("the child must be the installed binary's `product self-test` from /: path %q args %q dir %q", built.Path, built.Args, built.Dir)
	}
	if !slices.Equal(built.Env, []string{"PATH=/usr/bin:/bin"}) {
		t.Fatalf("the child's environment must be the one fixed PATH, not the administrator's: %q", built.Env)
	}
	if len(built.ExtraFiles) != 0 {
		t.Fatalf("the child inherits extra descriptors: %v", built.ExtraFiles)
	}
	for name, stream := range map[string]any{"stdin": run.Stdin, "stdout": run.Stdout, "stderr": run.Stderr} {
		if _, isFile := stream.(*os.File); isFile || stream == nil {
			t.Errorf("the child's %s must be a private pipe, not an inherited descriptor: %T", name, stream)
		}
	}
}
