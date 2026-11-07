//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// productCosignMemoFixture is a probe-style launcher over completeD09Host with an absolute fake cosign binary, so the
// memo can identify it. host selects a verified product host (true) or the thesis launcher (false).
func productCosignMemoFixture(t *testing.T, host bool) (*D09PodmanCrunLauncher, D09Config, *fakeD09Runner, string) {
	t.Helper()
	productCosignMemo.Lock()
	productCosignMemo.key = ""
	productCosignMemo.Unlock()
	config, runner := completeD09Host(t)
	cosign := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(cosign, []byte("cosign"), 0o700); err != nil {
		t.Fatal(err)
	}
	verify := " verify --offline --insecure-ignore-tlog=true --key " + config.CosignPublicKeyPath + " --local-image " + config.OCILayoutPath
	runner.results[cosign+verify] = runner.results["cosign"+verify]
	launcher := newTestLauncher(config, runner)
	waitUntilSettled()
	if host {
		launcher.productHost = &D09ProductCommandHost{loaded: true}
	}
	return launcher, config, runner, cosign
}

func cosignCalls(runner *fakeD09Runner, cosign string) (count int) {
	for _, call := range runner.calls {
		if call[0] == cosign {
			count++
		}
	}
	return count
}

func init() { productMemoMinAge = 100 * time.Millisecond }

// waitUntilSettled waits until files touched now are older than productMemoMinAge, so a verification may memoize them.
func waitUntilSettled() { time.Sleep(productMemoMinAge + 50*time.Millisecond) }

// rewriteKeepingMtime changes only ctime (and the bytes, if mutate changes them) while keeping inode, size and mtime.
func rewriteKeepingMtime(t *testing.T, name string, mutate func([]byte)) {
	t.Helper()
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	mutate(data)
	file, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, time.Now(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	waitUntilSettled()
}

func TestProductProbeCosignMemoRunsCosignOnce(t *testing.T) {
	launcher, config, runner, cosign := productCosignMemoFixture(t, true)
	for i := 0; i < 3; i++ {
		if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := cosignCalls(runner, cosign); got != 1 {
		t.Fatalf("cosign ran %d times for three product probes, want 1", got)
	}
}

func TestProductProbeCosignMemoRerunsOnAnyInputChange(t *testing.T) {
	launcher, config, runner, cosign := productCosignMemoFixture(t, true)
	verify := func() error {
		return launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, false)
	}
	if err := verify(); err != nil {
		t.Fatal(err)
	}
	blobs := filepath.Join(config.OCILayoutPath, "blobs", "sha256")
	entries, err := os.ReadDir(blobs)
	if err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"layout blob, ctime only":   filepath.Join(blobs, entries[0].Name()),
		"index.json, ctime only":    filepath.Join(config.OCILayoutPath, "index.json"),
		"cosign binary, ctime only": cosign,
	} {
		before := cosignCalls(runner, cosign)
		rewriteKeepingMtime(t, target, func([]byte) {})
		if err := verify(); err != nil || cosignCalls(runner, cosign) != before+1 {
			t.Fatalf("%s: cosign did not rerun after an identity change: %v", name, err)
		}
		if err := verify(); err != nil || cosignCalls(runner, cosign) != before+1 {
			t.Fatalf("%s: the re-verified result was not memoized again: %v", name, err)
		}
	}
	// A new blob in the layout (an added signature) is a change too.
	before := cosignCalls(runner, cosign)
	if err := os.WriteFile(filepath.Join(blobs, strings.Repeat("0", 64)), []byte("sig"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verify(); err != nil || cosignCalls(runner, cosign) != before+1 {
		t.Fatalf("an added layout blob did not trigger cosign: %v", err)
	}
	// A tampered public key (same size, same mtime) is refused, not answered from the memo.
	rewriteKeepingMtime(t, config.CosignPublicKeyPath, func(data []byte) { data[0] ^= 0xff })
	if err := verify(); err == nil {
		t.Fatal("tampered cosign public key accepted from the memo")
	}
	rewriteKeepingMtime(t, config.CosignPublicKeyPath, func(data []byte) { data[0] ^= 0xff })
	if err := verify(); err != nil {
		t.Fatal(err)
	}
	// A tampered manifest blob (same size, same mtime) is refused too.
	rewriteKeepingMtime(t, filepath.Join(blobs, strings.TrimPrefix(config.ExpectedImageDigest, "sha256:")), func(data []byte) { data[len(data)/2] ^= 0x01 })
	if err := verify(); err == nil {
		t.Fatal("tampered manifest accepted from the memo")
	}
}

func TestProductProbeCosignFreshFilesAreNotMemoized(t *testing.T) {
	launcher, config, runner, cosign := productCosignMemoFixture(t, true)
	defer func(old time.Duration) { productMemoMinAge = old }(productMemoMinAge)
	productMemoMinAge = time.Hour
	for i := 1; i <= 2; i++ {
		if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, false); err != nil {
			t.Fatal(err)
		}
		if got := cosignCalls(runner, cosign); got != i {
			t.Fatalf("cosign ran %d times after %d probes of files newer than the threshold, want %d", got, i, i)
		}
	}
}

func TestProductProbeCosignFailureIsNotMemoized(t *testing.T) {
	launcher, config, runner, cosign := productCosignMemoFixture(t, true)
	key := cosign + " verify --offline --insecure-ignore-tlog=true --key " + config.CosignPublicKeyPath + " --local-image " + config.OCILayoutPath
	good := runner.results[key]
	runner.results[key] = D09CommandResult{ExitCode: 1, Stderr: []byte("signature mismatch")}
	for i := 1; i <= 2; i++ {
		if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, false); err == nil {
			t.Fatal("failed cosign accepted")
		}
		if got := cosignCalls(runner, cosign); got != i {
			t.Fatalf("after failure %d cosign ran %d times: a failure was memoized", i, got)
		}
	}
	runner.results[key] = good
	if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, false); err != nil {
		t.Fatal(err)
	}
	runner.results[key] = D09CommandResult{ExitCode: 1}
	if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, false); err != nil {
		t.Fatalf("an unchanged verified input was re-run: %v", err)
	}
}

