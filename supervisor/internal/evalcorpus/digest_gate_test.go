package evalcorpus

import "testing"

// TestPilotManifestDigestsAreUnchanged pins the canonical length, Digest and ContentDigest of every committed
// pilot manifest. The values were recorded from the code before the optional sample_part, mechanism_id and
// derived members were added (D48 stage 1); the two drafts were re-pinned for the D50 token budgets and again for the 11-call compliant and ambiguous budgets (provenance unchanged). A new member that is written when absent moves these digests, so
// this gate must pass both before and after that change. Digest is the SHA-256 of the canonical bytes.
func TestPilotManifestDigestsAreUnchanged(t *testing.T) {
	pins := []struct {
		path      string
		canonical int
		digest    string
		content   string
	}{
		{
			path:      "../../../corpus/pilot/manifest.draft.json",
			canonical: 139288,
			digest:    "sha256:abdbd9b9375b0965fd631bf2e330f40f9d97e321b73cb95d5fdd6709fa6cdb9a",
			content:   "sha256:65f3b594fdd73ffd73e7abe50709df388f31eb7ad79707f5bef015a8bdf0d378",
		},
		{
			path:      "../../../corpus/pilot/provenance-2026-10-29/manifest.draft.json",
			canonical: 139266,
			digest:    "sha256:f7b5eb238bba2c1aee3508e1ad841034b449a507018ddd6edff63e19d3961edd",
			content:   "sha256:0a94f507a2b1f515d9d780be96946395ab232d84e95463d3506d8de1ec8af922",
		},
		{
			path:      "../../../corpus/pilot/t5-2026-10-29/manifest.draft.json",
			canonical: 144471,
			digest:    "sha256:dde22c458b6166a605c520c2e634eeae01d28889886270944c567afd1ce7c6f2",
			content:   "sha256:050f79e1715ec97a382dc9ad83f502cb411e3ad1d789925097cdc703f1f5433c",
		},
	}
	for _, p := range pins {
		t.Run(p.path, func(t *testing.T) {
			m := loadManifestFileForDigest(t, p.path)
			canon, err := CanonicalJSON(m)
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			if len(canon) != p.canonical {
				t.Errorf("canonical length = %d; want %d", len(canon), p.canonical)
			}
			digest, err := Digest(m)
			if err != nil {
				t.Fatalf("Digest: %v", err)
			}
			if digest != p.digest {
				t.Errorf("Digest = %s; want %s", digest, p.digest)
			}
			content, err := ContentDigest(m)
			if err != nil {
				t.Fatalf("ContentDigest: %v", err)
			}
			if content != p.content {
				t.Errorf("ContentDigest = %s; want %s", content, p.content)
			}
		})
	}
}
