//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTrialBinaryDigestRunningExecutable(t *testing.T) {
	if want := os.Getenv("TBOUND_TEST_RUNNING_BINARY_DIGEST"); want != "" {
		path, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		replacement := path + ".new"
		if err := os.WriteFile(replacement, []byte("replacement installed after process start"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		got, err := trialBinaryDigest()
		if err != nil || got != want {
			t.Fatalf("digest after atomic install = %q, %v; want running binary %q", got, err, want)
		}
		return
	}

	// Run a private copy so the test can replace its pathname without touching the test runner.
	src, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), "tbound.test")
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(dst, hash), src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy test binary: %v, %v", copyErr, closeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-test.run=^TestTrialBinaryDigestRunningExecutable$", "-test.v")
	cmd.Env = append(os.Environ(), "TBOUND_TEST_RUNNING_BINARY_DIGEST=sha256:"+hex.EncodeToString(hash.Sum(nil)))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh process: %v\n%s", err, out)
	}
}
