//go:build windows

package openrouter

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

const (
	seFileObject             = 1
	ownerSecurityInformation = 0x1
	daclSecurityInformation  = 0x4
	aclSizeInformation       = 2
	accessAllowedACEType     = 0
	accessDeniedACEType      = 1
	fileFlagSequentialScan   = 0x08000000
)

var (
	advapi32ForCredential = syscall.NewLazyDLL("advapi32.dll")
	procGetSecurityInfo   = advapi32ForCredential.NewProc("GetSecurityInfo")
	procGetACLInformation = advapi32ForCredential.NewProc("GetAclInformation")
	procGetACE            = advapi32ForCredential.NewProc("GetAce")
)

type credentialACLSizeInformation struct {
	AceCount      uint32
	AclBytesInUse uint32
	AclBytesFree  uint32
}

type credentialACEHeader struct {
	AceType  byte
	AceFlags byte
	AceSize  uint16
}

type credentialACLHeader struct {
	AclRevision byte
	Sbz1        byte
	AclSize     uint16
	AceCount    uint16
	Sbz2        uint16
}

func readSecureCredentialFile(path string) ([]byte, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		name,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT|fileFlagSequentialScan,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), "openrouter-credential")
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, os.ErrInvalid
	}
	defer file.Close()

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&(syscall.FILE_ATTRIBUTE_DIRECTORY|syscall.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return nil, os.ErrPermission
	}
	fileInfo, err := file.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() {
		return nil, os.ErrPermission
	}
	if err := verifyCredentialFileOwnerAndDACL(handle); err != nil {
		return nil, os.ErrPermission
	}
	content, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(content) > maxCredentialBytes {
		return nil, os.ErrInvalid
	}
	return content, nil
}

func verifyCredentialFileOwnerAndDACL(handle syscall.Handle) error {
	var owner *syscall.SID
	var dacl *byte
	var descriptor uintptr
	result, _, _ := procGetSecurityInfo.Call(
		uintptr(handle), seFileObject, ownerSecurityInformation|daclSecurityInformation,
		uintptr(unsafe.Pointer(&owner)), 0, uintptr(unsafe.Pointer(&dacl)), 0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if result != 0 {
		return syscall.Errno(result)
	}
	defer syscall.LocalFree(syscall.Handle(descriptor))
	if owner == nil || dacl == nil {
		return os.ErrPermission
	}

	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return os.ErrPermission
	}
	ownerSID, err := owner.String()
	if err != nil {
		return err
	}
	currentSID, err := user.User.Sid.String()
	if err != nil || ownerSID != currentSID {
		return os.ErrPermission
	}

	var aclInfo credentialACLSizeInformation
	result, _, callErr := procGetACLInformation.Call(
		uintptr(unsafe.Pointer(dacl)), uintptr(unsafe.Pointer(&aclInfo)), unsafe.Sizeof(aclInfo), aclSizeInformation,
	)
	if result == 0 {
		if callErr != nil {
			return callErr
		}
		return os.ErrPermission
	}
	aclBase := uintptr(unsafe.Pointer(dacl))
	aclBytesInUse := uintptr(aclInfo.AclBytesInUse)
	aclHeaderSize := unsafe.Sizeof(credentialACLHeader{})
	if aclBytesInUse < aclHeaderSize {
		return os.ErrPermission
	}
	aclSize := uintptr((*credentialACLHeader)(unsafe.Pointer(dacl)).AclSize)
	if !credentialACLHasValidExtent(aclBytesInUse, aclSize) {
		return os.ErrPermission
	}
	var previousACEEnd uintptr
	for index := uint32(0); index < aclInfo.AceCount; index++ {
		var ace *credentialACEHeader
		result, _, _ = procGetACE.Call(uintptr(unsafe.Pointer(dacl)), uintptr(index), uintptr(unsafe.Pointer(&ace)))
		if result == 0 || ace == nil {
			return os.ErrPermission
		}
		aceAddress := uintptr(unsafe.Pointer(ace))
		if !credentialACLRangeFits(aclBase, aclBytesInUse, aceAddress, aclHeaderSize) {
			return os.ErrPermission
		}
		aceOffset := aceAddress - aclBase
		if aceOffset < previousACEEnd {
			return os.ErrPermission
		}
		aceSize := uintptr(ace.AceSize)
		if aceSize < aclHeaderSize || !credentialACLRangeFits(aclBase, aclBytesInUse, aceAddress, aceSize) {
			return os.ErrPermission
		}
		previousACEEnd = aceOffset + aceSize
		switch ace.AceType {
		case accessDeniedACEType:
			continue
		case accessAllowedACEType:
			const sidOffset = unsafe.Sizeof(credentialACEHeader{}) + unsafe.Sizeof(uint32(0))
			if !credentialACESIDFits(ace, sidOffset) {
				return os.ErrPermission
			}
			mask := *(*uint32)(unsafe.Add(unsafe.Pointer(ace), unsafe.Sizeof(credentialACEHeader{})))
			const (
				genericAll     = 0x10000000
				genericExecute = 0x20000000
				fileExecute    = 0x00000020
			)
			if mask&(genericAll|genericExecute|fileExecute) != 0 {
				return os.ErrPermission
			}
			aceSID := (*syscall.SID)(unsafe.Add(unsafe.Pointer(ace), sidOffset))
			aceSIDString, err := aceSID.String()
			if err != nil || aceSIDString != currentSID {
				return os.ErrPermission
			}
		default:
			// Unknown/callback ACE forms can carry conditional grants; fail closed.
			return os.ErrPermission
		}
	}
	return nil
}

const credentialSIDHeaderSize = 8

// credentialACLHasValidExtent verifies the size reported by GetAclInformation
// against both the ACL header and the ACL's declared allocation size.
func credentialACLHasValidExtent(aclBytesInUse, aclSize uintptr) bool {
	headerSize := unsafe.Sizeof(credentialACLHeader{})
	return aclSize >= headerSize && aclBytesInUse >= headerSize && aclBytesInUse <= aclSize
}

// credentialACLRangeFits checks [address, address+length) without forming an
// end address, so neither pointer arithmetic nor integer overflow can expand
// the accepted ACL extent.
func credentialACLRangeFits(aclBase, aclBytesInUse, address, length uintptr) bool {
	if address < aclBase {
		return false
	}
	offset := address - aclBase
	return offset <= aclBytesInUse && length <= aclBytesInUse-offset
}

// credentialACESIDFits reports whether the SID embedded at sidOffset lies
// entirely within the ACE. The SID sub-authority count is read only after the
// fixed SID header is known to fit inside AceSize.
func credentialACESIDFits(ace *credentialACEHeader, sidOffset uintptr) bool {
	if uintptr(ace.AceSize) < sidOffset+credentialSIDHeaderSize {
		return false
	}
	subAuthorityCount := *(*byte)(unsafe.Add(unsafe.Pointer(ace), sidOffset+1))
	return uintptr(ace.AceSize) >= sidOffset+credentialSIDHeaderSize+4*uintptr(subAuthorityCount)
}
