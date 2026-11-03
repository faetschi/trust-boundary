//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

func TestProductImageProvisionRefusesDigestSignatureAndMappingDrift(t *testing.T) {
	for _, fault := range []string{"", "registration", "archive", "signed manifest", "rewritten manifest", "RepoDigest", "missing image", "cosign", "cosign claim", "load", "active", "static", "mapping", "unknown store", "turn", "global override", "shadow unit", "effective environment"} {
		t.Run(fault, func(t *testing.T) {
			f := newProductImageFixture(t)
			switch fault {
			case "registration":
				var r productsession.Registration
				json.Unmarshal(f.inputs.Registration, &r)
				r.ImageDigest = piruntime.DigestBytes([]byte("another image"))
				raw, sig, err := productsession.SignRegistration(r, ed25519.NewKeyFromSeed(make([]byte, 32)))
				if err != nil {
					t.Fatal(err)
				}
				f.write(productsetup.RegistrationPath, raw)
				f.write(productsetup.RegistrationSignaturePath, []byte(base64.StdEncoding.EncodeToString(sig)))
			case "archive":
				os.WriteFile(f.archive, []byte("not an OCI archive"), 0644)
			case "signed manifest":
				os.WriteFile(filepath.Join(f.layout, "blobs/sha256/"+strings.TrimPrefix(f.digest, "sha256:")), []byte("rewritten manifest bytes"), 0644)
			case "global override":
				f.write("/etc/systemd/system/service.d/override.conf", []byte("[Service]\nPrivateUsers=yes\n"))
			case "shadow unit":
				f.write("/run/systemd/generator/"+productsetup.UnitName, productsetup.RenderUnit())
			case "static":
				os.Remove(f.path(productsetup.CommandCellTemplatePath))
			case "mapping":
				f.write("/etc/subuid", []byte("tbound-product:100000:65536\n"))
			case "unknown store":
				f.write(productsetup.PodmanGraphRootDir+"/old", []byte("old"))
			case "turn":
				f.env.Turns = func() ([]productsetup.OpenTurn, error) {
					return []productsetup.OpenTurn{{ID: "unresolved", State: "unknown"}}, nil
				}
			}
			f.fault = fault
			var out bytes.Buffer
			err := productImageProvision(f.env, f.archive, f.layout, &out)
			if fault == "" && err != nil {
				t.Fatalf("provision: %v\n%s", err, out.String())
			}
			if fault != "" && err == nil {
				t.Fatalf("accepted %s", fault)
			}
			if fault != "" && strings.Contains(out.String(), "provisioned") {
				t.Fatal("failure reported activation")
			}
			for _, staticFault := range []string{"registration", "archive", "signed manifest", "active", "static", "mapping", "unknown store", "turn", "global override", "shadow unit", "effective environment", "cosign", "cosign claim"} {
				if fault == staticFault && strings.Contains(strings.Join(f.calls, "\n"), "load --input") {
					t.Fatalf("load reached after %s", fault)
				}
			}
			for _, call := range f.calls {
				if strings.Contains(call, "pull") || strings.Contains(call, "systemctl start") || strings.Contains(call, "systemctl restart") {
					t.Fatalf("unsafe command: %s", call)
				}
			}
			if fault == "" {
				if !strings.Contains(out.String(), f.digest) || !strings.Contains(strings.Join(f.calls, "\n"), "load --input") {
					t.Fatalf("no verified load: %s %v", out.String(), f.calls)
				}
				for _, call := range f.calls {
					if strings.HasPrefix(call, "systemd-run ") {
						for _, want := range []string{"NoNewPrivileges=yes", "PrivateNetwork=yes", "DelegateSubgroup=runner", "CONTAINERS_STORAGE_CONF=", "env -i"} {
							if !strings.Contains(call, want) {
								t.Fatalf("context missing %s: %s", want, call)
							}
						}
					}
				}
			}
		})
	}
}

