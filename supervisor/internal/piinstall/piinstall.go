// Package piinstall discovers and fingerprints an already installed official
// Pi CLI. TBound never installs Pi or loads its SDK in-process.
package piinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"tbound/supervisor/internal/broker/openrouter"
)

const PinnedPiVersion = "0.87.1"
const PinnedPiProvider = "tbound-go-broker"

// PinnedPiModel is the Pi model id under the TBound broker provider. It is the
// broker-approved model because the adapter rejects any other model id.
const PinnedPiModel = openrouter.ApprovedModel

// GovernedRPCArgs returns only flags documented by the pinned Pi 0.87.1 CLI.
// This version has no --no-mcp switch; isolated HOME and environment are
// separate launch-profile requirements.
func GovernedRPCArgs(extensionPath string) ([]string, error) {
	if !filepath.IsAbs(extensionPath) || strings.ContainsAny(extensionPath, "\x00\r\n") {
		return nil, errors.New("Pi extension path must be absolute and contain no control characters")
	}
	return []string{
		"--mode", "rpc", "--no-session", "--no-extensions", "--no-builtin-tools",
		"--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
		"--no-approve", "--offline", "--extension", extensionPath,
		"--provider", PinnedPiProvider, "--model", PinnedPiProvider + "/" + PinnedPiModel,
	}, nil
}

type Runner func(context.Context, string, string, ...string) ([]byte, error)

type Options struct {
	ExplicitPi string
	Run        Runner
	LookPath   func(string) (string, error)
}

type PiInfo struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

var versionRE = regexp.MustCompile(`(?:^|\s)v?([0-9]+\.[0-9]+\.[0-9]+)(?:\s|$)`)

func ParsePiVersion(s string) (string, bool) {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(s))
	if len(m) != 2 {
		return "", false
	}
	return m[1], true
}

// DiscoverPi accepts only an explicitly selected or PATH-resolved executable,
// checks its self-reported version, and fingerprints the exact file on disk.
func DiscoverPi(ctx context.Context, o Options) (PiInfo, error) {
	if o.Run == nil {
		return PiInfo{}, errors.New("piinstall: Options.Run is required")
	}
	path := o.ExplicitPi
	if path == "" {
		lookup := o.LookPath
		if lookup == nil {
			lookup = exec.LookPath
		}
		var err error
		path, err = lookup("pi")
		if err != nil || path == "" {
			return PiInfo{}, errors.New("official Pi CLI not found on PATH (install Pi 0.87.1 separately or pass --pi /path/to/pi)")
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return PiInfo{}, fmt.Errorf("resolve Pi CLI path: %w", err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return PiInfo{}, fmt.Errorf("resolve installed Pi CLI entrypoint: %w", err)
	}
	out, err := o.Run(ctx, "", abs, "--version")
	if err != nil {
		return PiInfo{}, fmt.Errorf("run Pi CLI %s --version: %w", abs, err)
	}
	version, ok := ParsePiVersion(string(out))
	if !ok {
		return PiInfo{}, fmt.Errorf("Pi CLI %s returned an unrecognized version: %q", abs, strings.TrimSpace(string(out)))
	}
	if version != PinnedPiVersion {
		return PiInfo{}, fmt.Errorf("Pi CLI version %s does not match pinned %s", version, PinnedPiVersion)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return PiInfo{}, fmt.Errorf("stat Pi CLI: %w", err)
	}
	if !info.Mode().IsRegular() {
		return PiInfo{}, errors.New("Pi CLI path does not resolve to a regular file")
	}
	f, err := os.Open(abs)
	if err != nil {
		return PiInfo{}, fmt.Errorf("open Pi CLI for integrity fingerprint: %w", err)
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return PiInfo{}, fmt.Errorf("fingerprint Pi CLI: %w", err)
	}
	return PiInfo{Path: abs, Version: version, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
