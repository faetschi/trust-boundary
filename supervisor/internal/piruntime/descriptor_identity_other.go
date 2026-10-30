//go:build !linux

package piruntime

import "os"

// Non-Linux builds are fixtures/tooling only for this runtime. Some platforms
// report anonymous pipe handles with colliding or non-stable FileInfo identity;
// reject direct handle aliasing above, but reserve kernel-object identity checks
// for concrete regular files/directories. Linux production uses the strict
// os.SameFile implementation in descriptor_identity_linux.go.
func sameDescriptorKernelObject(left, right os.FileInfo) bool {
	if left == nil || right == nil || (!left.Mode().IsRegular() && !left.IsDir()) ||
		(!right.Mode().IsRegular() && !right.IsDir()) {
		return false
	}
	return os.SameFile(left, right)
}
