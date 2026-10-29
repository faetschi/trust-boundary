package productsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// system reports whether account lookups ask the host (getent) instead of the root's passwd and group
// files.
func (h *host) system() bool { return h.env.Live || h.root == "/" }

func notFound(err error) bool {
	var code interface{ ExitCode() int }
	return errors.As(err, &code) && code.ExitCode() == 2 // getent: the key was not found
}

// entry returns the colon fields of the named record in a passwd or group file or getent line.
func entry(data string, name string) ([]string, bool) {
	for _, line := range strings.Split(data, "\n") {
		if fields := strings.Split(strings.TrimSpace(line), ":"); len(fields) >= 3 && fields[0] == name {
			return fields, true
		}
	}
	return nil, false
}

func (h *host) database(kind string, name string) ([]string, bool, error) {
	if h.system() {
		out, err := h.exec("getent", kind, name)
		if notFound(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("getent %s %s: %w", kind, name, err)
		}
		fields, ok := entry(string(out), name)
		return fields, ok, nil
	}
	data, err := os.ReadFile(filepath.Join(h.root, "etc", kind))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	fields, ok := entry(string(data), name)
	return fields, ok, nil
}

func (h *host) lookupGroup(name string) (gid int, found bool, err error) {
	fields, found, err := h.database("group", name)
	if err != nil || !found {
		return 0, false, err
	}
	gid, ok := atoi(fields[2])
	if !ok {
		return 0, false, fmt.Errorf("group %s has an invalid GID", name)
	}
	return gid, true, nil
}

func (h *host) lookupUser(name string) (uid, gid int, found bool, err error) {
	fields, found, err := h.database("passwd", name)
	if err != nil || !found {
		return 0, 0, false, err
	}
	uid, uidOK := atoi(fields[2])
	if len(fields) < 4 {
		return 0, 0, false, fmt.Errorf("user %s has an invalid passwd entry", name)
	}
	gid, gidOK := atoi(fields[3])
	if !uidOK || !gidOK {
		return 0, 0, false, fmt.Errorf("user %s has invalid IDs", name)
	}
	return uid, gid, true, nil
}

// usedIDs lists every UID and GID in the root's passwd and group files.
func (h *host) usedIDs() map[int]bool {
	used := map[int]bool{}
	for _, kind := range []string{"passwd", "group"} {
		data, _ := os.ReadFile(filepath.Join(h.root, "etc", kind))
		for _, line := range strings.Split(string(data), "\n") {
			if fields := strings.Split(line, ":"); len(fields) >= 3 {
				if n, ok := atoi(fields[2]); ok {
					used[n] = true
				}
			}
		}
	}
	return used
}

// recordGroup learns the GID groupadd chose. On a live host it asks the system again. On a test root
// it plays groupadd: it appends the entry to the root's group file with the requested or the lowest
// free ID from 900. A dry run learns nothing.
func (h *host) recordGroup(want int) (int, error) {
	switch {
	case h.env.DryRun:
		return 0, nil
	case h.system():
		gid, found, err := h.lookupGroup(ServiceGroup)
		if err == nil && !found {
			err = fmt.Errorf("group %s is missing after groupadd", ServiceGroup)
		}
		return gid, err
	}
	gid := freeID(h.usedIDs(), want)
	return gid, h.appendLine("/etc/group", fmt.Sprintf("%s:x:%d:", ServiceGroup, gid))
}

// recordUser is recordGroup for the user, whose primary group is gid.
func (h *host) recordUser(want, gid int) error {
	switch {
	case h.env.DryRun:
		return nil
	case h.system():
		uid, ugid, found, err := h.lookupUser(ServiceUser)
		if err == nil && !found {
			err = fmt.Errorf("user %s is missing after useradd", ServiceUser)
		}
		h.uid, h.gid, h.haveIDs = uid, ugid, err == nil
		return err
	}
	uid := freeID(h.usedIDs(), want)
	h.uid, h.gid, h.haveIDs = uid, gid, true
	return h.appendLine("/etc/passwd",
		fmt.Sprintf("%s:x:%d:%d::%s:/usr/sbin/nologin", ServiceUser, uid, gid, StateDir))
}

// freeID is the requested ID, or the lowest unused ID from 900.
func freeID(used map[int]bool, want int) int {
	if want != 0 {
		return want
	}
	id := 900
	for used[id] {
		id++
	}
	return id
}
