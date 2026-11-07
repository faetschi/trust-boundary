package evalcorpus

import "testing"

// TestPilotManifestDigestsAreUnchanged pins the canonical length, Digest and ContentDigest of every committed
// pilot manifest. The values were recorded from the code before the optional sample_part, mechanism_id and
// derived members were added (D48 stage 1). A new member that is written when absent moves these digests, so
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
			canonical: 139266,
			digest:    "sha256:f7b5eb238bba2c1aee3508e1ad841034b449a507018ddd6edff63e19d3961edd",
			content:   "sha256:0a94f507a2b1f515d9d780be96946395ab232d84e95463d3506d8de1ec8af922",
		},
		{
			path:      "../../../corpus/pilot/provenance-2026-10-29/manifest.draft.json",
			canonical: 139266,
			digest:    "sha256:f7b5eb238bba2c1aee3508e1ad841034b449a507018ddd6edff63e19d3961edd",
			content:   "sha256:0a94f507a2b1f515d9d780be96946395ab232d84e95463d3506d8de1ec8af922",
		},
		{
			path:      "../../../corpus/pilot/t5-2026-10-29/manifest.draft.json",
			canonical: 144449,
			digest:    "sha256:0b3c2d7bead5c954e98f9ee89ebea67958aa14ca30b8b1561f621cbcf0049cf6",
			content:   "sha256:d26999011961f2436cf94bdd40eeab82faae7004ad30c3a2c74c1130ec65c009",
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
