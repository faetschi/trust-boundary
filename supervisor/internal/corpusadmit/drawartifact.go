package corpusadmit

import (
	"errors"
	"fmt"
	"io/fs"

	"tbound/supervisor/internal/evalcorpus"
)

// The corpus quota profiles, as the service config and the tools name them.
const (
	ProfileThesis = "thesis"
	ProfilePilot  = "pilot"
)

// DrawArtifactName is the file name of the D48 draw output whose digest is digest: draw-<64 lowercase hex>.json.
func DrawArtifactName(digest string) (string, bool) { return artifactName("draw-", digest) }

// LoadDraw reads the draw output of digest from fsys, the corpus artifact directory it is installed with (D39), and
// returns it only if its bytes digest to exactly digest and it is a complete registered draw (evalcorpus.ParseDraw).
func LoadDraw(fsys fs.FS, digest string) (evalcorpus.Draw, error) {
	name, ok := DrawArtifactName(digest)
	if !ok {
		return evalcorpus.Draw{}, fmt.Errorf("draw digest %q is not sha256 followed by 64 lowercase hex digits", digest)
	}
	if fsys == nil {
		return evalcorpus.Draw{}, fmt.Errorf("artifact %s: no artifact directory", name)
	}
	raw, err := readArtifactMax(fsys, name, digest, evalcorpus.MaxDrawBytes)
	if err != nil {
		return evalcorpus.Draw{}, err
	}
	d, err := evalcorpus.ParseDraw(raw)
	if err != nil {
		return evalcorpus.Draw{}, fmt.Errorf("artifact %s: %w", name, err)
	}
	return d, nil
}

// QuotasFor returns the quotas a manifest is validated under in a profile. The thesis profile takes them from the
// main draw the manifest pins (D48, D53 item 4) and refuses a manifest that pins none. The pilot profile takes the
// research pilot's adversarial split from the pinned pilot draw; a pilot manifest that pins no draw is a pre-D48
// rehearsal or screening carrier and keeps evalcorpus.PilotQuotas.
func QuotasFor(profile string, m evalcorpus.Manifest, artifacts fs.FS) (evalcorpus.Quotas, error) {
	switch profile {
	case ProfileThesis:
		if m.Pins.DrawDigest == "" {
			return evalcorpus.Quotas{}, errors.New("a thesis manifest must pin its D48 main draw (pins.draw_digest)")
		}
		d, err := LoadDraw(artifacts, m.Pins.DrawDigest)
		if err != nil {
			return evalcorpus.Quotas{}, fmt.Errorf("pins.draw_digest: %w", err)
		}
		return evalcorpus.ThesisQuotasFromDraw(d, m)
	case ProfilePilot:
		if m.Pins.DrawDigest == "" {
			return evalcorpus.PilotQuotas(), nil
		}
		d, err := LoadDraw(artifacts, m.Pins.DrawDigest)
		if err != nil {
			return evalcorpus.Quotas{}, fmt.Errorf("pins.draw_digest: %w", err)
		}
		return evalcorpus.PilotQuotasFromDraw(d)
	}
	return evalcorpus.Quotas{}, fmt.Errorf("unknown quota profile %q: use %q or %q", profile, ProfileThesis, ProfilePilot)
}
