//go:build linux && tboundreplay

package workflowrecovery

import (
	"fmt"
	"os"
)

// ReplayRecordedHostIdentities lets a replay build verify the files of an exported guest state as
// the guest's own. Their device- and inode-bound identities are those of this host's copies, so the
// protected root at rootPath and each listed owner store report the identity their records hold,
// after the same file checks the measurement runs. Every other path is measured as usual.
func ReplayRecordedHostIdentities(rootPath, rootIdentity string, stores map[string]string) {
	measureProtectedRootIdentity = func(path, ownerStoreRoot string) (string, error) {
		if _, err := protectedRootIdentity(path, ownerStoreRoot); err != nil {
			return "", err
		}
		if path != rootPath {
			return "", fmt.Errorf("%w: replay has no recorded identity for protected root %s", ErrOwnerStore, path)
		}
		return rootIdentity, nil
	}
	measureSQLiteStorageIdentity = func(path string, info os.FileInfo) (string, error) {
		measured, err := computeSQLiteStorageIdentity(path, info)
		if err != nil {
			return "", err
		}
		if recorded, ok := stores[path]; ok {
			return recorded, nil
		}
		return measured, nil
	}
}
