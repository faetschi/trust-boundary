//go:build linux

package piruntime

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
)

const sysD09LandlockCreateRuleset = 444

// D09LandlockABI is the registered process-wide TSYNC ABI required by the Go
// entrypoint. ABI 8 is explicit because LANDLOCK_RESTRICT_SELF_TSYNC is
// required; no silent ABI-1 fallback exists.
const D09LandlockABI = 8

const (
	D09LandlockFSExecute     uint64 = 1 << 0
	D09LandlockFSWriteFile   uint64 = 1 << 1
	D09LandlockFSReadFile    uint64 = 1 << 2
	D09LandlockFSReadDir     uint64 = 1 << 3
	D09LandlockFSRemoveDir   uint64 = 1 << 4
	D09LandlockFSRemoveFile  uint64 = 1 << 5
	D09LandlockFSMakeChar    uint64 = 1 << 6
	D09LandlockFSMakeDir     uint64 = 1 << 7
	D09LandlockFSMakeReg     uint64 = 1 << 8
	D09LandlockFSMakeSock    uint64 = 1 << 9
	D09LandlockFSMakeFifo    uint64 = 1 << 10
	D09LandlockFSMakeBlock   uint64 = 1 << 11
	D09LandlockFSMakeSym     uint64 = 1 << 12
	D09LandlockFSRefer       uint64 = 1 << 13
	D09LandlockFSTruncate    uint64 = 1 << 14
	D09LandlockNetBindTCP    uint64 = 1 << 0
	D09LandlockNetConnectTCP uint64 = 1 << 1
)

// D09LandlockKernelABI measures the current kernel ABI with the real
// LANDLOCK_CREATE_RULESET_VERSION syscall. It returns no asserted/fixture value.
func D09LandlockKernelABI() (int, error) {
	version, _, errno := syscall.Syscall(uintptr(sysD09LandlockCreateRuleset), 0, 0, 1)
	if errno != 0 {
		return 0, errno
	}
	if version == 0 {
		return 0, errors.New("kernel reported Landlock ABI 0")
	}
	return int(version), nil
}

const (
	D09LandlockHandledFS = D09LandlockFSExecute | D09LandlockFSWriteFile | D09LandlockFSReadFile |
		D09LandlockFSReadDir | D09LandlockFSRemoveDir | D09LandlockFSRemoveFile |
		D09LandlockFSMakeChar | D09LandlockFSMakeDir | D09LandlockFSMakeReg |
		D09LandlockFSMakeSock | D09LandlockFSMakeFifo | D09LandlockFSMakeBlock |
		D09LandlockFSMakeSym | D09LandlockFSRefer | D09LandlockFSTruncate
	D09LandlockHandledNet = D09LandlockNetBindTCP | D09LandlockNetConnectTCP
	D09LandlockReadExec   = D09LandlockFSExecute | D09LandlockFSReadFile | D09LandlockFSReadDir
)

type d09LandlockRule struct {
	path   string
	access uint64
}

// Landlock scope bits (ABI 6 and later). The net-sink cell, which shares the host
// network namespace, scopes abstract unix sockets so it cannot reach the abstract
// sockets of host processes outside its Landlock domain.
const (
	D09LandlockScopeAbstractUnix uint64 = 1 << 0
	d09LandlockScopeKnown               = D09LandlockScopeAbstractUnix | 1<<1
)

// DigestD09LandlockRules returns the canonical digest of the exact static
// ABI-8 filesystem+network policy the in-image entrypoint installs. Network bind
// and connect rights are handled with no allow rules, so both are denied. The
// path order and effective access masks are part of the digest.
func DigestD09LandlockRules(readOnlyPaths, readWritePaths []string) (string, error) {
	return DigestD09LandlockRulesNet(readOnlyPaths, readWritePaths, nil, 0)
}

// DigestD09LandlockRulesNet is DigestD09LandlockRules for a command-cell delta
// that allows TCP connect to the given ports (strictly increasing, nonzero) and
// scopes the given Landlock scope bits. With no port and no scope it equals
// DigestD09LandlockRules, so the base digest is unchanged by the delta support.
func DigestD09LandlockRulesNet(readOnlyPaths, readWritePaths []string, connectPorts []uint16, scoped uint64) (string, error) {
	if scoped&^d09LandlockScopeKnown != 0 {
		return "", errors.New("Landlock profile names an unregistered scope bit")
	}
	for i, port := range connectPorts {
		if port == 0 || i > 0 && port <= connectPorts[i-1] {
			return "", errors.New("Landlock connect ports must be nonzero and strictly increasing")
		}
	}
	paths := make(map[string]bool, len(readOnlyPaths)+len(readWritePaths))
	rules := make([]d09LandlockRule, 0, len(readOnlyPaths)+len(readWritePaths))
	for _, path := range readOnlyPaths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00,\r\n") {
			return "", errors.New("Landlock read-only path must be a clean absolute path")
		}
		if paths[path] {
			return "", errors.New("Landlock profile repeats a path")
		}
		paths[path] = true
		rules = append(rules, d09LandlockRule{path: path, access: D09LandlockReadExec})
	}
	for _, path := range readWritePaths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00,\r\n") {
			return "", errors.New("Landlock read-write path must be a clean absolute path")
		}
		if paths[path] {
			return "", errors.New("Landlock profile repeats a path")
		}
		paths[path] = true
		rules = append(rules, d09LandlockRule{path: path, access: D09LandlockHandledFS})
	}
	if len(rules) == 0 {
		return "", errors.New("Landlock profile must contain at least one path rule")
	}
	hash := sha256.New()
	var field [8]byte
	binary.LittleEndian.PutUint64(field[:], D09LandlockHandledFS)
	_, _ = hash.Write(field[:])
	binary.LittleEndian.PutUint64(field[:], D09LandlockHandledNet)
	_, _ = hash.Write(field[:])
	for _, rule := range rules {
		binary.LittleEndian.PutUint64(field[:], uint64(len(rule.path)))
		_, _ = hash.Write(field[:])
		_, _ = hash.Write([]byte(rule.path))
		binary.LittleEndian.PutUint64(field[:], rule.access)
		_, _ = hash.Write(field[:])
	}
	if len(connectPorts) != 0 || scoped != 0 {
		_, _ = hash.Write([]byte("net-delta/v1\x00"))
		binary.LittleEndian.PutUint64(field[:], scoped)
		_, _ = hash.Write(field[:])
		binary.LittleEndian.PutUint64(field[:], uint64(len(connectPorts)))
		_, _ = hash.Write(field[:])
		for _, port := range connectPorts {
			binary.LittleEndian.PutUint64(field[:], uint64(port))
			_, _ = hash.Write(field[:])
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// ParseD09LandlockPaths parses the exact comma-separated canonical path lists
// stored in the registered D09 template. Empty components and whitespace are
// refused so the entrypoint's rights digest cannot differ from the operator's
// reviewed template.
func ParseD09LandlockPaths(value string) ([]string, error) {
	if value == "" {
		return nil, errors.New("Landlock path list is empty")
	}
	parts := strings.Split(value, ",")
	paths := make([]string, len(parts))
	for i, part := range parts {
		if part == "" || strings.TrimSpace(part) != part {
			return nil, errors.New("Landlock path list contains an empty or whitespace-padded component")
		}
		if !filepath.IsAbs(part) || filepath.Clean(part) != part || strings.ContainsAny(part, "\x00\r\n") {
			return nil, errors.New("Landlock path list contains a non-canonical absolute path")
		}
		paths[i] = part
	}
	return paths, nil
}
