package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cgroupCheck() check {
	return cgroupCheckFrom(os.ReadFile, os.Stat)
}

func cgroupCheckFrom(readFile func(string) ([]byte, error), stat func(string) (os.FileInfo, error)) check {
	result := check{
		Name: "cgroup-v2", Status: warn,
		Remediation: "Run doctor in the intended cgroup scope; the host operator must provide a delegated domain cgroup with memory/pids controls and cgroup.kill. Read-only inventory does not verify writable delegation.",
	}
	membership, mount, directory, err := currentCgroup(readFile)
	if err != nil {
		result.Detail = "cannot locate current cgroup v2: " + err.Error()
		return result
	}
	details := []string{fmt.Sprintf("current cgroup: %s; mount: %s; directory: %s", membership, mount, directory)}
	read := func(name, label string) string {
		data, err := readFile(filepath.Join(directory, name))
		if err != nil {
			details = append(details, fmt.Sprintf("%s unreadable: %v", name, err))
			return ""
		}
		value := strings.TrimSpace(string(data))
		display := value
		if display == "" {
			display = "(none)"
		}
		details = append(details, label+": "+display)
		return value
	}
	kind := read("cgroup.type", "type")
	if kind != "domain" {
		details = append(details, "required domain cgroup not established")
	}
	controllers := strings.Fields(read("cgroup.controllers", "available controllers"))
	var missing []string
	for _, required := range []string{"memory", "pids"} {
		found := false
		for _, available := range controllers {
			found = found || available == required
		}
		if !found {
			missing = append(missing, required)
		}
	}
	if len(missing) != 0 {
		details = append(details, "missing required controllers: "+strings.Join(missing, ", "))
	}
	read("cgroup.subtree_control", "controllers enabled for children")
	read("memory.max", "memory.max")
	read("pids.max", "pids.max")
	// cgroup.kill is write-only. Stat inventories its presence without opening
	// it for writing or mistaking a read failure for a missing kernel feature.
	if _, err := stat(filepath.Join(directory, "cgroup.kill")); err != nil {
		details = append(details, fmt.Sprintf("cgroup.kill unavailable: %v", err))
	} else {
		details = append(details, "cgroup.kill: present (writability unverified)")
	}
	details = append(details, "writable delegation unverified (read-only inventory)")
	result.Detail = strings.Join(details, "; ")
	return result
}

func currentCgroup(readFile func(string) ([]byte, error)) (membership, mount, directory string, err error) {
	data, err := readFile("/proc/self/cgroup")
	if err != nil {
		return "", "", "", fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			membership = path
		}
	}
	if !filepath.IsAbs(membership) || filepath.Clean(membership) != membership {
		return "", "", "", fmt.Errorf("missing or unresolved unified membership %q", membership)
	}
	data, err = readFile("/proc/self/mountinfo")
	if err != nil {
		return "", "", "", fmt.Errorf("read /proc/self/mountinfo: %w", err)
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	bestRoot := -1
	for _, line := range strings.Split(string(data), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields := strings.Fields(before)
		if !ok || !strings.HasPrefix(after, "cgroup2 ") || len(fields) < 5 {
			continue
		}
		root, point := unescape.Replace(fields[3]), unescape.Replace(fields[4])
		if !filepath.IsAbs(root) || !filepath.IsAbs(point) {
			continue
		}
		relative, e := filepath.Rel(root, membership)
		if e != nil || relative == ".." || strings.HasPrefix(relative, "../") || len(root) < bestRoot {
			continue
		}
		bestRoot = len(root)
		mount, directory = point, filepath.Join(point, relative)
	}
	if directory == "" {
		return "", "", "", fmt.Errorf("no cgroup2 mount covers membership %q", membership)
	}
	return membership, mount, directory, nil
}
