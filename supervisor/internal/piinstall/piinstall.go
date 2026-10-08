// Package piinstall provides Node.js discovery and side-by-side installation of
// the pinned Pi coding-agent packages for a tbound user install.
//
// It is intentionally small and injectable: all process execution and PATH
// lookup go through Options so the logic is unit-testable without a real Node,
// npm, or network. Nothing here modifies the user's global Node/Pi installs; the
// side-by-side packages live under the tbound prefix.
package piinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Pinned dependency identities. These must match adapter/package.json.
const (
	PiCodingAgentPkg     = "@earendil-works/pi-coding-agent"
	PiAIPkg              = "@earendil-works/pi-ai"
	PiTUIPkg             = "@earendil-works/pi-tui"
	TypeboxPkg           = "typebox"
	PinnedPiVersion      = "0.87.1"
	PinnedTypeboxVersion = "1.3.27"
	MinNodeMajor         = 22
	MinNodeMinor         = 19
)

// PinnedPackages are the exact package@version specs the adapter depends on.
var PinnedPackages = []string{
	PiCodingAgentPkg + "@" + PinnedPiVersion,
	PiAIPkg + "@" + PinnedPiVersion,
	PiTUIPkg + "@" + PinnedPiVersion,
	TypeboxPkg + "@" + PinnedTypeboxVersion,
}

// ErrNetworkRequired is returned when an operation needs the network but the
// caller did not opt in.
var ErrNetworkRequired = errors.New("network access is required but was not enabled (pass --install-pi / TBOUND_ALLOW_NETWORK)")

// Runner executes a command and returns its combined output. It is injected so
// tests can avoid real processes.
type Runner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// Options configure discovery and installation.
type Options struct {
	// Prefix is the tbound data prefix, e.g. ~/.local/share/tbound.
	Prefix string
	// ExplicitNode, when set, is used instead of PATH/prefix discovery.
	ExplicitNode string
	// GOOS is the target OS ("linux", "windows", ...). Defaults to runtime.GOOS.
	GOOS string
	// Run executes commands. Required for install/link.
	Run Runner
	// LookPath resolves a binary name on PATH. Defaults to exec.LookPath.
	LookPath func(string) (string, error)
}

// NodeInfo describes a discovered Node runtime.
type NodeInfo struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	Major   int    `json:"major"`
	Minor   int    `json:"minor"`
	Source  string `json:"source"` // explicit | path | prefix
}

// Satisfies reports whether the Node version meets the pinned minimum.
func (n NodeInfo) Satisfies() bool {
	if n.Major > MinNodeMajor {
		return true
	}
	return n.Major == MinNodeMajor && n.Minor >= MinNodeMinor
}

var nodeVersionRE = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// ParseNodeVersion extracts major/minor from a `node --version` string.
func ParseNodeVersion(s string) (major, minor int, ok bool) {
	m := nodeVersionRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, true
}

func (o Options) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return "linux"
}

func (o Options) nodeBinaryName() string {
	if o.goos() == "windows" {
		return "node.exe"
	}
	return "node"
}

// DiscoverNode finds a usable Node in priority order: explicit path, tbound
// prefix, then PATH. It returns the first candidate that runs, even if it is
// older than the minimum; callers decide whether that is acceptable.
func DiscoverNode(ctx context.Context, o Options) (NodeInfo, error) {
	if o.Run == nil {
		return NodeInfo{}, errors.New("piinstall: Options.Run is required")
	}
	type cand struct{ path, source string }
	var candidates []cand
	if o.ExplicitNode != "" {
		candidates = append(candidates, cand{o.ExplicitNode, "explicit"})
	}
	if o.Prefix != "" {
		candidates = append(candidates, cand{filepath.Join(o.Prefix, "node", "bin", o.nodeBinaryName()), "prefix"})
	}
	if o.LookPath != nil {
		if p, err := o.LookPath("node"); err == nil && p != "" {
			candidates = append(candidates, cand{p, "path"})
		}
	}
	var lastErr error
	for _, c := range candidates {
		out, err := o.Run(ctx, "", c.path, "--version")
		if err != nil {
			lastErr = fmt.Errorf("%s node (%s): %w", c.source, c.path, err)
			continue
		}
		major, minor, ok := ParseNodeVersion(string(out))
		if !ok {
			lastErr = fmt.Errorf("%s node (%s): unrecognized version %q", c.source, c.path, strings.TrimSpace(string(out)))
			continue
		}
		return NodeInfo{Path: c.path, Version: strings.TrimSpace(string(out)), Major: major, Minor: minor, Source: c.source}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no Node.js found (set --node or install Node >= 22.19)")
	}
	return NodeInfo{}, lastErr
}

