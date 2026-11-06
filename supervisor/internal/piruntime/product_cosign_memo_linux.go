//go:build linux

package piruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// productCosignRuns counts cosign runs by the product signature check, so tests can prove the memo avoids them.
var productCosignRuns atomic.Int64

// productLayoutMountCheck is requireReadOnlyMount; tests replace it because a unit test cannot make a read-only mount.
var productLayoutMountCheck = requireReadOnlyMount

var productCosignMemo struct {
	sync.Mutex
	key string // the last offline verification that succeeded
}

// verifyOfflineSignatureForProbe is verifyOfflineSignature for a launcher: a verified product host memoizes a success,
// any other launcher (the thesis one) runs the full check every time.
func (l *D09PodmanCrunLauncher) verifyOfflineSignatureForProbe(ctx context.Context, config D09Config, cosignBinary string, requireImmutable bool) error {
	if !l.productHost.verified() {
		return l.verifyOfflineSignature(ctx, config, cosignBinary, requireImmutable)
	}
	return l.verifyOfflineSignatureMemoized(ctx, config, cosignBinary, requireImmutable)
}

// verifyOfflineSignatureMemoized skips the cosign run while every input it read is unchanged. Failures are never memoized,
// and a key that differs before and after the run is not stored.
func (l *D09PodmanCrunLauncher) verifyOfflineSignatureMemoized(ctx context.Context, config D09Config, cosignBinary string, requireImmutable bool) error {
	key, identified, _ := productCosignMemoKey(config, cosignBinary, requireImmutable)
	if identified {
		productCosignMemo.Lock()
		hit := productCosignMemo.key == key
		productCosignMemo.Unlock()
		if hit {
			// The uncached path also requires a read-only layout mount, which no file identity shows (a same-tree bind mount keeps them).
			if requireImmutable {
				if err := productLayoutMountCheck(config.OCILayoutPath); err != nil {
					return fmt.Errorf("local OCI image layout is not on a read-only mount: %w", err)
				}
			}
			return nil
		}
	}
	productCosignRuns.Add(1)
	if err := l.verifyOfflineSignature(ctx, config, cosignBinary, requireImmutable); err != nil {
		return err
	}
	if after, ok, settled := productCosignMemoKey(config, cosignBinary, requireImmutable); identified && ok && settled && after == key {
		productCosignMemo.Lock()
		productCosignMemo.key = key
		productCosignMemo.Unlock()
	}
	return nil
}

// productCosignMemoKey names every input of the offline verification: the cosign binary, the public key (registered digest
// and file identity), the expected manifest digest, the fixed flags, and the identity of the layout directories,
// oci-layout, index.json and every blob (which holds the signature). ok is false if any is unreadable or cosign is not
// an absolute path; settled is false if any ctime is too recent to trust (see productFilesSettled).
func productCosignMemoKey(config D09Config, cosignBinary string, requireImmutable bool) (key string, ok, settled bool) {
	if !filepath.IsAbs(cosignBinary) || !filepath.IsAbs(config.CosignPublicKeyPath) || !filepath.IsAbs(config.OCILayoutPath) {
		return "", false, false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\x00%s\x00%v\x00%s\x00--offline --insecure-ignore-tlog=true", config.ExpectedImageDigest, config.CosignPublicKeyDigest,
		requireImmutable, config.CosignPublicKeyPath)
	blobs := filepath.Join(config.OCILayoutPath, "blobs", "sha256")
	names := []string{cosignBinary, config.CosignPublicKeyPath, config.OCILayoutPath, filepath.Join(config.OCILayoutPath, "blobs"), blobs,
		filepath.Join(config.OCILayoutPath, "index.json"), filepath.Join(config.OCILayoutPath, "oci-layout")}
	entries, err := os.ReadDir(blobs)
	if err != nil {
		return "", false, false
	}
	for _, entry := range entries {
		names = append(names, filepath.Join(blobs, entry.Name()))
	}
	// A symlinked cosign or key is identified by its target too.
	for _, link := range []string{cosignBinary, config.CosignPublicKeyPath} {
		if target, err := filepath.EvalSymlinks(link); err == nil && target != link {
			names = append(names, target)
		}
	}
	now := time.Now() // before the stats: a file is settled only if it was already old enough when its identity was taken
	var newest time.Time
	for _, name := range names {
		identity, ctime, ok := productFileIdentity(name)
		if ctime.After(newest) {
			newest = ctime
		}
		if !ok {
			// oci-layout is optional for a layout; its absence is part of the key.
			if filepath.Base(name) == "oci-layout" {
				identity = name + ":absent"
			} else {
				return "", false, false
			}
		}
		b.WriteString("\x00" + identity)
	}
	return b.String(), true, productFilesSettled(now, newest)
}
