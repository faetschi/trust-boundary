//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProductD09ProbeAcceptsProductBindingOnly(t *testing.T) {
	f := newProductD09TestFixture(t)
	productD09TestSetServiceIdentity(&f.template, os.Geteuid(), os.Getegid())
	f.rebind(t)
	f.profile.CgroupRootDigest, _ = DigestD09CgroupRoot(productD09CellsHierarchyPath, os.Geteuid())
	host := &D09ProductCommandHost{loaded: true, snapshot: productD09Snapshot{
		profile: f.profile, template: f.template, serviceUID: os.Geteuid(), serviceGID: os.Getegid(),
	}}
	config, err := productD09LauncherConfig(host)
	if err != nil { t.Fatal(err) }
	if err := validateD09CellIdentityMeasurements(config); err != nil { t.Fatal(err) }
	if err := validateD09CellIdentity(config); err == nil { t.Fatal("product config passed thesis identity admission") }
	if config.TemplateDigest != f.profile.ContainmentProfileDigest || config.ManagedLiveRootRegistrationDigest != "" || config.registeredTemplate {
		t.Fatal("product config borrowed thesis template authority")
	}
	for name, mutate := range map[string]func(*D09ProductCommandHost){
		"unloaded": func(h *D09ProductCommandHost) { h.loaded = false },
		"thesis binding": func(h *D09ProductCommandHost) { h.snapshot.template.ManagedLiveRootRegistrationDigest = DigestBytes([]byte("thesis")) },
		"no product binding": func(h *D09ProductCommandHost) { h.snapshot.template.ProductRegistrationDigest = "" },
		"profile substitution": func(h *D09ProductCommandHost) { h.snapshot.profile.ContainmentProfileDigest = DigestBytes([]byte("other")) },
		"host filesystem digest": func(h *D09ProductCommandHost) { h.snapshot.profile.CgroupRootDigest, _ = DigestD09CgroupRoot(productD09CellsHostPath, os.Geteuid()) },
		"wrong cells path": func(h *D09ProductCommandHost) { h.snapshot.template.CgroupsPath = "/sys/fs/cgroup/tbound" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := *host
			mutate(&bad)
			if _, err := productD09LauncherConfig(&bad); err == nil { t.Fatal("invalid product binding accepted") }
		})
	}
	// Even a same-package constructed loaded host cannot cross the exported constructor or Probe:
	// they must reload the installed signed inputs before using this snapshot.
	if launcher, err := NewProductD09PodmanCrunLauncher(context.Background(), host, &LinuxCgroupScope{}); err == nil || launcher != nil {
		t.Fatal("constructed host bypassed installed revalidation")
	}
	config, runner := completeD09Host(t)
	launcher := newTestLauncher(config, runner)
	launcher.productHost = host
	if _, err := launcher.Probe(context.Background()); err == nil { t.Fatal("Probe skipped product revalidation") }
	if len(runner.calls) != 0 { t.Fatal("Probe ran children before product revalidation") }
}

func TestProductD09CellsInitialization(t *testing.T) {
	for _, host := range []*D09ProductCommandHost{nil, {}} {
		if root, err := PrepareProductD09CgroupRoot(context.Background(), host); err == nil || root != nil { t.Fatal("unverified host initialized cells") }
	}
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false:"cold", true:"restart"}[restart], func(t *testing.T) {
			fake := newFakeServiceCgroup(t, true)
			if restart { fake.precreateCells(t, 0o755) }
			if err := ensureCellsCgroup(fake.parent, fake.uid); err != nil { t.Fatal(err) }
			requireControllersEnabled(t, filepath.Join(fake.dir, "cgroup.subtree_control"))
			requireControllersEnabled(t, filepath.Join(fake.cells(), "cgroup.subtree_control"))
		})
	}
	for name, mutate := range map[string]func(*testing.T, *fakeServiceCgroup){
		"populated parent": func(t *testing.T, f *fakeServiceCgroup) { if err := os.WriteFile(filepath.Join(f.dir,"cgroup.procs"), []byte("1\n"), 0o600); err != nil { t.Fatal(err) } },
		"missing controllers": func(t *testing.T, f *fakeServiceCgroup) { if err := os.WriteFile(filepath.Join(f.dir,"cgroup.controllers"), []byte("cpu pids"), 0o600); err != nil { t.Fatal(err) } },
		"wrong cells mode": func(t *testing.T, f *fakeServiceCgroup) { f.precreateCells(t, 0o777) },
	} {
		t.Run(name,func(t *testing.T) { f:=newFakeServiceCgroup(t,true); mutate(t,f); if err:=ensureCellsCgroup(f.parent,f.uid); err==nil { t.Fatal("invalid cells topology accepted") } })
	}
	if err := validateProductD09CgroupPaths(productD09RunnerHierarchyPath, productD09ServiceHostPath, productD09ServiceHostPath+"/runner"); err != nil { t.Fatal(err) }
	for _, paths := range [][3]string{
		{productD09CellsHierarchyPath, productD09ServiceHostPath, productD09ServiceHostPath+"/runner"},
		{productD09RunnerHierarchyPath, "/sys/fs/cgroup/tbound-pi.service", productD09ServiceHostPath+"/runner"},
		{productD09RunnerHierarchyPath, productD09ServiceHostPath, productD09CellsHostPath},
	} { if err:=validateProductD09CgroupPaths(paths[0],paths[1],paths[2]); err==nil { t.Fatal("wrong runner/parent path accepted") } }
	f:=newFakeServiceCgroup(t,true)
	if err:=requireCgroup2Descriptor(f.parent); err==nil { t.Fatal("ordinary filesystem accepted as cgroup2") }
	if err:=ensureCellsCgroup(f.parent,f.uid+1); err==nil { t.Fatal("wrong service owner accepted") }
}

func TestProductD09AdmissionCannotAuthorizePiOrThesis(t *testing.T) {
	config, runner := completeD09Host(t)
	launcher := newTestLauncher(config,runner)
	launcher.productHost = &D09ProductCommandHost{}
	if err:=launcher.admitsFrozenLinuxProfile(validD09Profile()); !errors.Is(err,ErrProductionLauncherUnavailable) { t.Fatalf("thesis admission: %v",err) }
	if _,_,err:=launcher.StartVerifiedWithProof(context.Background(),ExecutableLaunchRequest{}); !errors.Is(err,ErrProductionLauncherUnavailable) { t.Fatalf("product Pi launch: %v",err) }
	if launcher.launchInProg { t.Fatal("product Pi attempted a launch") }
	for _, host := range []*D09ProductCommandHost{nil,{}} {
		if l,err:=NewProductD09PodmanCrunLauncher(context.Background(),host,nil); err==nil || l!=nil { t.Fatal("zero host granted product admission") }
	}
}
