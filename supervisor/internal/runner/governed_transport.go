package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Transport carries one governed client invocation to the guest. argv is the
// argument vector of the `tbound` client, after the binary (GovernedRequest.Argv);
// the transport adds whatever puts the process on the right guest identity. It
// returns what the client wrote and its exit code. A non-nil error means the
// invocation could not be carried out or observed (no start, no wait, cancelled
// context); a client that ran and exited non-zero is exit code with a nil error.
type Transport interface {
	Run(ctx context.Context, argv []string) (stdout, stderr []byte, exitCode int, err error)
}

// DefaultClientCommand is the guest-side launcher of the client, as the runbook
// documents it (infra/guest/PROVISIONING-SIGNING.md, Part L, placeholder CLIENT):
// uid 994 (tbound-client) and primary gid 985 (tbound-pi-clients) through setpriv,
// because the client account's login shell is nologin. The service admits only that
// uid and gid (AllowedClientUIDs and AllowedClientPrimaryGID of the signed
// registration), and the client checks its own identity before it dials. setpriv also
// clears the supplementary groups, empties the bounding, inheritable and ambient
// capability sets and sets no_new_privs, so the client runs with no capability and
// cannot regain one; `--` ends setpriv's options before the binary.
func DefaultClientCommand() []string {
	return []string{"setpriv", "--reuid", "994", "--regid", "985", "--clear-groups",
		"--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "--no-new-privs", "--", "/usr/local/bin/tbound"}
}

// SSHTransport runs the client on the guest through the Git Bash ssh binary.
//
// Host checking is fixed and cannot be configured away: StrictHostKeyChecking=yes
// against one pinned known_hosts file, under HostKeyAlias, with BatchMode=yes. Every
// option is given on the command line, where it wins over any ssh_config. The
// transport only passes the identity file's path to ssh; it never opens or reads it.
//
// The login User must be able to run setpriv to the client uid and gid, that is root:
// the runbook has no non-root path to the client (the client account is nologin and
// passwordless sudo is revoked at Z1, "nothing after Z1 uses sudo"). Which account
// that is, is the owner's decision and configuration, not this package's.
type SSHTransport struct {
	// SSHPath is the ssh binary (Git Bash ssh on the owner machine).
	SSHPath string
	// IdentityFile is the path of the private key. Never read here.
	IdentityFile string
	// User and Host name the guest login. HostKeyAlias is the name the pinned
	// known_hosts entry is filed under (the guest's stable alias, not its current IP).
	User, Host, HostKeyAlias string
	// KnownHostsFile holds the pinned host key.
	KnownHostsFile string
	// ClientCommand is the guest-side launcher up to and including the tbound
	// binary. Empty means DefaultClientCommand.
	ClientCommand []string
}

var (
	// sshUserRE and sshHostRE admit only plain names and addresses: no leading
	// dash (an option), no space, no @, no shell or ssh_config syntax.
	sshUserRE  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	sshHostRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)
	sshAliasRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

// Validate checks the configuration without touching the network or any file.
func (c SSHTransport) Validate() error {
	switch {
	case c.SSHPath == "" || c.IdentityFile == "" || c.User == "" || c.Host == "" || c.HostKeyAlias == "" || c.KnownHostsFile == "":
		return errors.New("ssh transport needs ssh path, identity file, user, host, host key alias and known_hosts file")
	case !sshUserRE.MatchString(c.User):
		return fmt.Errorf("ssh user %q is not a plain account name", c.User)
	case !sshHostRE.MatchString(c.Host):
		return fmt.Errorf("ssh host %q is not a plain host name or address", c.Host)
	case !sshAliasRE.MatchString(c.HostKeyAlias):
		return fmt.Errorf("ssh host key alias %q is not a plain name", c.HostKeyAlias)
	case !plainOptionValue(c.KnownHostsFile) || c.KnownHostsFile == "/dev/null" || strings.EqualFold(c.KnownHostsFile, "none"):
		return errors.New("ssh known_hosts file must be one real file path without spaces or quotes")
	case strings.ContainsRune(c.SSHPath, 0) || strings.ContainsRune(c.IdentityFile, 0):
		return errors.New("ssh paths must not contain NUL")
	}
	for _, w := range c.launcher() {
		if w == "" || strings.ContainsRune(w, 0) {
			return errors.New("client command words must be non-empty and free of NUL")
		}
	}
	return nil
}

func (c SSHTransport) launcher() []string {
	if len(c.ClientCommand) == 0 {
		return DefaultClientCommand()
	}
	return c.ClientCommand
}

// plainOptionValue is true for a value that survives ssh's option parser as one
// token: printable, no whitespace, no quotes.
func plainOptionValue(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r == '"' || r == '\'' || r == '\\' {
			return false
		}
	}
	return true
}

