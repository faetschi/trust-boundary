//go:build linux

package piruntime

import "os"

func sameDescriptorKernelObject(left, right os.FileInfo) bool {
	return os.SameFile(left, right)
}
