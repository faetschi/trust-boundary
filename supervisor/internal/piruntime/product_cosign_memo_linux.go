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
)

// productCosignRuns counts cosign runs by the product signature check, so tests can prove the memo avoids them.
var productCosignRuns atomic.Int64

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
	key, identified := productCosignMemoKey(config, cosignBinary, requireImmutable)
	if identified {
		productCosignMemo.Lock()
		hit := productCosignMemo.key == key
		productCosignMemo.Unlock()
		if hit {
			return nil
		}
	}
	productCosignRuns.Add(1)
	if err := l.verifyOfflineSignature(ctx, config, cosignBinary, requireImmutable); err != nil {
		return err
	}
	if after, ok := productCosignMemoKey(config, cosignBinary, requireImmutable); identified && ok && after == key {
		productCosignMemo.Lock()
		productCosignMemo.key = key
		productCosignMemo.Unlock()
	}
	return nil
}

// productCosignMemoKey names every input of the offline verification: the cosign binary, the public key (registered digest
// and file identity), the expected manifest digest, the fixed flags, and the identity of the layout directories,
// oci-layout, index.json and every blob (which holds the signature). ok is false if any is unreadable or cosign is not
// an absolute path.
func productCosignMemoKey(config D09Config, cosignBinary string, requireImmutable bool) (string, bool) {
	if !filepath.IsAbs(cosignBinary) || !filepath.IsAbs(config.CosignPublicKeyPath) || !filepath.IsAbs(config.OCILayoutPath) {
		return "", false
	}
	var key strings.Builder
	fmt.Fprintf(&key, "%s\x00%s\x00%v\x00%s\x00--offline --insecure-ignore-tlog=true", config.ExpectedImageDigest, config.CosignPublicKeyDigest,
		requireImmutable, config.CosignPublicKeyPath)
	blobs := filepath.Join(config.OCILayoutPath, "blobs", "sha256")
	names := []string{cosignBinary, config.CosignPublicKeyPath, config.OCILayoutPath, filepath.Join(config.OCILayoutPath, "blobs"), blobs,
		filepath.Join(config.OCILayoutPath, "index.json"), filepath.Join(config.OCILayoutPath, "oci-layout")}
	entries, err := os.ReadDir(blobs)
	if err != nil {
		return "", false
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
	for _, name := range names {
		identity, ok := productFileIdentity(name)
		if !ok {
			// oci-layout is optional for a layout; its absence is part of the key.
			if filepath.Base(name) == "oci-layout" {
				identity = name + ":absent"
			} else {
				return "", false
			}
		}
		key.WriteString("\x00" + identity)
	}
	return key.String(), true
}
