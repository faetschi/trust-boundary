package guestevidence

// A G1 run, a route-probe run and a direct-runtime probe run each need their own service config, managed
// live root registration and so their own cell template digest and signed host profile: the registration
// digest is a member of the template and the template digest is a member of the profile. None of them can
// therefore carry the pilot's profile_hash or containment_profile_digest. What they do share with the
// pilot is everything else, and that is what the artifacts are bound to: the frozen source, the base cell
// template (the template with its registration digest emptied, as `tbound-profile corpus-pins` and
// `template-digest` compute it), the cell image, the Pi runtime bundle of the signed profile, the binary that
// made the artifact (the envelope) and the admin key that verified the run's own signed profile and signed
// registration. Taking the registration out of the shared template hash also takes out its runner (the
// service executable), so the runner is pinned on its own: the run's signed registration must name the digest
// of the build of the frozen commit.

// ProfileBinding states how the run behind an artifact is tied to the pilot's pins. The producer fills it
// from the run's own signed host profile (verified under the admin key the run pins) and its own cell
// template; the validator compares it with run.json and with the facts of the run.
type ProfileBinding struct {
	// ProfileDigest is the digest of the run's own signed host profile.
	ProfileDigest string `json:"profile_digest"`
	// TemplateDigest is the digest of the cell template the run's profile names (ContainmentProfileDigest).
	TemplateDigest string `json:"template_digest"`
	// BaseTemplateDigest is that template with its registration digest emptied. It must equal
	// run.json pins.base_template_digest.
	BaseTemplateDigest string `json:"base_template_digest"`
	// RegistrationDigest is the managed live root registration of the run's own service config.
	RegistrationDigest string `json:"registration_digest"`
	// ImageDigest is the cell image the template names.
	ImageDigest string `json:"image_digest"`
	// SourceDigest is the profile's source provenance digest: the frozen source.
	SourceDigest string `json:"source_digest"`
	// AdminPubkeyDigest is the digest of the admin public key the profile was verified under.
	AdminPubkeyDigest string `json:"admin_pubkey_digest"`
	// RuntimeBundleDigest is the Pi runtime bundle the run's profile holds. It must equal run.json
	// pins.runtime_bundle_digest: a profile of another bundle is not the pilot's runtime.
	RuntimeBundleDigest string `json:"runtime_bundle_digest"`
	// RunnerDigest is the service executable the run's signed registration names (verified under the admin key,
	// and the registration's digest is the template's RegistrationDigest). It must equal run.json
	// pins.runner_digest, the digest of the build of the frozen commit.
	RunnerDigest string `json:"runner_digest"`
}

// bindingProblems compares a ProfileBinding with the run.json expectations and with the profile, template
// and image the artifact states for the run itself.
func bindingProblems(b ProfileBinding, e Expect, runProfile, runTemplate, runImage, runSource string, add func(format string, args ...any)) {
	for name, d := range map[string]string{"profile_digest": b.ProfileDigest, "template_digest": b.TemplateDigest, "base_template_digest": b.BaseTemplateDigest,
		"registration_digest": b.RegistrationDigest, "image_digest": b.ImageDigest, "source_digest": b.SourceDigest, "admin_pubkey_digest": b.AdminPubkeyDigest,
		"runtime_bundle_digest": b.RuntimeBundleDigest, "runner_digest": b.RunnerDigest} {
		if !IsDigest(d) {
			add("profile binding: %s %q is not a sha256 digest", name, d)
		}
	}
	if b.TemplateDigest == b.BaseTemplateDigest {
		add("profile binding: the run's template equals the base template: the run has no registration of its own")
	}
	pin := func(name, have, want string) {
		switch {
		case !IsDigest(want):
			add("run.json %s is required to bind the run's profile", name)
		case have != want:
			add("profile binding: %s is %q, the run pins %q", name, have, want)
		}
	}
	pin("pins.base_template_digest", b.BaseTemplateDigest, e.BaseTemplateDigest)
	pin("pins.frozen_source_digest", b.SourceDigest, e.FrozenSourceDigest)
	pin("pins.admin_pubkey_digest", b.AdminPubkeyDigest, e.AdminPubkeyDigest)
	pin("v_image_digest", b.ImageDigest, e.ImageDigest)
	pin("pins.runtime_bundle_digest", b.RuntimeBundleDigest, e.RuntimeBundleDigest)
	pin("pins.runner_digest", b.RunnerDigest, e.RunnerDigest)
	for _, c := range []struct{ name, have, want string }{
		{"profile", b.ProfileDigest, runProfile}, {"template", b.TemplateDigest, runTemplate},
		{"image", b.ImageDigest, runImage}, {"source", b.SourceDigest, runSource},
	} {
		if c.have != c.want {
			add("profile binding: the %s is %q, the run's own is %q", c.name, c.have, c.want)
		}
	}
}
