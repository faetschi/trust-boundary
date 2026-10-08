package piruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

type DescriptorRole string

const (
	DescriptorWorkerExposure DescriptorRole = "worker-exposure"
	DescriptorBootstrap      DescriptorRole = "bootstrap"
	DescriptorProposalIPC    DescriptorRole = "proposal-ipc"
	DescriptorProviderBridge DescriptorRole = "provider-bridge"
	DescriptorWorkerEvents   DescriptorRole = "worker-events"
	DescriptorRuntimeBundle  DescriptorRole = "runtime-bundle"

	PiWorkerDescriptorProfile = "tbound-pi-sdk-fdmap/v1;exec=3;exposure=4;bootstrap=5;ipc=6;provider=7;events=8;bundle=9;control=0"
)

// WorkerExposureEvidence binds the exact sealed sessionrepo generation/view
// exported by the trusted sessionlaunch owner. The descriptor alone is not
// provenance; its generation, tree, manifest, D06 binding, registered view,
// filesystem identity, and mount identity are all required.
type WorkerExposureEvidence struct {
	GenerationID     string
	TreeDigest       string
	ManifestDigest   string
	BindingDigest    string
	ProvenanceDigest string
	ViewIdentity     string
	Device           uint64
	Inode            uint64
	MountID          uint64
}

// VerifiedWorkerExposure is implemented by the sessionlaunch-owned sealed
// WorkerView adapter. Export must return a fresh O_PATH descriptor for the
// selected generation; Verify must re-check durable D06 provenance and that
// this same registered view remains selected. Raw privategit Repository.Root
// handles are not valid implementations.
type VerifiedWorkerExposure interface {
	ExportVerifiedWorkerExposure(context.Context) (*os.File, WorkerExposureEvidence, error)
	VerifyWorkerExposure(context.Context, WorkerExposureEvidence) error
}

// WorkerRuntimeAssetEvidence binds the signed Pi runtime content to the
// launcher-owned read-only view created for the child namespace. Its source FD
// is Go-only; it is not itself proof that the child received a safe mount.
type WorkerRuntimeAssetEvidence struct {
	BundleDigest           string
	ViewIdentity           string
	NamespaceProfileDigest string
	Device                 uint64
	Inode                  uint64
	MountID                uint64
}

// VerifiedWorkerRuntimeAssets is implemented by the trusted runtime-bundle
// owner. The D09 launcher must reopen this source inside its controlled
// namespace and independently verify the consumer-side read-only mount before
// it creates child FD 9.
type VerifiedWorkerRuntimeAssets interface {
	ExportVerifiedWorkerRuntimeAssets(context.Context) (*os.File, WorkerRuntimeAssetEvidence, error)
	VerifyWorkerRuntimeAssets(context.Context, WorkerRuntimeAssetEvidence) error
}

func (e WorkerExposureEvidence) validateShape() error {
	if !validIdentity(e.GenerationID) || !validDigest(e.TreeDigest) || !validDigest(e.ManifestDigest) ||
		!validDigest(e.BindingDigest) || !validDigest(e.ProvenanceDigest) || !validDigest(e.ViewIdentity) ||
		e.Device == 0 || e.Inode == 0 || e.MountID == 0 {
		return errors.New("verified worker exposure evidence is incomplete")
	}
	return nil
}

func (e WorkerRuntimeAssetEvidence) validateShape() error {
	if !validDigest(e.BundleDigest) || !validDigest(e.ViewIdentity) || !validDigest(e.NamespaceProfileDigest) ||
		e.Device == 0 || e.Inode == 0 || e.MountID == 0 {
		return errors.New("verified worker runtime-asset evidence is incomplete")
	}
	return nil
}

// InheritedDescriptor is a child-side endpoint retained by the trusted host
// composition root. The file itself never crosses a JSON or environment
// boundary. ChildFD values are the descriptor numbers in the launched process.
type InheritedDescriptor struct {
	Role    DescriptorRole
	ChildFD int
	File    *os.File
}

func piWorkerDescriptorLayout() []struct {
	role DescriptorRole
	fd   int
} {
	return []struct {
		role DescriptorRole
		fd   int
	}{
		{DescriptorWorkerExposure, 4},
		{DescriptorBootstrap, 5},
		{DescriptorProposalIPC, 6},
		{DescriptorProviderBridge, 7},
		{DescriptorWorkerEvents, 8},
		{DescriptorRuntimeBundle, 9},
	}
}

func validatePiWorkerDescriptors(profile string, descriptors []InheritedDescriptor, workerExposure, runtimeBundle *os.File) error {
	if profile != PiWorkerDescriptorProfile || len(descriptors) != len(piWorkerDescriptorLayout()) {
		return errors.New("Pi worker descriptor profile or descriptor count is not admitted")
	}
	ordered := append([]InheritedDescriptor(nil), descriptors...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ChildFD < ordered[j].ChildFD })
	for index, expected := range piWorkerDescriptorLayout() {
		actual := ordered[index]
		if actual.Role != expected.role || actual.ChildFD != expected.fd || actual.File == nil {
			return fmt.Errorf("Pi worker descriptor %d does not match fixed role %q at fd %d", index, expected.role, expected.fd)
		}
		if actual.Role == DescriptorWorkerExposure && workerExposure != nil && actual.File != workerExposure {
			return errors.New("Pi worker exposure descriptor differs from the verified sealed-view handle")
		}
		if actual.Role == DescriptorRuntimeBundle && runtimeBundle != nil && actual.File != runtimeBundle {
			return errors.New("Pi worker runtime-bundle descriptor differs from the admitted bundle handle")
		}
	}
	seenFiles := make(map[*os.File]struct{}, len(ordered))
	for index, descriptor := range ordered {
		if _, duplicate := seenFiles[descriptor.File]; duplicate {
			return errors.New("Pi worker descriptor plan aliases file handles")
		}
		seenFiles[descriptor.File] = struct{}{}
		left, err := descriptor.File.Stat()
		if err != nil {
			return fmt.Errorf("stat Pi worker descriptor role %q: %w", descriptor.Role, err)
		}
		for _, previous := range ordered[:index] {
			right, err := previous.File.Stat()
			if err != nil {
				return fmt.Errorf("stat Pi worker descriptor role %q: %w", previous.Role, err)
			}
			if os.SameFile(left, right) {
				return fmt.Errorf("Pi worker descriptor roles %q and %q refer to the same kernel object", previous.Role, descriptor.Role)
			}
		}
	}
	return nil
}

func cloneInheritedDescriptors(descriptors []InheritedDescriptor) []InheritedDescriptor {
	copyOf := append([]InheritedDescriptor(nil), descriptors...)
	sort.Slice(copyOf, func(i, j int) bool { return copyOf[i].ChildFD < copyOf[j].ChildFD })
	return copyOf
}

func sameInheritedDescriptors(left, right []InheritedDescriptor) bool {
	a, b := cloneInheritedDescriptors(left), cloneInheritedDescriptors(right)
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index].Role != b[index].Role || a[index].ChildFD != b[index].ChildFD || a[index].File != b[index].File {
			return false
		}
	}
	return true
}

func descriptorRoles(descriptors []InheritedDescriptor) string {
	ordered := cloneInheritedDescriptors(descriptors)
	parts := make([]string, len(ordered))
	for index, descriptor := range ordered {
		parts[index] = fmt.Sprintf("%d=%s", descriptor.ChildFD, descriptor.Role)
	}
	return strings.Join(parts, ";")
}
