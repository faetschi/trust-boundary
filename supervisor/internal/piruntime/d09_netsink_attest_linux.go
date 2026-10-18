//go:build linux

package piruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// D09BootIDPath is the kernel's id of the current boot.
const D09BootIDPath = "/proc/sys/kernel/random/boot_id"

// D09NetSinkAttestation is what the operator reset writes (root-owned, 0644) after it loaded the
// pinned net-sink rule with nft and read the table back. nft resolves the cgroup path of the rule to
// a cgroup id when it loads, so the rule confines the net-sink cgroup only while that very
// directory exists in the boot that loaded it. A reboot, an unloaded table or a recreated parent
// directory removes the confinement silently; the attestation names the boot and the parent
// directory (device and inode) it was loaded for, so none of them can pass as the current one.
type D09NetSinkAttestation struct {
	Schema     string `json:"schema"`
	BootID     string `json:"boot_id"`
	ParentDev  uint64 `json:"parent_dev"`
	ParentIno  uint64 `json:"parent_ino"`
	RuleDigest string `json:"rule_digest"`
}

// ReadD09BootID returns the current boot id.
func ReadD09BootID() (string, error) {
	data, err := os.ReadFile(D09BootIDPath)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", errors.New("empty boot id")
	}
	return id, nil
}

// NewD09NetSinkAttestation is the attestation for the open net-sink parent cgroup directory.
func NewD09NetSinkAttestation(bootID string, parent *os.File) (D09NetSinkAttestation, error) {
	if bootID == "" || parent == nil {
		return D09NetSinkAttestation{}, errors.New("net-sink attestation needs a boot id and the parent cgroup")
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(parent.Fd()), &st); err != nil {
		return D09NetSinkAttestation{}, err
	}
	return D09NetSinkAttestation{Schema: D09NetSinkAttestSchema, BootID: bootID,
		ParentDev: uint64(st.Dev), ParentIno: st.Ino, RuleDigest: D09NetSinkRuleDigest}, nil
}

func (a D09NetSinkAttestation) Marshal() ([]byte, error) {
	data, err := json.Marshal(a)
	return append(data, '\n'), err
}

// requireNetSinkRule is the service-side fail-closed check before a net-sink command cell starts.
// The installed rule file must be the pinned one, and the root-owned attestation (read through the
// same root-owned reader) must name the current boot, the net-sink parent cgroup directory the
// service holds open, and the pinned rule digest: the reset loaded the rule for exactly this
// parent in this boot. Alone, the file proves nothing about the kernel's table.
func requireNetSinkRule(read func(path string, maximum int64) ([]byte, error), bootID func() (string, error), parent *os.File) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrProductionLauncherUnavailable, fmt.Sprintf(format, args...))
	}
	raw, err := read(D09NetSinkRulePath, 8<<10)
	if err != nil {
		return refuse("net-sink nftables rule %s is not installed: %v", D09NetSinkRulePath, err)
	}
	if DigestBytes(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))) != D09NetSinkRuleDigest {
		return refuse("installed net-sink nftables rule differs from the pinned one")
	}
	boot, err := bootID()
	if err != nil {
		return refuse("the boot id cannot be read: %v", err)
	}
	want, err := NewD09NetSinkAttestation(boot, parent)
	if err != nil {
		return refuse("the net-sink parent cgroup cannot be identified: %v", err)
	}
	data, err := read(D09NetSinkAttestPath, 4<<10)
	if err != nil {
		return refuse("net-sink load attestation %s is missing or not root-owned (run tbound trial reset): %v", D09NetSinkAttestPath, err)
	}
	var got D09NetSinkAttestation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil || decoder.More() {
		return refuse("net-sink load attestation is not valid")
	}
	if got != want {
		return refuse("net-sink load attestation is not for this boot, this net-sink parent cgroup and the pinned rule (run tbound trial reset)")
	}
	return nil
}
