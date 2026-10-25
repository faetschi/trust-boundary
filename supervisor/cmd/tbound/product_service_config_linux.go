//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
)

const (
	productServiceConfigSchema   = "tbound-product-service-config/v1"
	productServiceMaxConfigBytes = 64 << 10
	productServiceMaxGraceSecond = 600
	productServiceDefaultGrace   = 30

	productServiceWorkerFixture = productsession.WorkerHeadlessFixture
)

// productServiceThesisRoots are the thesis service's paths. A product path may never equal or sit
// inside one, compared by whole path components.
var productServiceThesisRoots = []string{"/etc/tbound", "/var/lib/tbound", "/run/tbound", "/run/tbound-pi"}

var errProductServicePi = errors.New("worker mode \"pi\" requires a qualified product host; a real Pi worker per turn is not implemented yet")

// productServiceConfig is tbound-product-service-config/v1. Every path is explicit; a path must sit at
// or below the directory of a product default (see productsession paths) and never at or below a
// thesis path.
type productServiceConfig struct {
	SchemaVersion             string `json:"schema_version"`
	RegistrationPath          string `json:"registration_path"`
	RegistrationSignaturePath string `json:"registration_signature_path"`
	RegistrationPublicKeyPath string `json:"registration_public_key_path"`
	CatalogPath               string `json:"catalog_path"`
	// StateRoot holds each turn's retained evidence and each project's publication journal.
	StateRoot  string `json:"state_root"`
	SocketPath string `json:"socket_path"`
	WorkerMode string `json:"worker_mode"`
	// Projects maps each registered project ID to its managed directory.
	Projects map[string]string `json:"projects"`
	// ShutdownGraceSeconds bounds how long a graceful stop waits for running turns; zero means 30.
	ShutdownGraceSeconds int `json:"shutdown_grace_seconds,omitempty"`
}

// loadProductServiceConfig reads and strictly decodes the config file. It does not check paths.
func loadProductServiceConfig(file string) (productServiceConfig, error) {
	var cfg productServiceConfig
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > productServiceMaxConfigBytes {
		return cfg, errors.New("product service config must be a regular non-symlink file of at most 64 KiB")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return cfg, fmt.Errorf("read product service config: %w", err)
	}
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return cfg, fmt.Errorf("product service config is not strict JSON: %w", err)
	}
	if err := unmarshalGovernedJSON(raw, &cfg); err != nil {
		return productServiceConfig{}, fmt.Errorf("product service config does not match its schema: %w", err)
	}
	return cfg, nil
}

// productServiceRoots are the directories product paths may live in: those of the product defaults,
// plus test-only extra roots.
func productServiceRoots(extra []string) []string {
	return append([]string{path.Dir(productsession.DefaultRegistrationPath), path.Dir(productsession.DefaultCatalogPath),
		path.Dir(productsession.DefaultServiceSocketPath)}, extra...)
}

// productPathInside reports whether p equals root or sits below it, comparing whole components.
func productPathInside(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// validate checks the schema, the worker mode, and that every path is absolute, clean, below an
// allowed root and outside every thesis path. extraRoots are test overrides.
func (c productServiceConfig) validate(extraRoots []string) error {
	if c.SchemaVersion != productServiceConfigSchema {
		return fmt.Errorf("product service config schema_version must be %q", productServiceConfigSchema)
	}
	switch c.WorkerMode {
	case productServiceWorkerFixture:
	case productsession.WorkerPi:
		return errProductServicePi
	default:
		return fmt.Errorf("unknown worker mode %q", c.WorkerMode)
	}
	if c.ShutdownGraceSeconds < 0 || c.ShutdownGraceSeconds > productServiceMaxGraceSecond {
		return fmt.Errorf("shutdown_grace_seconds must be at most %d", productServiceMaxGraceSecond)
	}
	if len(c.Projects) == 0 {
		return errors.New("product service config names no project")
	}
	roots := productServiceRoots(extraRoots)
	check := func(field, p string) error {
		if !path.IsAbs(p) || path.Clean(p) != p {
			return fmt.Errorf("%s must be an absolute clean path", field)
		}
		for _, thesis := range productServiceThesisRoots {
			if productPathInside(p, thesis) {
				return fmt.Errorf("%s is a thesis service path", field)
			}
		}
		if !slices.ContainsFunc(roots, func(root string) bool { return productPathInside(p, root) }) {
			return fmt.Errorf("%s is not below a product default directory", field)
		}
		return nil
	}
	seen := map[string]string{}
	for id, dir := range c.Projects {
		if id == "" {
			return errors.New("product service config has an empty project ID")
		}
		if other, dup := seen[dir]; dup {
			return fmt.Errorf("projects %q and %q share a managed directory", id, other)
		}
		seen[dir] = id
	}
	for _, field := range []struct{ name, value string }{
		{"registration_path", c.RegistrationPath}, {"registration_signature_path", c.RegistrationSignaturePath},
		{"registration_public_key_path", c.RegistrationPublicKeyPath}, {"catalog_path", c.CatalogPath},
		{"state_root", c.StateRoot}, {"socket_path", c.SocketPath},
	} {
		if err := check(field.name, field.value); err != nil {
			return err
		}
	}
	for id, dir := range c.Projects {
		if err := check(fmt.Sprintf("projects[%q]", id), dir); err != nil {
			return err
		}
	}
	return nil
}

func (c productServiceConfig) grace() int {
	if c.ShutdownGraceSeconds == 0 {
		return productServiceDefaultGrace
	}
	return c.ShutdownGraceSeconds
}
