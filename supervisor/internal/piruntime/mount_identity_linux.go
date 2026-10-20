//go:build linux

package piruntime

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// mountRecord is the namespace-stable part of one /proc/self/mountinfo line.
// The mount ID, parent ID and the shared:/master: propagation fields are
// deliberately absent: they are local to a mount namespace, and a service that
// runs in its own namespace sees different values than the host that measured
// its registration.
type mountRecord struct {
	id           uint64
	device       string
	root         string
	mountpoint   string
	flags        string
	fstype       string
	source       string
	superOptions string
}

func parseMountRecord(mountinfo string, mountID uint64) (mountRecord, error) {
	for _, line := range strings.Split(mountinfo, "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 3 {
			continue
		}
		id, err := strconv.ParseUint(left[0], 10, 64)
		if err != nil || id != mountID {
			continue
		}
		return mountRecord{id: id, device: left[2], root: left[3], mountpoint: left[4], flags: left[5],
			fstype: right[0], source: right[1], superOptions: right[2]}, nil
	}
	return mountRecord{}, errors.New("source descriptor mount ID is absent from mountinfo")
}

// options is the string whose digest registrations pin. It excludes the mount
// ID, the device number (compared against st_dev separately) and propagation.
func (m mountRecord) options() string {
	return m.optionsWithFlags(m.flags)
}

func (m mountRecord) optionsWithFlags(flags string) string {
	return strings.Join([]string{m.root, m.mountpoint, flags, m.fstype, m.source, m.superOptions}, "\x00")
}

func (m mountRecord) readOnly() bool {
	return strings.Contains(","+m.flags+",", ",ro,")
}

// onDevice reports whether the record describes the mount of a descriptor
// whose st_dev is dev.
func (m mountRecord) onDevice(dev uint64) bool {
	major := (dev >> 8 & 0xfff) | (dev >> 32 & 0xfffff000)
	minor := (dev & 0xff) | (dev >> 12 & 0xffffff00)
	return m.device == fmt.Sprintf("%d:%d", major, minor)
}

// matchesRegisteredOptions reports whether the record hashes to the digest a
// registration pinned. With allowNarrowing, the per-mount flags may differ from
// the registered flags only by narrowing: rw becoming ro and nosuid, nodev or
// noexec being added, which is what service-unit filesystem hardening does to
// a path outside ReadWritePaths. Widening and every other flag change refuse.
func (m mountRecord) matchesRegisteredOptions(digest string, allowNarrowing bool) bool {
	if DigestBytes([]byte(m.options())) == digest {
		return true
	}
	if !allowNarrowing {
		return false
	}
	flags := strings.Split(m.flags, ",")
	var removable []int
	for index, flag := range flags {
		if flag == "nosuid" || flag == "nodev" || flag == "noexec" {
			removable = append(removable, index)
		}
	}
	for _, widenAccess := range []bool{false, true} {
		if widenAccess && (len(flags) == 0 || flags[0] != "ro") {
			continue
		}
		for subset := 0; subset < 1<<len(removable); subset++ {
			candidate := make([]string, 0, len(flags))
			for index, flag := range flags {
				skip := false
				for bit, position := range removable {
					if position == index && subset&(1<<bit) != 0 {
						skip = true
					}
				}
				if skip {
					continue
				}
				if widenAccess && index == 0 {
					flag = "rw"
				}
				candidate = append(candidate, flag)
			}
			if DigestBytes([]byte(m.optionsWithFlags(strings.Join(candidate, ",")))) == digest {
				return true
			}
		}
	}
	return false
}

// mountRecordForDescriptor reads the mountinfo line of the mount the open
// descriptor lives on and requires its device to be the descriptor's st_dev.
func mountRecordForDescriptor(file *os.File) (mountRecord, error) {
	info, err := file.Stat()
	if err != nil {
		return mountRecord{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return mountRecord{}, errors.New("descriptor has no device number")
	}
	mountID, err := descriptorMountID(file.Fd())
	if err != nil {
		return mountRecord{}, err
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return mountRecord{}, err
	}
	record, err := parseMountRecord(string(data), mountID)
	if err != nil {
		return mountRecord{}, err
	}
	if !record.onDevice(uint64(stat.Dev)) {
		return mountRecord{}, errors.New("descriptor device differs from its mountinfo device")
	}
	return record, nil
}