func TestThesisProbeCosignAndImageScansRunEveryTime(t *testing.T) {
	launcher, config, runner, cosign := productCosignMemoFixture(t, false)
	for i := 1; i <= 3; i++ {
		if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, false); err != nil {
			t.Fatal(err)
		}
		if got := cosignCalls(runner, cosign); got != i {
			t.Fatalf("thesis cosign ran %d times after %d probes", got, i)
		}
	}
	productCosignMemo.Lock()
	memoized := productCosignMemo.key != ""
	productCosignMemo.Unlock()
	if memoized {
		t.Fatal("thesis launcher wrote the product cosign memo")
	}
	// The entrypoint check uses the thesis reader: it never touches the product reader or memo, and sees a ctime-only
	// layer tamper at once.
	layout, manifest, _ := productOCIMemoTestLayout(t)
	imageConfig := D09Config{OCILayoutPath: layout, ExpectedImageDigest: manifest, EntrypointPath: "/entrypoint", EntrypointDigest: DigestBytes([]byte("entry"))}
	thesis := &D09PodmanCrunLauncher{}
	reads := productOCILayerReads.Load()
	for i := 0; i < 2; i++ {
		if err := thesis.verifyEntrypointInImage(context.Background(), imageConfig); err != nil {
			t.Fatal(err)
		}
	}
	productOCIMemo.Lock()
	memoFiles := len(productOCIMemo.files)
	productOCIMemo.Unlock()
	if productOCILayerReads.Load() != reads || memoFiles != 0 {
		t.Fatal("thesis entrypoint check used the product reader or memo")
	}
	layers, err := ociManifestLayers(layout, manifest, false)
	if err != nil {
		t.Fatal(err)
	}
	rewriteKeepingMtime(t, filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(layers[len(layers)-1].Digest, "sha256:")), func(data []byte) { data[len(data)/2] ^= 0xff })
	if err := thesis.verifyEntrypointInImage(context.Background(), imageConfig); err == nil {
		t.Fatal("thesis entrypoint check accepted a tampered layer")
	}
}

