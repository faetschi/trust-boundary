//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
)

// corpusPinsDomain labels every manifest pin. Each pin is
// sha256(domain "/" kind NUL compact-JSON(document)), so a pin never equals a file digest,
// a template digest or another pin.
const corpusPinsDomain = "tbound-corpus-pin/v1"

var pinDigestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// The four documents below are what the manifest pins bind. Every value in them exists before
// the corpus digest does: none of them reads the corpus manifest, the corpus-mode service
// config (S3'), the managed-root registration, the template digest TD or the signed host
// profile (S4, S4'). That order is what removes the circularity of BL-4: the corpus digest
// is pinned by the service config, the config by the registration, the registration by the
// template, the template by the host profile, so a manifest pin may bind none of those.

// environmentPinDoc is what the cell runs in: the signed image, the entrypoint and the shell it
// starts, the seccomp profile, the Landlock ABI and ruleset, the Pi runtime bundle and the
// host Node. All are measured in the signing session before the template is signed.
type environmentPinDoc struct {
	ImageManifestDigest     string `json:"image_manifest_digest"`
	EntrypointDigest        string `json:"entrypoint_digest"`
	CommandExecutableDigest string `json:"command_executable_digest"`
	SeccompProfileDigest    string `json:"seccomp_profile_digest"`
	LandlockABI             int    `json:"landlock_abi"`
	LandlockRulesetDigest   string `json:"landlock_ruleset_digest"`
	RuntimeBundleDigest     string `json:"runtime_bundle_digest"`
	NodeExecutableDigest    string `json:"node_executable_digest"`
}

// modelPinDoc is the provider route profile. The engineering route (OpenCode Go) is the
// endpoint and the approved model of the code; the research route is the digest of the frozen
// research profile.
type modelPinDoc struct {
	Route                 string `json:"route"`
	Endpoint              string `json:"endpoint,omitempty"`
	Model                 string `json:"model,omitempty"`
	ResearchProfileDigest string `json:"research_profile_digest,omitempty"`
}

// profilePinDoc is the base cell template: the digest of the measured D09 cell template with
// its managed_live_root_registration_digest member emptied. That member is the only one that
// follows the registration, so it is the one that makes TD move at D6; the rest is the
// containment profile (tools, uid mapping, xattr profile, cgroup layout, limits).
type profilePinDoc struct {
	BaseTemplateDigest string `json:"base_template_digest"`
}

// policyPinDoc is the policy the service enforces: the production gate profile and the two
// admission policy digests that run.json carries as admission_policy.
type policyPinDoc struct {
	GateProfileDigest    string `json:"gate_profile_digest"`
	SessionPolicyDigest  string `json:"session_policy_digest"`
	MetadataPolicyDigest string `json:"metadata_policy_digest"`
}

