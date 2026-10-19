//go:build linux

package main

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
)

// The G1 run, the route-probe run and the direct-runtime probe run each run under a config of their own, so
// under their own managed live root registration, cell template and signed host profile. The guest artifacts
// of those runs are therefore not bound to the pilot's profile_hash or containment_profile_digest but to what
// the runs share with the pilot (guestevidence.ProfileBinding): the frozen source, the base cell template
// (the template without its registration digest, and without a route probe), the cell image and the admin key
// that verifies the run's own signed profile. These helpers read those pins from run.json and build the binding
// from the run's own signed profile and template.

// baseTemplateDigest is the digest of a cell template with the members that differ between runs of one
// freeze emptied: the registration digest (it follows the service config) and the route probe. For the pilot's
// template it is the profile_digest pin's input and the base_template_digest run.json pin.
func baseTemplateDigest(t piruntime.D09CellTemplate) (string, error) {
	t.ManagedLiveRootRegistrationDigest = ""
	t.RouteProbe = nil
	return t.Digest()
}

// runBindingPins reads the expectations of a guest artifact from run.json. The freeze commit in the file must
// be --commit. Every pin a producer compares the run with must be there; none is taken from a flag.
func runBindingPins(runConfig, commit string) (guestevidence.RunBinding, error) {
	if runConfig == "" {
		return guestevidence.RunBinding{}, errors.New("this artifact needs --run-config: the expectations come from run.json, not from flags")
	}
	data, err := readBoundedFile(runConfig, maxGuestInputSize)
	if err != nil {
		return guestevidence.RunBinding{}, fmt.Errorf("--run-config: %w", err)
	}
	b, err := guestevidence.ParseRunBinding(data)
	if err != nil {
		return b, err
	}
	switch {
	case b.Commit != commit:
		return b, fmt.Errorf("run.json is frozen at %s, --commit is %s", b.Commit, commit)
	case !guestevidence.IsDigest(b.FrozenSourceDigest) || !guestevidence.IsDigest(b.BaseTemplateDigest) || !guestevidence.IsDigest(b.AdminPubkeyDigest) || !guestevidence.IsDigest(b.ImageDigest):
		return b, errors.New("run.json needs pins.frozen_source_digest, pins.base_template_digest, pins.admin_pubkey_digest and v_image_digest as sha256:<64 hex>")
	}
	return b, nil
}

// runProfileFiles names the signed host profile of a run, its signature and the admin public key, and the
// cell template the run used.
type runProfileFiles struct {
	template, hostProfile, hostProfileSig, hostProfileKey string
}

// verifyRunProfile verifies the signed host profile of a run under the admin public key the run pins. The key
// is read once: the bytes that hash to the pinned digest are the bytes that verify the profile.
func verifyRunProfile(env routeProbeEnv, f runProfileFiles, adminPubkeyDigest string) (piruntime.HostProfile, error) {
	key, err := env.readFile(f.hostProfileKey)
	if err != nil {
		return piruntime.HostProfile{}, fmt.Errorf("--host-profile-key: %w", err)
	}
	if got := piruntime.DigestBytes(key); got != adminPubkeyDigest {
		return piruntime.HostProfile{}, fmt.Errorf("--host-profile-key hashes to %s, the run pins admin_pubkey_digest %s: the profile is not verified under the admin key of the run", got, adminPubkeyDigest)
	}
	profileBytes, err := env.readFile(f.hostProfile)
	if err != nil {
		return piruntime.HostProfile{}, fmt.Errorf("--host-profile: %w", err)
	}
	signature, err := env.readFile(f.hostProfileSig)
	if err != nil {
		return piruntime.HostProfile{}, fmt.Errorf("--host-profile-signature: %w", err)
	}
	return piruntime.VerifyLinuxSignedHostProfile(profileBytes, signature, ed25519.PublicKey(key))
}

// bindRunProfile states how a run is tied to the pilot's pins, from the run's own verified signed profile and the
// cell template it used. The profile must name that template and hold the pinned source, and the template must
// be of the pinned base and image.
func bindRunProfile(profile piruntime.HostProfile, template piruntime.D09CellTemplate, pins guestevidence.RunBinding) (guestevidence.ProfileBinding, error) {
	var none guestevidence.ProfileBinding
	templateDigest, err := template.Digest()
	if err != nil {
		return none, err
	}
	base, err := baseTemplateDigest(template)
	if err != nil {
		return none, err
	}
	switch {
	case profile.ContainmentProfileDigest != templateDigest:
		return none, fmt.Errorf("the profile names containment template %s, the run's template is %s", profile.ContainmentProfileDigest, templateDigest)
	case profile.SourceProvenanceDigest != pins.FrozenSourceDigest:
		return none, fmt.Errorf("the profile of the run holds source %s, the run pins frozen_source_digest %s", profile.SourceProvenanceDigest, pins.FrozenSourceDigest)
	case base != pins.BaseTemplateDigest:
		return none, fmt.Errorf("the run's cell template has base %s, the run pins base_template_digest %s: it is not the pilot's cell", base, pins.BaseTemplateDigest)
	case template.ExpectedImageDigest != pins.ImageDigest:
		return none, fmt.Errorf("the run's cell template names image %s, the run pins v_image_digest %s", template.ExpectedImageDigest, pins.ImageDigest)
	}
	return guestevidence.ProfileBinding{ProfileDigest: profile.Digest, TemplateDigest: templateDigest, BaseTemplateDigest: base,
		RegistrationDigest: template.ManagedLiveRootRegistrationDigest, ImageDigest: template.ExpectedImageDigest,
		SourceDigest: profile.SourceProvenanceDigest, AdminPubkeyDigest: pins.AdminPubkeyDigest}, nil
}

// bindRunFromFiles verifies the signed profile in the files under the pinned admin key, requires it to be the
// profile of the run (wantProfile: the digest the receipt or the journal names), loads the run's template and
// states the binding.
func bindRunFromFiles(env routeProbeEnv, f runProfileFiles, pins guestevidence.RunBinding, wantProfile string) (guestevidence.ProfileBinding, error) {
	var none guestevidence.ProfileBinding
	profile, err := verifyRunProfile(env, f, pins.AdminPubkeyDigest)
	if err != nil {
		return none, fmt.Errorf("the signed host profile of the run: %w", err)
	}
	if profile.Digest != wantProfile {
		return none, fmt.Errorf("the signed host profile given has digest %s, the run's profile is %s: not the profile the service ran", profile.Digest, wantProfile)
	}
	template, _, err := loadTemplateFile(env, f.template)
	if err != nil {
		return none, fmt.Errorf("--template: %w", err)
	}
	return bindRunProfile(profile, template, pins)
}

// providerExchangeBound reads the response line of a G1 run and binds the run's own signed profile and template to
// the pins.
func providerExchangeBound(response string, f runProfileFiles, pins guestevidence.RunBinding, env routeProbeEnv) (guestevidence.ProviderExchange, error) {
	exchange, err := providerExchangeFrom(response)
	if err != nil {
		return exchange, err
	}
	identity, err := guestevidence.RunIdentityFromResponse(exchange.Response)
	if err != nil {
		return exchange, err
	}
	exchange.Binding, err = bindRunFromFiles(env, f, pins, identity.ProfileHash)
	return exchange, err
}
