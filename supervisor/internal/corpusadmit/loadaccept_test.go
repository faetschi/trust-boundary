package corpusadmit

import (
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/taskaccept"
)

func TestLoadAcceptReturnsTheSpecOfThePinnedDigest(t *testing.T) {
	digest := taskaccept.DigestOf([]byte(cmpAccept))
	name, _ := AcceptArtifactName(digest)
	spec, err := LoadAccept(fstest.MapFS{name: {Data: []byte(cmpAccept)}}, digest)
	if err != nil || spec.Digest() != digest {
		t.Fatalf("LoadAccept = %q, %v", spec.Digest(), err)
	}
}

func TestLoadAcceptRefusals(t *testing.T) {
	digest := taskaccept.DigestOf([]byte(cmpAccept))
	name, _ := AcceptArtifactName(digest)
	for label, tc := range map[string]struct {
		fsys   fstest.MapFS
		digest string
		want   string
	}{
		"bad digest":  {fstest.MapFS{}, "sha256:../x", "not sha256"},
		"missing":     {fstest.MapFS{}, digest, "accept-"},
		"other bytes": {fstest.MapFS{name: {Data: []byte(cmpWeak)}}, digest, "digests to"},
		"unparsable":  {fstest.MapFS{accName(`{}`): {Data: []byte(`{}`)}}, taskaccept.DigestOf([]byte(`{}`)), "acceptance spec"},
	} {
		if _, err := LoadAccept(tc.fsys, tc.digest); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", label, err, tc.want)
		}
	}
}

func accName(raw string) string {
	name, _ := AcceptArtifactName(taskaccept.DigestOf([]byte(raw)))
	return name
}
