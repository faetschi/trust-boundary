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
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

func TestProductImageProvisionRefusesDigestSignatureAndMappingDrift(t *testing.T) {
	for _, fault := range []string{"", "registration", "archive", "rewritten manifest", "RepoDigest", "missing image", "cosign", "load", "active", "static", "mapping", "unknown store", "turn"} {
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
	t.Helper()
	cfg, r, in := productCommandStaticFixture(t)
	f := &productImageFixture{t: t, root: t.TempDir(), inputs: in}
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
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
			return []byte("LoadState=loaded\nActiveState=inactive\n"), nil
		}
		if strings.Contains(call, " verify ") {
			if f.fault == "cosign" {
				return nil, errors.New("wrong cosign key")
			}
			return []byte(`[{"critical":{"image":{"docker-manifest-digest":"` + f.digest + `"}}}]`), nil
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
