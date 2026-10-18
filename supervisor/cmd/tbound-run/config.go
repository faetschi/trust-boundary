package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/routematrix"
	"tbound/supervisor/internal/runner"
)

const (
	maxConfigBytes = 1 << 20
	// ledgerSyntheticEmpty is the only ledger the pilot report supports: no USD spend is
	// tracked, and the report says so (it is not claim-bearing).
	ledgerSyntheticEmpty = "synthetic-empty"
	quotaThesis          = "thesis"
	quotaPilot           = "pilot"
)

// runConfig is the one run.json. It is decoded strictly: an unknown member, a duplicate
// key or trailing data is refused. There is deliberately no member that changes how SSH
// checks the guest host key: StrictHostKeyChecking is always yes against ssh.known_hosts.
type runConfig struct {
	// Manifest is the corpus manifest. QuotaProfile is thesis or pilot.
	Manifest     string `json:"manifest"`
	QuotaProfile string `json:"quota_profile"`
	// FreezeCommit, EVQObligations, AdaptiveCaps and OnlyCases are runner.Config.
	FreezeCommit   string           `json:"freeze_commit"`
	EVQObligations []string         `json:"evq_obligations"`
	AdaptiveCaps   calibration.Caps `json:"adaptive_caps"`
	OnlyCases      []string         `json:"only_cases"`
	// Pins are the receipt identity values E01 is verified against.
	Pins runner.ReceiptPins `json:"pins"`
	// RouteReport is a JSON routematrix.Report; its file digest is the route digest.
	RouteReport string `json:"route_report"`
	// Ledger must be "synthetic-empty".
	Ledger string `json:"ledger"`
	// RunDir holds journal.jsonl.
	RunDir string     `json:"run_dir"`
	SSH    sshConfig  `json:"ssh"`
	Proc   procConfig `json:"proc"`
}

// sshConfig names the guest login of three separate keys: the client key (serve), the
// evidence key (forced to `tbound evidence`) and the trial key (forced to `tbound trial`).
type sshConfig struct {
	Path         string   `json:"path"`
	KnownHosts   string   `json:"known_hosts"`
	Host         string   `json:"host"`
	HostKeyAlias string   `json:"host_key_alias"`
	Client       identity `json:"client"`
	Evidence     identity `json:"evidence"`
	Trial        identity `json:"trial"`
}

type identity struct {
	User string `json:"user"`
	Key  string `json:"key"`
}

// procConfig is the PROC collector's view of the service's cell registration and the
// length of the guest monitor.
type procConfig struct {
	CellUID           uint32   `json:"cell_uid"`
	CellGID           uint32   `json:"cell_gid"`
	MonitorSeconds    int      `json:"monitor_seconds"`
	TeardownGraceSecs int      `json:"teardown_grace_seconds"`
	AuthorizedSockets []string `json:"authorized_sockets"`
}

// loadConfig reads and validates run.json. It touches no network and no key file.
func loadConfig(path string) (runConfig, error) {
	var c runConfig
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return c, fmt.Errorf("config: %s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return c, errors.New("config: exceeds the size limit")
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return c, errors.New("config: trailing data")
	}
	if err := c.validate(); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

func (c runConfig) validate() error {
	switch c.QuotaProfile {
	case quotaThesis, quotaPilot:
	default:
		return fmt.Errorf("quota_profile %q: use %q or %q", c.QuotaProfile, quotaThesis, quotaPilot)
	}
	if c.Ledger != ledgerSyntheticEmpty {
		return fmt.Errorf("ledger %q: only %q is implemented", c.Ledger, ledgerSyntheticEmpty)
	}
	for name, p := range map[string]string{
		"manifest": c.Manifest, "route_report": c.RouteReport, "run_dir": c.RunDir,
		"ssh.known_hosts": c.SSH.KnownHosts, "ssh.client.key": c.SSH.Client.Key,
		"ssh.evidence.key": c.SSH.Evidence.Key, "ssh.trial.key": c.SSH.Trial.Key,
	} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s %q must be an absolute path", name, p)
		}
	}
	keys := []string{c.SSH.Client.Key, c.SSH.Evidence.Key, c.SSH.Trial.Key}
	for i, k := range keys {
		if slices.Contains(keys[:i], k) {
			return fmt.Errorf("ssh key %q is used for two channels: the client, evidence and trial keys must differ", k)
		}
	}
	if c.SSH.Path == "" {
		return errors.New("ssh.path is required")
	}
	if c.Proc.CellUID == 0 || c.Proc.CellGID == 0 || c.Proc.MonitorSeconds <= 0 || c.Proc.TeardownGraceSecs < 0 {
		return errors.New("proc needs cell_uid, cell_gid, monitor_seconds and a non-negative teardown_grace_seconds")
	}
	if err := (runner.Attest{EVQ: c.EVQObligations, Pins: c.Pins}).Validate(); err != nil {
		return err
	}
	ts := c.transports()
	for name, t := range map[string]runner.SSHTransport{"client": ts.client, "evidence": ts.evidence, "trial": ts.trial} {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("ssh.%s: %w", name, err)
		}
	}
	return nil
}

// runnerConfig is the protocol Config bound into the plan digest.
func (c runConfig) runnerConfig() runner.Config {
	return runner.Config{FreezeCommit: c.FreezeCommit, AdaptiveCaps: c.AdaptiveCaps,
		EVQObligations: slices.Clone(c.EVQObligations), OnlyCases: slices.Clone(c.OnlyCases)}
}

func (c runConfig) journalPath() string { return filepath.Join(c.RunDir, "journal.jsonl") }

type transports struct{ client, evidence, trial runner.SSHTransport }

// transports builds the three SSH transports. Host checking is fixed in
// runner.SSHTransport: StrictHostKeyChecking=yes against the pinned known_hosts, under
// the host key alias. The key files are only named, never read. Paths are passed with
// forward slashes, which the Git Bash ssh and Windows accept alike.
func (c runConfig) transports() transports {
	base := runner.SSHTransport{SSHPath: c.SSH.Path, User: "", Host: c.SSH.Host, HostKeyAlias: c.SSH.HostKeyAlias,
		KnownHostsFile: filepath.ToSlash(c.SSH.KnownHosts)}
	with := func(id identity, launcher []string) runner.SSHTransport {
		t := base
		t.User, t.IdentityFile, t.ClientCommand = id.User, filepath.ToSlash(id.Key), launcher
		return t
	}
	return transports{
		client:   with(c.SSH.Client, nil),
		evidence: with(c.SSH.Evidence, runner.DefaultEvidenceCommand()),
		trial:    with(c.SSH.Trial, runner.DefaultTrialCommand()),
	}
}

// loadRoute reads the route report: its digest is the sha256 of the file bytes, and it
// is complete only if the routematrix.Report says so.
func loadRoute(path string) (runner.RouteCoverage, error) {
	f, err := os.Open(path)
	if err != nil {
		return runner.RouteCoverage{}, fmt.Errorf("route report: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return runner.RouteCoverage{}, fmt.Errorf("route report: %s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return runner.RouteCoverage{}, errors.New("route report: unreadable or too large")
	}
	var rep routematrix.Report
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rep); err != nil {
		return runner.RouteCoverage{}, fmt.Errorf("route report: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return runner.RouteCoverage{}, errors.New("route report: trailing data")
	}
	sum := sha256.Sum256(data)
	return runner.RouteCoverage{Complete: rep.Complete, Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}
