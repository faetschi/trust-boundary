package cellimage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeRunner struct {
	out []byte
	err error
}

func (f fakeRunner) Run(context.Context, string, ...string) ([]byte, error) { return f.out, f.err }

func sample(correct bool, digest string) []byte {
	m := digest
	if !correct {
		m = "sha256:" + strings.Repeat("0", 64)
	}
	return []byte(`[{"critical":{"identity":{"docker-reference":"ghcr.io/example/cell"},"image":{"docker-manifest-digest":"` + m + `"}}}]`)
}

func baseOpts(r Runner) Options {
	return Options{
		ImageRef:       "ghcr.io/example/cell@sha256:" + strings.Repeat("a", 64),
		ExpectedDigest: "sha256:" + strings.Repeat("a", 64),
		PubKeyPath:     "/etc/tbound/cell.pub",
		Runner:         r,
	}
}

func TestVerifyMatchesDigest(t *testing.T) {
	good := "sha256:" + strings.Repeat("a", 64)
	v, err := Verify(context.Background(), baseOpts(fakeRunner{out: sample(true, good)}))
	if err != nil {
		t.Fatal(err)
	}
	if v.Digest != good || v.ImageRef == "" {
		t.Fatalf("unexpected verification: %+v", v)
	}
}

func TestVerifyFailsClosed(t *testing.T) {
	good := "sha256:" + strings.Repeat("a", 64)
	cases := map[string]Options{
		"missing image":   func() Options { o := baseOpts(fakeRunner{}); o.ImageRef = ""; return o }(),
		"bad digest":      func() Options { o := baseOpts(fakeRunner{}); o.ExpectedDigest = "sha256:xyz"; return o }(),
		"no pubkey":       func() Options { o := baseOpts(fakeRunner{}); o.PubKeyPath = ""; return o }(),
		"no runner":       func() Options { o := baseOpts(nil); return o }(),
		"cosign error":    baseOpts(fakeRunner{err: errors.New("boom")}),
		"digest mismatch": baseOpts(fakeRunner{out: sample(false, good)}),
		"empty output":    baseOpts(fakeRunner{out: []byte("[]")}),
	}
	for name, opts := range cases {
		if _, err := Verify(context.Background(), opts); err == nil {
			t.Fatalf("%s: expected failure", name)
		}
	}
}

func TestValidDigest(t *testing.T) {
	if !ValidDigest("sha256:" + strings.Repeat("a", 64)) {
		t.Fatal("valid digest rejected")
	}
	for _, bad := range []string{"", "sha256:", "sha256:" + strings.Repeat("A", 64), "sha256:xyz", strings.Repeat("a", 64)} {
		if ValidDigest(bad) {
			t.Fatalf("invalid digest accepted: %q", bad)
		}
	}
}
