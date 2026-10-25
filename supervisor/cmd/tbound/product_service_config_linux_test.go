//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsession"
)

func validProductServiceConfig() productServiceConfig {
	return productServiceConfig{SchemaVersion: productServiceConfigSchema,
		RegistrationPath: productsession.DefaultRegistrationPath, RegistrationSignaturePath: productsession.DefaultRegistrationSignaturePath,
		RegistrationPublicKeyPath: productsession.DefaultRegistrationPublicKeyPath, CatalogPath: productsession.DefaultCatalogPath,
		StateRoot: "/var/lib/tbound-product", SocketPath: productsession.DefaultServiceSocketPath, WorkerMode: productServiceWorkerFixture,
		Projects: map[string]string{"alpha": "/var/lib/tbound-product/projects/alpha"}}
}

func TestProductServiceConfigAcceptsProductPathsAndRefusesThesisPaths(t *testing.T) {
	if err := validProductServiceConfig().validate(nil); err != nil {
		t.Fatalf("the product defaults were refused: %v", err)
	}
	fields := map[string]func(*productServiceConfig, string){
		"registration_path":            func(c *productServiceConfig, p string) { c.RegistrationPath = p },
		"registration_signature_path":  func(c *productServiceConfig, p string) { c.RegistrationSignaturePath = p },
		"registration_public_key_path": func(c *productServiceConfig, p string) { c.RegistrationPublicKeyPath = p },
		"catalog_path":                 func(c *productServiceConfig, p string) { c.CatalogPath = p },
		"state_root":                   func(c *productServiceConfig, p string) { c.StateRoot = p },
		"socket_path":                  func(c *productServiceConfig, p string) { c.SocketPath = p },
		"projects":                     func(c *productServiceConfig, p string) { c.Projects = map[string]string{"alpha": p} },
	}
	thesis := []string{"/etc/tbound", "/etc/tbound/registration.json", "/var/lib/tbound", "/var/lib/tbound/audit/catalog.jsonl",
		"/run/tbound", "/run/tbound/serve-pi.sock", "/run/tbound-pi", "/run/tbound-pi/x.sock"}
	for name, set := range fields {
		for _, p := range thesis {
			cfg := validProductServiceConfig()
			set(&cfg, p)
			// An extra root that contains the thesis path must not make it acceptable.
			err := cfg.validate([]string{"/etc", "/var/lib", "/run"})
			if err == nil || !strings.Contains(err.Error(), "thesis service path") {
				t.Errorf("%s=%s: %v, want a thesis path refusal", name, p, err)
			}
		}
		// Whole-component comparison: a sibling that only shares a string prefix is a product path.
		cfg := validProductServiceConfig()
		set(&cfg, "/etc/tbound-product/sibling")
		if name == "state_root" || name == "catalog_path" || name == "projects" {
			set(&cfg, "/var/lib/tbound-product/sibling")
		}
		if name == "socket_path" {
			set(&cfg, "/run/tbound-product/sibling.sock")
		}
		if err := cfg.validate(nil); err != nil {
			t.Errorf("%s: a product sibling of a thesis path was refused: %v", name, err)
		}
	}
}

func TestProductServiceConfigRefusesPathsOutsideTheProductDirectories(t *testing.T) {
	for _, p := range []string{"/tmp/catalog.jsonl", "relative/catalog.jsonl", "/var/lib/tbound-product/../tbound/x",
		"/var/lib/tbound-product//x", "/var/lib/tbound-product/x/", "/var/lib/tbound-productx/x", ""} {
		cfg := validProductServiceConfig()
		cfg.CatalogPath = p
		if err := cfg.validate(nil); err == nil {
			t.Errorf("catalog path %q was accepted", p)
		}
	}
	cfg := validProductServiceConfig()
	cfg.CatalogPath = "/tmp/x/catalog.jsonl"
	if err := cfg.validate([]string{"/tmp/x"}); err != nil {
		t.Errorf("an explicit test root was refused: %v", err)
	}
}

func TestProductServiceConfigWorkerModeAndShape(t *testing.T) {
	cfg := validProductServiceConfig()
	cfg.WorkerMode = productsession.WorkerPi
	if err := cfg.validate(nil); err == nil || !strings.Contains(err.Error(), "requires a qualified product host") {
		t.Fatalf("worker mode pi: %v", err)
	}
	for name, mutate := range map[string]func(*productServiceConfig){
		"unknown worker mode":   func(c *productServiceConfig) { c.WorkerMode = "docker" },
		"empty worker mode":     func(c *productServiceConfig) { c.WorkerMode = "" },
		"wrong schema":          func(c *productServiceConfig) { c.SchemaVersion = "tbound-product-service-config/v0" },
		"no projects":           func(c *productServiceConfig) { c.Projects = nil },
		"empty project ID":      func(c *productServiceConfig) { c.Projects = map[string]string{"": "/var/lib/tbound-product/p"} },
		"shared project dir":    func(c *productServiceConfig) { c.Projects["beta"] = c.Projects["alpha"] },
		"grace out of range":    func(c *productServiceConfig) { c.ShutdownGraceSeconds = productServiceMaxGraceSecond + 1 },
		"negative grace":        func(c *productServiceConfig) { c.ShutdownGraceSeconds = -1 },
		"missing socket":        func(c *productServiceConfig) { c.SocketPath = "" },
		"missing registration":  func(c *productServiceConfig) { c.RegistrationPath = "" },
		"missing state root":    func(c *productServiceConfig) { c.StateRoot = "" },
		"relative project path": func(c *productServiceConfig) { c.Projects = map[string]string{"alpha": "projects/alpha"} },
	} {
		cfg := validProductServiceConfig()
		mutate(&cfg)
		if err := cfg.validate(nil); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestProductServiceConfigFileIsStrict(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := `{"schema_version":"tbound-product-service-config/v1","registration_path":"/etc/tbound-product/registration.json",` +
		`"registration_signature_path":"/etc/tbound-product/registration.json.sig","registration_public_key_path":"/etc/tbound-product/registration.pub",` +
		`"catalog_path":"/var/lib/tbound-product/catalog.jsonl","state_root":"/var/lib/tbound-product",` +
		`"socket_path":"/run/tbound-product/product.sock","worker_mode":"headless-fixture",` +
		`"projects":{"alpha":"/var/lib/tbound-product/projects/alpha"}}`
	cfg, err := loadProductServiceConfig(write("good.json", good))
	if err != nil || cfg.validate(nil) != nil || cfg.Projects["alpha"] == "" {
		t.Fatalf("a valid config: %+v %v", cfg, err)
	}
	for name, content := range map[string]string{
		"unknown field": strings.Replace(good, `"worker_mode"`, `"principal":0,"worker_mode"`, 1),
		"duplicate key": strings.Replace(good, `"worker_mode":"headless-fixture"`, `"worker_mode":"headless-fixture","worker_mode":"pi"`, 1),
		"trailing data": good + `{}`,
		"not JSON":      "worker_mode: pi",
		"wrong type":    strings.Replace(good, `"projects":{"alpha":"/var/lib/tbound-product/projects/alpha"}`, `"projects":["alpha"]`, 1),
	} {
		if _, err := loadProductServiceConfig(write(name+".json", content)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(filepath.Join(dir, "good.json"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProductServiceConfig(link); err == nil {
		t.Error("a symlinked config was accepted")
	}
	if _, err := loadProductServiceConfig(write("big.json", strings.Repeat(" ", productServiceMaxConfigBytes+1))); err == nil {
		t.Error("an oversized config was accepted")
	}
}