func TestProductImageProvisionPublicStagesIgnoreRootUmask(t *testing.T) {
	f := newProductImageFixture(t)
	f.write("/etc/group", []byte("tbound-product:x:1002:alice,bob\n"))
	previous := syscall.Umask(0077)
	defer syscall.Umask(previous)
	var out bytes.Buffer
	if err := productImageProvision(f.env, f.archive, f.layout, &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{productsetup.CommandOCILayoutPath + "/oci-layout", productImageStoreRecord} {
		info, err := os.Stat(f.path(name))
		if err != nil || info.Mode().Perm() != 0644 {
			t.Fatalf("unreadable public file %s: %v %v", name, info, err)
		}
	}
	for _, name := range []string{productsetup.CommandOCILayoutPath, productsetup.CommandOCILayoutPath + "/blobs", productsetup.CommandOCILayoutPath + "/blobs/sha256"} {
		info, err := os.Stat(f.path(name))
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatalf("unreadable public directory %s: %v %v", name, info, err)
		}
	}
}

func TestProductImageProvisionDryRunAndTemplateDigest(t *testing.T) {
	f := newProductImageFixture(t)
	f.env.DryRun = true
	var out bytes.Buffer
	if err := productImageProvision(f.env, f.archive, f.layout, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "systemctl show") {
		t.Fatalf("dry run executed import: %v", f.calls)
	}
	if _, err := os.Stat(f.path(productsetup.CommandOCILayoutPath)); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote layout: %v", err)
	}
	out.Reset()
	if err := productImageTemplateDigestCommand([]string{"--template", f.path(productsetup.CommandCellTemplatePath)}, &out); err != nil {
		t.Fatal(err)
	}
	template, _ := piruntime.DecodeProductD09CellTemplate(f.inputs.Template)
	want, _ := template.Digest()
	if strings.TrimSpace(out.String()) != want {
		t.Fatalf("digest %q != %q", out.String(), want)
	}
}

func TestProductImageProvisionRejectsEffectiveUnitOverrides(t *testing.T) {
	for _, fault := range []string{"NeedDaemonReload=yes", "DropInPaths=/etc/systemd/system/service.d/global.conf", "EnvironmentFiles=/etc/environment", "FragmentPath=/run/systemd/generator/tbound-product.service", "User=root", "Group=root", "environment tab", "environment double space", "environment duplicate"} {
		t.Run(fault, func(t *testing.T) {
			f := newProductImageFixture(t)
			exec := f.env.Exec
			f.env.Exec = func(name string, args ...string) ([]byte, error) {
				raw, err := exec(name, args...)
				if name != "systemctl" {
					t.Fatal("transient command reached despite effective unit drift")
				}
				if strings.HasPrefix(fault, "environment") {
					want := strings.Join(productImageEnvironment(), " ")
					changed := strings.Replace(want, " ", "\t", 1)
					if fault == "environment double space" {
						changed = strings.Replace(want, " ", "  ", 1)
					} else if fault == "environment duplicate" {
						changed = want + " " + productImageEnvironment()[0]
					}
					return []byte(strings.Replace(string(raw), want, changed, 1)), err
				}
				key, _, _ := strings.Cut(fault, "=")
				lines := strings.Split(string(raw), "\n")
				for i, line := range lines {
					if strings.HasPrefix(line, key+"=") {
						lines[i] = fault
					}
				}
				return []byte(strings.Join(lines, "\n")), err
			}
			if err := productImageProvision(f.env, f.archive, f.layout, &bytes.Buffer{}); err == nil {
				t.Fatal("effective unit drift accepted")
			}
		})
	}
}

