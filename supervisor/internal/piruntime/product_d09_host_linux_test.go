//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

// The product command host is trusted only as one chain: a registration signed by the fixed product key, a host
// profile signed by that key and bound to the measured product template, and a template bound back to the
// registration. Each refusal below mutates one link of a valid chain, so it proves only the rule it names.

const (
	productD09TestCellUID    = 1000
	productD09TestCellGID    = 1000
	productD09TestServiceUID = 1001
	productD09TestServiceGID = 1002
)

type productD09TestFixture struct {
	productKey   ed25519.PrivateKey
	registration productsession.Registration
	template     D09CellTemplate
	profile      HostProfile
	serviceUID   int
	serviceGID   int
}

// productD09TestInputs are the bytes the loader reads, plus the service account IDs it derives.
type productD09TestInputs struct {
	profileRaw      []byte
	profileSig      []byte
	registrationRaw []byte
	registrationSig []byte
	publicKey       []byte
	templateRaw     []byte
	serviceUID      int
	serviceGID      int
}

type productD09TestBuild func(t *testing.T, f *productD09TestFixture) productD09TestInputs

type productD09TestRefusal struct {
	name  string
	build productD09TestBuild
}

func TestProductD09HostTrustAndBindings(t *testing.T) {
	t.Run("valid signed product registration, profile and template verify together", func(t *testing.T) {
		f := newProductD09TestFixture(t)
		in := f.inputs(t)
		profile, template, err := validateProductD09Artifacts(in.profileRaw, in.profileSig, in.registrationRaw,
			in.registrationSig, in.publicKey, in.templateRaw, in.serviceUID, in.serviceGID)
		if err != nil {
			t.Fatalf("valid product artifacts refused: %v", err)
		}
		want, err := f.template.Digest()
		if err != nil {
			t.Fatal(err)
		}
		if got, err := template.Digest(); err != nil || got != want {
			t.Errorf("verified template digest = %s (%v), want %s", got, err, want)
		}
		if profile.ContainmentProfileDigest != want {
			t.Errorf("profile binds template %s, want %s", profile.ContainmentProfileDigest, want)
		}
		if template.ProductRegistrationDigest == "" || template.ProductRegistrationDigest != f.template.ProductRegistrationDigest {
			t.Errorf("template binds registration %q, want %q", template.ProductRegistrationDigest, f.template.ProductRegistrationDigest)
		}
		if template.ManagedLiveRootRegistrationDigest != "" {
			t.Errorf("product template carries managed-live-root digest %s", template.ManagedLiveRootRegistrationDigest)
		}
		if profile.Mode != ModeProduction || profile.ContainmentProfile != D09ContainmentProfile {
			t.Errorf("profile is %s/%s, want production D09", profile.Mode, profile.ContainmentProfile)
		}
		if placeholders := profile.PlaceholderFields(); len(placeholders) != 0 {
			t.Errorf("verified profile still has placeholders %v", placeholders)
		}
		if template.Config().registeredTemplate {
			t.Fatal("validating product inputs granted launcher admission")
		}
		decoded, err := DecodeProductD09CellTemplate(in.templateRaw)
		if err != nil {
			t.Fatalf("product template decoder refused the valid template: %v", err)
		}
		if got, _ := decoded.Digest(); got != want {
			t.Errorf("decoded product template digest = %s, want %s", got, want)
		}
		if _, err := DecodeD09CellTemplate(in.templateRaw); err == nil {
			t.Error("thesis template decoder accepted a product template")
		}
	})

	t.Run("signature keys", func(t *testing.T) {
		runProductD09Refusals(t, []productD09TestRefusal{
			{"registration signed by another key", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.registrationSig = productD09TestRegistrationSignature(t, f.registration, productD09TestKey(0x22))
				return in
			}},
			{"profile signed by another key", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.profileSig = productD09TestProfileSignature(t, f.profile, productD09TestKey(0x22))
				return in
			}},
			{"registration signature corrupted", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.registrationSig[0] ^= 0x01
				return in
			}},
			{"profile signature corrupted", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.profileSig[0] ^= 0x01
				return in
			}},
			{"public key is another key", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.publicKey = productD09TestPublicKey(0x22)
				return in
			}},
			{"public key is truncated", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.publicKey = in.publicKey[:ed25519.PublicKeySize-1]
				return in
			}},
		})
	})

	t.Run("digests", func(t *testing.T) {
		runProductD09Refusals(t, []productD09TestRefusal{
			{"registration body altered after signing", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.registrationRaw = productD09TestReplace(t, in.registrationRaw,
					[]byte(`"service_identity":"tbound-product-test-service"`), []byte(`"service_identity":"tbound-product-other"`))
				return in
			}},
			{"registration digest field rewritten", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				digest := productD09TestRegistrationDigest(t, f.registration, f.productKey)
				in.registrationRaw = productD09TestReplace(t, in.registrationRaw,
					[]byte(`"digest":"`+digest+`"`), []byte(`"digest":"sha256:`+strings.Repeat("0", 64)+`"`))
				return in
			}},
			{"profile body altered after signing", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.profileRaw = productD09TestReplace(t, in.profileRaw,
					[]byte(`"ID":"pi-host-profile-product-test"`), []byte(`"ID":"pi-host-profile-other"`))
				return in
			}},
			{"profile digest field rewritten", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				var decoded HostProfile
				if err := json.Unmarshal(in.profileRaw, &decoded); err != nil {
					t.Fatal(err)
				}
				decoded.Digest = "sha256:" + strings.Repeat("0", 64)
				encoded, err := json.Marshal(decoded)
				if err != nil {
					t.Fatal(err)
				}
				in.profileRaw = encoded
				return in
			}},
			{"template bytes altered after the profile was signed", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.templateRaw = productD09TestReplace(t, in.templateRaw, []byte(`"cpus":"1"`), []byte(`"cpus":"2"`))
				return in
			}},
			{"profile binds another template", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				f.profile.ContainmentProfileDigest = DigestBytes([]byte("another template"))
				return f.inputs(t)
			}},
			{"profile binds another offline verifier", productD09TestProfileCase(func(profile *HostProfile) {
				profile.OfflineVerifierDigest = DigestBytes([]byte("another offline verifier"))
			})},
			{"template bound to another registration", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.ProductRegistrationDigest = DigestBytes([]byte("another registration"))
			})},
			{"template image differs from the registration image", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.ExpectedImageDigest = DigestBytes([]byte("another image"))
			})},
			{"profile is not the D09 containment profile", productD09TestProfileCase(func(profile *HostProfile) {
				profile.ContainmentProfile = "pi-cli-uncontained/v1"
			})},
			{"profile is not production mode", productD09TestProfileCase(func(profile *HostProfile) {
				profile.Mode = ModeFixture
			})},
		})
	})

	t.Run("placeholders", func(t *testing.T) {
		runProductD09Refusals(t, []productD09TestRefusal{
			{"profile ID is the template placeholder", productD09TestProfileCase(func(profile *HostProfile) {
				profile.ID = LinuxHostProfileTemplateID
			})},
			{"profile node executable digest is a placeholder", productD09TestProfileCase(func(profile *HostProfile) {
				profile.ExecutableDigest = placeholderDigest("node-executable")
			})},
			{"profile cgroup root digest is a placeholder", productD09TestProfileCase(func(profile *HostProfile) {
				profile.CgroupRootDigest = placeholderDigest("cgroup-root")
			})},
			{"profile runtime bundle digest is a placeholder", productD09TestProfileCase(func(profile *HostProfile) {
				profile.PiRuntimeBundleDigest = placeholderDigest("pi-runtime-bundle")
			})},
			{"profile source provenance digest is a placeholder", productD09TestProfileCase(func(profile *HostProfile) {
				profile.SourceProvenanceDigest = placeholderDigest("source-provenance")
			})},
			{"profile offline verifier digest is a placeholder", productD09TestProfileCase(func(profile *HostProfile) {
				profile.OfflineVerifierDigest = placeholderDigest("cosign-offline")
			})},
			{"profile provider digest is a placeholder", productD09TestProfileCase(func(profile *HostProfile) {
				profile.ProviderProfileDigest = placeholderDigest("go-providerbridge")
			})},
			{"unmeasured default template", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				f.template = DefaultD09CellTemplate()
				f.rebind(t)
				return f.inputs(t)
			}},
			{"entrypoint digest left empty", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.EntrypointDigest = ""
			})},
		})
	})

	t.Run("fixed product paths are not thesis paths", func(t *testing.T) {
		fixed := []struct{ name, got, want string }{
			{"host profile", productsetup.CommandHostProfilePath, "/etc/tbound-product/podman/host-profile.json"},
			{"cell template", productsetup.CommandCellTemplatePath, "/etc/tbound-product/podman/d09-cell.json"},
			{"seccomp profile", productsetup.CommandSeccompPath, "/etc/tbound-product/podman/seccomp.json"},
			{"cosign public key", productsetup.CommandCosignPublicKeyPath, "/etc/tbound-product/podman/cosign.pub"},
			{"signed OCI layout", productsetup.CommandOCILayoutPath, "/etc/tbound-product/podman/cell-oci"},
			{"containers.conf", productsetup.ContainersConfPath, "/etc/tbound-product/podman/containers.conf"},
			{"storage.conf", productsetup.StorageConfPath, "/etc/tbound-product/podman/storage.conf"},
		}
		for _, p := range fixed {
			if p.got != p.want {
				t.Errorf("%s path = %s, want %s", p.name, p.got, p.want)
			}
			if productsetup.IsThesisPath(p.got) {
				t.Errorf("%s path %s is a thesis path", p.name, p.got)
			}
		}
		signature := productsetup.CommandHostProfileSignaturePath
		if filepath.Ext(signature) != ".sig" || !strings.HasPrefix(signature, "/etc/tbound-product/podman/") || productsetup.IsThesisPath(signature) {
			t.Errorf("host profile signature path %s is not a .sig under the product podman directory", signature)
		}
		if productsetup.IsThesisPath(productsession.DefaultRegistrationPublicKeyPath) {
			t.Errorf("registration key path %s is a thesis path", productsession.DefaultRegistrationPublicKeyPath)
		}
	})

	t.Run("template paths and bindings", func(t *testing.T) {
		runProductD09Refusals(t, []productD09TestRefusal{
			{"cosign key is a thesis path", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.CosignPublicKeyPath = "/etc/tbound/cosign.pub"
			})},
			{"OCI layout is a thesis path", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.OCILayoutPath = "/etc/tbound/pi-cell-oci"
			})},
			{"seccomp profile is a thesis path", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.SeccompProfilePath = "/etc/tbound/seccomp.json"
			})},
			{"cosign key is outside the fixed product path", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.CosignPublicKeyPath = "/etc/tbound-product/cosign.pub"
			})},
			{"managed-live-root digest on a product template", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.ManagedLiveRootRegistrationDigest = DigestBytes([]byte("managed-live-root-registration"))
			})},
			{"thesis-shaped template with no product registration digest", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.ManagedLiveRootRegistrationDigest = DigestBytes([]byte("managed-live-root-registration"))
				template.ProductRegistrationDigest = ""
			})},
			{"product registration digest missing", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.ProductRegistrationDigest = ""
			})},
			{"route probe template", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.RouteProbe = &D09RouteProbe{Name: D09RouteProbeName, Target: "/home/node/.profile", Control: PiWorkerCellDefaultWorkingDirectory + "/src/release.txt"}
			})},
		})
	})

	t.Run("service and in-cell account IDs", func(t *testing.T) {
		runProductD09Refusals(t, []productD09TestRefusal{
			{"verifier service UID differs from the scanner UID", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.serviceUID = f.serviceUID + 1
				return in
			}},
			{"verifier service GID differs from the scanner GID", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				in := f.inputs(t)
				in.serviceGID = f.serviceGID + 1
				return in
			}},
			{"service account is root", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				f.useServiceIdentity(t, 0, 0)
				return f.inputs(t)
			}},
			{"service UID equals the in-cell UID", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				f.useServiceIdentity(t, productD09TestCellUID, f.serviceGID)
				return f.inputs(t)
			}},
			{"service GID equals the in-cell GID", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				f.useServiceIdentity(t, f.serviceUID, productD09TestCellGID)
				return f.inputs(t)
			}},
			{"mapping digest computed for other IDs", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.UserNamespaceMappingDigest = DigestD09UserNamespaceMapping(productD09TestCellUID, productD09TestCellGID, 2001, 2002)
			})},
			{"in-cell UID is root", func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
				f.template.CellUID = 0
				f.template.UserNamespaceMappingDigest = DigestD09UserNamespaceMapping(0, productD09TestCellGID, f.serviceUID, f.serviceGID)
				f.rebind(t)
				return f.inputs(t)
			}},
			{"sub-ID range mapping profile", productD09TestTemplateCase(func(template *D09CellTemplate) {
				template.UserNamespaceMappingProfile = "subordinate-range/v1"
			})},
		})
	})

	t.Run("unit overrides", func(t *testing.T) {
		unit := productsetup.RenderUnit()
		if err := productD09ValidateUnit(unit); err != nil {
			t.Fatalf("rendered product unit refused: %v", err)
		}
		for _, c := range []struct {
			name    string
			unit    []byte
			dropIns [][]byte
		}{
			{"drop-in adds an environment override", unit, [][]byte{[]byte("[Service]\nEnvironment=HOME=/tmp\n")}},
			{"hardening value changed", productD09TestReplace(t, unit, []byte("NoNewPrivileges=yes"), []byte("NoNewPrivileges=no")), nil},
			{"directive repeated", productD09TestReplace(t, unit, []byte("NoNewPrivileges=yes\n"), []byte("NoNewPrivileges=yes\nNoNewPrivileges=no\n")), nil},
			{"directive added to the service section", productD09TestReplace(t, unit, []byte("[Service]\n"), []byte("[Service]\nExecStartPre=/bin/true\n")), nil},
			{"thesis path named in the unit", productD09TestReplace(t, unit, []byte(productsetup.StateDir), []byte("/var/lib/tbound")), nil},
		} {
			t.Run(c.name, func(t *testing.T) {
				if err := productD09ValidateUnit(c.unit, c.dropIns...); !errors.Is(err, ErrProductD09HostRefused) {
					t.Fatalf("want ErrProductD09HostRefused, got %v", err)
				}
			})
		}
	})

	t.Run("public loader takes no path and fails closed without installed inputs", func(t *testing.T) {
		var load func(context.Context) (*D09ProductCommandHost, error) = LoadProductD09CommandHost
		_ = load
		if _, err := os.Lstat(productsetup.CommandHostProfilePath); err == nil {
			t.Fatalf("product command host inputs are installed at %s; this test needs a host without them", productsetup.CommandHostProfilePath)
		}
		host, err := LoadProductD09CommandHost(context.Background())
		if host != nil {
			t.Error("loader returned a host although no product inputs are installed")
		}
		if !errors.Is(err, ErrProductD09HostRefused) {
			t.Fatalf("loader error = %v, want ErrProductD09HostRefused", err)
		}
	})

	t.Run("opaque host has no exported state and only its three methods", func(t *testing.T) {
		hostType := reflect.TypeOf(D09ProductCommandHost{})
		for i := range hostType.NumField() {
			if field := hostType.Field(i); field.IsExported() {
				t.Errorf("product command host exports field %s", field.Name)
			}
		}
		pointerType := reflect.TypeOf(&D09ProductCommandHost{})
		want := map[string]bool{"Profile": true, "Template": true, "Revalidate": true}
		if pointerType.NumMethod() != len(want) {
			t.Errorf("product command host has %d exported methods, want %d", pointerType.NumMethod(), len(want))
		}
		for i := range pointerType.NumMethod() {
			if name := pointerType.Method(i).Name; !want[name] {
				t.Errorf("product command host exports unexpected method %s", name)
			}
		}
	})

	t.Run("protected reader refuses symlinks, writable files and non-root owners", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		productD09TestWriteFile(t, target, 0o644)
		link := filepath.Join(dir, "link.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		worldWritable := filepath.Join(dir, "world-writable.json")
		productD09TestWriteFile(t, worldWritable, 0o666)
		groupWritable := filepath.Join(dir, "group-writable.json")
		productD09TestWriteFile(t, groupWritable, 0o664)
		nonRoot := filepath.Join(dir, "non-root.json")
		productD09TestWriteFile(t, nonRoot, 0o644)
		if os.Geteuid() == 0 {
			if err := os.Chown(nonRoot, 65534, 65534); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range []struct{ name, path string }{
			{"symlink", link},
			{"world-writable file", worldWritable},
			{"group-writable file", groupWritable},
			{"file owned by a non-root account", nonRoot},
			{"missing file", filepath.Join(dir, "missing.json")},
		} {
			t.Run(c.name, func(t *testing.T) {
				if _, err := productD09ReadFile(c.path, 4096); !errors.Is(err, ErrProductD09HostRefused) {
					t.Fatalf("productD09ReadFile(%s) error = %v, want ErrProductD09HostRefused", c.path, err)
				}
			})
		}
	})

	t.Run("protected ancestors refuse symlinked and writable directories", func(t *testing.T) {
		dir := t.TempDir()
		realDir := filepath.Join(dir, "real")
		if err := os.Mkdir(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(dir, "linked")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		writableDir := filepath.Join(dir, "writable")
		if err := os.Mkdir(writableDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(writableDir, 0o777); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ name, path string }{
			{"symlinked directory", filepath.Join(linkDir, "host-profile.json")},
			{"world-writable directory", filepath.Join(writableDir, "host-profile.json")},
		} {
			t.Run(c.name, func(t *testing.T) {
				if err := productD09ValidateAncestors(c.path); !errors.Is(err, ErrProductD09HostRefused) {
					t.Fatalf("productD09ValidateAncestors(%s) error = %v, want ErrProductD09HostRefused", c.path, err)
				}
			})
		}
	})

	t.Run("file metadata decides protected inputs without root", func(t *testing.T) {
		for _, c := range []struct {
			name    string
			info    productD09TestInfo
			maximum int64
			accept  bool
		}{
			{"root-owned read-only file", productD09TestInfo{mode: 0o644, size: 10}, 100, true},
			{"root-owned owner-only file", productD09TestInfo{mode: 0o600, size: 10}, 100, true},
			{"root-owned file readable by the service group", productD09TestInfo{mode: 0o640, size: 10, gid: productD09TestServiceGID}, 100, true},
			{"root-owned file exactly at the byte limit", productD09TestInfo{mode: 0o644, size: 100}, 100, true},
			{"root-owned file over the byte limit", productD09TestInfo{mode: 0o644, size: 101}, 100, false},
			{"group-writable file", productD09TestInfo{mode: 0o664, size: 10}, 100, false},
			{"world-writable file", productD09TestInfo{mode: 0o666, size: 10}, 100, false},
			{"service-owned file", productD09TestInfo{mode: 0o600, size: 10, uid: productD09TestServiceUID}, 100, false},
			{"symlink", productD09TestInfo{mode: fs.ModeSymlink | 0o777, size: 10}, 100, false},
			{"directory", productD09TestInfo{mode: fs.ModeDir | 0o755, size: 10}, 100, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				err := productD09ValidateFileInfo(c.info, c.maximum)
				if c.accept {
					if err != nil {
						t.Fatalf("protected file refused: %v", err)
					}
					return
				}
				if !errors.Is(err, ErrProductD09HostRefused) {
					t.Fatalf("want ErrProductD09HostRefused, got %v", err)
				}
			})
		}
	})

	t.Run("directory metadata decides protected ancestors without root", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			info   productD09TestInfo
			accept bool
		}{
			{"root-owned directory", productD09TestInfo{mode: fs.ModeDir | 0o755}, true},
			{"group-writable directory", productD09TestInfo{mode: fs.ModeDir | 0o775}, false},
			{"world-writable sticky directory", productD09TestInfo{mode: fs.ModeDir | fs.ModeSticky | 0o777}, false},
			{"service-owned directory", productD09TestInfo{mode: fs.ModeDir | 0o700, uid: productD09TestServiceUID}, false},
			{"symlink", productD09TestInfo{mode: fs.ModeSymlink | 0o777}, false},
			{"regular file", productD09TestInfo{mode: 0o644}, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				err := productD09ValidateDirectoryInfo(c.info)
				if c.accept {
					if err != nil {
						t.Fatalf("protected directory refused: %v", err)
					}
					return
				}
				if !errors.Is(err, ErrProductD09HostRefused) {
					t.Fatalf("want ErrProductD09HostRefused, got %v", err)
				}
			})
		}
	})
}

