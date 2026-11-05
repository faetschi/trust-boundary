package productsetup

import "fmt"

// RenderProductContainersConf returns the containers.conf that CONTAINERS_CONF names for every product Podman child.
// cgroupfs is the manager that needs no user bus, because the unit unsets DBUS_SESSION_BUS_ADDRESS.
func RenderProductContainersConf() []byte {
	return []byte(`[engine]
cgroup_manager = "cgroupfs"
events_logger = "file"
runtime = "crun"
`)
}

// RenderProductStorageConf returns the storage.conf that CONTAINERS_STORAGE_CONF names. The graph and rootless storage
// paths are explicit, so Podman cannot fall back to its default store. ignore_chown_errors lets the flattened layers of
// the image load into the one-ID namespace, and the rootfs stays read-only.
func RenderProductStorageConf() []byte {
	return []byte(fmt.Sprintf(`[storage]
driver = "overlay"
graphroot = "%[1]s"
rootless_storage_path = "%[1]s"
runroot = "%[2]s"

[storage.options]
ignore_chown_errors = "true"
`, PodmanGraphRootDir, PodmanRunrootDir))
}
