//go:build linux

package piruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const D09UserNamespaceMappingProfile = "rootless-single-own-uid-gid/v1"
const d09UserNamespaceMappingDomain = "tbound-d09-user-namespace-map/v1\x00"

// D09IDMapRange is one exact /proc/PID/{uid,gid}_map line. ParentID is relative
// to the reader namespace: inside the container this is the immediate parent;
// host verification reads the same proc files from the registered initial user
// namespace and therefore sees host IDs.
type D09IDMapRange struct {
	ContainerID uint64
	ParentID    uint64
	Length      uint64
}

// DigestD09UserNamespaceMapping freezes both the in-container rootless mapping
// and its translation into the trusted initial host user namespace. Service
// host IDs and Cell IDs are deliberately independent. The only admitted map has
// one range for the requested non-root cell UID and one for its GID.
func DigestD09UserNamespaceMapping(cellUID, cellGID, serviceUID, serviceGID int) string {
	if cellUID <= 0 || cellGID <= 0 || serviceUID <= 0 || serviceGID <= 0 {
		return ""
	}
	identity := fmt.Sprintf("%s%s\x00uid-inside=%d:0:1\x00gid-inside=%d:0:1\x00uid-host=%d:%d:1\x00gid-host=%d:%d:1\x00parent=initial-userns/v1",
		d09UserNamespaceMappingDomain, D09UserNamespaceMappingProfile,
		cellUID, cellGID, cellUID, serviceUID, cellGID, serviceGID)
	return DigestBytes([]byte(identity))
}

const D09UserNamespaceParentSemantics = "initial-host-userns/v1"

func D09InsideUIDMap(cellUID int) string { return fmt.Sprintf("%d 0 1", cellUID) }
func D09InsideGIDMap(cellGID int) string { return fmt.Sprintf("%d 0 1", cellGID) }

func ParseD09IDMap(raw []byte) (D09IDMapRange, error) {
	var mapping D09IDMapRange
	if len(raw) == 0 || len(raw) > 4096 {
		return mapping, errors.New("ID map is empty or exceeds its strict bound")
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		return mapping, errors.New("ID map must contain exactly one range")
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 3 {
		return mapping, errors.New("ID map line must have exactly three decimal fields")
	}
	values := make([]uint64, 3)
	for index, field := range fields {
		value, err := strconv.ParseUint(field, 10, 32)
		if err != nil {
			return mapping, errors.New("ID map contains a non-decimal or out-of-range field")
		}
		values[index] = value
	}
	if values[2] != 1 || values[0] == 0 {
		return mapping, errors.New("ID map must map exactly one non-root container identity")
	}
	return D09IDMapRange{ContainerID: values[0], ParentID: values[1], Length: values[2]}, nil
}

// VerifyD09InsideIDMaps verifies the entrypoint-visible maps and the digest
// supplied by the registered template. This proves only the child-to-parent
// namespace mapping line; the host supervisor must also call
// VerifyD09HostIDMaps on /proc/<host-pid> before acknowledging the proof.
func VerifyD09InsideIDMaps(profile, digest string, cellUID, cellGID, serviceUID, serviceGID int, uidMap, gidMap []byte) error {
	if profile != D09UserNamespaceMappingProfile || digest == "" || digest != DigestD09UserNamespaceMapping(cellUID, cellGID, serviceUID, serviceGID) {
		return errors.New("registered D09 user-namespace map profile/digest mismatch")
	}
	uidRange, err := ParseD09IDMap(uidMap)
	if err != nil || uidRange != (D09IDMapRange{ContainerID: uint64(cellUID), ParentID: 0, Length: 1}) {
		return errors.Join(errors.New("in-container uid_map is not the exact registered single-ID mapping"), err)
	}
	gidRange, err := ParseD09IDMap(gidMap)
	if err != nil || gidRange != (D09IDMapRange{ContainerID: uint64(cellGID), ParentID: 0, Length: 1}) {
		return errors.Join(errors.New("in-container gid_map is not the exact registered single-ID mapping"), err)
	}
	return nil
}

// VerifyD09HostIDMaps independently verifies the kernel's translation of the
// same process into the initial host user namespace. A self-reported in-cell
// uid_map cannot satisfy this check; the caller must read host-visible
// /proc/<host-pid> files after confirming the user-namespace inode.
func VerifyD09HostIDMaps(profile, digest string, cellUID, cellGID, serviceUID, serviceGID int, uidMap, gidMap []byte) error {
	if profile != D09UserNamespaceMappingProfile || digest == "" || digest != DigestD09UserNamespaceMapping(cellUID, cellGID, serviceUID, serviceGID) {
		return errors.New("registered D09 host user-namespace map profile/digest mismatch")
	}
	uidRange, err := ParseD09IDMap(uidMap)
	if err != nil || uidRange != (D09IDMapRange{ContainerID: uint64(cellUID), ParentID: uint64(serviceUID), Length: 1}) {
		return errors.Join(errors.New("host-visible uid_map does not map only the registered service UID"), err)
	}
	gidRange, err := ParseD09IDMap(gidMap)
	if err != nil || gidRange != (D09IDMapRange{ContainerID: uint64(cellGID), ParentID: uint64(serviceGID), Length: 1}) {
		return errors.Join(errors.New("host-visible gid_map does not map only the registered service GID"), err)
	}
	return nil
}

func verifyD09HostProcessIDMaps(pid int, expectedUserNSInode uint64, config D09Config,
	insideUIDMap, insideGIDMap string) error {
	if pid <= 0 || expectedUserNSInode == 0 || config.CellUID <= 0 || config.CellGID <= 0 ||
		config.UserNamespaceHostUID <= 0 || config.UserNamespaceHostGID <= 0 {
		return ErrProductionLauncherUnavailable
	}
	userNSInfo, err := os.Stat(fmt.Sprintf("/proc/%d/ns/user", pid))
	if err != nil {
		return err
	}
	userNSStat, ok := userNSInfo.Sys().(*syscall.Stat_t)
	if !ok || userNSStat.Ino != expectedUserNSInode {
		return errors.New("host leader user namespace differs from the captured entrypoint TCB proof")
	}
	uidMap, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "uid_map"))
	if err != nil {
		return err
	}
	gidMap, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "gid_map"))
	if err != nil {
		return err
	}
	if err := VerifyD09InsideIDMaps(config.UserNamespaceMappingProfile, config.UserNamespaceMappingDigest,
		config.CellUID, config.CellGID, config.UserNamespaceHostUID, config.UserNamespaceHostGID,
		[]byte(insideUIDMap), []byte(insideGIDMap)); err != nil {
		return err
	}
	return VerifyD09HostIDMaps(config.UserNamespaceMappingProfile, config.UserNamespaceMappingDigest,
		config.CellUID, config.CellGID, config.UserNamespaceHostUID, config.UserNamespaceHostGID, uidMap, gidMap)
}