// runProductD09Refusals runs each case as a subtest that must end in ErrProductD09HostRefused.
func runProductD09Refusals(t *testing.T, cases []productD09TestRefusal) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newProductD09TestFixture(t)
			in := c.build(t, f)
			_, _, err := validateProductD09Artifacts(in.profileRaw, in.profileSig, in.registrationRaw, in.registrationSig,
				in.publicKey, in.templateRaw, in.serviceUID, in.serviceGID)
			if !errors.Is(err, ErrProductD09HostRefused) {
				t.Fatalf("want ErrProductD09HostRefused, got %v", err)
			}
		})
	}
}

// productD09TestProfileCase changes one profile field and re-signs; the template binding stays valid.
func productD09TestProfileCase(mutate func(profile *HostProfile)) productD09TestBuild {
	return func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
		t.Helper()
		mutate(&f.profile)
		return f.inputs(t)
	}
}

// productD09TestTemplateCase changes one template field and rebinds the profile to the new template digest,
// so only that field can cause the refusal.
func productD09TestTemplateCase(mutate func(template *D09CellTemplate)) productD09TestBuild {
	return func(t *testing.T, f *productD09TestFixture) productD09TestInputs {
		t.Helper()
		mutate(&f.template)
		f.rebind(t)
		return f.inputs(t)
	}
}

