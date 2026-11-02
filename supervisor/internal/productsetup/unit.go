package productsetup

import (
	"fmt"
	"strings"
)

const unitExecStart = BinaryPath + " product-serve --config " + ServiceConfigPath

// unitWantedBy is the boot activation: the service listens on its own socket (no socket unit), so
// multi-user.target pulls the service itself in.
const unitWantedBy = "multi-user.target"

// unitDirectives is the closed set of directives and the exact value each must have, in the order the
// unit lists them. The hardening matches the research unit (infra/guest/systemd/tbound-pi.service)
// where it applies. DelegateSubgroup keeps the service process in its runner subgroup, so the command cells can run
// beside it in the delegated tree. The one Environment line matches the environment of the product Podman children.
var unitDirectives = []struct{ key, value string }{
	{"User", ServiceUser},
	{"Group", ServiceGroup},
	{"ExecStart", unitExecStart},
	{"KillMode", "control-group"},
	{"Restart", "on-failure"},
	{"RestartSec", "5"},
	{"UMask", "0077"},
	{"NoNewPrivileges", "yes"},
	{"ProtectSystem", "strict"},
	{"ProtectHome", "yes"},
	{"PrivateTmp", "yes"},
	{"Delegate", "yes"},
	{"DelegateSubgroup", "runner"},
	{"RuntimeDirectory", "tbound-product"},
	{"RuntimeDirectoryMode", "0750"},
	{"ReadWritePaths", RuntimeDir + " " + StateDir},
	{"UnsetEnvironment", "DBUS_SESSION_BUS_ADDRESS"},
	{"Environment", strings.Join(commandCellEnvironment(), " ")},
}

// commandCellEnvironment is the environment of every product Podman and cosign child, sorted by name. The
// unit sets it on the service too, so ValidateUnit refuses a drop-in that would change what a child inherits.
func commandCellEnvironment() []string {
	return []string{
		"CONTAINERS_CONF=" + ContainersConfPath,
		"CONTAINERS_STORAGE_CONF=" + StorageConfPath,
		"HOME=" + PodmanHomeDir,
		"LANG=C",
		"LC_ALL=C",
		"PATH=" + CommandCellPath,
		"TMPDIR=" + PodmanTmpDir,
		"XDG_CACHE_HOME=" + PodmanCacheDir,
		"XDG_RUNTIME_DIR=" + RuntimeDir,
	}
}

// RenderUnit returns the tbound-product.service unit.
func RenderUnit() []byte {
	var b strings.Builder
	b.WriteString("# Installed by `tbound product setup`. Root-owned 0644; install verbatim, no drop-ins.\n")
	b.WriteString("[Unit]\nDescription=TBound product service (governed coding conversations)\n\n[Service]\n")
	for _, d := range unitDirectives {
		fmt.Fprintf(&b, "%s=%s\n", d.key, d.value)
	}
	b.WriteString("\n[Install]\nWantedBy=" + unitWantedBy + "\n")
	return []byte(b.String())
}

// ValidateUnit parses data with the narrow unit grammar (no continuations, no repeated directive) and
// refuses anything but the exact directive set of RenderUnit. It also refuses a thesis path anywhere.
func ValidateUnit(data []byte) error {
	sections := map[string]map[string]string{}
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasSuffix(line, "\\"):
			return refuse("unit line continuations are not part of the pinned grammar")
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = line[1 : len(line)-1]
			if sections[section] != nil {
				return refuse("unit section [%s] appears twice", section)
			}
			sections[section] = map[string]string{}
		default:
			key, value, ok := strings.Cut(line, "=")
			if section == "" || !ok || key == "" {
				return refuse("unit line %q is outside a section or not a directive", line)
			}
			if _, dup := sections[section][key]; dup {
				return refuse("unit directive %s repeats", key)
			}
			sections[section][key] = strings.TrimSpace(value)
		}
	}
	if len(sections) != 3 || len(sections["Unit"]) != 1 || sections["Unit"]["Description"] == "" {
		return refuse("the unit must be exactly [Unit] with Description, [Service] and [Install]")
	}
	if install := sections["Install"]; len(install) != 1 || install["WantedBy"] != unitWantedBy {
		return refuse("[Install] must be exactly WantedBy=%s", unitWantedBy)
	}
	service := sections["Service"]
	if len(service) != len(unitDirectives) {
		return refuse("[Service] has %d directives, want exactly %d", len(service), len(unitDirectives))
	}
	for _, d := range unitDirectives {
		if got, ok := service[d.key]; !ok || got != d.value {
			return refuse("unit directive %s=%q, want %q", d.key, got, d.value)
		}
	}
	for _, section := range sections {
		for _, value := range section {
			for _, field := range strings.Fields(value) {
				if IsThesisPath(strings.Trim(field, `"'`)) {
					return refuse("the unit names a thesis service path: %s", field)
				}
			}
		}
	}
	return nil
}
