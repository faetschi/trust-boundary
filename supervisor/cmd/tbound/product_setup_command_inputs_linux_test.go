//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

func TestProductAdminStaticBashInputsAreNotReadiness(t *testing.T) {
	f := newProductImageFixture(t)
	var service productsetup.ServiceConfig
	raw, err := os.ReadFile(f.path(productsetup.ServiceConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &service); err != nil {
		t.Fatal(err)
	}
	cfg := productsetup.Config{
		SchemaVersion: productsetup.ConfigSchema, WorkerMode: service.WorkerMode, Projects: service.Projects,
		Registration: f.path(productsetup.RegistrationPath), RegistrationSignature: f.path(productsetup.RegistrationSignaturePath),
		RegistrationPublicKey: f.path(productsetup.RegistrationPublicKeyPath), CommandBundle: f.path(productsetup.PodmanConfigDir),
		GatePolicyPaths: service.GatePolicyPaths, GitPath: service.GitPath, GitDigest: service.GitDigest, XattrCalibrationPath: service.XattrCalibrationPath,
	}
	for _, dry := range []bool{true, false} {
		root := t.TempDir()
		env := productsetup.Env{Root: root, DryRun: dry, VerifyRegistration: verifyRegistrationBytes, ValidateCommandInputs: validateProductSetupCommandInputs}
		rep, err := productsetup.Setup(env, cfg, productsetup.Candidate{})
		if err != nil {
			t.Fatalf("fresh static setup (dry=%v): %v", dry, err)
		}
		for _, call := range rep.Commands {
			if strings.Contains(call, "podman") || strings.Contains(call, "systemctl start") {
				t.Fatalf("static setup ran %s", call)
			}
		}
		if !dry {
			installed, err := os.ReadFile(root + productsetup.CommandCellTemplatePath)
			if err != nil || !bytes.Equal(installed, f.inputs.Template) {
				t.Fatalf("candidate template not installed: %v", err)
			}
		}
	}
	// The same real hook refuses a malformed proposed binding before setup writes anything.
	f.write(productsetup.CommandSeccompPath, []byte("different seccomp"))
	root := t.TempDir()
	_, err = productsetup.Setup(productsetup.Env{Root: root, VerifyRegistration: verifyRegistrationBytes, ValidateCommandInputs: validateProductSetupCommandInputs}, cfg, productsetup.Candidate{})
	if err == nil || !strings.Contains(err.Error(), "seccomp") {
		t.Fatalf("mismatched proposed bundle accepted: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refused setup wrote files: %v %v", entries, err)
	}
}
