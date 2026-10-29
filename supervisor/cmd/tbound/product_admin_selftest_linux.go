//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

// Seams for tests: the account lookups behind the self-test client's credential, and the child command.
var (
	productAdminLookupUserID    = user.LookupId
	productAdminUserGroups      = func(account *user.User) ([]string, error) { return account.GroupIds() }
	productAdminSelfTestCommand = func(ctx context.Context, credential *syscall.Credential) *exec.Cmd {
		cmd := exec.CommandContext(ctx, productsetup.BinaryPath, "product", "self-test")
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
		cmd.Dir = "/"
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		return cmd
	}
)

// productAdminRunSelfTest runs the synthetic turn. The service authorizes a caller by its UID, and root is
// no registered principal, so a root caller on the real root runs the turn in a child process that drops
// to the configured client UID, and only that child does. Any other caller runs it as itself.
func productAdminRunSelfTest(root string) func(productsetup.SelfTest) (string, error) {
	return func(st productsetup.SelfTest) (string, error) {
		if root == "/" && os.Geteuid() == 0 {
			return productAdminSelfTestAsClient(st)
		}
		return productSelfTestFromRoot(root, st)
	}
}

func productSelfTestFromRoot(root string, st productsetup.SelfTest) (string, error) {
	real := func(p string) string { return filepath.Join(root, p) }
	registration, err := productsession.LoadRegistration(real(productsetup.RegistrationPath),
		real(productsetup.RegistrationSignaturePath), real(productsetup.RegistrationPublicKeyPath))
	if err != nil {
		return "", err
	}
	return productSelfTestTurn(real(productsetup.SocketPath), registration, st)
}

// productAdminSelfTestCredential is the client account's identity: its UID, primary GID and supplementary
// groups, which carry the access to the service socket.
func productAdminSelfTestCredential(uid int) (*syscall.Credential, error) {
	account, err := productAdminLookupUserID(strconv.Itoa(uid))
	if err != nil {
		return nil, fmt.Errorf("the self-test client UID %d has no account on this host: %w", uid, err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("the self-test client UID %d has a non-numeric primary group", uid)
	}
	names, err := productAdminUserGroups(account)
	if err != nil {
		return nil, fmt.Errorf("list the groups of the self-test client UID %d: %w", uid, err)
	}
	groups := []uint32{uint32(gid)}
	for _, name := range names {
		id, err := strconv.ParseUint(name, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("the self-test client UID %d has a non-numeric group", uid)
		}
		if uint32(id) != uint32(gid) {
			groups = append(groups, uint32(id))
		}
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}, nil
}

// productAdminSelfTestAsClient runs `product self-test` from the installed binary with the client's
// credential and the request on stdin. The privileged process never connects to the service itself.
func productAdminSelfTestAsClient(st productsetup.SelfTest) (string, error) {
	credential, err := productAdminSelfTestCredential(st.ClientUID)
	if err != nil {
		return "", err
	}
	request, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(st.Timeout()+15)*time.Second)
	defer cancel()
	cmd := productAdminSelfTestCommand(ctx, credential)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(request), &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("as UID %d: %w: %s", st.ClientUID, err, strings.TrimSpace(truncate(stderr.String(), 2048)))
	}
	return fmt.Sprintf("%s as UID %d", strings.TrimSpace(truncate(stdout.String(), 2048)), st.ClientUID), nil
}

func truncate(s string, limit int) string {
	if len(s) > limit {
		return s[:limit]
	}
	return s
}

// productAdminSelfTestChild is the internal `product self-test`: it reads the self-test request from in and
// runs the turn as the process's own identity. The administrative binary starts it as the client UID.
func productAdminSelfTestChild(root string, in io.Reader, out io.Writer) error {
	var st productsetup.SelfTest
	decoder := json.NewDecoder(io.LimitReader(in, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&st); err != nil {
		return adminRefusal("the self-test request is not valid: %v", err)
	}
	detail, err := productSelfTestFromRoot(root, st)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, detail)
	return err
}
