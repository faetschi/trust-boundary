//go:build linux && (amd64 || arm64)

package wsoracle

// oPath is the Linux O_PATH open flag. The standard syscall package does not
// export it. The value 010000000 (octal) is the asm-generic layout that amd64 and
// arm64 use. Other Linux architectures use different values for some flags, so
// this file restricts the package to these two. Building for any other Linux
// architecture then fails at compile time.
const oPath = 0x200000
