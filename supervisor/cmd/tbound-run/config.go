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
	"regexp"
	"slices"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/routematrix"
	"tbound/supervisor/internal/runner"
)

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

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
	// Artifacts is the directory of the corpus artifacts (attacker-<hex>.json among them). A manifest that pins an
	// attacker artifact needs it: the adaptive caps are then those of the artifact (adaptiveCapsFor).
	Artifacts string   `json:"artifacts_dir"`
	OnlyCases []string `json:"only_cases"`
	// Pins are the receipt identity values E01 is verified against.
	Pins runner.ReceiptPins `json:"pins"`
	// AdmissionPolicy pins the two Store policy digests the service commits its admission_policy_digest to.
	// With the manifest they give the digest a scripted receipt must carry.
	AdmissionPolicy admissionPolicyPins `json:"admission_policy"`
	// VImageDigest pins the cell image Configuration V runs, as sha256:<64 hex>: a V outcome is
	// E01-verified only when the run's document names this image. It is required when the plan
	// has V steps (loadPlan) and optional otherwise.
	VImageDigest string `json:"v_image_digest"`
	// RouteReport is a JSON routematrix.Report; its file digest is the route digest.
	RouteReport string `json:"route_report"`
	// EVQResults pins the conformance results file (strict JSON {obligations:[{id,passed,
	// evidence_ref}]}) by path and sha256. The results are established outside the trial
	// executor; Attest takes every EVQ pass from them and from nowhere else.
	EVQResults pinnedFile `json:"evq_results"`
	// Ledger must be "synthetic-empty".
	Ledger string `json:"ledger"`
	// RunDir holds journal.jsonl.
	RunDir string     `json:"run_dir"`
	SSH    sshConfig  `json:"ssh"`
	Proc   procConfig `json:"proc"`
}

// admissionPolicyPins are the Store policy and metadata policy digests of the governed service.
type admissionPolicyPins struct {
	PolicyDigest         string `json:"policy_digest"`
	MetadataPolicyDigest string `json:"metadata_policy_digest"`
}

// pinnedFile is a file named by absolute path and pinned by "sha256:<hex>" of its bytes.
type pinnedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
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
	if c.Artifacts != "" && !filepath.IsAbs(c.Artifacts) {
		return fmt.Errorf("artifacts_dir %q must be an absolute path", c.Artifacts)
	}
	if !digestRE.MatchString(c.AdmissionPolicy.PolicyDigest) || !digestRE.MatchString(c.AdmissionPolicy.MetadataPolicyDigest) {
		return errors.New("admission_policy needs policy_digest and metadata_policy_digest as sha256:<64 hex>")
	}
	if c.VImageDigest != "" && !digestRE.MatchString(c.VImageDigest) {
		return errors.New("v_image_digest must be sha256:<64 hex>")
	}
	if !filepath.IsAbs(c.EVQResults.Path) || !digestRE.MatchString(c.EVQResults.SHA256) {
		return errors.New("evq_results needs an absolute path and a sha256:<64 hex> digest")
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
func loadRoute(path, freezeCommit string) (runner.RouteCoverage, error) {
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
	return parseRouteReport(data, freezeCommit)
}

// parseRouteReport decodes the route report strictly and takes its digest over the bytes.
func parseRouteReport(data []byte, freezeCommit string) (runner.RouteCoverage, error) {
	var rep routematrix.Report
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rep); err != nil {
		return runner.RouteCoverage{}, fmt.Errorf("route report: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return runner.RouteCoverage{}, errors.New("route report: trailing data")
	}
	// A report made for another commit, or not made by `route-report` at all, is not evidence for this freeze.
	if !routeBoundTo(rep.Binding, freezeCommit) {
		return runner.RouteCoverage{}, fmt.Errorf("route report: it is not bound to the freeze commit %s (binding %q)", freezeCommit, rep.Binding)
	}
	sum := sha256.Sum256(data)
	return runner.RouteCoverage{Complete: rep.Complete, Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

// loadEVQResults reads the pinned EVQ results file: it must hash to the pinned digest and
// parse strictly (runner.ParseEVQResults). A changed file is refused, never re-pinned.
func loadEVQResults(pin pinnedFile) (runner.EVQResults, string, error) {
	f, err := os.Open(pin.Path)
	if err != nil {
		return nil, "", fmt.Errorf("evq results: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("evq results: %s is not a regular file", pin.Path)
	}
	data, err := io.ReadAll(io.LimitReader(f, runner.MaxEVQResultsBytes+1))
	if err != nil || len(data) > runner.MaxEVQResultsBytes {
		return nil, "", errors.New("evq results: unreadable or too large")
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != pin.SHA256 {
		return nil, "", fmt.Errorf("evq results: %s hashes to %s, run.json pins %s", pin.Path, digest, pin.SHA256)
	}
	results, err := runner.ParseEVQResults(data)
	if err != nil {
		return nil, "", err
	}
	return results, digest, nil
}

// runBindingDomain labels the run binding digest.
const runBindingDomain = "tbound-run-binding/v1"

// runBinding is the normalized digest of every attestation and collector input that is not
// in the plan digest: the receipt pins, the route report (its digest and completeness), the
// EVQ results file digest, the admission policy pins, the V cell image pin and the PROC collector settings. It is recorded in the journal
// header, so a journal is only resumed, rerun, exported or reported under the inputs it
// was started with. The authorized sockets are a set and are sorted.
func (c runConfig) runBinding(route runner.RouteCoverage, evqDigest string) (string, error) {
	doc := struct {
		Domain           string              `json:"domain"`
		Pins             runner.ReceiptPins  `json:"pins"`
		RouteDigest      string              `json:"route_digest"`
		RouteComplete    bool                `json:"route_complete"`
		EVQResultsDigest string              `json:"evq_results_digest"`
		Proc             procConfig          `json:"proc"`
		AdmissionPolicy  admissionPolicyPins `json:"admission_policy"`
		VImageDigest     string              `json:"v_image_digest"`
	}{runBindingDomain, c.Pins, route.Digest, route.Complete, evqDigest, c.Proc, c.AdmissionPolicy, c.VImageDigest}
	doc.Proc.AuthorizedSockets = slices.Sorted(slices.Values(c.Proc.AuthorizedSockets))
	if doc.Proc.AuthorizedSockets == nil {
		doc.Proc.AuthorizedSockets = []string{}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("run binding: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
