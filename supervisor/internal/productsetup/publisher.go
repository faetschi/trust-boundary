package productsetup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"tbound/supervisor/internal/releasetrust"
)

// authenticateSetupBinary admits the bytes setup would install. The running executable is already trusted,
// because root started it. Any other binary runs as root in the self-test and is kept as the last-known-good
// that a rollback runs, so it must be listed by a manifest signed under the publisher key this setup pins, or
// under the key already pinned when the config names none. Without either key setup refuses it.
func (h *host) authenticateSetupBinary(candidate Candidate, data, configuredKey []byte) error {
	if running, err := isRunningExecutable(data); err != nil {
		return err
	} else if running {
		return nil
	}
	var key ed25519.PublicKey
	var err error
	if configuredKey != nil {
		key, err = releasetrust.ParsePublicKey(configuredKey)
	} else {
		key, err = h.pinnedPublisherKey()
	}
	if errors.Is(err, os.ErrNotExist) {
		return refuse("%s is not the running tbound binary, and no publisher key is configured or pinned to authenticate it. Set release_public_key in the setup config and pass --manifest and --signature for the binary, or run setup from the tbound binary you want to install and omit --binary; nothing was changed", candidate.Binary)
	} else if err != nil {
		return err
	}
	if candidate.Manifest == "" || candidate.Signature == "" {
		return refuse("%s is not the running tbound binary, so the publisher key %s must authenticate it: pass --manifest and --signature (the files `tbound release manifest` and `tbound release sign` made), or run setup from the tbound binary you want to install and omit --binary; nothing was changed", candidate.Binary, releasetrust.Fingerprint(key))
	}
	manifest, err := h.verifiedManifest(candidate, key)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if err := manifest.CheckArtifact(filepath.Base(candidate.Binary), int64(len(data)), hex.EncodeToString(sum[:])); err != nil {
		return refuse("the candidate is refused: %v", err)
	}
	h.note("%s (sha256:%x) is listed in the verified manifest", filepath.Base(candidate.Binary), sum[:8])
	return nil
}

// isRunningExecutable reports whether data is the executable this process runs.
func isRunningExecutable(data []byte) (bool, error) {
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return false, refuse("cannot read the running tbound binary (/proc/self/exe), so --binary cannot be compared with it: %v", err)
	}
	defer self.Close()
	if info, err := self.Stat(); err != nil || info.Size() != int64(len(data)) {
		return false, err
	}
	running, err := io.ReadAll(io.LimitReader(self, maxBinaryBytes+1))
	return err == nil && bytes.Equal(running, data), err
}
