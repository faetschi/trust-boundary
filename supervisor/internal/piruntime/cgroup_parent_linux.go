//go:build linux

package piruntime

import (
	"fmt"
	"path/filepath"
	"strings"
)

// d09PodmanCgroupParent converts the host path of the held cgroup scope into the form
// podman's cgroupfs manager takes for --cgroup-parent: relative to the cgroup2 root, with
// a leading slash. podman and crun prepend /sys/fs/cgroup themselves, so the host path
// would be created as /sys/fs/cgroup/sys/fs/cgroup/.... A scope outside the mount is refused.
func d09PodmanCgroupParent(hostPath string) (string, error) {
	relative, found := strings.CutPrefix(hostPath, cgroup2MountPrefix)
	if !found || relative == "" || filepath.Clean(hostPath) != hostPath || strings.ContainsAny(relative, ",\x00\n") {
		return "", fmt.Errorf("%w: cgroup scope %q is not a clean path below %s", ErrCgroupScopeRefused, hostPath, cgroup2MountPrefix)
	}
	return "/" + relative, nil
}

// d09PodmanCgroupParentOrSelf is for the argument builders, whose callers have already
// refused a scope that d09PodmanCgroupParent rejects.
func d09PodmanCgroupParentOrSelf(hostPath string) string {
	if parent, err := d09PodmanCgroupParent(hostPath); err == nil {
		return parent
	}
	return hostPath
}