func corpusPin(kind string, doc any) (string, error) {
	body, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(corpusPinsDomain+"/"+kind+"\x00"), body...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

const (
	pinRouteEngineering = "engineering"
	pinRouteResearch    = "research"
)

// runCorpusPins prints the four manifest pins as the JSON of evalcorpus.Pins (without the
// attacker digest, which build.py owns).
func runCorpusPins(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile corpus-pins", flag.ContinueOnError)
	flags.SetOutput(stderr)
	templatePath := flags.String("template", "", "measured D09 cell template JSON (d09-cell.json); its registration digest is ignored")
	bundle := flags.String("runtime-bundle-digest", "", "Pi runtime bundle digest (BUNDLE_DIGEST, tbound-profile runtime-bundle-digest)")
	node := flags.String("node-digest", "", "digest of the host Node executable (the host profile ExecutableDigest)")
	route := flags.String("provider-route", pinRouteEngineering, "provider route: engineering or research")
	researchProfile := flags.String("research-profile", "", "frozen research profile JSON (required for, and only for, --provider-route research)")
	manifestPath := flags.String("manifest", "", "optional corpus manifest whose four pins must equal the computed ones")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *templatePath == "" {
		return errors.New("--template is required")
	}
	for name, value := range map[string]string{"--runtime-bundle-digest": *bundle, "--node-digest": *node} {
		if !pinDigestRE.MatchString(value) {
			return fmt.Errorf("%s must be sha256:<64 lowercase hex>", name)
		}
	}
	raw, err := os.ReadFile(*templatePath)
	if err != nil {
		return fmt.Errorf("read D09 cell template: %w", err)
	}
	template, err := piruntime.DecodeD09CellTemplate(raw)
	if err != nil {
		return err
	}
	model, err := modelPinFor(*route, *researchProfile)
	if err != nil {
		return err
	}
	policy, err := gate.NewPolicy(gate.ProductionProfile())
	if err != nil {
		return err
	}
	baseDigest, err := baseTemplateDigest(template)
	if err != nil {
		return err
	}
	var pins evalcorpus.Pins
	for _, p := range []struct {
		dst  *string
		kind string
		doc  any
	}{
		{&pins.EnvironmentDigest, "environment", environmentPinDoc{
			ImageManifestDigest: template.ExpectedImageDigest, EntrypointDigest: template.EntrypointDigest,
			CommandExecutableDigest: template.CommandExecutableDigest, SeccompProfileDigest: template.SeccompProfileDigest,
			LandlockABI: template.LandlockABI, LandlockRulesetDigest: template.LandlockRulesetDigest,
			RuntimeBundleDigest: *bundle, NodeExecutableDigest: *node}},
		{&pins.ModelDigest, "model", model},
		{&pins.ProfileDigest, "profile", profilePinDoc{BaseTemplateDigest: baseDigest}},
		{&pins.PolicyDigest, "policy", policyPinDoc{GateProfileDigest: policy.Digest(),
			SessionPolicyDigest:  piruntime.DigestBytes([]byte(gate.ProductionSessionPolicyLabel)),
			MetadataPolicyDigest: piruntime.DigestBytes([]byte(gate.ProductionMetadataPolicyLabel))}},
	} {
		if *p.dst, err = corpusPin(p.kind, p.doc); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(pins)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", encoded); err != nil {
		return err
	}
	if *manifestPath == "" {
		return nil
	}
	return checkManifestPins(*manifestPath, pins)
}

func modelPinFor(route, researchProfile string) (modelPinDoc, error) {
	switch route {
	case pinRouteEngineering:
		if researchProfile != "" {
			return modelPinDoc{}, errors.New("--research-profile belongs to --provider-route research")
		}
		return modelPinDoc{Route: route, Endpoint: openrouter.Endpoint, Model: openrouter.ApprovedModel}, nil
	case pinRouteResearch:
		if researchProfile == "" {
			return modelPinDoc{}, errors.New("--provider-route research needs --research-profile")
		}
		digest, err := researchProfileDigest(researchProfile)
		if err != nil {
			return modelPinDoc{}, err
		}
		return modelPinDoc{Route: route, ResearchProfileDigest: digest}, nil
	}
	return modelPinDoc{}, fmt.Errorf("unknown --provider-route %q: use %q or %q", route, pinRouteEngineering, pinRouteResearch)
}

// checkManifestPins compares the four pins of a manifest with the computed ones and names the
// members that differ.
func checkManifestPins(path string, want evalcorpus.Pins) error {
	f, err := openRegularFile(path)
	if err != nil {
		return fmt.Errorf("corpus manifest: %w", err)
	}
	defer f.Close()
	manifest, err := evalcorpus.Load(f)
	if err != nil {
		return fmt.Errorf("corpus manifest refused: %w", err)
	}
	var differ []string
	for _, p := range []struct{ name, have, want string }{
		{"environment_digest", manifest.Pins.EnvironmentDigest, want.EnvironmentDigest},
		{"model_digest", manifest.Pins.ModelDigest, want.ModelDigest},
		{"profile_digest", manifest.Pins.ProfileDigest, want.ProfileDigest},
		{"policy_digest", manifest.Pins.PolicyDigest, want.PolicyDigest},
	} {
		if p.have != p.want {
			differ = append(differ, p.name)
		}
	}
	if len(differ) != 0 {
		return fmt.Errorf("manifest pins differ from the computed ones: %s", strings.Join(differ, ", "))
	}
	return nil
}