// PiPackageDir returns the expected source directory for the Pi coding-agent
// package inside a node_modules tree.
func PiPackageDir(nodeModules string) string {
	return filepath.Join(nodeModules, PiCodingAgentPkg, "package.json")
}

type packageJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DetectNodeModulesPi reads the installed Pi version from a node_modules dir.
func DetectNodeModulesPi(nodeModules string) (version string, ok bool) {
	b, err := os.ReadFile(PiPackageDir(nodeModules))
	if err != nil {
		return "", false
	}
	var p packageJSON
	if json.Unmarshal(b, &p) != nil {
		return "", false
	}
	return p.Version, p.Version != ""
}

// RuntimeNodeModules is where the worker resolves Pi deps inside the prefix.
func RuntimeNodeModules(prefix string) string {
	return filepath.Join(prefix, "runtime", "node_modules")
}

// PrefixPIModules is the side-by-side install location for pinned packages.
func PrefixPIModules(prefix string) string {
	return filepath.Join(prefix, "pi", "node_modules")
}

// InstallPinnedPackages installs the pinned Pi packages into prefix/pi using
// npm. It never touches the user's global Node or Pi. Network is required and
// must be explicitly enabled.
func InstallPinnedPackages(ctx context.Context, o Options, allowNetwork bool) error {
	if !allowNetwork {
		return ErrNetworkRequired
	}
	if o.Run == nil {
		return errors.New("piinstall: Options.Run is required")
	}
	if o.Prefix == "" {
		return errors.New("piinstall: prefix is required")
	}
	node, err := DiscoverNode(ctx, o)
	if err != nil {
		return err
	}
	piDir := filepath.Join(o.Prefix, "pi")
	if err := os.MkdirAll(piDir, 0o700); err != nil {
		return err
	}
	// Local install into the prefix; --no-save keeps it isolated.
	args := append([]string{"install", "--prefix", piDir, "--omit=dev", "--no-save", "--no-audit", "--no-fund"}, PinnedPackages...)
	if _, err := o.Run(ctx, piDir, node.Path, append([]string{npmCLIPath(node.Path)}, args...)...); err != nil {
		// Fall back to a sibling npm executable if the CLI shim is absent.
		if _, err2 := o.Run(ctx, piDir, npmBinary(node.Path), args...); err2 != nil {
			return fmt.Errorf("install pinned Pi packages: %w", err)
		}
	}
	got, ok := DetectNodeModulesPi(PrefixPIModules(o.Prefix))
	if !ok || got != PinnedPiVersion {
		return fmt.Errorf("pinned Pi install did not yield %s (got %q)", PinnedPiVersion, got)
	}
	return nil
}

// npmCLIPath returns the npm CLI entrypoint shipped next to node, if present.
func npmCLIPath(nodePath string) string {
	dir := filepath.Dir(nodePath)
	for _, rel := range []string{"../lib/node_modules/npm/bin/npm-cli.js", "npm-cli.js"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(dir, "npm-cli.js")
}

func npmBinary(nodePath string) string {
	if strings.HasSuffix(strings.ToLower(nodePath), ".exe") {
		return filepath.Join(filepath.Dir(nodePath), "npm.cmd")
	}
	return filepath.Join(filepath.Dir(nodePath), "npm")
}

// LinkRuntimeNodeModules points the runtime's node_modules at the prefix Pi
// install so the worker resolves the side-by-side packages.
func LinkRuntimeNodeModules(prefix string) error {
	target := PrefixPIModules(prefix)
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("pinned Pi modules not installed at %s: %w", target, err)
	}
	link := RuntimeNodeModules(prefix)
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(link); err == nil {
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("link %s -> %s: %w", link, target, err)
	}
	return nil
}
