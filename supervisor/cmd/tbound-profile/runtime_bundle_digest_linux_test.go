//go:build linux

package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/piruntime"
)

func TestRuntimeBundleDigestRejectsInvalidRoots(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--root", "relative"}, {"--root", filepath.Join(t.TempDir(), "missing")},
		{"--root", t.TempDir(), "unexpected"},
	} {
		var out, stderr bytes.Buffer
		if err := runRuntimeBundleDigest(args, &out, &stderr); err == nil {
			t.Fatalf("accepted invalid arguments %q", args)
		}
		if out.Len() != 0 {
			t.Fatalf("printed a digest for invalid arguments %q", args)
		}
	}
}

func TestRuntimeBundleDigestRefusesWritableMount(t *testing.T) {
	var out, stderr bytes.Buffer
	err := run([]string{"runtime-bundle-digest", "--root", t.TempDir()}, &out, &stderr)
	if !errors.Is(err, piruntime.ErrRuntimeBundleRefused) {
		t.Fatalf("writable mount: got %v, want runtime bundle refusal", err)
	}
	if out.Len() != 0 {
		t.Fatal("printed a digest for an unverified tree")
	}
}