// shellQuote quotes one word for a POSIX shell in single quotes; each embedded
// single quote becomes close-quote, backslash, quote, open-quote. Every word is
// quoted, whatever it contains. NUL
// cannot be passed through a shell and is refused.
func shellQuote(s string) (string, error) {
	if strings.ContainsRune(s, 0) {
		return "", errors.New("argument contains NUL")
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'", nil
}

// RemoteCommand is the one string the guest receives for argv: the launcher and argv, every
// word shell-quoted. The guest's client forced-command entry (`tbound client-forced`) is held
// to it by test.
func (c SSHTransport) RemoteCommand(argv []string) (string, error) { return c.remoteCommand(argv) }

// remoteCommand is the one string the guest shell receives.
func (c SSHTransport) remoteCommand(argv []string) (string, error) {
	words := append(append([]string(nil), c.launcher()...), argv...)
	quoted := make([]string, len(words))
	for i, w := range words {
		q, err := shellQuote(w)
		if err != nil {
			return "", err
		}
		quoted[i] = q
	}
	return strings.Join(quoted, " "), nil
}

// sshArgs is the full argument vector of the ssh process. The destination follows
// `--`, so no value can be read as an option.
func (c SSHTransport) sshArgs(argv []string) ([]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	remote, err := c.remoteCommand(argv)
	if err != nil {
		return nil, err
	}
	// -F /dev/null and the global known-hosts file keep the user's and the system's ssh
	// configuration (host-key files, proxies, canonicalization, match blocks) out of the
	// connection: only the pinned known-hosts file and the options below apply.
	return []string{
		"-F", "/dev/null",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "HostKeyAlias=" + c.HostKeyAlias,
		"-o", "UserKnownHostsFile=" + c.KnownHostsFile,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "CanonicalizeHostname=no",
		"-o", "ProxyCommand=none",
		"-o", "ProxyJump=none",
		"-o", "IdentitiesOnly=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "ForwardAgent=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "ControlPath=none",
		"-o", "ConnectTimeout=15",
		"-i", c.IdentityFile,
		"--", c.User + "@" + c.Host, remote,
	}, nil
}

// Run implements Transport.
func (c SSHTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	args, err := c.sshArgs(argv)
	if err != nil {
		return nil, nil, 0, err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.SSHPath, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return stdout.Bytes(), stderr.Bytes(), 0, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		return stdout.Bytes(), stderr.Bytes(), exit.ExitCode(), nil
	}
	if runErr != nil {
		return stdout.Bytes(), stderr.Bytes(), 0, runErr
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

// firstLineWriter buffers everything written to it and calls ready once, with the first
// complete line (newline included), as soon as that line is in.
type firstLineWriter struct {
	buf   bytes.Buffer
	ready func([]byte)
	fired bool
}

func (w *firstLineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if !w.fired && w.ready != nil {
		if i := bytes.IndexByte(w.buf.Bytes(), '\n'); i >= 0 {
			w.fired = true
			w.ready(append([]byte(nil), w.buf.Bytes()[:i+1]...))
		}
	}
	return len(p), nil
}

// RunReady implements ReadyTransport: Run, plus a call to ready with the first stdout line
// while the remote command is still running.
func (c SSHTransport) RunReady(ctx context.Context, argv []string, ready func([]byte)) ([]byte, []byte, int, error) {
	args, err := c.sshArgs(argv)
	if err != nil {
		return nil, nil, 0, err
	}
	stdout := &firstLineWriter{ready: ready}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.SSHPath, args...)
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return stdout.buf.Bytes(), stderr.Bytes(), 0, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		return stdout.buf.Bytes(), stderr.Bytes(), exit.ExitCode(), nil
	}
	return stdout.buf.Bytes(), stderr.Bytes(), 0, runErr
}
