//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrCgroupUnsupported is returned when the cgroup v2 hierarchy is absent or
// is not delegated to the caller. This is recorded honestly rather than
// treated as success.
var ErrCgroupUnsupported = errors.New("cgroup v2 limits are not delegated")

// cgroupScope is one best-effort cgroup v2 scope for the cell. A nil scope, or
// a scope whose setup returned ErrCgroupUnsupported, means limits and
// populated=0 confirmation are not established.
type cgroupScope struct {
	path         string
	memoryMax    bool
	pidsMax      bool
	cpuWeight    bool
	killWritable bool
}

// findCgroup2Mount returns the first cgroup2 mount point from mountinfo.
func findCgroup2Mount() (string, bool) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 0 || separator+1 >= len(fields) {
			continue
		}
		if fields[separator+1] == "cgroup2" {
			return fields[4], true
		}
	}
	return "", false
}

// establishCgroup creates a private cgroup v2 scope and applies the requested
// limits where the hierarchy is writable. It fails with ErrCgroupUnsupported
// when the scope cannot be created and never silently drops a limit.
func establishCgroup(token string, limits Limits) (*cgroupScope, error) {
	root, ok := findCgroup2Mount()
	if !ok {
		return nil, ErrCgroupUnsupported
	}
	path := filepath.Join(root, "tbound-"+token)
	if err := os.Mkdir(path, 0o755); err != nil {
		return nil, fmt.Errorf("%w: create scope: %v", ErrCgroupUnsupported, err)
	}
	scope := &cgroupScope{path: path}
	cleanup := func() {
		_ = os.Remove(path)
	}
	if limits.MemoryBytes > 0 {
		if err := writeControl(path, "memory.max", strconv.FormatInt(limits.MemoryBytes, 10)); err != nil {
			cleanup()
			return nil, fmt.Errorf("%w: memory.max: %v", ErrCgroupUnsupported, err)
		}
		scope.memoryMax = true
	}
	if limits.PidsMax > 0 {
		if err := writeControl(path, "pids.max", strconv.FormatInt(limits.PidsMax, 10)); err != nil {
			cleanup()
			return nil, fmt.Errorf("%w: pids.max: %v", ErrCgroupUnsupported, err)
		}
		scope.pidsMax = true
	}
	if limits.CPUWeight > 0 {
		if err := writeControl(path, "cpu.weight", strconv.FormatInt(limits.CPUWeight, 10)); err != nil {
			cleanup()
			return nil, fmt.Errorf("%w: cpu.weight: %v", ErrCgroupUnsupported, err)
		}
		scope.cpuWeight = true
	}
	if file, err := os.OpenFile(filepath.Join(path, "cgroup.kill"), os.O_WRONLY, 0); err == nil {
		_ = file.Close()
		scope.killWritable = true
	}
	return scope, nil
}

func writeControl(dir, name, value string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644)
}

// addProcess places pid in the scope. A best-effort scope may be non-nil only
// when establishCgroup succeeded.
func (scope *cgroupScope) addProcess(pid int) error {
	if scope == nil {
		return nil
	}
	return writeControl(scope.path, "cgroup.procs", strconv.Itoa(pid))
}

// kill sends SIGKILL to every process in the scope using cgroup.kill.
func (scope *cgroupScope) kill() error {
	if scope == nil || !scope.killWritable {
		return errors.New("cgroup.kill is not writable")
	}
	return writeControl(scope.path, "cgroup.kill", "1")
}

// waitEmpty waits, bounded, for cgroup.events to report populated 0.
func (scope *cgroupScope) waitEmpty(timeout time.Duration) error {
	if scope == nil {
		return errors.New("no cgroup scope")
	}
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(filepath.Join(scope.path, "cgroup.events"))
		if err != nil {
			return err
		}
		populated := false
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "populated" {
				populated = fields[1] != "0"
			}
		}
		if !populated {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("cgroup scope stayed populated")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// close removes the scope directory. It never masks a containment result.
func (scope *cgroupScope) close() {
	if scope == nil {
		return
	}
	_ = os.Remove(scope.path)
}
