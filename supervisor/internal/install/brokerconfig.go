package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// ApprovedModel is the single authorized provider model for the registered
// profile. Other models require a separate approval and profile change.
const ApprovedModel = "nvidia/nemotron-3.5-lightning:free"

// BrokerConfigFileName is the owner-only broker configuration file name.
const BrokerConfigFileName = "broker.env"

var secretAssignment = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[:=]\s*([^\s]+)`)

// BrokerConfigTemplate returns a key-free template describing the broker
// configuration. It deliberately contains no credential value.
func BrokerConfigTemplate() string {
	return strings.Join([]string{
		"# TBound broker configuration (owner-only, mode 0600).",
		"# Do NOT paste secrets into this template file. The operator sets the",
		"# provider credential out-of-band so it never enters Pi or the agent.",
		"TBOUND_OPENROUTER_MODEL=" + ApprovedModel,
		"# Provide the credential at run time, e.g. via a separate 0600 secret file",
		"# referenced by TBOUND_OPENROUTER_API_KEY_FILE, or the host environment.",
		"TBOUND_OPENROUTER_API_KEY_FILE=/etc/tbound/openrouter.key",
		"",
	}, "\n")
}

// WriteBrokerConfig writes the broker configuration 0600 and refuses any
// obvious real secret value, so the credential never lives in this file.
func WriteBrokerConfig(path string, content []byte) error {
	if path == "" {
		return errors.New("broker config path is required")
	}
	if dir := filepath.Dir(path); dir != "." {
		if info, err := os.Stat(dir); err != nil {
			return fmt.Errorf("broker config directory: %w", err)
		} else if posixModes() && info.Mode().Perm()&0o002 != 0 {
			return fmt.Errorf("broker config directory %s is world-writable", dir)
		}
	}
	if err := refuseSecret(content); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil && posixModes() {
		if info.Mode().Perm() != 0o600 {
			return fmt.Errorf("existing broker config %s must be mode 0600, found %o", path, info.Mode().Perm())
		}
	}
	return os.WriteFile(path, content, 0o600)
}

// posixModes reports whether file permission bits are meaningful on this OS.
// On Windows these bits are emulated and must not drive security decisions.
func posixModes() bool { return runtime.GOOS != "windows" }

func refuseSecret(content []byte) error {
	for _, m := range secretAssignment.FindAllStringSubmatch(string(content), -1) {
		value := m[2]
		if isPlaceholder(value) {
			continue
		}
		return fmt.Errorf("refusing to write %q value into the broker config; keep credentials out-of-band", m[1])
	}
	return nil
}

func isPlaceholder(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return true
	}
	switch {
	case strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">"):
		return true
	case strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}"):
		return true
	case v == "changeme" || v == "placeholder" || v == "todo":
		return true
	}
	return false
}
