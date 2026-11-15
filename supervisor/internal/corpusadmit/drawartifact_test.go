package corpusadmit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/evalcorpus"
)

func drawVector(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "evalcorpus", "testdata", "d48", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func drawFS(raw []byte) fstest.MapFS {
	name, _ := DrawArtifactName(evalcorpus.DrawDigest(raw))
	return fstest.MapFS{name: &fstest.MapFile{Data: raw}}
}

// The profiles resolve their quotas from the pinned draw: the research pilot gets the ledger-v3 split, the thesis
// profile refuses a manifest without a draw, and a pre-D48 pilot manifest keeps the rehearsal counts.
func TestQuotasForResolvesThePinnedDraw(t *testing.T) {
	pilot, main := drawVector(t, "pilot-output.json"), drawVector(t, "draw-output.json")
	m := evalcorpus.Manifest{Pins: evalcorpus.Pins{DrawDigest: evalcorpus.DrawDigest(pilot)}}
	q, err := QuotasFor(ProfilePilot, m, drawFS(pilot))
	if err != nil || q.Adversarial[evalcorpus.ClassTMA] != 9 || q.Adversarial[evalcorpus.ClassTMB] != 2 || q.Draw == nil {
		t.Fatalf("research pilot quotas %+v: %v", q.Adversarial, err)
	}
	if _, err := QuotasFor(ProfileThesis, m, drawFS(pilot)); err == nil {
		t.Fatal("the thesis profile accepted a pilot draw")
	}
	if _, err := QuotasFor(ProfileThesis, evalcorpus.Manifest{}, drawFS(main)); err == nil || !strings.Contains(err.Error(), "D48 main draw") {
		t.Fatalf("the thesis profile accepted a manifest without a pinned draw: %v", err)
	}
	if q, err := QuotasFor(ProfilePilot, evalcorpus.Manifest{}, nil); err != nil || q.Draw != nil || q.Adversarial[evalcorpus.ClassTMA] != 7 {
		t.Fatalf("pre-D48 pilot quotas %+v: %v", q, err)
	}
	thesis := evalcorpus.Manifest{Pins: evalcorpus.Pins{DrawDigest: evalcorpus.DrawDigest(main)}}
	if q, err := QuotasFor(ProfileThesis, thesis, drawFS(main)); err != nil || q.Adversarial[evalcorpus.ClassTMA]+q.Adversarial[evalcorpus.ClassTMB] != 304 {
		t.Fatalf("thesis quotas %+v: %v", q, err)
	}

	tampered := append([]byte(nil), pilot...)
	tampered[len(tampered)-2] = ' '
	name, _ := DrawArtifactName(m.Pins.DrawDigest)
	for label, fsys := range map[string]fstest.MapFS{
		"tampered": {name: &fstest.MapFile{Data: tampered}},
		"missing":  {},
	} {
		if _, err := QuotasFor(ProfilePilot, m, fsys); err == nil {
			t.Errorf("%s draw file accepted", label)
		}
	}
	if _, err := QuotasFor(ProfilePilot, m, nil); err == nil {
		t.Error("a pinned draw without an artifact directory was accepted")
	}
	if _, err := QuotasFor("full", m, drawFS(pilot)); err == nil {
		t.Error("an unknown profile was accepted")
	}
}