func TestProductImageReplacementRetainsSignedHistory(t *testing.T) {
	f := newProductImageFixture(t)
	if err := productImageProvision(f.env, f.archive, f.layout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	oldDigest := f.digest
	candidate := newProductImageFixtureWithConfig(t, []byte(`{"architecture":"amd64","os":"linux","config":{"Labels":{"version":"2"}}}`))
	if err := filepath.WalkDir(candidate.root, func(name string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(candidate.root, name)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(name)
		if err == nil {
			f.write("/"+rel, raw)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.digest, f.configID, f.inputs = candidate.digest, candidate.configID, candidate.inputs
	if err := productImageProvision(f.env, f.archive, f.layout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, digest string }{
		{productsetup.CommandOCILayoutPath, f.digest},
		{productsetup.PodmanConfigDir + "/images/" + strings.TrimPrefix(oldDigest, "sha256:"), oldDigest},
	} {
		files, err := productOCILayout(f.root, f.path(tc.path))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := productOCIIdentity(files, tc.digest); err != nil {
			t.Fatalf("retained/current image: %v", err)
		}
	}
	entries, err := os.ReadDir(f.path(productsetup.PodmanConfigDir + "/hosts"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("signed host history lost: %v %v", entries, err)
	}
	if err := productImageProvision(f.env, f.archive, f.layout, &bytes.Buffer{}); err != nil {
		t.Fatalf("repeat import: %v", err)
	}
}

type productImageFixture struct {
	t                                              *testing.T
	root, archive, layout, digest, configID, fault string
	inputs                                         productCommandInputs
	env                                            productsetup.Env
	calls                                          []string
}

func (f *productImageFixture) path(p string) string { return filepath.Join(f.root, p) }
func (f *productImageFixture) write(p string, b []byte) {
	f.t.Helper()
	p = f.path(p)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0644); err != nil {
		f.t.Fatal(err)
	}
}
func newProductImageFixture(t *testing.T) *productImageFixture {
	return newProductImageFixtureWithConfig(t, []byte(`{"architecture":"amd64","os":"linux"}`))
}

func newProductImageFixtureWithConfig(t *testing.T, config []byte) *productImageFixture {
	t.Helper()
	cfg, r, in := productCommandStaticFixture(t)
	f := &productImageFixture{t: t, root: t.TempDir(), inputs: in}
	f.configID = piruntime.DigestBytes(config)
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + f.configID + `","size":` + fmtInt(len(config)) + `},"layers":[]}`)
	f.digest = piruntime.DigestBytes(manifest)
	r.ImageDigest = f.digest
	var err error
	var sig []byte
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	f.inputs.Registration, sig, err = productsession.SignRegistration(r, key)
	if err != nil {
		t.Fatal(err)
	}
	f.inputs.RegistrationSignature = []byte(base64.StdEncoding.EncodeToString(sig))
	json.Unmarshal(f.inputs.Registration, &r)
	template, _ := piruntime.DecodeProductD09CellTemplate(in.Template)
	template.ExpectedImageDigest = f.digest
	template.ProductRegistrationDigest = r.Digest
	f.inputs.Template, _ = json.Marshal(template)
	profile, err := piruntime.VerifyLinuxSignedHostProfile(in.Profile, mustDecodeImage64(t, in.ProfileSignature), key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	profile.ContainmentProfileDigest, _ = template.Digest()
	f.inputs.Profile, sig, _, err = piruntime.SignLinuxHostProfile(profile, key)
	if err != nil {
		t.Fatal(err)
	}
	f.inputs.ProfileSignature = []byte(base64.StdEncoding.EncodeToString(sig))
	cfg.RegistrationPath = productsetup.RegistrationPath
	cfg.RegistrationSignaturePath = productsetup.RegistrationSignaturePath
	cfg.RegistrationPublicKeyPath = productsetup.RegistrationPublicKeyPath
	cfg.StateRoot = productsetup.TurnsDir
	cfg.CatalogPath = productsetup.CatalogPath
	cfg.SocketPath = productsetup.SocketPath
	for id := range cfg.Projects {
		cfg.Projects[id] = productsetup.ProjectsDir + "/" + id
	}
	raw, _ := json.Marshal(cfg)
	f.write(productsetup.ServiceConfigPath, raw)
	for p, b := range map[string][]byte{productsetup.RegistrationPath: f.inputs.Registration, productsetup.RegistrationSignaturePath: f.inputs.RegistrationSignature, productsetup.RegistrationPublicKeyPath: in.PublicKey, productsetup.CommandHostProfilePath: f.inputs.Profile, productsetup.CommandHostProfileSignaturePath: f.inputs.ProfileSignature, productsetup.CommandCellTemplatePath: f.inputs.Template, productsetup.CommandSeccompPath: in.Seccomp, productsetup.CommandCosignPublicKeyPath: in.CosignPublicKey, productsetup.UnitPath: in.Unit, productsetup.ContainersConfPath: productsetup.RenderProductContainersConf(), productsetup.StorageConfPath: productsetup.RenderProductStorageConf(), "/etc/passwd": []byte("tbound-product:x:1001:1002::/var/lib/tbound-product:/usr/sbin/nologin\n"), "/etc/group": []byte("tbound-product:x:1002:\n")} {
		f.write(p, b)
	}
	for _, dir := range []string{productsetup.PodmanHomeDir, productsetup.PodmanGraphRootDir} {
		os.MkdirAll(f.path(dir), 0700)
	}
	for _, tool := range []string{"podman", "crun", "cosign"} {
		f.write("/usr/bin/"+tool, []byte(tool))
		os.Chmod(f.path("/usr/bin/"+tool), 0755)
	}
	f.layout = f.path("/incoming/signed")
	f.archive = f.path("/incoming/cell.tar")
	files := map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`), "index.json": []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + f.digest + `","size":` + fmtInt(len(manifest)) + `}]}`), "blobs/sha256/" + strings.TrimPrefix(f.digest, "sha256:"): manifest, "blobs/sha256/" + strings.TrimPrefix(f.configID, "sha256:"): config}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for name, b := range files {
		f.write("/incoming/signed/"+name, b)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(b))})
		tw.Write(b)
	}
	tw.Close()
	os.WriteFile(f.archive, archive.Bytes(), 0644)
	f.env = productsetup.Env{Root: f.root, Turns: func() ([]productsetup.OpenTurn, error) { return nil, nil }, Exec: func(name string, args ...string) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		f.calls = append(f.calls, call)
		if name == "systemctl" {
			if f.fault == "active" {
				return []byte("LoadState=loaded\nActiveState=active\n"), nil
			}
			environment := strings.Join(productImageEnvironment(), " ")
			if f.fault == "effective environment" {
				environment = "HOME=/tmp"
			}
			return []byte("LoadState=loaded\nActiveState=inactive\nNeedDaemonReload=no\nFragmentPath=" + productsetup.UnitPath + "\nDropInPaths=\nEnvironmentFiles=\nUser=tbound-product\nGroup=tbound-product\nEnvironment=" + environment + "\n"), nil
		}
		if strings.Contains(call, " verify ") {
			if f.fault == "cosign" {
				return nil, errors.New("wrong cosign key")
			}
			digest := f.digest
			if f.fault == "cosign claim" {
				digest = piruntime.DigestBytes([]byte("wrong cosign claim"))
			}
			return []byte(`[{"critical":{"image":{"docker-manifest-digest":"` + digest + `"}}}]`), nil
		}
		if strings.Contains(call, " load ") && f.fault == "load" {
			return nil, errors.New("failed load")
		}
		if strings.Contains(call, " image exists ") && f.fault == "missing image" {
			return nil, errors.New("missing image")
		}
		if strings.Contains(call, " image inspect ") {
			digest, repo := f.digest, f.digest
			if f.fault == "rewritten manifest" {
				digest = piruntime.DigestBytes([]byte("rewrite"))
			}
			if f.fault == "RepoDigest" {
				repo = piruntime.DigestBytes([]byte("wrong repo"))
			}
			return []byte(`[{"Id":"` + f.configID + `","Digest":"` + digest + `","RepoDigests":["localhost/tbound/pi-cell@` + repo + `"]}]`), nil
		}
		return nil, nil
	}}
	return f
}
func mustDecodeImage64(t *testing.T, b []byte) []byte {
	t.Helper()
	v, e := base64.StdEncoding.DecodeString(string(b))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func fmtInt(n int) string { b, _ := json.Marshal(n); return string(b) }
