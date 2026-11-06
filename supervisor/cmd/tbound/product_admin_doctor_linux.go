//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/unix"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productprovider"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

const (
	vmPointer         = "use the qualified VM option: infra/guest/README.md and docs/guides/governed-setup.md"
	extSuperMagic     = 0xEF53
	selfTestPollEvery = 500 * time.Millisecond
)

// hostPrerequisites checks the product host the way the governed-host script and tbound-doctor do:
// Landlock ABI, cgroup v2 delegation, rootless Podman and crun, an ext4 managed root, and systemd.
// The results are advisory inventory, never an admission.
func hostPrerequisites() []productsetup.Check {
	return hostPrerequisitesAt("/")
}

func hostPrerequisitesAt(root string) []productsetup.Check {
	var checks []productsetup.Check
	add := func(name string, ok bool, detail string) {
		c := productsetup.Check{Name: name, Status: productsetup.Pass, Detail: detail}
		if !ok {
			c.Status, c.Fix = productsetup.Fail, vmPointer
		}
		checks = append(checks, c)
	}
	abi, err := piruntime.D09LandlockKernelABI()
	switch {
	case err != nil:
		add("landlock-abi", false, "Landlock ABI query failed: "+err.Error())
	default:
		add("landlock-abi", abi == piruntime.D09LandlockABI, fmt.Sprintf("kernel reports ABI %d, the qualified profile needs exactly ABI %d", abi, piruntime.D09LandlockABI))
	}
	controllers, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	add("cgroup-v2", err == nil && len(controllers) > 1 && cgroupKillAvailable("/sys/fs/cgroup"), "cgroup v2 with delegated controllers and cgroup.kill")
	for _, bin := range []string{"podman", "crun"} {
		path, err := exec.LookPath(bin)
		add(bin, err == nil, "rootless containment needs "+bin+" on PATH "+path)
	}
	probe := filepath.Join(root, productsetup.StateDir)
	for {
		if _, err := os.Stat(probe); err == nil || probe == "/" {
			break
		}
		probe = filepath.Dir(probe)
	}
	var fs unix.Statfs_t
	add("ext4-managed-root", unix.Statfs(probe, &fs) == nil && fs.Type == extSuperMagic, "the managed root at "+productsetup.StateDir+" must be on ext4")
	info, err := os.Stat(filepath.Join(root, "/run/systemd/system"))
	add("systemd", err == nil && info.IsDir(), "systemd is the service manager")
	return append(checks, productQualificationEvidenceCheck(filepath.Join(root, productsetup.TurnsDir)))
}

// cgroupKillAvailable looks below the root because the kernel creates cgroup.kill only in non-root cgroups.
func cgroupKillAvailable(root string) bool {
	found, err := filepath.Glob(filepath.Join(root, "*", "cgroup.kill"))
	return err == nil && len(found) > 0
}

func productAdminDoctor(env productsetup.Env, selfTest bool, out io.Writer) error {
	rep := productsetup.Doctor(env, selfTest)
	if _, err := io.WriteString(out, rep.String()); err != nil {
		return err
	}
	switch {
	case slices.ContainsFunc(rep.Checks, func(c productsetup.Check) bool { return c.Status == productsetup.Fail }):
		return &adminError{msg: "doctor: a check failed", code: 1}
	case !selfTest || rep.SelfTestErr == nil:
		return nil
	case errors.Is(rep.SelfTestErr, productsetup.ErrSelfTestNotConfigured):
		return &adminError{msg: "doctor: self-test not configured; governed readiness is not established", code: 3}
	}
	return &adminError{msg: "doctor: the self-test failed", code: 1}
}

// productAdminCredentialStatus reports a provider credential without its key, through the credential
// store's own status check.
func productAdminCredentialStatus(profile string) (string, error) {
	status, err := productprovider.NewStore().Status(profile)
	switch {
	case err != nil:
		return "", err
	case !status.Present:
		return "", errors.New("credential absent")
	}
	return fmt.Sprintf("present, owner %d:%d, mode %s, fingerprint %s", status.UID, status.GID, status.Mode, status.Fingerprint), nil
}

func selfTestKey(label string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "selftest-" + hex.EncodeToString(b[:]) + "-" + label
}

// productSelfTestTurn runs the synthetic turn: a read-only session on the self-test project, one turn, and a
// wait for it to settle. It passes only when the service settled the turn with no diff, so no stub or an
// unreachable worker can pass it. It runs as the calling process's UID.
func productSelfTestTurn(socket string, registration productsession.Registration, st productsetup.SelfTest) (string, error) {
	var mode string
	found := false
	for _, p := range registration.Projects {
		if p.ProjectID == st.ProjectID {
			mode, found = p.PublicationModes[0], true
		}
	}
	if !found || !slices.Contains(registration.ProviderProfiles, st.ProviderProfile) {
		return "", errors.New("the self-test project or provider profile is not in the signed registration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(st.Timeout())*time.Second)
	defer cancel()
	client := &chatClient{socket: socket, attempts: chatDefaultAttempt, backoff: chatDefaultBackoff}
	budget := registration.Limits.Turn
	resp, err := client.call(ctx, productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &productprotocol.CreateSession{
		ProjectID: st.ProjectID, ProviderProfile: st.ProviderProfile, PublicationMode: mode, ReadOnly: true, RequestKey: selfTestKey("create"),
		Budget: productprotocol.Budget{HostCalls: budget.HostCalls, Tokens: budget.Tokens, DurationSeconds: budget.DurationSeconds},
	}}, chatCallTimeout)
	if err == nil && resp.Session == nil {
		err = errors.New("no session in the response")
	}
	if err != nil {
		return "", fmt.Errorf("create the self-test session: %w", err)
	}
	session := resp.Session
	resp, err = client.call(ctx, productprotocol.Request{Op: productprotocol.OpSubmitTurn, SubmitTurn: &productprotocol.SubmitTurn{
		SessionID: session.ID, ExpectedRevision: session.Revision, RequestKey: selfTestKey("turn"), Prompt: st.PromptText(),
	}}, chatCallTimeout)
	if err == nil && resp.Turn == nil {
		err = errors.New("no turn in the response")
	}
	if err != nil {
		return "", fmt.Errorf("submit the self-test turn: %w", err)
	}
	turnID := resp.Turn.ID
	for {
		resp, err := client.call(ctx, productprotocol.Request{Op: productprotocol.OpGetSession, GetSession: &productprotocol.SessionRef{SessionID: session.ID}}, chatCallTimeout)
		if err == nil && resp.Session == nil {
			err = errors.New("no session in the response")
		}
		if err != nil {
			return "", fmt.Errorf("read the self-test session: %w", err)
		}
		for _, turn := range resp.Session.Turns {
			if turn.ID != turnID {
				continue
			}
			switch turn.State {
			case string(productsession.StateTerminal):
				if turn.Outcome != productsession.OutcomeNoDiff {
					return "", fmt.Errorf("the self-test turn %s ended with outcome %q (reason %q), want %q", turnID, turn.Outcome, turn.TerminalReason, productsession.OutcomeNoDiff)
				}
				return fmt.Sprintf("on project %s (turn %s settled with %s)", st.ProjectID, turnID, turn.Outcome), nil
			case string(productsession.StateAbandoned), string(productsession.StateUnknown), string(productsession.StateAwaitingReview):
				return "", fmt.Errorf("the self-test turn %s is %s, not settled", turnID, turn.State)
			}
		}
		select {
		case <-time.After(selfTestPollEvery):
		case <-ctx.Done():
			return "", fmt.Errorf("the self-test turn %s did not settle within %ds", turnID, st.Timeout())
		}
	}
}