func newProductD09TestFixture(t *testing.T) *productD09TestFixture {
	t.Helper()
	fixture := &productD09TestFixture{
		productKey: productD09TestKey(0x11),
		serviceUID: productD09TestServiceUID,
		serviceGID: productD09TestServiceGID,
	}
	imageDigest := DigestBytes([]byte("product-cell-image"))
	fixture.registration = productsession.Registration{
		GitDigest:          DigestBytes([]byte("git")),
		HostIdentityDigest: DigestBytes([]byte("host")),
		SchemaVersion:      productsession.RegistrationSchema,
		RegistrationID:     "product-registration-test",
		ServiceIdentity:    "tbound-product-test-service",
		AllowedPrincipals:  []productsession.Principal{1000},
		Projects: []productsession.Project{{
			ProjectID:          "project-test",
			RootIdentityDigest: DigestBytes([]byte("root")),
			PolicyDigest:       DigestBytes([]byte("policy")),
			BashOutputPaths:    []string{},
			PublicationModes:   []string{productsession.PublicationReview},
		}},
		ProviderProfiles: []string{"provider-test"},
		Limits: productsession.Limits{
			Turn:                 productsession.Budget{HostCalls: 10, Tokens: 1000, DurationSeconds: 60},
			Session:              productsession.Budget{HostCalls: 100, Tokens: 10000, DurationSeconds: 600},
			MaxTurnsPerSession:   5,
			ReviewTimeoutSeconds: 3600,
		},
		RuntimeDigest:   DigestBytes([]byte("runtime")),
		ImageDigest:     imageDigest,
		ExtensionDigest: DigestBytes([]byte("extension")),
	}
	fixture.template = productD09TestTemplate(t, imageDigest, fixture.serviceUID, fixture.serviceGID)
	fixture.template.ProductRegistrationDigest = productD09TestRegistrationDigest(t, fixture.registration, fixture.productKey)
	fixture.profile = measuredProfile()
	fixture.profile.ID = "pi-host-profile-product-test"
	fixture.profile.OfflineVerifierDigest = OfflineVerifierDigest(fixture.template.CosignDigest, fixture.template.CosignPublicKeyDigest)
	fixture.rebind(t)
	return fixture
}

