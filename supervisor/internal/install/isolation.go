package install

import (
	"errors"
	"fmt"
	"strings"
)

// IsolationOptions configure the generated user service unit.
type IsolationOptions struct {
	UnitPath    string
	ExecStart   string
	MemoryMax   string
	TasksMax    string
	CPUWeight   string
	SubUIDRange string
}

// IsolationPlan is the reviewable result of planning rootless isolation.
type IsolationPlan struct {
	User        string `json:"user"`
	UnitPath    string `json:"unit_path"`
	Unit        string `json:"unit"`
	SubUIDRange string `json:"subuid_range,omitempty"`
}

// BuildIsolationPlan produces a systemd user unit that delegates a cgroup v2
// subtree to the session (Delegate=yes) with bounded resources. It refuses the
// root user and empty inputs. It does not enable or install the unit.
func BuildIsolationPlan(user string, opts IsolationOptions) (IsolationPlan, error) {
	if user == "" {
		return IsolationPlan{}, errors.New("isolation plan requires a non-root user")
	}
	if user == "root" {
		return IsolationPlan{}, errors.New("refuse to plan isolation for root")
	}
	if opts.ExecStart == "" {
		opts.ExecStart = "/usr/local/bin/tbound serve --pi"
	}
	if opts.UnitPath == "" {
		opts.UnitPath = fmt.Sprintf("/home/%s/.config/systemd/user/tbound.service", user)
	}
	if opts.MemoryMax == "" {
		opts.MemoryMax = "4G"
	}
	if opts.TasksMax == "" {
		opts.TasksMax = "512"
	}
	if opts.CPUWeight == "" {
		opts.CPUWeight = "100"
	}
	if err := validateUnitValue(opts.MemoryMax); err != nil {
		return IsolationPlan{}, err
	}
	if err := validateUnitValue(opts.TasksMax); err != nil {
		return IsolationPlan{}, err
	}
	if err := validateUnitValue(opts.CPUWeight); err != nil {
		return IsolationPlan{}, err
	}
	if err := validateUnitValue(opts.ExecStart); err != nil {
		return IsolationPlan{}, err
	}
	unit := renderUserUnit(opts)
	return IsolationPlan{User: user, UnitPath: opts.UnitPath, Unit: unit, SubUIDRange: opts.SubUIDRange}, nil
}

func validateUnitValue(v string) error {
	if strings.ContainsAny(v, "\n\r") {
		return errors.New("isolation value must be a single line")
	}
	return nil
}

func renderUserUnit(opts IsolationOptions) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=TBound governed Pi session\n")
	b.WriteString("After=default.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=oneshot\n")
	b.WriteString("RemainAfterExit=yes\n")
	// Delegate=yes gives the unit a writable, non-threaded cgroup v2 subtree
	// with the required controllers for rootless containers and cgroup.kill.
	b.WriteString("Delegate=yes\n")
	b.WriteString("MemoryMax=" + opts.MemoryMax + "\n")
	b.WriteString("TasksMax=" + opts.TasksMax + "\n")
	b.WriteString("CPUWeight=" + opts.CPUWeight + "\n")
	b.WriteString("ExecStart=" + opts.ExecStart + "\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}