func TestProductProbeEntrypointReusesImageFileMemo(t *testing.T) {
	layout, manifest, paths := productOCIMemoTestLayout(t)
	imageConfig := D09Config{OCILayoutPath: layout, ExpectedImageDigest: manifest, EntrypointPath: "/entrypoint", EntrypointDigest: DigestBytes([]byte("entry"))}
	launcher := &D09PodmanCrunLauncher{productHost: &D09ProductCommandHost{loaded: true}}
	// The host's three-path verification primes the memo; the probe's entrypoint and Node checks then scan nothing.
	if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths, false); err != nil {
		t.Fatal(err)
	}
	reads := productOCILayerReads.Load()
	for i := 0; i < 2; i++ {
		if err := launcher.verifyEntrypointInImage(context.Background(), imageConfig); err != nil {
			t.Fatal(err)
		}
		if err := launcher.verifyImageFile(context.Background(), imageConfig, "/usr/bin/node", DigestBytes([]byte("node")), "node"); err != nil {
			t.Fatal(err)
		}
	}
	if got := productOCILayerReads.Load() - reads; got != 0 {
		t.Fatalf("product probe rescanned layers (%d reads)", got)
	}
	// A wrong expected digest is still refused from the memo, and a ctime-only layer tamper is rescanned and refused.
	bad := imageConfig
	bad.EntrypointDigest = DigestBytes([]byte("other"))
	if err := launcher.verifyEntrypointInImage(context.Background(), bad); err == nil {
		t.Fatal("wrong entrypoint digest accepted")
	}
	layers, err := ociManifestLayers(layout, manifest, false)
	if err != nil {
		t.Fatal(err)
	}
	rewriteKeepingMtime(t, filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(layers[len(layers)-1].Digest, "sha256:")), func(data []byte) { data[len(data)/2] ^= 0xff })
	if err := launcher.verifyEntrypointInImage(context.Background(), imageConfig); err == nil || productOCILayerReads.Load() == reads {
		t.Fatalf("ctime-only layer tamper not rescanned and refused: %v", err)
	}
}

func TestProductProbeCosignMemoHitRechecksReadOnlyMount(t *testing.T) {
	launcher, config, runner, cosign := productCosignMemoFixture(t, true)
	key, ok, _ := productCosignMemoKey(config, cosign, true)
	if !ok {
		t.Fatal("memo key not identified")
	}
	productCosignMemo.Lock()
	productCosignMemo.key = key
	productCosignMemo.Unlock()
	defer func(old func(string) error) { productLayoutMountCheck = old }(productLayoutMountCheck)
	productLayoutMountCheck = func(string) error { return errors.New("mount is writable") }
	if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, true); err == nil || !strings.Contains(err.Error(), "read-only mount") {
		t.Fatalf("primed memo with a writable layout mount: err = %v, want refusal", err)
	}
	productLayoutMountCheck = func(string) error { return nil }
	if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, true); err != nil {
		t.Fatalf("read-only mount memo hit: %v", err)
	}
	if got := cosignCalls(runner, cosign); got != 0 {
		t.Fatalf("cosign ran %d times on memo hits", got)
	}
}

// A file fresh at pre-snapshot time is never memoized, even if verification stalls past the threshold after the snapshot.
func TestProductProbeCosignStalledScanIsNotMemoized(t *testing.T) {
	launcher, config, runner, cosign := productCosignMemoFixture(t, true)
	now := time.Now()
	if err := os.Chtimes(config.CosignPublicKeyPath, now, now); err != nil {
		t.Fatal(err)
	}
	productAfterSnapshot = waitUntilSettled
	defer func() { productAfterSnapshot = nil }()
	for i := 1; i <= 2; i++ {
		if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, false); err != nil {
			t.Fatal(err)
		}
		if got := cosignCalls(runner, cosign); got != i {
			t.Fatalf("cosign ran %d times after %d probes with a key fresh at snapshot time, want %d", got, i, i)
		}
	}
}

// Inputs that are settled before the first call are memoized even if verification stalls after the snapshot.
func TestProductProbeCosignSettledFilesAreMemoizedAfterStall(t *testing.T) {
	launcher, config, runner, cosign := productCosignMemoFixture(t, true)
	productAfterSnapshot = waitUntilSettled
	defer func() { productAfterSnapshot = nil }()
	for i := 0; i < 2; i++ {
		if err := launcher.verifyOfflineSignatureForProbe(context.Background(), config, cosign, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := cosignCalls(runner, cosign); got != 1 {
		t.Fatalf("cosign ran %d times for settled inputs, want 1", got)
	}
}