func productD09TestTemplate(t *testing.T, imageDigest string, serviceUID, serviceGID int) D09CellTemplate {
	t.Helper()
	landlockDigest, err := DigestD09LandlockRules([]string{"/usr"}, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	template := D09CellTemplate{
		Image:                       d09CellImageReference,
		ExpectedImageDigest:         imageDigest,
		CosignPublicKeyPath:         productsetup.CommandCosignPublicKeyPath,
		CosignPublicKeyDigest:       DigestBytes([]byte("cosign-public-key")),
		OCILayoutPath:               productsetup.CommandOCILayoutPath,
		SeccompProfilePath:          productsetup.CommandSeccompPath,
		SeccompProfileDigest:        DigestBytes([]byte("seccomp")),
		LandlockABI:                 D09LandlockABI,
		LandlockRulesetDigest:       landlockDigest,
		LandlockROPath:              "/usr",
		LandlockRWPath:              "/work",
		FrozenCapabilities:          d09EmptyCapabilitySet,
		EntrypointPath:              d09EntrypointPath,
		EntrypointDigest:            DigestBytes([]byte("entrypoint")),
		Runtime:                     "crun",
		CgroupsPath:                 "/sys/fs/cgroup/system.slice/tbound-product.service/cells",
		CPUs:                        "1",
		PidsMax:                     256,
		MemoryMB:                    1024,
		PodmanPath:                  "/usr/bin/podman",
		PodmanDigest:                DigestBytes([]byte("podman")),
		CrunPath:                    "/usr/bin/crun",
		CrunDigest:                  DigestBytes([]byte("crun")),
		CosignPath:                  "/usr/bin/cosign",
		CosignDigest:                DigestBytes([]byte("cosign")),
		CommandExecutable:           "/bin/bash",
		CommandExecutableDigest:     DigestBytes([]byte("bash")),
		CellUID:                     productD09TestCellUID,
		CellGID:                     productD09TestCellGID,
		NamespaceProfile:            "user,mount,pid,ipc,uts,network-none/v1",
		CgroupVersion:               "v2",
		CgroupControllers:           "cpu,memory,pids",
		XattrVisibilityAuthority:    D09XattrVisibilityAuthority,
		XattrFilesystemMagic:        0xef53,
		XattrMountOptionsDigest:     DigestBytes([]byte("ro,relatime")),
		XattrScanPrivilegeProfile:   D09XattrScanPrivilegeProfile,
		UserNamespaceMappingProfile: D09UserNamespaceMappingProfile,
	}
	productD09TestSetServiceIdentity(&template, serviceUID, serviceGID)
	return template
}

// productD09TestSetServiceIdentity sets every service-account digest that depends on the scanner IDs.
func productD09TestSetServiceIdentity(template *D09CellTemplate, serviceUID, serviceGID int) {
	template.XattrScanUID, template.XattrScanGID = serviceUID, serviceGID
	template.XattrVisibilityProfileDigest = DigestD09XattrVisibilityProfile(template.XattrFilesystemMagic,
		template.XattrMountOptionsDigest, serviceUID, serviceGID, D09XattrScanPrivilegeProfile)
	template.UserNamespaceMappingDigest = DigestD09UserNamespaceMapping(template.CellUID, template.CellGID, serviceUID, serviceGID)
}

// rebind keeps the profile's containment digest equal to the current template digest.
func (f *productD09TestFixture) rebind(t *testing.T) {
	t.Helper()
	digest, err := f.template.Digest()
	if err != nil {
		t.Fatalf("digest product template: %v", err)
	}
	f.profile.ContainmentProfileDigest = digest
}

func (f *productD09TestFixture) useServiceIdentity(t *testing.T, serviceUID, serviceGID int) {
	t.Helper()
	f.serviceUID, f.serviceGID = serviceUID, serviceGID
	productD09TestSetServiceIdentity(&f.template, serviceUID, serviceGID)
	f.rebind(t)
}

// inputs signs the fixture as it stands. It never rebinds, so a case that breaks a binding breaks only that one.
func (f *productD09TestFixture) inputs(t *testing.T) productD09TestInputs {
	t.Helper()
	registrationRaw, registrationSig, err := productsession.SignRegistration(f.registration, f.productKey)
	if err != nil {
		t.Fatalf("sign product registration: %v", err)
	}
	profileRaw, profileSig, _, err := SignLinuxHostProfile(f.profile, f.productKey)
	if err != nil {
		t.Fatalf("sign product host profile: %v", err)
	}
	templateRaw, err := json.Marshal(f.template)
	if err != nil {
		t.Fatalf("encode product template: %v", err)
	}
	return productD09TestInputs{
		profileRaw:      profileRaw,
		profileSig:      profileSig,
		registrationRaw: registrationRaw,
		registrationSig: registrationSig,
		publicKey:       []byte(f.productKey.Public().(ed25519.PublicKey)),
		templateRaw:     templateRaw,
		serviceUID:      f.serviceUID,
		serviceGID:      f.serviceGID,
	}
}

func productD09TestKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

func productD09TestPublicKey(seed byte) []byte {
	return []byte(productD09TestKey(seed).Public().(ed25519.PublicKey))
}

func productD09TestRegistrationDigest(t *testing.T, registration productsession.Registration, key ed25519.PrivateKey) string {
	t.Helper()
	data, signature, err := productsession.SignRegistration(registration, key)
	if err != nil {
		t.Fatalf("sign product registration: %v", err)
	}
	verified, err := productsession.VerifyRegistration(data, signature, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatalf("verify product registration: %v", err)
	}
	return verified.Digest
}

func productD09TestRegistrationSignature(t *testing.T, registration productsession.Registration, key ed25519.PrivateKey) []byte {
	t.Helper()
	_, signature, err := productsession.SignRegistration(registration, key)
	if err != nil {
		t.Fatalf("sign product registration: %v", err)
	}
	return signature
}

func productD09TestProfileSignature(t *testing.T, profile HostProfile, key ed25519.PrivateKey) []byte {
	t.Helper()
	_, signature, _, err := SignLinuxHostProfile(profile, key)
	if err != nil {
		t.Fatalf("sign product host profile: %v", err)
	}
	return signature
}

// productD09TestReplace fails the test if old is absent, so a mutation cannot silently change nothing.
func productD09TestReplace(t *testing.T, raw, old, replacement []byte) []byte {
	t.Helper()
	if !bytes.Contains(raw, old) {
		t.Fatalf("mutation target %q is absent", old)
	}
	return bytes.Replace(raw, old, replacement, 1)
}

func productD09TestWriteFile(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Chmod after writing, so the umask cannot hide the permission bit under test.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// productD09TestInfo is file metadata for a trust decision, so the root-owned accept path runs without root.
type productD09TestInfo struct {
	mode fs.FileMode
	size int64
	uid  uint32
	gid  uint32
}

func (i productD09TestInfo) Name() string       { return "product-input" }
func (i productD09TestInfo) Size() int64        { return i.size }
func (i productD09TestInfo) Mode() fs.FileMode  { return i.mode }
func (i productD09TestInfo) ModTime() time.Time { return time.Time{} }
func (i productD09TestInfo) IsDir() bool        { return i.mode.IsDir() }
func (i productD09TestInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid, Gid: i.gid} }

func TestThesisD09DecoderRefusesEvenEmptyProductMember(t *testing.T) {
	template := measuredRouteProbeTemplate(t)
	raw, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"product_registration_digest":""}`)...)
	if _, err := DecodeD09CellTemplate(raw); err == nil {
		t.Fatal("thesis decoder accepted an explicitly present product member")
	}
}

func TestProductD09ScannerMatchesTemplateAndCalibration(t *testing.T) {
	fixture := newProductD09TestFixture(t)
	template := fixture.template
	observed := D09XattrVisibilityMeasurements{
		FilesystemMagic: template.XattrFilesystemMagic, MountOptionsDigest: template.XattrMountOptionsDigest,
		ScanUID: fixture.serviceUID, ScanGID: fixture.serviceGID, ScanPrivilegeProfile: template.XattrScanPrivilegeProfile,
		NoNewPrivs: true, InitialUserNamespace: true, EffectiveCapabilities: "0000000000000000",
	}
	calibration := D09XattrEnumerationCalibration{
		Authority: template.XattrVisibilityAuthority, FilesystemMagic: observed.FilesystemMagic,
		MountOptionsDigest: observed.MountOptionsDigest, ScanUID: observed.ScanUID, ScanGID: observed.ScanGID,
		ScanPrivilegeProfile: observed.ScanPrivilegeProfile, VisibilityProfileDigest: template.XattrVisibilityProfileDigest,
		SourceAccessProfile: D09XattrManagedRWSourceProfile,
	}
	if err := productD09ValidateStateScanner(observed, calibration, template, fixture.serviceUID, fixture.serviceGID); err != nil {
		t.Fatalf("matching scanner: %v", err)
	}
	for _, field := range []string{"filesystem", "mount", "visibility", "uid", "gid", "privilege"} {
		t.Run(field, func(t *testing.T) {
			changed := template
			switch field {
			case "filesystem":
				changed.XattrFilesystemMagic++
			case "mount":
				changed.XattrMountOptionsDigest = DigestBytes([]byte("another mount"))
			case "visibility":
				changed.XattrVisibilityProfileDigest = DigestBytes([]byte("another visibility profile"))
			case "uid":
				changed.XattrScanUID++
			case "gid":
				changed.XattrScanGID++
			case "privilege":
				changed.XattrScanPrivilegeProfile = "other"
			}
			if err := productD09ValidateStateScanner(observed, calibration, changed, fixture.serviceUID, fixture.serviceGID); err == nil {
				t.Fatal("template/actual scanner mismatch accepted")
			}
		})
	}
}

func TestProductD09SetupManagedRoots(t *testing.T) {
	service := productsetup.ServiceConfig{
		StateRoot: productsetup.TurnsDir,
		Projects:  map[string]string{"project": productsetup.ProjectsDir + "/project"},
	}
	if err := productD09ValidateServiceRoots(service); err != nil {
		t.Fatalf("setup's managed roots refused: %v", err)
	}
	for _, root := range []string{productsetup.StateDir, productsetup.ConfigDir, productsetup.RuntimeDir,
		productsetup.ProjectsDir, "/var/lib/tbound/project", productsetup.ProjectsDir + "/../turns", "/tmp/project"} {
		t.Run(root, func(t *testing.T) {
			changed := service
			changed.Projects = map[string]string{"project": root}
			if err := productD09ValidateServiceRoots(changed); !errors.Is(err, ErrProductD09HostRefused) {
				t.Fatalf("project outside the managed projects tree accepted: %v", err)
			}
		})
	}
}

func TestProductD09HostZeroValueHasNoAuthority(t *testing.T) {
	for _, host := range []*D09ProductCommandHost{nil, {}} {
		if host.Profile() != (HostProfile{}) || !reflect.DeepEqual(host.Template(), D09CellTemplate{}) {
			t.Fatal("unloaded host returned trusted inputs")
		}
		if err := host.Revalidate(context.Background()); !errors.Is(err, ErrProductD09HostRefused) {
			t.Fatalf("unloaded host revalidated: %v", err)
		}
	}
}

func TestProductD09ServiceShellIsSetupNologin(t *testing.T) {
	if !productD09IsNologinShell("/usr/sbin/nologin") {
		t.Fatal("setup nologin shell refused")
	}
	for _, shell := range []string{"/var/lib/tbound-product/nologin", "nologin", "/tmp/false", "/bin/bash", ""} {
		if productD09IsNologinShell(shell) {
			t.Fatalf("untrusted service shell accepted: %q", shell)
		}
	}
}

func TestProductD09EffectiveUnitRefusesOverridesAndPendingReload(t *testing.T) {
	properties := map[string]string{
		"LoadState": "loaded", "NeedDaemonReload": "no", "FragmentPath": productsetup.UnitPath,
		"DropInPaths": "", "Environment": "", "EnvironmentFiles": "",
		"User": productsetup.ServiceUser, "Group": productsetup.ServiceGroup,
	}
	if err := productD09ValidateEffectiveUnit(properties); err != nil {
		t.Fatalf("installed effective unit refused: %v", err)
	}
	for property := range properties {
		t.Run(property, func(t *testing.T) {
			changed := make(map[string]string, len(properties))
			for key, value := range properties {
				changed[key] = value
			}
			changed[property] = "unexpected"
			if err := productD09ValidateEffectiveUnit(changed); !errors.Is(err, ErrProductD09HostRefused) {
				t.Fatalf("effective unit drift accepted: %v", err)
			}
			delete(changed, property)
			if err := productD09ValidateEffectiveUnit(changed); !errors.Is(err, ErrProductD09HostRefused) {
				t.Fatalf("missing effective unit property accepted: %v", err)
			}
		})
	}
}
